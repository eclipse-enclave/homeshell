package provider

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
	"github.com/eclipsesource/homeshell/daemon/internal/opener"
	"github.com/eclipsesource/homeshell/daemon/internal/store"
	"github.com/eclipsesource/homeshell/daemon/internal/terminal"
)

// PlaceholderIDPrefix is the ID prefix for creation-in-progress placeholder
// workstreams. The reconciler preserves workstreams with this prefix across
// cycles; Service.finishCreate manages their lifecycle.
//
// Defined as the store's own constant rather than beside it: the store has to
// recognize a placeholder too (its workstream-name rule excludes them), and two
// copies of the same string prefix is the kind of thing that drifts silently.
const PlaceholderIDPrefix = store.PlaceholderIDPrefix

// defaultPlaceholderName is the display name for a placeholder workstream
// when the user did not supply one.
const defaultPlaceholderName = "New workstream"

// StartingIDPrefix is the ID prefix for the placeholder session the daemon holds
// while a provider brings a real one up. Like the workstream placeholder above
// the reconciler preserves these across cycles, and Service.finishCreateSession
// owns their lifecycle — but unlike a session, a placeholder is never persisted
// (store.AddEphemeralSession explains why).
//
// Defined as the store's own constant for the same reason PlaceholderIDPrefix is:
// the store enforces both halves of that sentence — never persisted, never
// dropped by a projection — so it has to recognize a placeholder too.
const StartingIDPrefix = store.StartingIDPrefix

// IsStartingPlaceholder reports whether a session is a creation placeholder
// rather than a session a provider is actually reporting. It stays true after a
// create *fails*: the row keeps its ID and takes domain.SessionError, and it is
// still the daemon's own bookkeeping — which is what keeps the reconciler from
// dropping the explanation on its next cycle.
func IsStartingPlaceholder(sess domain.Session) bool {
	return strings.HasPrefix(sess.ID, StartingIDPrefix)
}

// startupTailLines is how many trailing lines of a provider's startup output the
// placeholder carries. It is set just above what a client can actually show —
// settings.sessions.previewLines defaults to 20 and is capped at 24 — because
// every retained line rides in a full-state broadcast to every connected client,
// re-serialized on each publish for the whole of a build. Retaining hundreds
// would put lines nothing renders on the wire several times a second.
//
// The headroom over the cap is deliberate: it keeps the box full at the largest
// preview the settings allow, without the daemon having to read the setting. A
// genuinely scrollable build log would mean taking the preview off the state
// snapshot and giving it a stream of its own, which this constant cannot buy.
const startupTailLines = 30

// startupFlushInterval is the shortest gap between two startup-progress
// broadcasts. A container build emits output far faster than a human reads it,
// and every store mutation pushes a complete state snapshot to every SSE
// subscriber, so writing each line straight through would turn one build into
// thousands of full-state frames. Coalescing to this cadence keeps the tail live
// to the eye while bounding the traffic.
const startupFlushInterval = 250 * time.Millisecond

// Service is the provider subsystem the HTTP API depends on: it owns the
// registry (workstream + session providers), the built-in core workstream
// provider, and the reconciler, and exposes whole-operation methods to the
// endpoints.
type Service struct {
	registry   *Registry
	reconciler *Reconciler
	store      *store.Store
	core       WorkstreamProvider
	// term opens a terminal window on the daemon host.
	term terminal.Launcher
	// commands starts a detached "open with" command in a workstream's directory.
	commands CommandRunner
	// opener hands a file to the desktop's default application (the workstream
	// note), the second hand-off beside term.
	opener opener.Opener
	// settings supplies the user's "open with" entries and terminal emulator,
	// read live; the shipped defaults until SetSettings is called.
	settings SettingsHolder

	// mu guards inflight, creating, restarting, removing and removingProjects.
	//
	// inflight holds per-workstream reservations for sessions whose async creation
	// has been admitted but not yet landed in the store. Counting these alongside
	// stored sessions makes the concurrency check atomic, so two rapid creates
	// can't both pass before either session appears.
	//
	// creating holds one handle per in-flight session create, keyed by the
	// placeholder ID that stands for it. A create runs on a detached context so
	// the HTTP request returning does not kill it — which leaves the daemon no way
	// to stop it either, unless it keeps the handle here. Tearing the workstream
	// or project down out from under a build is what needs it (Service.Remove,
	// Service.RemoveProject).
	//
	// restarting holds the same handle for each session restart in flight, keyed
	// by session ID: removing the session or its project cancels it, and a second
	// restart of the session is refused while it is there (RestartSession).
	//
	// removing counts the removals in progress per session ID, during which a
	// restart is refused (beginRemoval); removingProjects does the same per project
	// ID for its session creates and restarts (beginProjectRemoval).
	//
	// mu is held across a store call in places (removeProjectFromStore); the
	// store never calls back into the service, so the order cannot invert.
	mu               sync.Mutex
	inflight         map[string]int
	creating         map[string]createInFlight
	restarting       map[string]restartInFlight
	removing         map[string]int
	removingProjects map[string]int

	// createMu serializes the synchronous half of workstream creates.
	createMu sync.Mutex

	// lifetime is what the session creates and restarts that outlive their
	// requests run on; Shutdown cancels it, which kills what they started, and
	// waits for background, the goroutines they run in. backgroundMu orders
	// goBackground against Shutdown, so nothing joins background once it is
	// waited for.
	lifetime       context.Context
	cancelLifetime context.CancelFunc
	background     sync.WaitGroup
	backgroundMu   sync.Mutex
}

// Sentinel errors the API maps to HTTP statuses.
var (
	ErrProviderNotFound    = errors.New("unknown provider")
	ErrProviderUnavailable = errors.New("provider is unavailable")
	ErrCannotCreate        = errors.New("provider cannot create a workstream on this project")
	ErrProjectNotFound     = errors.New("project not found")
	ErrWorkstreamNotFound  = errors.New("workstream not found")
	ErrSessionNotFound     = errors.New("session not found")
	// ErrConcurrencyLimit is returned when creating or restarting a session would
	// exceed the provider's MaxConcurrentSessions for the workstream.
	ErrConcurrencyLimit = errors.New("concurrent session limit reached")
	// ErrSessionStopped is returned when a session has no live terminal to type
	// into; it must be started first.
	ErrSessionStopped = errors.New("session is stopped; start it before sending text")
	// ErrSessionGone is returned when a session's terminal is no longer there and
	// the provider could not bring it back. Like ErrSessionStopped it is a conflict
	// with the session's current state and not a fault: the session reports
	// SessionStopped, and starting it launches the agent again.
	ErrSessionGone = errors.New("session terminal is gone; start the session to bring it back")
	// ErrInvalidKey is returned for a key name that is not one of SessionKeys.
	ErrInvalidKey = errors.New("unknown key")
	// ErrEmptySessionTitle is returned when a rename would leave a session with no
	// name at all. Clearing the name is a distinct operation (ResetSessionTitle),
	// which restores the provider's own title rather than removing it.
	ErrEmptySessionTitle = errors.New("a session title must not be empty")
	// ErrSessionStarting is returned when a session is still being created and the
	// requested action needs the real one. It is a conflict with the session's
	// current state rather than a bad request: the same call succeeds once the
	// create lands, under the ID the provider then issues.
	ErrSessionStarting = errors.New("session is still starting; wait for it to come up")
	// ErrSessionNeverStarted is returned when an action needs a session that
	// was never created: its create failed, and the row stands only for the
	// attempt. Nothing will change that, so removing it is all there is to do.
	ErrSessionNeverStarted = errors.New("session never started; remove it and start a new one")
	// ErrSessionNotRestartable is returned when a provider that restarts sessions
	// cannot restart this one, whatever it would be restarted with.
	ErrSessionNotRestartable = errors.New("session cannot be restarted")
	// ErrRestartInterrupted is returned by a RestartFunc cancelled after it began
	// taking its session down, which it leaves neither as it was nor restarted.
	ErrRestartInterrupted = errors.New("the restart was interrupted")
	// ErrStartIsRestart is returned by a SessionStartStopper's StartSession when
	// the stopped session starts by a restart with its recorded settings, which
	// the service then runs in the background as RestartSession does.
	ErrStartIsRestart = errors.New("the session starts by a restart")
	// ErrCannotContinue is returned when a restart cannot continue a session's
	// conversation, and can only start a fresh one.
	ErrCannotContinue = errors.New("cannot continue the session's conversation")
	// ErrOptionNotOffered is returned when the session option a session would be
	// restarted as is no longer offered by its provider.
	ErrOptionNotOffered = errors.New("the session type it was started as is no longer offered; start a new session instead")
	// ErrCapabilityUnsupported is returned when a client requests an action the
	// owning provider does not support (e.g. start/stop on a provider that is not
	// a SessionStartStopper).
	ErrCapabilityUnsupported = errors.New("provider does not support this action")
	// ErrNoWorkingDir is returned when a workstream has no working directory on
	// disk to open.
	ErrNoWorkingDir = errors.New("workstream has no working directory to open")
	// ErrNoShellEnvironment is returned when a session has no live environment to
	// open a shell in: it has ended or gone, or its ShellOpener finds its container
	// gone. The user asked for that environment, so there is no fallback.
	ErrNoShellEnvironment = errors.New("session has no live environment to open a shell in")
	// ErrNoLink is returned when no live session declares the link, or the one
	// that does cannot resolve it right now.
	ErrNoLink = errors.New("this link is not reachable for the workstream")
	// ErrUnknownOpenMode is returned for an open mode the daemon does not
	// implement.
	ErrUnknownOpenMode = errors.New("unknown open mode")
	// ErrOpenWithFailed is returned when an entry from the user's "open with" list
	// could not be launched on the daemon host.
	ErrOpenWithFailed = errors.New("the command for this open-with entry could not be launched")
	// ErrHandoffFailed is returned when the desktop could not be asked to open a
	// file — typically no opener installed, which a headless host or a minimal
	// container simply does not have. That is a fact about how the daemon's host
	// is set up, not a fault in the daemon, so it is a conflict rather than a 500.
	ErrHandoffFailed = errors.New("the desktop on the daemon host could not open this file")
	// ErrWorkstreamBusy is returned when removing a workstream would discard live
	// work: at least one session is running or waiting for input and the caller
	// did not set RemoveOptions.Force. Errors wrapping it carry the offending
	// sessions (see BusyError / BusySessions).
	ErrWorkstreamBusy = errors.New("workstream has sessions that are still busy")
)

