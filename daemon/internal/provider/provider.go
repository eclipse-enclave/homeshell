// Package provider defines HomeShell's two provider contracts — a
// WorkstreamProvider owns workstream records and their directories, a
// SessionProvider owns the sessions running inside them — and the seam between
// them: domain.Workstream.WorkingDir, the public join key the daemon matches
// discovered sessions to workstreams by. Optional capabilities are separate
// interfaces discovered by type assertion.
package provider

import (
	"context"
	"encoding/json"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
)

// WorkstreamProvider owns the lifecycle of workstream records and their backing
// working directories. It never touches sessions.
type WorkstreamProvider interface {
	// ID is the stable, opaque identifier stamped onto a workstream's Provider
	// field. It is never shown to users.
	ID() string

	// Label is the user-presentable provider name shown in the create pick-list.
	Label() string

	// Available reports whether the provider can create workstreams right now.
	Available(ctx context.Context) (bool, error)

	// CanCreate reports whether this provider can create a workstream on the
	// given project (e.g. the git-worktree strategy needs a git-backed project).
	CanCreate(ctx context.Context, project ProjectRef) bool

	// Schema is the JSON Schema for this provider's creation form, possibly
	// specialized to the project.
	Schema(ctx context.Context, project ProjectRef) json.RawMessage

	// Create materializes a workstream on a project and returns it with Name,
	// Strategy, Branch, WorkingDir and CreatorData populated. The daemon assigns
	// the ID and stamps Provider; the provider must not.
	Create(ctx context.Context, req CreateRequest) (domain.Workstream, error)

	// Remove discards the working directory the provider materialized, unless
	// opts.KeepFiles is set. The daemon has already removed the sessions.
	Remove(ctx context.Context, project ProjectRef, ws domain.Workstream, opts RemoveOptions) error
}

// SessionProvider owns the lifecycle and state of sessions inside workstreams.
// It does not create, rename or remove workstreams: it reports the sessions it
// observes, tagged with the directory they run in.
type SessionProvider interface {
	// ID is the stable, opaque identifier stamped onto the sessions this provider
	// owns (domain.Session.Provider), so the daemon can route calls back to it.
	ID() string

	// Label is the user-presentable provider name shown in the new-session form.
	Label() string

	// Available reports whether the provider's backend is usable right now. An
	// unavailable provider contributes no sessions and rejects lifecycle calls.
	Available(ctx context.Context) (bool, error)

	// Capabilities declares the data-only capabilities. Behavioural ones are
	// discovered by type-asserting the optional interfaces in this package.
	Capabilities() Capabilities

	// SessionSchemas returns the ordered set of session types the provider can
	// create in the workstream. An empty slice means none can be created here.
	SessionSchemas(ctx context.Context, ref WorkstreamRef) ([]SessionSchemaOption, error)

	// CreateSession starts a new session from a chosen schema option and config.
	// Provider-specific state rides in domain.Session.ProviderData; the daemon
	// stamps Provider.
	CreateSession(ctx context.Context, req CreateSessionRequest) (domain.Session, error)

	// RemoveSession drops a session the provider owns, tearing down any runtime.
	RemoveSession(ctx context.Context, ref SessionRef) error

	// SessionState returns the current state of a single session the provider
	// owns.
	SessionState(ctx context.Context, ref SessionRef) (domain.Session, error)

	// Open resolves how to open a session for interactive work in the given mode
	// (one of Capabilities.OpenModes).
	Open(ctx context.Context, ref SessionRef, mode string) (OpenResult, error)

	// DiscoverSessions reports every session the provider observes for a project,
	// each tagged with the working directory it runs in, which the daemon matches
	// to a workstream.
	DiscoverSessions(ctx context.Context, project ProjectRef) ([]DiscoveredSession, error)
}

// --- Optional capability interfaces (discovered by type assertion) ---

