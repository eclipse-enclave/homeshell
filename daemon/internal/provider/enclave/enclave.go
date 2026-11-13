package enclave

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/agentmodel"
	"github.com/eclipsesource/homeshell/daemon/internal/agentspec"
	"github.com/eclipsesource/homeshell/daemon/internal/domain"
	"github.com/eclipsesource/homeshell/daemon/internal/pathutil"
	"github.com/eclipsesource/homeshell/daemon/internal/provider"
	"github.com/eclipsesource/homeshell/daemon/internal/sbridge"
	"github.com/eclipsesource/homeshell/daemon/internal/store"
	"github.com/eclipsesource/homeshell/daemon/internal/terminal"
)

// watchInterval is the fallback hot snapshot cadence without injected settings.
// This hot path queries inventory only for bridge loss; the independent drift
// check also queries inventory every driftCheckInterval.
const watchInterval = 2 * time.Second

// defaultTool is the profile a session falls back to: the one every Enclave
// installation has, used when `enclave tools` cannot be asked and when a create
// request names no option. It is a fact about Enclave's images rather than a user
// setting, which is why it is here and not a row — what that profile *offers* is
// rows (sessionconfig.go).
const defaultTool = "claude"

// containerShell is the shell OpenShell drops the user into inside a container.
// Enclave's images ship bash, so it is not probed per container.
const containerShell = "bash"

// Compile-time checks.
var (
	_ provider.SessionStartStopper  = (*Provider)(nil)
	_ provider.WatchedDiscoverer    = (*Provider)(nil)
	_ provider.SessionRestarter     = (*Provider)(nil)
	_ provider.ContinueRefuser      = (*Provider)(nil)
	_ provider.LaunchObserver       = (*Provider)(nil)
	_ provider.OptionSchemaProvider = (*Provider)(nil)
	_ provider.SessionProvider      = (*Provider)(nil)
	_ provider.Watcher              = (*Provider)(nil)
	_ provider.SettingsAware        = (*Provider)(nil)
	_ provider.Prompter             = (*Provider)(nil)
	_ provider.ShellOpener          = (*Provider)(nil)
	_ provider.ModelCatalogAware    = (*Provider)(nil)
	_ provider.AgentSpecAware       = (*Provider)(nil)
)

// Provider wraps the enclave CLI to implement HomeShell's session-provider
// contract. Each session is one container running one agent in a working
// directory, which is the join key the daemon matches to a workstream. It
// uses the CLI for container lifecycle and host bridges for new sessions.
// It never talks to Docker directly.
type Provider struct {
	bridges *hostBridges
	cli     CLI
	term    terminal.Launcher
	store   *store.Store
	provider.SettingsHolder

	// mu protects the watch notification sink; bridges.mu protects the rest.
	mu   sync.Mutex
	sink provider.Sink

	// workstreamsRoot returns the default workstreams root, the only place whose
	// directories a session asks Enclave to mount despite its sensitive-mount guard
	// (sensitiveDirsFor). Tests point it at a temporary directory.
	workstreamsRoot func() (string, error)

	// models remembers what a running container answered when asked which models
	// its tool has, keyed by tool (sessionconfig.go).
	models agentmodel.Cache

	// catalog is the user's authored model list, shared with every other provider
	// that launches the same agent (internal/agentcatalog). Injected after
	// construction (the provider is built from init(), before there is a catalog),
	// so it is nil until then, and nil offers no list rather than an old one.
	catalog agentmodel.Catalog

	// specs is the user's authored agent descriptions — which settings a new session
	// offers and how each value reaches the tool's command line (sessionconfig.go).
	// Shared with every other provider that launches the same agent, and injected
	// after construction for the same reason as the catalog, so it is nil until then:
	// a profile nothing describes gets a bare option, which is what an unlisted
	// profile always got.
	specs agentspec.Source

	// bases caches what Enclave resolves for a tool in a directory — the sandbox
	// settings' defaults (sandbox.go).
	bases sandboxBases
	now   func() time.Time

	// inventoryEvery overrides driftCheckInterval when positive (inventory.go).
	inventoryEvery time.Duration
}