// SessionKeys are the normalized key names a client may send to a session to
// navigate an agent's interactive menu.
var SessionKeys = map[string]bool{
	"up":     true,
	"down":   true,
	"left":   true,
	"right":  true,
	"enter":  true,
	"escape": true,
	"tab":    true,
}

// ValidSessionKey reports whether key is one of the normalized SessionKeys.
func ValidSessionKey(key string) bool { return SessionKeys[key] }

// SessionKeyBytes maps each SessionKeys name to the byte sequence a terminal
// emits for it. The CSI arrow forms are accepted regardless of cursor-key mode.
var SessionKeyBytes = map[string]string{
	"up":     "\x1b[A",
	"down":   "\x1b[B",
	"left":   "\x1b[D",
	"right":  "\x1b[C",
	"enter":  "\r",
	"escape": "\x1b",
	"tab":    "\t",
}

// ServiceOption tunes a Service at construction.
type ServiceOption func(*Service)

// WithTerminal overrides the launcher OpenWorkstream opens a terminal window
// with, so a test never spawns a window.
func WithTerminal(launcher terminal.Launcher) ServiceOption {
	return func(s *Service) { s.term = launcher }
}

// WithCommandRunner overrides the runner an "open with" command is started
// through, so a test never spawns a process.
func WithCommandRunner(runner CommandRunner) ServiceOption {
	return func(s *Service) { s.commands = runner }
}

// WithOpener overrides how OpenNote hands the note to the desktop, for the same
// reason as WithTerminal: a test run must not pop an application open.
func WithOpener(o opener.Opener) ServiceOption {
	return func(s *Service) { s.opener = o }
}

// NewService builds the provider subsystem over a store and registry. poll is
// the reconciler's periodic refresh interval (0 disables polling). The core
// workstream provider is registered so it appears in the create pick-list.
func NewService(st *store.Store, reg *Registry, poll time.Duration, opts ...ServiceOption) *Service {
	c := newCore()
	// Register the core workstream provider if the caller didn't.
	if _, ok := reg.WorkstreamProvider(c.ID()); !ok {
		_ = reg.RegisterWorkstreamProvider(c)
	}
	svc := &Service{
		registry:         reg,
		reconciler:       NewReconciler(st, reg, poll),
		store:            st,
		core:             c,
		commands:         NewOSCommandRunner(),
		opener:           opener.NewOSOpener(),
		inflight:         map[string]int{},
		creating:         map[string]createInFlight{},
		restarting:       map[string]restartInFlight{},
		removing:         map[string]int{},
		removingProjects: map[string]int{},
	}
	svc.lifetime, svc.cancelLifetime = context.WithCancel(context.Background())
	svc.term = terminal.NewOSLauncher(svc.settings.TerminalPreference)
	for _, opt := range opts {
		opt(svc)
	}
	// The reconciler projects the derived workstream fields; only the service
	// knows how to derive them (it holds the settings and the host).
	svc.reconciler.annotate = svc.annotate
	return svc
}

// Reconcile projects provider sessions into the store once.
func (s *Service) Reconcile(ctx context.Context) { s.reconciler.Reconcile(ctx) }

// Refresh asks for a fresh projection pass, so fields derived at projection
// time (a workstream's open modes) are recomputed and pushed to every client.
// It never blocks; a pending refresh coalesces with this one.
func (s *Service) Refresh() { s.reconciler.Changed() }

// Run keeps the projection fresh (watchers, pushes, poll) until ctx is done.
func (s *Service) Run(ctx context.Context) { s.reconciler.Run(ctx) }

// Shutdown cancels the session creates and restarts in flight, as the daemon
// exits: left running, they would build on, out of its sight, and land
// containers nothing records. It waits for them to return until ctx is done.
func (s *Service) Shutdown(ctx context.Context) {
	s.backgroundMu.Lock()
	s.cancelLifetime()
	s.backgroundMu.Unlock()
	stopped := make(chan struct{})
	go func() {
		s.background.Wait()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-ctx.Done():
	}
}

// goBackground runs work in a goroutine Shutdown waits for. Once shutting down,
// it runs work untracked: joining background as it is waited for would race the
// wait, and the daemon is exiting anyway.
func (s *Service) goBackground(work func()) {
	s.backgroundMu.Lock()
	defer s.backgroundMu.Unlock()
	if s.lifetime.Err() != nil {
		go work()
		return
	}
	s.background.Go(work)
}

// Describe returns the descriptor for every registered session provider.
func (s *Service) Describe(ctx context.Context) []ProviderDescriptor {
	return s.registry.DescribeAll(ctx)
}

// WorkstreamProvidersFor returns the descriptors of the workstream providers
// that can create a workstream on a project, each with its creation schema. It
// reports ErrProjectNotFound if the project is unknown.
func (s *Service) WorkstreamProvidersFor(ctx context.Context, projectID string) ([]ProviderDescriptor, error) {
	ref, ok := s.projectRef(projectID)
	if !ok {
		return nil, ErrProjectNotFound
	}
	ref.FolderTaken = s.store.WorkingDirUser(projectID, ref.Path) != ""
	var out []ProviderDescriptor
	for _, p := range s.registry.WorkstreamProviders() {
		available, err := p.Available(ctx)
		if err != nil || !available {
			continue
		}
		if !p.CanCreate(ctx, ref) {
			continue
		}
		out = append(out, ProviderDescriptor{
			ID:               p.ID(),
			Label:            p.Label(),
			Status:           ProviderStatus{State: StatusAvailable},
			WorkstreamSchema: p.Schema(ctx, ref),
		})
	}
	return out, nil
}

// CreateInput is a request to create a workstream on a project.
type CreateInput struct {
	ProjectID  string
	ProviderID string
	Name       string
	Config     []byte
}