// WorkstreamStartStopper is a workstream provider that can start and stop a
// workstream's runtime (e.g. a container) without destroying it.
type WorkstreamStartStopper interface {
	StartWorkstream(ctx context.Context, ref WorkstreamRef) error
	StopWorkstream(ctx context.Context, ref WorkstreamRef) error
}

// Prompter is a session provider that can deliver one-off input to a session: a
// line of text (Prompt) or a single named key event (SendKey). Both ride the
// same capability, oneOffPrompt.
type Prompter interface {
	Prompt(ctx context.Context, ref SessionRef, text string) error
	SendKey(ctx context.Context, ref SessionRef, key string) error
}

// ConfigValidator is a workstream provider that checks its own creation config
// **synchronously**, before the daemon reserves anything for it.
//
// It exists because JSON Schema cannot express every rule that matters. A git
// branch name is the case it was added for: git's refname grammar has rules
// (no `..`, no trailing `.lock`, no `@{`, no leading dot or dash) that a Go
// regexp cannot state without lookaheads, so a `pattern` in the schema would
// either reject valid names or admit invalid ones. Validating in Go instead keeps
// one authoritative rule and lets the refusal say what is actually wrong.
//
// Why *synchronously* is the whole point. Creation is asynchronous, so a rule
// checked inside Create surfaces minutes later as a workstream in `error` — a
// card the user has to read and then dispose of, for input that was refusable the
// moment it arrived. Implementing this turns that into a 400 on the create
// request, with nothing materialized and the form still open on the offending
// value.
//
// Return a *ValidationError with Fields keyed by JSON pointer ("/branch") so the
// API can tell the client which input to blame; any other error is reported as a
// plain refusal. Providers that need nothing beyond their schema simply do not
// implement it.
type ConfigValidator interface {
	ValidateCreate(ctx context.Context, req CreateRequest) error
}

// ValidationSchemer is a workstream provider whose Schema does work only the form
// needs, such as asking git for values to suggest. ValidationSchema is the same
// schema without that work, which the daemon validates a create against.
type ValidationSchemer interface {
	ValidationSchema(ctx context.Context, project ProjectRef) json.RawMessage
}

// DraftResolver is a workstream provider that can say what a create would
// derive for the values it leaves blank, so a form can show them before it is
// sent: the name, and each config value it derives ("" where none applies).
// The daemon also names a blank create by it before reserving the placeholder.
type DraftResolver interface {
	ResolveDraft(ctx context.Context, req CreateRequest) Draft
}

// StrategyTeller is a workstream provider that can say a create's strategy
// (domain.Workstream.StrategyID) before the create runs. The create's
// placeholder carries it, so an in-place create is listed in the project folder
// from the start.
type StrategyTeller interface {
	CreateStrategy(req CreateRequest) string
}

// Refresher is a workstream provider whose workstreams change under it between
// creates, outside HomeShell (a branch switched in the folder). The reconciler
// asks it on every projection cycle for the workstream as it is now; it must
// stay cheap, and return ws unchanged when it cannot tell. A name it gives is
// kept only if no other workstream in the project answers to it.
//
// Nothing else notices a branch switched with no session running, so the
// reconciler also asks every RefreshInterval (falling back to its own period
// when that is 0) whether a workstream's branch moved, and re-projects if so.
type Refresher interface {
	Refresh(ctx context.Context, project ProjectRef, ws domain.Workstream) domain.Workstream
	RefreshInterval() time.Duration
}

// Draft is a create as the provider would carry it out.
type Draft struct {
	Name   string            `json:"name"`
	Config map[string]string `json:"config"`
}

// ShellOpener is a session provider whose sessions run in an environment of
// their own (a container, a VM). It opens an interactive shell inside that
// environment, which the daemon offers as one open mode per live session,
// beside the shell in the host directory.
//
// Return ErrNoShellEnvironment when the session has no live environment right
// now (a conflict for the client); any other error is reported as it is.
type ShellOpener interface {
	OpenShell(ctx context.Context, ref SessionRef) (OpenResult, error)
}

