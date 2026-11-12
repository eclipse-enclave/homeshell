package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
)

// sessionOptionsOf lists a provider's session options for a workstream.
//
// A provider that cannot list them does not fail the caller: the options carry a
// label and an optional schema, and refusing to start the session the user asked
// for because they could not be listed would be the worse answer.
func sessionOptionsOf(ctx context.Context, sessionProvider SessionProvider, ref WorkstreamRef) []SessionSchemaOption {
	options, err := sessionProvider.SessionSchemas(ctx, ref)
	if err != nil {
		slog.Debug("listing a provider's session options failed; going on without them",
			"provider", sessionProvider.ID(), "error", err)
		return nil
	}
	return options
}

// restartTarget is what a session is restarted as: the option it was last
// started as, or for a session with no record the option its agent names, with
// the config a bare restart replays and the option's schema. An option its
// provider no longer offers is refused with ErrOptionNotOffered. So is one whose
// schema the provider cannot report, with ErrProviderUnavailable: a form opened,
// or a config checked, without the schema would restart the session on defaults
// and record them in place of its settings.
//
// A recorded option is looked up alone where the provider can build one
// (OptionSchemaProvider): listing them all can mean asking about every one. The
// options listed otherwise are returned too, for continueRefusal; nil when they
// were not.
func (s *Service) restartTarget(ctx context.Context, sessionProvider SessionProvider, ref SessionRef, session domain.Session) (domain.LaunchConfig, json.RawMessage, []SessionSchemaOption, error) {
	workstreamRef := WorkstreamRef{ProjectID: ref.ProjectID, WorkstreamID: ref.WorkstreamID}
	launch, recorded := s.recordedLaunch(sessionProvider, ref.SessionID)
	if single, ok := sessionProvider.(OptionSchemaProvider); ok && recorded {
		schema, err := single.SessionSchema(ctx, workstreamRef, launch.OptionID)
		return launch, schema, nil, settingsUnreadable(sessionProvider, err)
	}
	options, err := sessionProvider.SessionSchemas(ctx, workstreamRef)
	if err != nil {
		return launch, nil, nil, settingsUnreadable(sessionProvider, err)
	}
	if !recorded {
		launch = domain.LaunchConfig{ProviderID: sessionProvider.ID(), OptionID: restartOptionID(session, options)}
	}
	// A session whose option cannot be told is not refused here: it accepts no
	// settings (validateAgainstOption), and its provider starts it as its own choice.
	if launch.OptionID == "" {
		return launch, nil, options, nil
	}
	for _, option := range options {
		if option.ID == launch.OptionID {
			return launch, option.Schema, options, nil
		}
	}
	return launch, nil, nil, fmt.Errorf("%w (%q)", ErrOptionNotOffered, launch.OptionID)
}

// settingsUnreadable is err, or for a provider that failed to report a session's
// settings, and did not say it is unavailable, ErrProviderUnavailable saying so.
func settingsUnreadable(sessionProvider SessionProvider, err error) error {
	if err == nil || errors.Is(err, ErrOptionNotOffered) || errors.Is(err, ErrProviderUnavailable) {
		return err
	}
	return fmt.Errorf("%w: could not read %s's settings for the session; try again (%v)", ErrProviderUnavailable, sessionProvider.Label(), err)
}

// validateAgainstOption checks config against the schema of the option it is for.
// An option with no schema, or one not among options, accepts any config. A config
// that names no option accepts no settings — an empty object, as a form with no
// fields sends, or none: there is no schema to check them against, and the
// provider would start it as an option of its own choosing.
func validateAgainstOption(options []SessionSchemaOption, optionID string, config json.RawMessage) error {
	if optionID == "" {
		if absentConfig(config) || emptyObject(config) {
			return nil
		}
		return &ValidationError{Message: "a session config needs the option it is for (optionId)", Fields: map[string]string{}}
	}
	for _, option := range options {
		if option.ID == optionID && len(option.Schema) > 0 {
			return ValidateConfig(option.Schema, config)
		}
	}
	return nil
}

// RestartSettings is what a restart form opens with: what the session was last
// started with — the option and config a bare restart would use, so the two never
// disagree — and that option's schema, null when it has none. ContinueRefusal is
// why a restart cannot continue the session's conversation, so only a fresh one
// is accepted; empty when it can.
type RestartSettings struct {
	domain.LaunchConfig
	Schema          json.RawMessage `json:"schema"`
	ContinueRefusal string          `json:"continueRefusal,omitempty"`
}