// Create instantiates a workstream on a project via the chosen workstream
// provider. Validation is synchronous; the directory preparation runs in the
// background behind a placeholder, because copy/worktree can be slow.
func (s *Service) Create(ctx context.Context, in CreateInput) (domain.Workstream, error) {
	ref, wp, err := s.creator(ctx, in)
	if err != nil {
		return domain.Workstream{}, err
	}
	if err := ValidateConfig(validationSchema(ctx, wp, ref), in.Config); err != nil {
		return domain.Workstream{}, err
	}

	// Naming, validating and reserving are one step against other creates: a
	// default name is free only until a placeholder holds it.
	s.createMu.Lock()
	defer s.createMu.Unlock()
	// The checks hold createMu, so they share one bound. The create goes on
	// without the client, and so do they: a disconnect must not fail them.
	checkCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), createCheckTimeout)
	defer cancel()
	request := s.createRequest(in, ref)
	if resolver, ok := wp.(DraftResolver); ok && in.Name == "" {
		in.Name = resolver.ResolveDraft(checkCtx, request).Name
		request.Name = in.Name
	}
	if user := s.store.NameUser(in.ProjectID, in.Name); user != "" {
		return domain.Workstream{}, nameTakenError(user)
	}
	// The provider's own synchronous check, for the rules its schema cannot state
	// (ConfigValidator). It runs here rather than inside the async Create so a
	// refusable value is refused on this request — nothing reserved, nothing
	// materialized, and no failed workstream left for the user to dispose of.
	if validator, ok := wp.(ConfigValidator); ok {
		if err := validator.ValidateCreate(checkCtx, request); err != nil {
			return domain.Workstream{}, err
		}
	}

	placeholderID, err := generatePlaceholderID()
	if err != nil {
		return domain.Workstream{}, fmt.Errorf("generating placeholder ID: %w", err)
	}
	name := in.Name
	if name == "" {
		name = defaultPlaceholderName
	}
	placeholder := domain.Workstream{
		ID:            placeholderID,
		Name:          name,
		Provider:      wp.ID(),
		ProviderLabel: providerLabel(wp.ID(), s.registry),
		RuntimeState:  domain.RuntimeCreating,
		StatusMessage: "Setting up workstream\u2026",
		Sessions:      []domain.Session{},
	}
	if teller, ok := wp.(StrategyTeller); ok {
		placeholder.StrategyID = teller.CreateStrategy(request)
	}
	if err := s.store.AddWorkstream(in.ProjectID, placeholder); err != nil {
		return domain.Workstream{}, err
	}

	// Detached context so directory preparation is not cancelled when the HTTP
	// request completes.
	go s.finishCreate(context.WithoutCancel(ctx), in, ref, wp, placeholderID)

	if projected, err := s.store.Workstream(in.ProjectID, placeholderID); err == nil {
		return projected, nil
	}
	return placeholder, nil
}

// AddProject adds a project with its default workstream: one in place, in the
// project folder and named after the branch checked out there, so sessions can
// start as soon as the project exists. The project is echoed back as stored,
// default workstream included. A default workstream that cannot be made is
// logged and the project added without one: the user can still create one.
func (s *Service) AddProject(ctx context.Context, project *domain.Project) error {
	if err := s.store.AddProject(project); err != nil {
		return err
	}
	defaultWorkstream, err := s.AddDefaultWorkstream(ctx, project.ID)
	if err != nil {
		slog.Warn("adding the default workstream failed", "project", project.ID, "error", err)
		return nil
	}
	project.Workstreams = []domain.Workstream{defaultWorkstream}
	project.Recompute()
	return nil
}

// defaultWorkstreamConfig is the core create the default workstream is.
const defaultWorkstreamConfig = `{"strategy":"in-place"}`

// AddDefaultWorkstream creates a project's default workstream through the core
// provider and returns it. In place nothing is materialized, so it is created
// synchronously, with no placeholder in between. While another workstream is in
// the project folder, a pending or failed in-place create included, it is
// refused with store.ErrDuplicateWorkingDir.
func (s *Service) AddDefaultWorkstream(ctx context.Context, projectID string) (domain.Workstream, error) {
	project, ok := s.projectRef(projectID)
	if !ok {
		return domain.Workstream{}, ErrProjectNotFound
	}
	workstreamProvider, ok := s.registry.WorkstreamProvider(CoreProviderID)
	if !ok {
		return domain.Workstream{}, ErrProviderNotFound
	}
	created, err := s.addDefaultWorkstream(ctx, projectID, project, workstreamProvider)
	if err != nil {
		return domain.Workstream{}, err
	}
	// A projection pass attaches any session already running in the project
	// folder. It asks every session provider, so it runs after the request, as
	// finishCreate's does; a failed pass is left to the next.
	reconcileCtx := context.WithoutCancel(ctx)
	s.goBackground(func() {
		if err := s.reconciler.ReconcileProject(reconcileCtx, projectID); err != nil {
			slog.Warn("reconciling after adding the default workstream failed",
				"project", projectID, "error", err)
		}
	})
	return s.store.Workstream(projectID, created.ID)
}

// addDefaultWorkstream creates and stores the default workstream under
// createMu, so an in-place create cannot pass its check against the project
// folder in between.
func (s *Service) addDefaultWorkstream(ctx context.Context, projectID string, project ProjectRef, workstreamProvider WorkstreamProvider) (domain.Workstream, error) {
	s.createMu.Lock()
	defer s.createMu.Unlock()
	created, err := workstreamProvider.Create(ctx, s.createRequest(CreateInput{
		ProjectID: projectID, Config: []byte(defaultWorkstreamConfig),
	}, project))
	if err != nil {
		return domain.Workstream{}, err
	}
	created, err = stampCreated(created, workstreamProvider.ID())
	if err != nil {
		return domain.Workstream{}, err
	}
	if err := s.store.AddWorkstream(projectID, created); err != nil {
		return domain.Workstream{}, err
	}
	return created, nil
}

// stampCreated gives a workstream a provider created what the daemon assigns:
// its ID and its provider.
func stampCreated(created domain.Workstream, providerID string) (domain.Workstream, error) {
	workstreamID, err := generateWorkstreamID()
	if err != nil {
		return domain.Workstream{}, fmt.Errorf("generating workstream ID: %w", err)
	}
	created.ID = workstreamID
	created.Provider = providerID
	if created.Sessions == nil {
		created.Sessions = []domain.Session{}
	}
	return created, nil
}

// ResolveDraft says what Create would derive for what input leaves blank,
// without creating anything: what a form shows before it is sent.
func (s *Service) ResolveDraft(ctx context.Context, input CreateInput) (Draft, error) {
	project, workstreamProvider, err := s.creator(ctx, input)
	if err != nil {
		return Draft{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, createCheckTimeout)
	defer cancel()
	if resolver, ok := workstreamProvider.(DraftResolver); ok {
		return resolver.ResolveDraft(ctx, s.createRequest(input, project)), nil
	}
	return Draft{Name: input.Name, Config: map[string]string{}}, nil
}

// creator is the project and provider a create of input goes to, or why it
// can go to none.
func (s *Service) creator(ctx context.Context, input CreateInput) (ProjectRef, WorkstreamProvider, error) {
	project, ok := s.projectRef(input.ProjectID)
	if !ok {
		return ProjectRef{}, nil, ErrProjectNotFound
	}
	providerID := input.ProviderID
	if providerID == "" {
		providerID = CoreProviderID
	}
	workstreamProvider, ok := s.registry.WorkstreamProvider(providerID)
	if !ok {
		return ProjectRef{}, nil, ErrProviderNotFound
	}
	if available, err := workstreamProvider.Available(ctx); err != nil || !available {
		return ProjectRef{}, nil, ErrProviderUnavailable
	}
	if !workstreamProvider.CanCreate(ctx, project) {
		return ProjectRef{}, nil, ErrCannotCreate
	}
	return project, workstreamProvider, nil
}

// createRequest is input as the provider is asked it, checked against the
// project's workstreams.
func (s *Service) createRequest(input CreateInput, project ProjectRef) CreateRequest {
	return CreateRequest{
		Project:        project,
		Name:           input.Name,
		NameTaken:      func(name string) bool { return s.store.NameUser(input.ProjectID, name) != "" },
		WorkingDirUser: func(dir string) string { return s.store.WorkingDirUser(input.ProjectID, dir) },
		Config:         input.Config,
	}
}

// finishCreate runs the workstream provider's creation in the background. On
// success the placeholder is replaced by the real workstream; on failure it
// transitions to error state so the user sees what went wrong.
func (s *Service) finishCreate(ctx context.Context, in CreateInput, ref ProjectRef, wp WorkstreamProvider, placeholderID string) {
	created, err := wp.Create(ctx, CreateRequest{
		Project: ref,
		Name:    in.Name,
		NameTaken: func(name string) bool {
			return s.store.NameUser(in.ProjectID, name, placeholderID) != ""
		},
		Config: in.Config,
		OnProgress: func(msg string) {
			_ = s.store.SetStatusMessage(in.ProjectID, placeholderID, msg)
		},
	})
	if err != nil {
		slog.Warn("workstream creation failed",
			"project", in.ProjectID, "placeholder", placeholderID, "error", err)
		s.failCreate(in, placeholderID, err)
		return
	}

	created, err = stampCreated(created, wp.ID())
	if err != nil {
		slog.Warn("failed to generate workstream ID", "error", err)
		return
	}

	// Swap the placeholder for the real workstream in one mutation; the store
	// rejects a duplicate WorkingDir (the join key must be unique in a project) or
	// a name another workstream took while the provider worked. On rejection,
	// discard any directory the provider just materialized (a no-op for in-place)
	// and leave the placeholder in error state so the user sees why — the existing
	// workstream at that directory is the one to use. The directory is discarded
	// even when the rejection is the project having been removed meanwhile.
	if err := s.store.PromoteWorkstream(in.ProjectID, placeholderID, created); err != nil {
		if rerr := wp.Remove(ctx, ref, created, RemoveOptions{}); rerr != nil {
			slog.Warn("discarding rejected workstream directory failed",
				"project", in.ProjectID, "dir", created.WorkingDir, "error", rerr)
		}
		s.failCreate(in, placeholderID, err)
		return
	}
	// PromoteWorkstream saves and broadcasts the real workstream. Reconcile so its
	// provider-reported state shows up now rather than on the next cycle.
	_ = s.reconciler.ReconcileProject(ctx, in.ProjectID)
}

// createCheckTimeout bounds the naming and validation of a create together. A
// variable so tests can shorten it.
var createCheckTimeout = 5 * time.Second

// validationSchema is the schema a create on wp is validated against.
func validationSchema(ctx context.Context, wp WorkstreamProvider, project ProjectRef) json.RawMessage {
	if schemer, ok := wp.(ValidationSchemer); ok {
		return schemer.ValidationSchema(ctx, project)
	}
	return wp.Schema(ctx, project)
}

// failCreate leaves the placeholder in error state, saying why the create
// failed, so the user sees it and can dispose of it.
func (s *Service) failCreate(in CreateInput, placeholderID string, cause error) {
	// The placeholder as it is, so the failure keeps what it was listed by (its
	// name, provider and strategy) and stays where it was shown.
	failed, err := s.store.Workstream(in.ProjectID, placeholderID)
	if err != nil {
		slog.Warn("the failed create's placeholder is gone",
			"project", in.ProjectID, "placeholder", placeholderID, "error", err)
		return
	}
	failed.RuntimeState = domain.RuntimeError
	failed.StatusMessage = cause.Error()
	failed.CreateFailed = true
	if err := s.store.UpdateWorkstream(in.ProjectID, failed); err != nil {
		slog.Warn("failed to update placeholder to error state",
			"project", in.ProjectID, "placeholder", placeholderID, "error", err)
	}
}

// generatePlaceholderID creates a unique ID for a creating-state placeholder
// workstream. The prefix matches PlaceholderIDPrefix.
func generatePlaceholderID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s%x", PlaceholderIDPrefix, b), nil
}