// restartProjection publishes the in-flight replacement of an owned session.
type restartProjection struct {
	workingDir string
	session    domain.Session
	// projectDir is the project Enclave resolved the session's container to, which may own it (ownsContainerIn).
	projectDir string
	// target is the container the restart brings back: the session's own, or one under a new name
	// (restartNames), whose record the projection stands in for until the restart ends.
	target string
}

// renames reports whether the restart brings the session back under a new name.
func (restarting restartProjection) renames() bool { return restarting.target != restarting.session.ID }

// SetStore gives the provider access to the daemon's store so it can match a
// discovered container's directory against the working directories of the
// workstreams the daemon knows about.
func (p *Provider) SetStore(s *store.Store) { p.store = s }

// SetModelCatalog wires the user's model catalog (implements
// provider.ModelCatalogAware).
func (p *Provider) SetModelCatalog(c agentmodel.Catalog) { p.catalog = c }

// SetAgentSpecs wires the user's agent descriptions (implements
// provider.AgentSpecAware).
func (p *Provider) SetAgentSpecs(s agentspec.Source) { p.specs = s }

func (p *Provider) ID() string    { return "enclave" }
func (p *Provider) Label() string { return "Eclipse Enclave" }

func (p *Provider) Available(ctx context.Context) (bool, error) {
	available, err := p.cli.Available(ctx)
	if err != nil || !available {
		return available, err
	}
	if err := p.bridges.runtime.Available(ctx, binaryName); err != nil {
		return false, err
	}
	return true, nil
}

func (p *Provider) Capabilities() provider.Capabilities {
	return provider.Capabilities{
		OpenModes: []string{"terminal"},
		// Unlimited (0), like the Local provider: one container holds one agent,
		// but nothing makes a *workstream* hold one container. Several may run in
		// the same working directory, each its own session, because every
		// per-session path here is keyed on the container name (the session ID)
		// rather than on the workstream — and because CreateSession gives each
		// container a unique `--name`, without which same-tool containers in one
		// directory would collide on the CLI's deterministic
		// `enclave-<tool>-<projectHash>` name.
		//
		// Whether running two agents over one working copy is a good idea is the
		// user's call, not the provider's: a workstream is the isolation boundary,
		// and the provider's job is to report what it can do.
		MaxConcurrentSessions: 0,
	}
}

// SessionSchemas returns the available tool profiles as session options, each
// carrying the launch settings for that tool (the model, and every other knob the
// user's rows describe for the same agent — see sessionconfig.go) so the choices are
// made when the session is created rather than typed into the agent afterwards.
func (p *Provider) SessionSchemas(ctx context.Context, ref provider.WorkstreamRef) ([]provider.SessionSchemaOption, error) {
	tools, err := p.cli.Tools(ctx)
	if err != nil || len(tools) == 0 {
		// The one profile every Enclave installation has, offered so a create form is
		// never empty. Its settings come from the same rows as any other tool's.
		return []provider.SessionSchemaOption{{
			ID:     defaultTool,
			Label:  "Claude",
			Schema: p.listedSchema(ctx, ref, defaultTool),
		}}, nil
	}
	// Every tool's schema needs the one feature catalogue: asked once, up front.
	if dir, ok := p.workingDir(ref); ok {
		_, _ = p.enclaveFeatures(ctx, dir)
	}
	// One schema per tool, each asking Enclave what it resolves for that tool, so
	// they are built side by side: the form waits on the slowest, not on the sum.
	options := make([]provider.SessionSchemaOption, len(tools))
	var wait sync.WaitGroup
	for i, tool := range tools {
		wait.Add(1)
		go func() {
			defer wait.Done()
			// A tool that can list its own models is asked, through a container of it
			// that is already running (sessionconfig.go); one that cannot offers the
			// user's authored rows for the same agent.
			options[i] = provider.SessionSchemaOption{
				ID: tool.Name, Label: tool.Label, Schema: p.listedSchema(ctx, ref, tool.Name),
			}
		}()
	}
	wait.Wait()
	return options, nil
}