// SessionLaunchConfig returns what a session was last started with, and the
// schema to change it with. For a session HomeShell has no record of — one it
// adopted, or one started before launch configs were recorded — it is the option a
// restart would start it as, with no config. A session that cannot be restarted
// is refused as a restart of it would be, so the form for it never opens.
func (s *Service) SessionLaunchConfig(ctx context.Context, ref SessionRef) (RestartSettings, error) {
	workstream, err := s.store.Workstream(ref.ProjectID, ref.WorkstreamID)
	if err != nil {
		return RestartSettings{}, ErrWorkstreamNotFound
	}
	session, ok := findSession(workstream, ref.SessionID)
	if !ok {
		return RestartSettings{}, ErrSessionNotFound
	}
	if err := s.restartRefusal(ref, session, ""); err != nil {
		return RestartSettings{}, err
	}
	sessionProvider, err := s.sessionProvider(workstream, ref.SessionID)
	if err != nil {
		return RestartSettings{}, err
	}
	launch, schema, options, err := s.restartTarget(ctx, sessionProvider, ref, session)
	if err != nil {
		return RestartSettings{}, err
	}
	if err := sessionProvider.(SessionRestarter).CheckRestart(ref, launch.OptionID); err != nil {
		return RestartSettings{}, err
	}
	settings := RestartSettings{LaunchConfig: launch, Schema: schema}
	if err := s.continueRefusal(ctx, sessionProvider, ref, workstream, launch.OptionID, options); err != nil {
		settings.ContinueRefusal = err.Error()
	}
	return settings, nil
}

// continueRefusal is why a restart of a session as optionID cannot continue its
// conversation, nil when it can: its provider's answer, when it is a
// ContinueRefuser, given the options of the provider's other sessions working in
// the same directory — in its workstream, or another with the same working
// directory, in any project. A placeholder counts while its create is in flight,
// as the option it was asked for; a failed create's stands for no session.
//
// options are the provider's, as restartTarget listed them; they are listed
// here, once, only when it did not and a neighbour needs them (sessionOptionID).
func (s *Service) continueRefusal(ctx context.Context, sessionProvider SessionProvider, ref SessionRef, workstream domain.Workstream, optionID string, options []SessionSchemaOption) error {
	refuser, ok := sessionProvider.(ContinueRefuser)
	if !ok {
		return nil
	}
	listOptions := sync.OnceValue(func() []SessionSchemaOption {
		if options != nil {
			return options
		}
		return sessionOptionsOf(ctx, sessionProvider, WorkstreamRef{ProjectID: ref.ProjectID, WorkstreamID: ref.WorkstreamID})
	})
	seen := map[string]bool{ref.SessionID: true}
	var neighbourOptionIDs []string
	for _, project := range s.store.Projects() {
		for _, other := range project.Workstreams {
			own := project.ID == ref.ProjectID && other.ID == ref.WorkstreamID
			if !own && !domain.SameFolder(other.WorkingDir, workstream.WorkingDir) {
				continue
			}
			for _, session := range other.Sessions {
				if session.Provider != sessionProvider.ID() || seen[session.ID] {
					continue
				}
				seen[session.ID] = true
				if neighbourOptionID, ok := s.sessionOptionID(sessionProvider, session, listOptions); ok {
					neighbourOptionIDs = append(neighbourOptionIDs, neighbourOptionID)
				}
			}
		}
	}
	return refuser.ContinueRefusal(optionID, workstream.WorkingDir, neighbourOptionIDs)
}

// sessionOptionID is the option a session runs as: for a placeholder, the one
// its create in flight was asked for, and none once the create is over; else the
// one it was last started as or, with no record, the one its agent names among
// the provider's options (restartOptionID), which only such a session asks
// listOptions for. A nil listOptions takes the agent as it is named.
func (s *Service) sessionOptionID(sessionProvider SessionProvider, session domain.Session, listOptions func() []SessionSchemaOption) (string, bool) {
	if IsStartingPlaceholder(session) {
		s.mu.Lock()
		defer s.mu.Unlock()
		create, ok := s.creating[session.ID]
		return create.optionID, ok
	}
	if launch, recorded := s.recordedLaunch(sessionProvider, session.ID); recorded {
		return launch.OptionID, true
	}
	var options []SessionSchemaOption
	if listOptions != nil {
		options = listOptions()
	}
	return restartOptionID(session, options), true
}