// generateStartingID creates a unique ID for a starting-session placeholder. The
// prefix matches StartingIDPrefix, which is what marks it as a placeholder
// everywhere else (IsStartingPlaceholder). An exhausted entropy source falls
// back to the bare prefix rather than failing the create: two concurrent
// placeholders in one workstream would then collide, but the caller is already
// capped at one in-flight create per slot and a session the user is waiting for
// matters more than a unique suffix.
func generateStartingID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return StartingIDPrefix
	}
	return fmt.Sprintf("%s%x", StartingIDPrefix, b)
}

// CreateSessionInput is a request to create a session in a workstream.
type CreateSessionInput struct {
	ProjectID    string
	WorkstreamID string
	ProviderID   string
	OptionID     string
	Config       json.RawMessage
}

// CreateSession starts a new session in an existing workstream via the chosen
// session provider. The workstream's WorkingDir is passed to the provider so it
// launches in the right place. The provider runs in the background; what comes
// back is the starting placeholder that stands for the session until it lands.
func (s *Service) CreateSession(ctx context.Context, in CreateSessionInput) (domain.Session, error) {
	ws, err := s.store.Workstream(in.ProjectID, in.WorkstreamID)
	if err != nil {
		return domain.Session{}, ErrWorkstreamNotFound
	}
	p, ok := s.registry.SessionProvider(in.ProviderID)
	if !ok {
		return domain.Session{}, ErrProviderNotFound
	}
	if available, err := p.Available(ctx); err != nil || !available {
		return domain.Session{}, ErrProviderUnavailable
	}

	// The provider's options, looked up only when a config has to be validated
	// against their schema. For Enclave this shells out to the CLI with a timeout
	// measured in tens of seconds, and this request's whole point is to return
	// before the work starts — so a create that carries no config must not pay it
	// just to read a display label off the result.
	//
	// A provider that cannot list its options does not fail the create: the options
	// carry a label and an optional schema, and refusing to start the session the
	// user asked for because the label could not be looked up would be the worse
	// answer.
	var options []SessionSchemaOption
	if absentConfig(in.Config) {
		// A JSON null is no config. Normalized here, where a create first sees it,
		// so the provider is not handed a null to parse and the record a restart
		// replays holds none to validate.
		in.Config = nil
	} else {
		// Validate synchronously so a bad request is a 400 before a concurrency
		// slot is consumed.
		if in.OptionID != "" {
			options = sessionOptionsOf(ctx, p, WorkstreamRef{ProjectID: in.ProjectID, WorkstreamID: in.WorkstreamID})
		}
		if err := validateAgainstOption(options, in.OptionID, in.Config); err != nil {
			return domain.Session{}, err
		}
	}
	recorded := in.Config
	if index := slices.IndexFunc(options, func(option SessionSchemaOption) bool { return option.ID == in.OptionID }); index >= 0 {
		recorded = withoutDefaults(options[index].Schema, in.Config, nil)
	}

	// Reserve a concurrency slot atomically, counting in-flight creations.
	// Released when the async create finishes.
	key := concurrencyKey(in.ProjectID, in.WorkstreamID)
	if err := s.reserveSlot(key, ws, p.Capabilities()); err != nil {
		return domain.Session{}, err
	}

	// Name what is coming up as a placeholder session, so the wait is legible:
	// creation may take minutes (an image build) and until it returns there is
	// otherwise nothing in the UI to attach that time to.
	//
	// Deliberately *no* workstream status message for the progress: the placeholder
	// row already carries the agent's name and a `starting` badge, so a
	// "Starting <agent>\u2026" line beside it says the same thing twice. The message is
	// instead **cleared** here, because an earlier attempt's failure would otherwise
	// stay on screen for the whole of this one \u2014 a retry should look like a retry.
	// A failure now stays on its own row (finishCreateSession), so this only clears
	// the fallback message left by an attempt whose placeholder had gone.
	// Without a schema lookup this is the option id the client sent rather than the
	// provider's own label — legible, and only until the create lands and the real
	// session replaces the row with the provider's naming of itself.
	agent := agentLabel(options, in.OptionID)
	placeholder := domain.Session{
		ID:       generateStartingID(),
		Title:    agent + " session",
		Agent:    agent,
		Provider: p.ID(),
		State:    domain.SessionStarting,
	}
	s.clearStaleStatus(in.ProjectID, in.WorkstreamID)

	// Run creation in the background so the HTTP response returns immediately.
	// Detached from the request (which is about to end) but cancellable by the
	// daemon, so tearing the workstream down, or the daemon shutting down, can stop
	// the build rather than let it finish into nothing — registerCreate holds the
	// handle. Registered before the placeholder is added: a project removed in
	// between would otherwise take the placeholder with it and miss the create.
	createCtx, cancel := context.WithCancel(s.lifetime)
	s.registerCreate(in.ProjectID, placeholder.ID, in.OptionID, cancel)
	// The workstream was looked up above, so a failure here means it has since
	// been removed.
	if err := s.store.AddEphemeralSession(in.ProjectID, in.WorkstreamID, placeholder); err != nil {
		s.cancelCreate(placeholder.ID)
		s.releaseSlot(key)
		return domain.Session{}, fmt.Errorf("%w: %v", ErrWorkstreamNotFound, err)
	}
	s.goBackground(func() { s.finishCreateSession(createCtx, in, recorded, p, ws.WorkingDir, key, placeholder.ID) })

	return placeholder, nil
}

// clearStaleStatus drops a workstream's status message unless the message is the
// record of a failure, in which case it is all the account there is of why the
// workstream is red — the same thing the dismiss endpoint refuses to throw away
// (api.clearWorkstreamStatusHandler explains the distinction). RuntimeError is a
// separate sticky field, so clearing the message would not clear the state it
// explains: the card would stay red with nothing left saying why, permanently.
func (s *Service) clearStaleStatus(projectID, workstreamID string) {
	ws, err := s.store.Workstream(projectID, workstreamID)
	if err != nil || ws.RuntimeState == domain.RuntimeError {
		return
	}
	_ = s.store.SetStatusMessage(projectID, workstreamID, "")
}