// CreateSession starts an Enclave container for the workstream in its working
// directory.
func (p *Provider) CreateSession(ctx context.Context, req provider.CreateSessionRequest) (domain.Session, error) {
	if req.WorkingDir == "" {
		return domain.Session{}, fmt.Errorf("enclave provider requires a working directory")
	}

	tool := req.OptionID
	if tool == "" {
		tool = defaultTool
	}
	p.forgetBase(req.WorkingDir, tool)
	opts, err := p.launchOpts(ctx, req.WorkingDir, tool, req.Config)
	if err != nil {
		return domain.Session{}, err
	}

	// The build/startup output goes straight to the daemon's progress sink, so the
	// user watches the container come up instead of staring at a spinner for the
	// minutes an image build can take.
	//
	// SessionName is what makes more than one session per workstream possible. Left
	// empty, the CLI names the container `enclave-<tool>-<projectHash>` — derived
	// from the directory, so a second same-tool container in the same workstream
	// asks Docker for a name that is already taken. A unique name per session moves
	// the collision out of the way, and it is the name `enclave exec --name` uses to
	// target this container specifically (parseSessionName reads it back off the
	// container name).
	opts.SessionName = newSessionName()
	opts.Progress = req.Progress
	containerName, err := p.cli.Run(ctx, req.WorkingDir, opts)
	if err != nil {
		return domain.Session{}, fmt.Errorf("start enclave container: %w", err)
	}

	container := Container{Name: containerName, Tool: tool, Directory: req.WorkingDir, Status: "running", Background: true}
	if err := p.registerBridge(ctx, bridgeRecord{Container: container}); err != nil {
		return domain.Session{}, err
	}
	return p.bridgeSession(bridgeRecord{Container: container}), nil
}

// sensitiveDirsFor returns the directories a session in workingDir asks Enclave
// to mount despite its sensitive-mount guard: workingDir itself, as given, when it
// is a default-root workstream directory, and nothing otherwise. The default
// workstreams root lies in Homeshell's data directory, which the guard refuses
// (~/.local/share, $XDG_DATA_HOME, ~/Library/Application Support), so without
// this no session could start, or restart, in a worktree or copy made there.
// Homeshell vouches for nothing else: a directory elsewhere is the user's to
// exempt in Enclave's own config.
//
// The decision is by path alone, so it covers a directory however it got there.
// When either path cannot be resolved there is no exemption, and Enclave's own
// refusal reaches the user.
func (p *Provider) sensitiveDirsFor(workingDir string) []string {
	root, err := p.workstreamsRoot()
	if err != nil || !strictlyInside(root, workingDir) {
		return nil
	}
	return []string{workingDir}
}

// strictlyInside reports whether dir lies below root, not at it, with symlinks
// resolved on both sides. Resolving matters because Enclave resolves the path it
// is told to exempt: a link in the root leading to ~/.ssh would otherwise exempt
// ~/.ssh. pathutil.Within compares path components, not string prefixes, which
// keeps <root>-other out.
func strictlyInside(root, dir string) bool {
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return false
	}
	resolvedDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return false
	}
	return resolvedDir != resolvedRoot && pathutil.Within(resolvedDir, resolvedRoot)
}

// sessionNamePrefix marks a container as one HomeShell started, so a `enclave ps`
// listing says where a container came from.
const sessionNamePrefix = "hs-"

// newSessionName returns the per-session name for a new container: short, unique,
// and safe in both a Docker container name and an `enclave exec --name` argument.
//
// It must round-trip through parseSessionName, which takes everything after the
// 12-character project hash — so a dash inside the name is fine. The timestamp
// fallback exists because a name is not optional here: falling back to "" would
// restore the deterministic container name and with it the collision this exists
// to avoid.
func newSessionName() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%s%d", sessionNamePrefix, time.Now().UnixNano())
	}
	return fmt.Sprintf("%s%x", sessionNamePrefix, b)
}

// RemoveSession withdraws any in-flight restart, then removes the owned
// container, bridge and ownership record. Failed cleanup remains retryable.
func (p *Provider) RemoveSession(ctx context.Context, ref provider.SessionRef) error {
	p.bridges.mu.Lock()
	restarting, wasRestarting := p.bridges.restarting[ref.SessionID]
	delete(p.bridges.restarting, ref.SessionID)
	p.bridges.mu.Unlock()
	// One under a new name may have left its container behind, interrupted; one still running forgets it too.
	if wasRestarting && restarting.renames() {
		p.forgetRestarted(ctx, restarting.target)
	}
	return p.removeBridge(ctx, ref.SessionID, true)
}