// restartable is whether a session can be restarted, as far as restartRefusal
// tells.
func (s *Service) restartable(ref SessionRef, session domain.Session, optionID string) bool {
	return s.restartRefusal(ref, session, optionID) == nil
}

// restartRefusal is why a session cannot be restarted, judged without costly
// provider calls, nil when nothing here refuses it: a placeholder
// (placeholderRefusal), a session starting or restarting (ErrSessionStarting),
// a provider that is no SessionRestarter (ErrCapabilityUnsupported), or its
// CheckRestart refusing optionID — or, when empty, the option the session runs
// as, its agent taken as named.
func (s *Service) restartRefusal(ref SessionRef, session domain.Session, optionID string) error {
	if IsStartingPlaceholder(session) {
		return placeholderRefusal(session)
	}
	if session.State == domain.SessionStarting || s.isRestarting(session.ID) {
		return ErrSessionStarting
	}
	sessionProvider, ok := s.registry.SessionProvider(session.Provider)
	if !ok {
		return ErrSessionNotFound
	}
	restarter, ok := sessionProvider.(SessionRestarter)
	if !ok {
		return ErrCapabilityUnsupported
	}
	if optionID == "" {
		optionID, _ = s.sessionOptionID(sessionProvider, session, nil)
	}
	return restarter.CheckRestart(ref, optionID)
}

// placeholderRefusal is why a placeholder has no settings to restart with: it
// is still starting, or its create failed and no session ever stood behind it.
func placeholderRefusal(placeholder domain.Session) error {
	if placeholder.State == domain.SessionError {
		return ErrSessionNeverStarted
	}
	return ErrSessionStarting
}

// RestartSessionInput is a request to start a session again with new settings.
// A nil (or JSON null) Config restarts the session with what it was last started
// with.
type RestartSessionInput struct {
	Session SessionRef
	Config  json.RawMessage
	Fresh   bool
}

// absentConfig reports whether a restart request left the config out.
func absentConfig(config json.RawMessage) bool {
	trimmed := bytes.TrimSpace(config)
	return len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null"))
}

// emptyObject reports whether config is a JSON object with no members.
func emptyObject(config json.RawMessage) bool {
	var members map[string]json.RawMessage
	return json.Unmarshal(config, &members) == nil && members != nil && len(members) == 0
}

// recordedLaunch is what a session was last started with, when its provider
// recorded the option it started it as.
func (s *Service) recordedLaunch(sessionProvider SessionProvider, sessionID string) (domain.LaunchConfig, bool) {
	launch, ok := s.store.LaunchConfig(sessionID)
	if !ok || launch.ProviderID != sessionProvider.ID() || launch.OptionID == "" {
		return domain.LaunchConfig{}, false
	}
	return launch, true
}