// createInFlight is the service's handle on one session create.
type createInFlight struct {
	projectID string
	optionID  string
	cancel    context.CancelFunc
}

// registerCreate records the cancel func, and the option asked for, of an
// in-flight session create. One in a project being removed is cancelled at once:
// it would land in a project that is gone.
func (s *Service) registerCreate(projectID, placeholderID, optionID string, cancel context.CancelFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.removingProjects[projectID] > 0 {
		cancel()
		return
	}
	s.creating[placeholderID] = createInFlight{projectID: projectID, optionID: optionID, cancel: cancel}
}

// cancelCreate stops the session create one placeholder stands for, if it is
// still running. Unknown IDs (a create that already finished or was cancelled, a
// row from a previous daemon) are ignored, so a create calls it once it is over
// to release its context.
func (s *Service) cancelCreate(placeholderID string) {
	s.mu.Lock()
	create, ok := s.creating[placeholderID]
	delete(s.creating, placeholderID)
	s.mu.Unlock()
	if ok {
		create.cancel()
	}
}

// cancelCreates stops every session create in flight for a workstream and
// reports how many it stopped. Used when the workstream is being torn down: the
// build has nowhere to land any more, and left alone it would run to completion
// and start a container belonging to nothing (finishCreateSession disposes of
// one that slips through anyway).
func (s *Service) cancelCreates(ws domain.Workstream) int {
	s.mu.Lock()
	var cancels []context.CancelFunc
	for _, sess := range ws.Sessions {
		if !IsStartingPlaceholder(sess) {
			continue
		}
		if create, ok := s.creating[sess.ID]; ok {
			cancels = append(cancels, create.cancel)
			delete(s.creating, sess.ID)
		}
	}
	s.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	return len(cancels)
}

// agentLabel is the display name for what a create is about to start \u2014 the
// chosen option's own label ("Claude"), falling back to its ID and then to a
// generic word, so the placeholder always reads as something.
//
// The options come from the provider rather than the ID being formatted, because
// the label is the provider's to decide: Enclave names its tool profiles,
// and a future provider may label an option nothing like its ID. They are passed
// in rather than fetched here so the request path pays for one lookup.
func agentLabel(options []SessionSchemaOption, optionID string) string {
	for _, opt := range options {
		if opt.ID == optionID && opt.Label != "" {
			return opt.Label
		}
	}
	if optionID != "" {
		return optionID
	}
	return "Session"
}

// finishCreateSession runs the provider's session creation in the background.
// It releases the concurrency reservation on completion (success or failure), and
// owns the placeholder it was handed: whatever happens, the placeholder is gone
// by the time this returns — promoted into the real session, or removed. The
// session's launch is recorded with recordedConfig, in.Config without defaults.
func (s *Service) finishCreateSession(ctx context.Context, in CreateSessionInput, recordedConfig json.RawMessage, p SessionProvider, workingDir, key, placeholderID string) {
	defer s.releaseSlot(key)
	defer s.cancelCreate(placeholderID)

	// The provider's startup output lands on the placeholder's preview, which is
	// the same field a live session's terminal tail uses — so the UI shows the
	// build in the box that later shows the agent, with no second surface.
	sink := newStartupSink(startupTailLines, startupFlushInterval, func(tail string) {
		if err := s.store.SetSessionPreview(in.ProjectID, in.WorkstreamID, placeholderID, tail); err != nil {
			// The placeholder is gone (creation finished, or its workstream was
			// removed under us). Nothing to show it on; drop the line.
			slog.Debug("startup progress had nowhere to go", "session", placeholderID, "error", err)
		}
	})

	sess, err := p.CreateSession(ctx, CreateSessionRequest{
		Workstream: WorkstreamRef{ProjectID: in.ProjectID, WorkstreamID: in.WorkstreamID},
		WorkingDir: workingDir,
		OptionID:   in.OptionID,
		Config:     in.Config,
		Progress:   sink.Line,
	})
	// Before touching the placeholder, so no late line resurrects a preview on a
	// session that is about to be replaced or removed.
	sink.Close()

	if err != nil {
		slog.Warn("async session creation failed",
			"project", in.ProjectID, "workstream", in.WorkstreamID, "error", err)
		// The placeholder stays and becomes the failure: the row the user has been
		// watching is the one that says why it did not work, right where they were
		// already looking, keeping the startup output that led to it. SessionError is
		// terminal, so the row it leaves behind holds no concurrency slot and does not
		// make the workstream busy — the two things that made discarding it necessary
		// when the error had to travel to the workstream's status message instead.
		//
		// Only if the placeholder is gone (its workstream removed under us, or a
		// re-projection dropped it) does the error fall back to the workstream, which
		// is the last surface left that can show it. Losing it entirely would leave a
		// create that simply never happened.
		if failErr := s.store.FailEphemeralSession(in.ProjectID, in.WorkstreamID, placeholderID, err.Error()); failErr != nil {
			slog.Warn("recording the failed session on its placeholder failed",
				"session", placeholderID, "error", failErr)
			_ = s.store.SetStatusMessage(in.ProjectID, in.WorkstreamID, err.Error())
		}
		return
	}

	sess.Provider = p.ID()
	sess.Restartable = s.restartable(SessionRef{ProjectID: in.ProjectID, WorkstreamID: in.WorkstreamID, SessionID: sess.ID}, sess, in.OptionID)
	// Recorded with the promotion, so a restart can replay what the session was
	// started with.
	launch := domain.LaunchConfig{ProviderID: p.ID(), OptionID: in.OptionID, Config: recordedConfig}
	if addErr := s.store.PromoteEphemeralSession(in.ProjectID, in.WorkstreamID, placeholderID, sess, launch); addErr != nil {
		slog.Warn("failed to add created session to store", "error", addErr)
		// Two different failures arrive here, and only one of them orphans the
		// session. A write that failed leaves it live and in the projection, where
		// the user can still see and stop it — the cost is a row lost at the next
		// restart. A workstream that went away while the provider was building —
		// a force-remove, which cancels the create but cannot un-start what was
		// already up — leaves nothing to route a removal through, so nobody will
		// ever tear this session down and the next discovery pass adopts its
		// directory as a workstream of its own. Only that one is disposed of, on a
		// context of its own because the one that got us here is the cancelled one.
		if _, gone := s.store.Workstream(in.ProjectID, in.WorkstreamID); gone == nil {
			return
		}
		slog.Warn("the created session has no workstream to land in; tearing it down",
			"project", in.ProjectID, "workstream", in.WorkstreamID, "session", sess.ID)
		sref := SessionRef{ProjectID: in.ProjectID, WorkstreamID: in.WorkstreamID, SessionID: sess.ID}
		if rerr := p.RemoveSession(context.WithoutCancel(ctx), sref); rerr != nil {
			slog.Error("tearing down the orphaned session failed; it may still be running",
				"session", sess.ID, "error", rerr)
		}
		return
	}
	// Not on ctx, which a shutdown or a dismissal may have cancelled by now: a
	// listing cut short would drop the project's other sessions. And not as the
	// daemon shuts down, which has no one left to show it to.
	if s.lifetime.Err() == nil {
		_ = s.reconciler.ReconcileProject(context.WithoutCancel(ctx), in.ProjectID)
	}
}

// Open asks a session's owning provider to open it in the given mode.
func (s *Service) Open(ctx context.Context, ref SessionRef, mode string) (OpenResult, error) {
	ws, err := s.store.Workstream(ref.ProjectID, ref.WorkstreamID)
	if err != nil {
		return OpenResult{}, ErrWorkstreamNotFound
	}
	p, err := s.sessionProvider(ws, ref.SessionID)
	if err != nil {
		return OpenResult{}, err
	}
	return p.Open(ctx, ref, mode)
}

// OpenWorkstream hands a workstream to the user in the chosen mode, one of the
// IDs advertised on the workstream (domain.OpenModes). An empty mode means the
// user's default-open row.
func (s *Service) OpenWorkstream(ctx context.Context, ref WorkstreamRef, mode string) (OpenResult, error) {
	ws, err := s.store.Workstream(ref.ProjectID, ref.WorkstreamID)
	if err != nil {
		return OpenResult{}, ErrWorkstreamNotFound
	}
	if entryID, isOpenWith := domain.IsOpenWithMode(mode); isOpenWith {
		return s.openWithCommand(ctx, ws, entryID)
	}
	if sessionID, isShell := domain.IsSessionShellMode(mode); isShell {
		return s.openSessionShell(ctx, ref, ws, sessionID)
	}
	if linkID, isLink := domain.IsLinkMode(mode); isLink {
		return s.openLink(ctx, ref, ws, linkID)
	}
	switch mode {
	case "":
		return s.openDefault(ctx, ws)
	case domain.OpenModeTerminal:
		// Named explicitly, this is the host shell itself rather than the default
		// row, which may point elsewhere.
		return s.openWorkingDir(ctx, ws)
	default:
		return OpenResult{}, fmt.Errorf("%w: %q", ErrUnknownOpenMode, mode)
	}
}