// Link is one browser-openable hand-off a session provider reports for a
// session. The daemon advertises each as an open mode ("link:<id>") and
// resolves the URL only when the user opens it.
type Link struct {
	ID    string
	Label string
}

// LinkOpener is a session provider whose sessions offer browser-openable links.
// Links must not probe: it is read on every projection cycle. LinkURL resolves
// one declared link at open time, where probing is fine; the daemon hands the
// URL to the client and never proxies what is behind it.
//
// LinkURL reports ErrNoLink when the link is not reachable right now.
type LinkOpener interface {
	Links(session domain.Session) []Link
	LinkURL(ctx context.Context, ref SessionRef, linkID string) (string, error)
}

// SessionStartStopper is a session provider that can stop a running session and
// resume a stopped one. Stopping leaves the session in domain.SessionStopped.
// StartSession may return ErrStartIsRestart instead, when the session can only
// start again as a restart with its recorded settings; the service then runs one.
type SessionStartStopper interface {
	StartSession(ctx context.Context, ref SessionRef) error
	StopSession(ctx context.Context, ref SessionRef) error
}

// SessionRestarter is a session provider that can start one of its sessions again
// with new launch settings — the same session, under the same ID, so its title and
// its row survive. For a provider whose sessions keep a conversation the restart
// continues it unless the request asks for a fresh one.
//
// A restart has two halves. PrepareRestart is synchronous and changes nothing: it
// checks what can be checked up front — that the session can be restarted at all,
// that the settings can be turned into a launch — and refuses with an error the API
// maps to a status, so a request that cannot succeed leaves a working session
// alone. A prepared restart that is never run leaves nothing to undo. The service
// refuses a second restart of a session while one is in flight, so a provider need
// not.
//
// The RestartFunc it returns does the work, and may take as long as a create (an
// image rebuild); the service runs it in the background, at most once. It shows
// the session as starting while it works, and reports a failure to start it again
// on the session the way a failed start does. Its context is cancelled on the way
// to removing the session, alone or with its workstream, or to dropping its
// project, and as the daemon shuts down; a cancelled restart must not bring the
// session back.
//
// CheckRestart tells, before any settings are chosen, that a session cannot be
// restarted as optionID at all, refusing with ErrSessionNotRestartable and why;
// PrepareRestart refuses such a session too. It runs on every projection of the
// session, so it must be cheap and do no I/O.
type SessionRestarter interface {
	CheckRestart(ref SessionRef, optionID string) error
	PrepareRestart(ctx context.Context, req RestartSessionRequest) (RestartFunc, error)
}

// ContinueRefuser is a session restarter whose sessions' conversations are scoped
// to their working directory, so another session there may make continuing one
// ambiguous. ContinueRefusal tells whether a restart of a session as optionID can
// continue its conversation, given neighbourOptionIDs: the options of the
// provider's other sessions working in the same directory, workingDir. It is nil
// when it can, else ErrCannotContinue and why; the service then refuses any
// restart that is not fresh. A restarter without it can always continue.
type ContinueRefuser interface {
	ContinueRefusal(optionID, workingDir string, neighbourOptionIDs []string) error
}

// OptionSchemaProvider is a session provider that can build the schema of one of
// its options without listing them all, which can mean asking about every one.
// The schema is nil for an option with none; an option the provider does not
// offer is refused with ErrOptionNotOffered.
type OptionSchemaProvider interface {
	SessionSchema(ctx context.Context, ref WorkstreamRef, optionID string) (json.RawMessage, error)
}

// RestartFunc carries out a restart PrepareRestart accepted. It returns an error
// for a restart that never took the session down — the old one could not be
// stopped — and so left it as it was, with no failed start to show on it. One
// cancelled after it began taking the session down returns ErrRestartInterrupted:
// the session is then neither as it was nor restarted. A session or workstream
// removal cancels a restart and removes the session itself; a project removal
// cancels it, and the service then removes the session only if the restart was
// interrupted (a restart of a session another project lists is left to finish).
// The daemon shutting down cancels it too, and leaves the session as it is. One
// cancelled before it began returns the context's error: it left the session as
// it was. Any other restart returns nil.
type RestartFunc func(ctx context.Context) error