// RestartSession starts a session again through its provider's SessionRestarter,
// with the given config or, when none is given, the one it was last started with.
// Everything that can refuse the restart runs synchronously — the schema, a
// conversation that cannot be continued (continueRefusal), the concurrency
// limit, the provider's own PrepareRestart — so a bad request is refused before
// anything is torn down; then the restart runs in the background, since it can
// rebuild an image, and what comes back is the session as starting.
// The provider shows it so while it works, and reports a failure on the session.
//
// A session starting, or already restarting, is refused with ErrSessionStarting.
// Removing the session, or its project, cancels its restart (beginRemoval,
// removeProjectFromStore), and so does the daemon shutting down (Shutdown).
func (s *Service) RestartSession(ctx context.Context, in RestartSessionInput) (domain.Session, error) {
	ref := in.Session
	workstream, err := s.store.Workstream(ref.ProjectID, ref.WorkstreamID)
	if err != nil {
		return domain.Session{}, ErrWorkstreamNotFound
	}
	session, ok := findSession(workstream, ref.SessionID)
	if !ok {
		return domain.Session{}, ErrSessionNotFound
	}
	if err := s.restartRefusal(ref, session, ""); err != nil {
		return domain.Session{}, err
	}
	sessionProvider, err := s.sessionProvider(workstream, ref.SessionID)
	if err != nil {
		return domain.Session{}, err
	}
	restarter := sessionProvider.(SessionRestarter)
	if available, err := sessionProvider.Available(ctx); err != nil || !available {
		return domain.Session{}, ErrProviderUnavailable
	}

	launch, schema, options, err := s.restartTarget(ctx, sessionProvider, ref, session)
	if err != nil {
		return domain.Session{}, err
	}
	optionID := launch.OptionID
	config := in.Config
	replayed := absentConfig(config)
	if replayed {
		config = launch.Config
	}
	if err := validateAgainstOption([]SessionSchemaOption{{ID: optionID, Schema: schema}}, optionID, config); err != nil {
		var invalid *ValidationError
		if replayed && errors.As(err, &invalid) {
			return domain.Session{}, &ValidationError{
				Message: "the settings the session was last started with are no longer valid; restart it with new settings",
				Fields:  invalid.Fields,
				Err:     err,
			}
		}
		return domain.Session{}, err
	}
	if !in.Fresh {
		if err := s.continueRefusal(ctx, sessionProvider, ref, workstream, optionID, options); err != nil {
			return domain.Session{}, err
		}
	}

	restartCtx, cancel := context.WithCancel(s.lifetime)
	if err := s.registerRestart(ref, sessionProvider, cancel); err != nil {
		cancel()
		return domain.Session{}, err
	}
	// Removed while it was being validated, before registerRestart could refuse it.
	if workstream, err = s.store.Workstream(ref.ProjectID, ref.WorkstreamID); err == nil {
		session, ok = findSession(workstream, ref.SessionID)
	}
	if err != nil || !ok {
		s.finishedRestart(ref)
		return domain.Session{}, ErrSessionNotFound
	}
	// A session that is not running holds no concurrency slot, and starting it again
	// takes one, as StartSession does.
	if session.State.Terminal() {
		if err := s.reserveRestartSlot(ref, workstream, sessionProvider.Capabilities()); err != nil {
			s.finishedRestart(ref)
			return domain.Session{}, err
		}
	}
	run, err := restarter.PrepareRestart(ctx, RestartSessionRequest{
		Session:    ref,
		WorkingDir: workstream.WorkingDir,
		OptionID:   optionID,
		Config:     config,
		Fresh:      in.Fresh,
	})
	if err != nil {
		s.finishedRestart(ref)
		return domain.Session{}, err
	}
	// Recorded before the restart runs rather than after it: if the restart fails to
	// start the session, the form that opens to retry it should hold what the user
	// just asked for.
	previous, hadPrevious := s.store.LaunchConfig(ref.SessionID)
	running := s.markRestartRunning(restartCtx, ref.SessionID)
	if running == nil {
		// Cancelled while the restart was being prepared: by a removal of the
		// session, its workstream or its project, which forgot the record, or by the
		// daemon shutting down. A removal landing between this check and the write
		// below leaves a record of a session that is gone; the next reconcile prunes
		// it (pruneLaunchConfigsLocked).
		s.finishedRestart(ref)
		return domain.Session{}, ErrSessionNotFound
	}
	// A replayed config is kept as recorded: stripped again against today's
	// defaults, a setting chosen to differ from an older default would be lost.
	recorded := domain.LaunchConfig{ProviderID: sessionProvider.ID(), OptionID: optionID, Config: launch.Config}
	if !replayed {
		recorded.Config = withoutDefaults(schema, config, launch.Config)
	}
	if err := s.setRestartLaunchConfig(ref.SessionID, recorded); err != nil {
		slog.Warn("recording the restart settings failed", "session", ref.SessionID, "error", err)
	}
	// A retry looks like a retry: an earlier restart's account of why it left the
	// session as it was would otherwise stay on screen through this one.
	s.clearStaleStatus(ref.ProjectID, ref.WorkstreamID)

	s.goBackground(func() {
		err := run(restartCtx)
		interrupted := errors.Is(err, ErrRestartInterrupted)
		running.interrupted = interrupted
		close(running.done)
		if err != nil && !interrupted && !errors.Is(err, context.Canceled) {
			// The session is as it was, so there is no failed start to show on it; its
			// workstream is where the reason goes. Not for a restart cancelled before it
			// began, which did not fail.
			slog.Warn("restarting a session failed; it was left as it was", "session", ref.SessionID, "error", err)
			shown := s.restartRef(ref.SessionID)
			_ = s.store.SetStatusMessage(shown.ProjectID, shown.WorkstreamID, fmt.Sprintf("Restarting %s failed: %v", session.Title, err))
		}
		// A restart that did not happen — it failed, or was cancelled —
		// leaves the session started with what it was before. One that did is
		// not undone by a cancellation landing after it returned.
		if err != nil {
			s.restoreLaunchConfig(ref.SessionID, previous, hadPrevious)
		}
		// Forgotten first, so the reconcile marks the session restartable again, in
		// every project listing it: one sharing its directory may have been
		// reconciled while it restarted.
		s.finishedRestart(ref)
		// Not as the daemon shuts down, which has no one left to show it to.
		if s.lifetime.Err() == nil {
			s.reconciler.Reconcile(context.WithoutCancel(restartCtx))
		}
	})
	session.State = domain.SessionStarting
	session.Restartable = false
	return session, nil
}