// openLink resolves one provider-reported link for the client to open; the
// daemon never fetches or proxies it. The first live session whose provider
// declares the link and can answer for it wins.
//
// A session that cannot answer does not end the search: the workstream's other
// sessions are offered the request in turn. Stopping at the first refusal would
// let one row veto the whole workstream — and the rows most likely to refuse are
// the ones that sit around longest, a failed create's error record among them.
func (s *Service) openLink(ctx context.Context, ref WorkstreamRef, ws domain.Workstream, linkID string) (OpenResult, error) {
	// Whether any live session could declare links at all. It separates "that
	// link id means nothing here" (a bad request) from "nothing is serving it
	// right now" (a state the caller can wait out).
	anyOpener := false
	for _, sess := range ws.Sessions {
		if sess.State == domain.SessionStopped {
			continue
		}
		// A placeholder has no runtime yet (or never will): its ID is the daemon's
		// own, so the provider would only report a session it has never heard of.
		if IsStartingPlaceholder(sess) {
			continue
		}
		p, ok := s.registry.SessionProvider(sess.Provider)
		if !ok {
			continue
		}
		opener, ok := p.(LinkOpener)
		if !ok {
			continue
		}
		anyOpener = true
		if !declaresLink(opener, sess, linkID) {
			continue
		}
		sref := SessionRef{ProjectID: ref.ProjectID, WorkstreamID: ref.WorkstreamID, SessionID: sess.ID}
		url, err := opener.LinkURL(ctx, sref, linkID)
		if err != nil {
			return OpenResult{}, err
		}
		data, merr := json.Marshal(map[string]string{"target": "browser", "url": url})
		if merr != nil {
			return OpenResult{}, merr
		}
		return OpenResult{Mode: domain.LinkModeFor(linkID), Data: data}, nil
	}
	if anyOpener {
		return OpenResult{}, fmt.Errorf("%w: no session declares the link %q", ErrUnknownOpenMode, linkID)
	}
	return OpenResult{}, fmt.Errorf("%w: no live session offers %q", ErrNoLink, linkID)
}

// declaresLink reports whether the provider declares the link for the session.
func declaresLink(opener LinkOpener, sess domain.Session, linkID string) bool {
	for _, link := range opener.Links(sess) {
		if link.ID == linkID {
			return true
		}
	}
	return false
}