// SessionState captures an owned bridge session or reports its restart state.
func (p *Provider) SessionState(ctx context.Context, ref provider.SessionRef) (domain.Session, error) {
	p.bridges.mu.Lock()
	restarting, ok := p.bridges.restarting[ref.SessionID]
	p.bridges.mu.Unlock()
	if ok {
		return restarting.session, nil
	}
	record, ok := p.bridgeRecordFor(ref.SessionID)
	if !ok {
		return domain.Session{}, fmt.Errorf("enclave session %q not found", ref.SessionID)
	}
	if err := p.checkProject(ref, record); err != nil {
		return domain.Session{}, err
	}
	p.sampleBridge(ctx, ref.SessionID, p.detectionAgents(), false)
	bridges := p.bridges
	bridges.mu.Lock()
	defer bridges.mu.Unlock()
	record, ok = bridges.records[ref.SessionID]
	if !ok {
		return domain.Session{}, fmt.Errorf("enclave session %q not found", ref.SessionID)
	}
	if session, ok := bridges.observed[ref.SessionID]; ok {
		return bridges.withOutdated(session), nil
	}
	return bridges.withOutdated(p.bridgeSession(record)), nil
}

// checkProject rejects a ref whose project does not own the session's directory.
func (p *Provider) checkProject(ref provider.SessionRef, record bridgeRecord) error {
	path, owned := p.projectPath(ref.ProjectID)
	if !owned || !ownsContainerIn(p.storeProjects(), provider.ProjectRef{ID: ref.ProjectID, Path: path}, record.Container) {
		return fmt.Errorf("unknown project %q", ref.ProjectID)
	}
	return nil
}

// projectPath resolves a project's filesystem path from the store.
func (p *Provider) projectPath(projectID string) (string, bool) {
	if p.store == nil {
		return "", false
	}
	for _, proj := range p.store.Projects() {
		if proj.ID == projectID {
			return proj.Path, true
		}
	}
	return "", false
}

// Open launches a terminal window attached to the container.
func (p *Provider) Open(ctx context.Context, ref provider.SessionRef, mode string) (provider.OpenResult, error) {
	if mode != "terminal" {
		return provider.OpenResult{}, fmt.Errorf("unsupported open mode %q", mode)
	}
	record, err := p.liveBridgeRecord(ref.SessionID)
	if errors.Is(err, provider.ErrCapabilityUnsupported) && record.takeOver() {
		// Nothing of a session in the user's terminal can be attached; a shell in its container is what opens. Not
		// while it restarts: its container is being stopped and run again.
		return p.OpenShell(ctx, ref)
	}
	if err != nil {
		return provider.OpenResult{}, err
	}
	if err := p.checkProject(ref, record); err != nil {
		return provider.OpenResult{}, err
	}
	// A missing host socket cannot be attached to; let Watch recover it. Other
	// uncertain captures need not prevent opening a potentially live bridge.
	if _, err := p.captureSnapshot(ctx, record.target()); errors.Is(err, sbridge.ErrNotServing) {
		return provider.OpenResult{}, fmt.Errorf("cannot open enclave terminal: %w", err)
	}
	cmd := terminal.HoldOnFailure(p.bridges.runtime.AttachCommand(record.target())...)
	if err := p.term.Launch(context.Background(), cmd, ref.SessionID); err != nil {
		return provider.OpenResult{}, fmt.Errorf("open terminal: %w", err)
	}
	return provider.OpenResult{Mode: "terminal", Data: marshalJSON(map[string]string{"status": "launched"})}, nil
}

// OpenShell opens an interactive shell inside the session's container
// (provider.ShellOpener). It uses `enclave exec … -- <shell>`, run from the
// directory recorded for the session; Enclave resolves the container name verbatim,
// which covers a tool's unnamed default container. `enclave shell` would build a
// new container. An ended session yields ErrNoShellEnvironment. An exec that fails once the window is
// up holds the window open on its error; a shell the user exits does not.
func (p *Provider) OpenShell(ctx context.Context, ref provider.SessionRef) (provider.OpenResult, error) {
	record, ok := p.bridgeRecordFor(ref.SessionID)
	if !ok {
		return provider.OpenResult{}, provider.ErrNoShellEnvironment
	}
	observed, err := p.SessionState(ctx, ref)
	if err != nil || observed.State == domain.SessionStopped || observed.State == domain.SessionDone || observed.State == domain.SessionError {
		return provider.OpenResult{}, provider.ErrNoShellEnvironment
	}
	args := append([]string{binaryName}, execPrefix(record.Container.Name, record.Container.Tool)...)
	args = append(args, containerShell)
	if err := p.term.Launch(ctx, terminal.HoldOnStartupFailureIn(record.Container.Directory, args...), ref.SessionID); err != nil {
		return provider.OpenResult{}, fmt.Errorf("open terminal: %w", err)
	}
	return provider.OpenResult{Mode: "terminal", Data: marshalJSON(map[string]string{
		"target": "container", "container": ref.SessionID, "shell": containerShell,
	})}, nil
}