// restoreLaunchConfig puts back what a session was started with before a restart
// that did not happen, unless the session is gone, whose record went with it.
func (s *Service) restoreLaunchConfig(sessionID string, previous domain.LaunchConfig, hadPrevious bool) {
	ref := s.restartRef(sessionID)
	workstream, err := s.store.Workstream(ref.ProjectID, ref.WorkstreamID)
	if err != nil {
		return
	}
	if _, ok := findSession(workstream, sessionID); !ok {
		return
	}
	if hadPrevious {
		_ = s.setRestartLaunchConfig(sessionID, previous)
	} else {
		s.store.ForgetLaunchConfig(sessionID)
	}
}

// restartRef is where a session's restart in flight shows: in the project it
// was started in or, once that project is removed, in one that still lists the
// session (removeProjectFromStore).
func (s *Service) restartRef(sessionID string) SessionRef {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.restarting[sessionID].ref
}

// setRestartLaunchConfig records what a restarting session is started with,
// under the project its restart shows in. Under s.mu, store call included, so a
// project removal moves the restart along with the store (removeProjectFromStore).
func (s *Service) setRestartLaunchConfig(sessionID string, launch domain.LaunchConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.store.SetLaunchConfig(s.restarting[sessionID].ref.ProjectID, sessionID, launch)
}

// restartInFlight is the service's handle on one session restart.
type restartInFlight struct {
	ref      SessionRef
	provider SessionProvider
	cancel   context.CancelFunc
	// reservedSlot is whether the restart reserved a concurrency slot in the
	// session's workstream; while it holds one, the session itself is not counted
	// again.
	reservedSlot bool
	// running is the provider's restart once it runs (markRestartRunning); nil
	// before, when nothing has been touched yet.
	running *restartRun
}

// restartRun is a provider's restart running in the background.
type restartRun struct {
	// done is closed once the provider's restart has returned.
	done chan struct{}
	// interrupted is whether it was cancelled after it began taking the session
	// down (ErrRestartInterrupted), so the session is neither as it was nor
	// restarted. Set before done is closed.
	interrupted bool
}

// registerRestart records the cancel func of a session's restart. It refuses a
// session that already has one in flight, and one being removed, alone or with
// its project: a restart registered after the removal cancelled restarts would
// bring the session back.
func (s *Service) registerRestart(ref SessionRef, sessionProvider SessionProvider, cancel context.CancelFunc) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.removing[ref.SessionID] > 0 || s.removingProjects[ref.ProjectID] > 0 {
		return fmt.Errorf("%w: the session is being removed", ErrSessionNotFound)
	}
	if _, busy := s.restarting[ref.SessionID]; busy {
		return fmt.Errorf("%w: the session is already restarting", ErrSessionStarting)
	}
	s.restarting[ref.SessionID] = restartInFlight{ref: ref, provider: sessionProvider, cancel: cancel}
	return nil
}

// markRestartRunning marks a session's restart as running and returns its run,
// unless it was cancelled, which returns nil: under s.mu, so
// removeProjectFromStore either cancels it before it runs or finds it running.
func (s *Service) markRestartRunning(restartCtx context.Context, sessionID string) *restartRun {
	s.mu.Lock()
	defer s.mu.Unlock()
	restart, ok := s.restarting[sessionID]
	if !ok || restartCtx.Err() != nil {
		return nil
	}
	restart.running = &restartRun{done: make(chan struct{})}
	s.restarting[sessionID] = restart
	return restart.running
}

// reserveRestartSlot reserves a concurrency slot for a registered restart, as
// reserveSlot does for a create.
func (s *Service) reserveRestartSlot(ref SessionRef, workstream domain.Workstream, caps Capabilities) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.reserveSlotLocked(concurrencyKey(ref.ProjectID, ref.WorkstreamID), workstream, caps); err != nil {
		return err
	}
	restart := s.restarting[ref.SessionID]
	restart.reservedSlot = true
	s.restarting[ref.SessionID] = restart
	return nil
}