// materializedDir returns the workstream's working directory, having checked it
// is really a directory on this host.
func (s *Service) materializedDir(ws domain.Workstream) (string, error) {
	dir := ws.WorkingDir
	if dir == "" {
		return "", fmt.Errorf("%w: it is not materialized on disk", ErrNoWorkingDir)
	}
	info, err := os.Stat(dir)
	if err != nil {
		return "", fmt.Errorf("%w: %s cannot be read: %v", ErrNoWorkingDir, dir, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%w: %s is not a directory", ErrNoWorkingDir, dir)
	}
	return dir, nil
}

// openSessionShell opens a shell inside one live session's own environment
// (ShellOpener). A session that has ended or gone since the mode was offered,
// or whose container is gone, is ErrNoShellEnvironment; one whose provider has
// no environment of its own is an unknown mode, since its shell was never
// offered.
func (s *Service) openSessionShell(ctx context.Context, ref WorkstreamRef, ws domain.Workstream, sessionID string) (OpenResult, error) {
	for _, sess := range ws.Sessions {
		if sess.ID != sessionID {
			continue
		}
		if !hasEnvironment(sess.State) {
			return OpenResult{}, fmt.Errorf("%w: session %q is %s", ErrNoShellEnvironment, sessionID, sess.State)
		}
		p, ok := s.registry.SessionProvider(sess.Provider)
		if !ok {
			return OpenResult{}, fmt.Errorf("%w: session %q has no provider %q", ErrUnknownOpenMode, sessionID, sess.Provider)
		}
		opener, ok := p.(ShellOpener)
		if !ok {
			return OpenResult{}, fmt.Errorf("%w: session %q runs on this host", ErrUnknownOpenMode, sessionID)
		}
		return opener.OpenShell(ctx, SessionRef{ProjectID: ref.ProjectID, WorkstreamID: ref.WorkstreamID, SessionID: sessionID})
	}
	return OpenResult{}, fmt.Errorf("%w: no session %q in this workstream", ErrNoShellEnvironment, sessionID)
}

// openWorkingDir opens a shell in the workstream's directory on the daemon host.
func (s *Service) openWorkingDir(ctx context.Context, ws domain.Workstream) (OpenResult, error) {
	dir, err := s.materializedDir(ws)
	if err != nil {
		return OpenResult{}, err
	}
	if err := s.term.Launch(ctx, terminal.ShellIn(dir), ws.Name); err != nil {
		return OpenResult{}, fmt.Errorf("could not open a terminal: %w", err)
	}
	data, err := json.Marshal(map[string]string{"target": "host", "workingDir": dir})
	if err != nil {
		return OpenResult{}, err
	}
	return OpenResult{Mode: domain.OpenModeTerminal, Data: data}, nil
}

// nameTakenError refuses a name on the Name field, quoting user, the spelling of
// the workstream that already answers to it.
func nameTakenError(user string) error {
	return &ValidationError{
		Message: fmt.Sprintf("%v: %q", store.ErrDuplicateName, user),
		Fields:  map[string]string{"/name": fmt.Sprintf("%q is already used in this project; enter another", user)},
		Err:     store.ErrDuplicateName,
	}
}

// Rename changes a workstream's display name. The name is store data, not
// provider state, so the daemon updates it directly.
func (s *Service) Rename(ctx context.Context, ref WorkstreamRef, name string) error {
	ws, err := s.store.Workstream(ref.ProjectID, ref.WorkstreamID)
	if err != nil {
		return ErrWorkstreamNotFound
	}
	if user := s.store.NameUser(ref.ProjectID, name, ref.WorkstreamID); user != "" {
		return nameTakenError(user)
	}
	ws.Name = name
	if err := s.store.UpdateWorkstream(ref.ProjectID, ws); err != nil {
		// A name already taken in this project is the caller's mistake, not a
		// missing workstream — it has to reach them as itself, or a rename that
		// was refused for a reason they can fix reads as "no such workstream".
		if errors.Is(err, store.ErrDuplicateName) {
			return err
		}
		return ErrWorkstreamNotFound
	}
	// UpdateWorkstream already persisted the rename; reconcile refreshes the projection.
	_ = s.reconciler.ReconcileProject(ctx, ref.ProjectID)
	return nil
}

// RenameSession gives a session the display title the user typed.
//
// The owning provider is deliberately not involved (unlike stop/start/prompt,
// which are routed through it): a session's *name* is not part of the runtime it
// owns. Enclave's title is the agent's name and its session ID is a Docker
// container name that cannot be changed at all; Local's comes from the agent's
// terminal window. Neither can be told to call a session something else, so the
// daemon owns the display name over the projection — the same division as a
// workstream's name (Rename above), which no provider is asked about either.
//
// The name outlives the provider's own: it is re-applied on every reconcile cycle
// and reloaded at startup, so a rename is not undone by a poll, a container
// restart, or a daemon restart. It ends only with the session.
func (s *Service) RenameSession(ctx context.Context, ref SessionRef, title string) error {
	title = strings.TrimSpace(title)
	if title == "" {
		return ErrEmptySessionTitle
	}
	// A placeholder cannot be renamed, because the name would not survive: titles
	// are keyed on the session ID, and a placeholder's ID is discarded the moment
	// the real session takes its place (PromoteEphemeralSession) — the entry would
	// then match nothing and be pruned on the next cycle. Accepting the rename and
	// silently losing it is the worst of the three outcomes, so it is refused here
	// rather than only in the client, where an API or CLI caller walks straight
	// past it (the rule lives in the daemon).
	if strings.HasPrefix(ref.SessionID, StartingIDPrefix) {
		return ErrSessionStarting
	}
	if err := s.resolveSession(ref); err != nil {
		return err
	}
	if err := s.store.RenameSession(ref.ProjectID, ref.WorkstreamID, ref.SessionID, title); err != nil {
		// A title already showing on a sibling row is the caller's to fix, so it
		// travels as itself rather than under a "renaming session:" prefix that
		// reads like an internal failure.
		if errors.Is(err, store.ErrDuplicateSessionTitle) {
			return err
		}
		return fmt.Errorf("renaming session: %w", err)
	}
	return nil
}

// ResetSessionTitle drops a session's user-chosen title, so the name its provider
// reports is what shows again.
//
// The provider's title is not in the store to restore — what is there is the
// custom name, which every cycle since the rename has re-applied — so this
// re-projects rather than leaving the old name on screen until the next poll.
// Asking the providers is the only way to learn what the session is called
// otherwise, and a client that just cleared a name should see the default, not a
// pause.
func (s *Service) ResetSessionTitle(ctx context.Context, ref SessionRef) error {
	if err := s.resolveSession(ref); err != nil {
		return err
	}
	if err := s.store.ResetSessionTitle(ref.ProjectID, ref.WorkstreamID, ref.SessionID); err != nil {
		return fmt.Errorf("resetting session title: %w", err)
	}
	_ = s.reconciler.ReconcileProject(ctx, ref.ProjectID)
	return nil
}

// resolveSession reports whether a session ref names something that exists,
// returning the sentinel for whichever level is missing (both are a 404). Used by
// the calls that act on a session without routing through its provider, which is
// what would otherwise have caught a bad ref.
func (s *Service) resolveSession(ref SessionRef) error {
	ws, err := s.store.Workstream(ref.ProjectID, ref.WorkstreamID)
	if err != nil {
		return ErrWorkstreamNotFound
	}
	for _, sess := range ws.Sessions {
		if sess.ID == ref.SessionID {
			return nil
		}
	}
	return ErrSessionNotFound
}

// Remove tears down a workstream: its sessions via their owning session
// providers, then its working directory via the owning workstream provider,
// then the store record. Creation placeholders are removed directly.
//
// Removal is guarded: a workstream with a busy session (running or waiting for
// input) is refused with a BusyError naming those sessions unless opts.Force is
// set, in which case they are stopped and torn down with it. The guard lives
// here rather than in a client so every caller of the API gets it — a direct
// DELETE without force is refused even though the UI would have offered a
// confirmation first.
func (s *Service) Remove(ctx context.Context, ref WorkstreamRef, opts RemoveOptions) error {
	if strings.HasPrefix(ref.WorkstreamID, PlaceholderIDPrefix) {
		if err := s.store.RemoveWorkstream(ref.ProjectID, ref.WorkstreamID); err != nil {
			return ErrWorkstreamNotFound
		}
		return nil
	}

	ws, err := s.store.Workstream(ref.ProjectID, ref.WorkstreamID)
	if err != nil {
		return ErrWorkstreamNotFound
	}

	// Guard before anything is torn down, so a refusal leaves the workstream and
	// its sessions exactly as they were. It reads the same copy the teardown
	// below walks, so what was refused and what would have been stopped cannot
	// disagree.
	if !opts.Force {
		if busy := busySessions(ws); len(busy) > 0 {
			return newBusyError(busy)
		}
	}

	// Stop anything still being built before tearing the sessions down. A create
	// runs detached from the request that asked for it, so without this the build
	// outlives the workstream: `enclave run` keeps going, finishes into a store
	// that has no record to put it in, and leaves a container nothing owns. This
	// is the half of the force contract ("busy sessions are stopped and torn down
	// with it") that a placeholder needs, because the row a build is showing on is
	// busy but is not yet a session any provider could be asked to stop.
	if cancelled := s.cancelCreates(ws); cancelled > 0 {
		slog.Info("cancelled in-flight session creation for a workstream being removed",
			"workstream", ref.WorkstreamID, "creates", cancelled)
	}

	// Tear down the sessions first, via each session's owning provider.
	for _, sess := range ws.Sessions {
		// Held until the workstream is gone, so no restart starts on the way out.
		s.beginRemoval(sess.ID)
		defer s.endRemoval(sess.ID)
		// A placeholder names no runtime: its ID is the daemon's own, never issued
		// by a provider, so asking one to remove it can only fail. Cancelling above
		// is its teardown, and dropping the store record below is its disposal —
		// the same reasoning RemoveSession short-circuits on.
		if IsStartingPlaceholder(sess) {
			continue
		}
		p, ok := s.registry.SessionProvider(sess.Provider)
		if !ok {
			continue
		}
		sref := SessionRef{ProjectID: ref.ProjectID, WorkstreamID: ref.WorkstreamID, SessionID: sess.ID}
		if err := p.RemoveSession(ctx, sref); err != nil {
			slog.Warn("removing session during workstream teardown failed",
				"provider", sess.Provider, "session", sess.ID, "error", err)
		}
	}

	if wp, ok := s.registry.WorkstreamProvider(ws.Provider); ok {
		if project, ok := s.projectRef(ref.ProjectID); ok {
			if err := wp.Remove(ctx, project, ws, opts); err != nil {
				return err
			}
		}
	}

	if err := s.store.RemoveWorkstream(ref.ProjectID, ref.WorkstreamID); err != nil {
		return ErrWorkstreamNotFound
	}
	return nil
}

// RemoveProject drops a project from HomeShell. Its sessions are left running:
// removing a project stops HomeShell tracking them, nothing more. What is in
// flight for them would land in a project that is gone, so it is cancelled: its
// session creates (beginProjectRemoval), and its session restarts
// (removeProjectFromStore). A restart caught running is waited for in the
// background, and its session removed through its provider if the restart was
// interrupted (ErrRestartInterrupted) — cancelled halfway, it leaves its session
// neither as it was nor restarted, with nothing left to clear it away.
func (s *Service) RemoveProject(ctx context.Context, projectID string) error {
	s.beginProjectRemoval(projectID)
	// Held until the project is gone from the store, after which nothing new can
	// start in it anyway.
	defer s.endProjectRemoval(projectID)
	// Only the removal that took the project out of the store tears its restarts
	// down: a second one, landing before they finished, finds the same restarts.
	running, err := s.removeProjectFromStore(projectID)
	if err != nil {
		return ErrProjectNotFound
	}
	if len(running) > 0 {
		// Not waited for here: a cancelled restart may still take a while to stop
		// what it started, and the teardown must outlive the request.
		detached := context.WithoutCancel(ctx)
		s.goBackground(func() { s.removeInterrupted(detached, running) })
	}
	return nil
}

// removeInterrupted waits for cancelled restarts to return, and removes the
// session of each one that was interrupted through its provider.
func (s *Service) removeInterrupted(ctx context.Context, restarts []restartInFlight) {
	for _, restart := range restarts {
		<-restart.running.done
		if !restart.running.interrupted {
			continue
		}
		if err := restart.provider.RemoveSession(ctx, restart.ref); err != nil {
			slog.Warn("removing a restarting session during project removal failed",
				"provider", restart.provider.ID(), "session", restart.ref.SessionID, "error", err)
		}
	}
}

// sessionProvider looks up the session provider that owns a session, by the
// provider ID the session carries.
func (s *Service) sessionProvider(ws domain.Workstream, sessionID string) (SessionProvider, error) {
	for _, sess := range ws.Sessions {
		if sess.ID == sessionID && sess.Provider != "" {
			if p, ok := s.registry.SessionProvider(sess.Provider); ok {
				return p, nil
			}
		}
	}
	return nil, ErrSessionNotFound
}

// SessionSchemasFor returns the session creation options for a workstream,
// grouped by session provider.
func (s *Service) SessionSchemasFor(ctx context.Context, ref WorkstreamRef) ([]SessionProviderSchemas, error) {
	if _, err := s.store.Workstream(ref.ProjectID, ref.WorkstreamID); err != nil {
		return nil, ErrWorkstreamNotFound
	}
	var all []SessionProviderSchemas
	for _, p := range s.registry.SessionProviders() {
		if available, err := p.Available(ctx); err != nil || !available {
			continue
		}
		opts, err := p.SessionSchemas(ctx, ref)
		if err != nil {
			slog.Warn("provider failed to list session schemas", "provider", p.ID(), "error", err)
			continue
		}
		if len(opts) == 0 {
			continue
		}
		all = append(all, SessionProviderSchemas{
			ProviderID: p.ID(),
			Label:      p.Label(),
			Options:    opts,
		})
	}
	return all, nil
}

// StopSession asks the session's owning provider to stop it.
func (s *Service) StopSession(ctx context.Context, ref SessionRef) error {
	ws, err := s.store.Workstream(ref.ProjectID, ref.WorkstreamID)
	if err != nil {
		return ErrWorkstreamNotFound
	}
	p, err := s.sessionProvider(ws, ref.SessionID)
	if err != nil {
		return err
	}
	ss, ok := p.(SessionStartStopper)
	if !ok {
		return ErrCapabilityUnsupported
	}
	if err := ss.StopSession(ctx, ref); err != nil {
		return err
	}
	_ = s.reconciler.ReconcileProject(ctx, ref.ProjectID)
	return nil
}

// StartSession asks the session's owning provider to (re)start it.
func (s *Service) StartSession(ctx context.Context, ref SessionRef) error {
	ws, err := s.store.Workstream(ref.ProjectID, ref.WorkstreamID)
	if err != nil {
		return ErrWorkstreamNotFound
	}
	p, err := s.sessionProvider(ws, ref.SessionID)
	if err != nil {
		return err
	}
	ss, ok := p.(SessionStartStopper)
	if !ok {
		return ErrCapabilityUnsupported
	}
	if err := s.checkConcurrency(concurrencyKey(ref.ProjectID, ref.WorkstreamID), ws, p.Capabilities()); err != nil {
		return err
	}
	if err := ss.StartSession(ctx, ref); errors.Is(err, ErrStartIsRestart) {
		// RestartSession runs the rebuild on the service lifetime, not this request.
		_, err = s.RestartSession(ctx, RestartSessionInput{Session: ref})
		if errors.Is(err, ErrCannotContinue) {
			return fmt.Errorf("cannot start this session safely; use Restart with settings and choose a fresh conversation to restart: %w", err)
		}
		return err
	} else if err != nil {
		return err
	}
	_ = s.reconciler.ReconcileProject(ctx, ref.ProjectID)
	return nil
}

// RemoveSession drops a session from its workstream. Removal is allowed in any
// state: the owning provider tears down the runtime either way.
func (s *Service) RemoveSession(ctx context.Context, ref SessionRef) error {
	ws, err := s.store.Workstream(ref.ProjectID, ref.WorkstreamID)
	if err != nil {
		return ErrWorkstreamNotFound
	}
	// A placeholder row is the daemon's own bookkeeping, not a session any provider
	// has heard of — asking its provider to remove it would fail on an ID that was
	// never handed out. Dropping the row *is* the removal, and for a failed create
	// it is the only disposal there is: nothing else will ever clear it.
	//
	// If the create it stands for is still running, stopping it is part of the
	// removal: the user dismissing the row means they no longer want what it is
	// building, and a build left running would land a session on a row that is
	// gone. Cancelling a create that already finished is a no-op.
	if strings.HasPrefix(ref.SessionID, StartingIDPrefix) {
		s.cancelCreate(ref.SessionID)
		if err := s.store.RemoveEphemeralSession(ref.ProjectID, ref.WorkstreamID, ref.SessionID); err != nil {
			return ErrWorkstreamNotFound
		}
		return nil
	}
	p, err := s.sessionProvider(ws, ref.SessionID)
	if err != nil {
		return err
	}
	s.beginRemoval(ref.SessionID)
	defer s.endRemoval(ref.SessionID)
	if err := p.RemoveSession(ctx, ref); err != nil {
		return err
	}
	_ = s.reconciler.ReconcileProject(ctx, ref.ProjectID)
	return nil
}

// concurrencyKey identifies a workstream for in-flight session accounting.
func concurrencyKey(projectID, workstreamID string) string {
	return projectID + "\x00" + workstreamID
}

// Prompt delivers a one-off text prompt to a session via its owning Prompter.
// It does not re-project; the provider's watcher observes the effect.
func (s *Service) Prompt(ctx context.Context, ref SessionRef, text string) error {
	ws, err := s.store.Workstream(ref.ProjectID, ref.WorkstreamID)
	if err != nil {
		return ErrWorkstreamNotFound
	}
	p, err := s.sessionProvider(ws, ref.SessionID)
	if err != nil {
		return err
	}
	pr, ok := p.(Prompter)
	if !ok {
		return ErrCapabilityUnsupported
	}
	// A stopped session has no live terminal to type into.
	for _, sess := range ws.Sessions {
		if sess.ID == ref.SessionID && sess.State == domain.SessionStopped {
			return ErrSessionStopped
		}
	}
	return pr.Prompt(ctx, ref, text)
}

// SendKey delivers a single normalized key event (e.g. "up", "enter") to a
// session via its owning Prompter. An unknown key is ErrInvalidKey; a stopped
// session is rejected like Prompt.
func (s *Service) SendKey(ctx context.Context, ref SessionRef, key string) error {
	if !ValidSessionKey(key) {
		return fmt.Errorf("%q: %w", key, ErrInvalidKey)
	}
	ws, err := s.store.Workstream(ref.ProjectID, ref.WorkstreamID)
	if err != nil {
		return ErrWorkstreamNotFound
	}
	p, err := s.sessionProvider(ws, ref.SessionID)
	if err != nil {
		return err
	}
	pr, ok := p.(Prompter)
	if !ok {
		return ErrCapabilityUnsupported
	}
	for _, sess := range ws.Sessions {
		if sess.ID == ref.SessionID && sess.State == domain.SessionStopped {
			return ErrSessionStopped
		}
	}
	return pr.SendKey(ctx, ref, key)
}

// activeSessions counts a workstream's non-terminal (slot-occupying) sessions.
//
// Starting placeholders are skipped, because the creation each one stands for is
// already counted by the in-flight reservation it was created alongside. Counting
// both would charge one create twice and refuse a legitimate session on a
// provider that allows several.
func activeSessions(ws domain.Workstream) int {
	n := 0
	for _, sess := range ws.Sessions {
		if IsStartingPlaceholder(sess) {
			continue
		}
		if !sess.State.Terminal() {
			n++
		}
	}
	return n
}

// activeSessionsLocked is activeSessions without the sessions whose restart holds
// a reservation of its own: one counts once, whatever state it is reported in
// while it comes back up. Caller holds s.mu.
func (s *Service) activeSessionsLocked(workstream domain.Workstream) int {
	active := activeSessions(workstream)
	for _, session := range workstream.Sessions {
		if s.restarting[session.ID].reservedSlot && !session.State.Terminal() {
			active--
		}
	}
	return active
}

// checkConcurrency reports whether the workstream is at the provider's
// MaxConcurrentSessions limit, counting stored non-terminal sessions plus
// in-flight reservations. It reserves nothing.
func (s *Service) checkConcurrency(key string, ws domain.Workstream, caps Capabilities) error {
	if caps.MaxConcurrentSessions <= 0 {
		return nil
	}
	s.mu.Lock()
	active := s.inflight[key] + s.activeSessionsLocked(ws)
	s.mu.Unlock()
	if active >= caps.MaxConcurrentSessions {
		return fmt.Errorf("%w: this workstream allows at most %d concurrent session(s)", ErrConcurrencyLimit, caps.MaxConcurrentSessions)
	}
	return nil
}

// reserveSlot atomically checks the cap and, if there is room, records an
// in-flight reservation. Released by releaseSlot.
func (s *Service) reserveSlot(key string, ws domain.Workstream, caps Capabilities) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reserveSlotLocked(key, ws, caps)
}