// DiscoverSessions reports the containers belonging to a project, each tagged
// with the directory it runs in so the daemon can match it to a workstream.
func (p *Provider) DiscoverSessions(ctx context.Context, project provider.ProjectRef) ([]provider.DiscoveredSession, error) {
	projects := p.storeProjects()
	p.sampleBridges(ctx, p.detectionAgents(), func(container Container) bool { return ownsContainerIn(projects, project, container) }, false)
	return p.projectBridges(project, projects), nil
}

func (p *Provider) storeProjects() []domain.Project {
	if p.store == nil {
		return nil
	}
	return p.store.Projects()
}

// refOf is the provider's reference to a stored project.
func refOf(project domain.Project) provider.ProjectRef {
	return provider.ProjectRef{ID: project.ID, Path: project.Path}
}

// ownsContainerIn reports whether the project owns a container, against a store snapshot: it runs in the project
// folder or a workstream's working directory, or Enclave resolved it to the project. A container belongs to the
// project that owns its directory; only when none does, to the project Enclave resolved it to.
func ownsContainerIn(projects []domain.Project, project provider.ProjectRef, container Container) bool {
	if ownsDirIn(projects, project, container.Directory) {
		return true
	}
	if container.ProjectDir == "" || !domain.SameFolder(container.ProjectDir, project.Path) {
		return false
	}
	// A folder another project owns (a linked worktree added as its own project) is that project's session.
	return !slices.ContainsFunc(projects, func(other domain.Project) bool {
		return ownsDirIn(projects, refOf(other), container.Directory)
	})
}

// ownsDirIn reports whether a directory is the project's own or a workstream's working directory.
func ownsDirIn(projects []domain.Project, project provider.ProjectRef, sessionDir string) bool {
	if domain.SameFolder(sessionDir, project.Path) {
		return true
	}
	for _, candidate := range projects {
		if candidate.ID != project.ID {
			continue
		}
		for _, workstream := range candidate.Workstreams {
			if domain.SameFolder(workstream.WorkingDir, sessionDir) {
				return true
			}
		}
	}
	return false
}

// Prompt delivers a one-off line of text to the session's agent over the
// host bridge.
// ref.SessionID is the owned container name.
func (p *Provider) Prompt(ctx context.Context, ref provider.SessionRef, text string) error {
	record, err := p.liveBridgeRecord(ref.SessionID)
	if err != nil {
		return err
	}
	return p.bridges.runtime.SendInput(ctx, record.target(), text)
}

// SendKey delivers a single named key event to a session's agent, through the
// same bridge path as Prompt.
func (p *Provider) SendKey(ctx context.Context, ref provider.SessionRef, key string) error {
	record, err := p.liveBridgeRecord(ref.SessionID)
	if err != nil {
		return err
	}
	return p.bridges.runtime.SendKey(ctx, record.target(), key)
}

// Watch observes owned host bridges on the hot loop and independently checks
// the CLI inventory every driftCheckInterval, for launch-config drift and CLI
// sessions to adopt or end.
func (p *Provider) Watch(ctx context.Context, sink provider.Sink) {
	p.mu.Lock()
	p.sink = sink
	p.mu.Unlock()
	go func() {
		if p.checkInventory(ctx) {
			sink.Changed()
		}
		provider.WatchLoop(ctx, sink, nil, p.inventoryInterval(), p.checkInventory)
	}()
	provider.WatchLoop(ctx, sink, p.RefreshInterval, watchInterval, p.changeDetector())
}

func (p *Provider) clock() time.Time {
	if p.now != nil {
		return p.now()
	}
	return time.Now()
}