// RestartSessionRequest is what a SessionRestarter needs to start a session again:
// the session, where it runs, and what to start it with — the option and config
// shaped exactly like a CreateSessionRequest's.
type RestartSessionRequest struct {
	Session    SessionRef
	WorkingDir string
	OptionID   string
	Config     json.RawMessage
	// Fresh asks for a new conversation instead of continuing the last one.
	Fresh bool
}

// Pruner is a session provider that can drop sessions whose owning project no
// longer exists. The daemon calls Prune once at startup with the surviving
// project IDs.
type Pruner interface {
	Prune(ctx context.Context, keepProjectIDs []string) error
}

// Watcher is a session provider with a live runtime that pushes change
// notifications instead of relying solely on the daemon's polling.
type Watcher interface {
	// Watch runs until ctx is cancelled, calling sink.Changed() whenever the
	// provider's sessions change so the daemon re-projects.
	Watch(ctx context.Context, sink Sink)
}

// WatchedDiscoverer can project observations already captured by its watcher.
// Explicit reconciliation still uses fresh DiscoverSessions observations.
type WatchedDiscoverer interface {
	DiscoverWatchedSessions(context.Context, ProjectRef) ([]DiscoveredSession, error)
}

// Sink is how a Watcher-capable provider notifies the daemon that its state
// changed. The reconciler implements it.
type Sink interface {
	Changed()
}

// WatchLoop calls detect every poll period and notifies sink when it reports a
// change. The period is re-read from interval before each wait, so a settings
// change takes effect within one period; a nil or non-positive interval falls
// back to fallback. It blocks until ctx is cancelled.
func WatchLoop(ctx context.Context, sink Sink, interval func() time.Duration, fallback time.Duration, detect func(context.Context) bool) {
	timer := time.NewTimer(watchPeriod(interval, fallback))
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if detect(ctx) {
				sink.Changed()
			}
			timer.Reset(watchPeriod(interval, fallback))
		}
	}
}

// watchPeriod reads interval, falling back to fallback when it is nil or yields
// a non-positive duration.
func watchPeriod(interval func() time.Duration, fallback time.Duration) time.Duration {
	if interval != nil {
		if d := interval(); d > 0 {
			return d
		}
	}
	return fallback
}

// --- Supporting value types ---

// ProjectRef identifies the project a provider acts within and carries the
// filesystem facts a provider needs to create or inspect workstreams.
type ProjectRef struct {
	ID        string
	Path      string
	GitBacked bool
	// FolderTaken says a workstream already works in the project folder. Set only
	// for the creation form (Service.WorkstreamProvidersFor), so a form can leave
	// out what a create there would refuse.
	FolderTaken bool
}

// projectRefOf is the reference to project a provider is handed.
func projectRefOf(project domain.Project) ProjectRef {
	return ProjectRef{ID: project.ID, Path: project.Path, GitBacked: project.GitBacked}
}

// WorkstreamRef identifies a workstream within a project.
type WorkstreamRef struct {
	ProjectID    string
	WorkstreamID string
}

// SessionRef identifies a session within a workstream.
type SessionRef struct {
	ProjectID    string
	WorkstreamID string
	SessionID    string
}

// RemoveOptions controls the behaviour of WorkstreamProvider.Remove. The zero
// value deletes the files.
type RemoveOptions struct {
	// KeepFiles skips deleting the workstream's materialized directory. The
	// workstream is still removed from HomeShell.
	KeepFiles bool
	// Force, when true, removes the workstream even though sessions are busy
	// (running or waiting for input), stopping them as part of the teardown.
	// Without it, Service.Remove refuses such a removal (ErrWorkstreamBusy) so
	// live work is never discarded by a single unconfirmed click. It is consumed
	// by the service's guard, not by providers: by the time a provider's Remove
	// runs, the sessions are already gone either way.
	Force bool
}