// isRestarting is whether a session has a restart registered.
func (s *Service) isRestarting(sessionID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, busy := s.restarting[sessionID]
	return busy
}

// finishedRestart forgets a session's restart, and releases its context and the
// concurrency slot it reserved, if any, in the workstream the restart is now
// under (removeProjectFromStore may have moved it).
func (s *Service) finishedRestart(ref SessionRef) {
	s.mu.Lock()
	restart := s.restarting[ref.SessionID]
	delete(s.restarting, ref.SessionID)
	if restart.reservedSlot {
		s.releaseSlotLocked(concurrencyKey(restart.ref.ProjectID, restart.ref.WorkstreamID))
	}
	s.mu.Unlock()
	if restart.cancel != nil {
		restart.cancel()
	}
}

// beginRemoval marks a session as being removed until endRemoval, so no restart
// can start meanwhile, and stops the restart in flight, if any: a restart left
// running would bring the session back.
func (s *Service) beginRemoval(sessionID string) {
	s.mu.Lock()
	s.removing[sessionID]++
	restart := s.restarting[sessionID]
	s.mu.Unlock()
	if restart.cancel != nil {
		restart.cancel()
	}
}

// endRemoval lifts beginRemoval's mark.
func (s *Service) endRemoval(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.removing[sessionID]--; s.removing[sessionID] <= 0 {
		delete(s.removing, sessionID)
	}
}

// beginProjectRemoval marks a project as being removed until endProjectRemoval,
// so no session create or restart starts in it meanwhile, and cancels its
// session creates in flight.
func (s *Service) beginProjectRemoval(projectID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removingProjects[projectID]++
	for placeholderID, create := range s.creating {
		if create.projectID == projectID {
			create.cancel()
			delete(s.creating, placeholderID)
		}
	}
}

// removeProjectFromStore removes a project from the store and cancels the
// session restarts started in it, returning those it caught running, for
// RemoveProject to wait for in the background. A restart belongs to the project
// it was started in: a container listed under two projects sharing a directory
// keeps the other project's restart, and one started in this project is left to
// finish, as the other project still lists its session; it then shows in the
// project the store moved the session's records to. One the other project only
// hides on an untracked worktree is cancelled: no workstream there could show
// how it went.
//
// Under s.mu, store call included, so a restart is either cancelled before it
// runs or caught running (markRestartRunning), and records its launch under a
// project the store holds (setRestartLaunchConfig).
func (s *Service) removeProjectFromStore(projectID string) ([]restartInFlight, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rehomed, err := s.store.RemoveProject(projectID)
	if err != nil {
		return nil, err
	}
	var running []restartInFlight
	for sessionID, restart := range s.restarting {
		if restart.ref.ProjectID != projectID {
			continue
		}
		if home, ok := rehomed[sessionID]; ok && !home.Hidden() {
			// Its slot moves with it, for the workstream it now shows in to count it.
			if restart.reservedSlot {
				s.releaseSlotLocked(concurrencyKey(restart.ref.ProjectID, restart.ref.WorkstreamID))
				s.inflight[concurrencyKey(home.ProjectID, home.WorkstreamID)]++
			}
			restart.ref = SessionRef{ProjectID: home.ProjectID, WorkstreamID: home.WorkstreamID, SessionID: sessionID}
			s.restarting[sessionID] = restart
			continue
		}
		restart.cancel()
		if restart.running != nil {
			running = append(running, restart)
		}
	}
	return running, nil
}

// endProjectRemoval lifts beginProjectRemoval's mark.
func (s *Service) endProjectRemoval(projectID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.removingProjects[projectID]--; s.removingProjects[projectID] <= 0 {
		delete(s.removingProjects, projectID)
	}
}

// restartOptionID is the option a session with no recorded one is restarted as:
// the option its agent names — matched by ID, then by label, among options when
// they are known.
func restartOptionID(session domain.Session, options []SessionSchemaOption) string {
	for _, option := range options {
		if option.ID == session.Agent {
			return option.ID
		}
	}
	for _, option := range options {
		if option.Label == session.Agent {
			return option.ID
		}
	}
	return session.Agent
}

// findSession returns the session with the given ID in a workstream.
func findSession(workstream domain.Workstream, sessionID string) (domain.Session, bool) {
	for _, session := range workstream.Sessions {
		if session.ID == sessionID {
			return session, true
		}
	}
	return domain.Session{}, false
}