// reserveSlotLocked is reserveSlot for a caller holding s.mu.
func (s *Service) reserveSlotLocked(key string, ws domain.Workstream, caps Capabilities) error {
	if caps.MaxConcurrentSessions > 0 {
		if s.inflight[key]+s.activeSessionsLocked(ws) >= caps.MaxConcurrentSessions {
			return fmt.Errorf("%w: this workstream allows at most %d concurrent session(s)", ErrConcurrencyLimit, caps.MaxConcurrentSessions)
		}
	}
	s.inflight[key]++
	return nil
}

// releaseSlot drops one in-flight reservation for a workstream.
func (s *Service) releaseSlot(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.releaseSlotLocked(key)
}

// releaseSlotLocked is releaseSlot for a caller holding s.mu.
func (s *Service) releaseSlotLocked(key string) {
	if s.inflight[key] > 0 {
		s.inflight[key]--
		if s.inflight[key] == 0 {
			delete(s.inflight, key)
		}
	}
}

func (s *Service) projectRef(projectID string) (ProjectRef, bool) {
	for _, p := range s.store.Projects() {
		if p.ID == projectID {
			return ProjectRef{ID: p.ID, Path: p.Path, GitBacked: p.GitBacked}, true
		}
	}
	return ProjectRef{}, false
}