// CreateRequest is a request to instantiate a workstream.
type CreateRequest struct {
	Project   ProjectRef
	Name      string                 // optional display name; the provider defaults if empty
	NameTaken func(name string) bool // whether a workstream in the project has name; a default name avoids those (nil: none)
	// WorkingDirUser names the workstream in the project already working in dir,
	// or returns "" (nil: none).
	WorkingDirUser func(dir string) string
	Config         json.RawMessage // user-supplied values, valid against the provider's schema
	OnProgress     func(string)    // optional callback for intermediate status updates
	// HoldClaim, when set, is handed the release of the folder the provider holds while it creates
	// there, and the caller releases it once the workstream is stored or the create failed. Unset, the
	// provider releases it as Create returns. Why: until stored, no workstream claims the folder, and a
	// survey in between would suggest it.
	HoldClaim func(release func())
}

// Capabilities is the data-only capability declaration. Behavioural flags are
// merged in by Registry.Describe from the optional interfaces above.
type Capabilities struct {
	OpenModes []string `json:"openModes"` // opaque, e.g. "terminal", "ide"
	// MaxConcurrentSessions caps how many non-terminal sessions may run at once
	// in a single workstream. 0 means unlimited.
	MaxConcurrentSessions int `json:"maxConcurrentSessions"`
	// DetectsWaiting reports whether the provider can detect the waiting
	// (needs-input) session state.
	DetectsWaiting bool `json:"detectsWaiting"`
}

// OpenResult tells a client how to complete an "open" (a URL to launch, a
// command to run, etc.).
type OpenResult struct {
	Mode string          `json:"mode"`
	Data json.RawMessage `json:"data"`
}

// SessionSchemaOption is one session type a provider offers in a workstream: a
// stable, opaque ID (echoed back in CreateSessionRequest.OptionID), a
// user-presentable Label, and the JSON Schema for its creation form.
//
// A member of a group (an object property of the schema) left out of a config
// must start the session as its schema default would: the daemon records a
// session's config without the group members at their default.
type SessionSchemaOption struct {
	ID     string          `json:"id"`
	Label  string          `json:"label"`
	Schema json.RawMessage `json:"schema"`
}

// SessionProviderSchemas groups the session options a single provider offers.
// The session-schemas endpoint returns one per available provider.
type SessionProviderSchemas struct {
	ProviderID string                `json:"providerId"`
	Label      string                `json:"label"`
	Options    []SessionSchemaOption `json:"options"`
}

// CreateSessionRequest is a request to create a session in a workstream from a
// chosen schema option. WorkingDir is the workstream's working directory (the
// join key) so the provider launches in the right place without inspecting the
// workstream record. Config is the user-supplied values, valid against the
// option's Schema.
// Progress is the optional sink for startup output. A provider whose creation is
// slow (Enclave builds a container image) may call it with one plain-text line at
// a time so the daemon can show the user what is happening while they wait; the
// daemon keeps only the last few lines. It is never nil — the service supplies a
// no-op when nothing is listening — so a provider can call it unconditionally.
// It must not be assumed to be cheap-free: the sink coalesces, so a caller
// writing thousands of lines is fine.
//
// It may be called **concurrently, from any goroutine**: Enclave calls it from
// the two goroutines draining a subprocess's stdout and stderr. The daemon's sink
// takes a lock for that reason, and anything else passed here has to as well.
type CreateSessionRequest struct {
	Workstream WorkstreamRef
	WorkingDir string
	OptionID   string
	Config     json.RawMessage
	Progress   func(line string)
}

// DiscoveredSession is one session a SessionProvider observes, tagged with the
// working directory it runs in. The daemon matches WorkingDir to a workstream,
// or adopts one when nothing matches.
type DiscoveredSession struct {
	WorkingDir string
	Session    domain.Session
}
