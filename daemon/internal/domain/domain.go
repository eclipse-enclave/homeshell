// Package domain defines the normalized HomeShell vocabulary all providers map
// onto: session states, runtime states, and how they aggregate up to
// workstreams and projects. It is the only place the aggregates are derived;
// clients receive them as plain data.
package domain

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"time"
)

// ---------- Session states ----------

// SessionState is the normalized state of a session, independent of the agent
// driving it. The same vocabulary describes workstreams and projects.
type SessionState string

const (
	SessionIdle    SessionState = "idle"
	SessionRunning SessionState = "running"
	SessionWaiting SessionState = "waiting"
	// SessionStarting means the provider is still bringing the session up (for
	// Enclave, building or launching the container) and it has no agent to read a
	// state off yet. It is the state of the placeholder the daemon holds while an
	// async creation is in flight, so the UI can name what is coming up and show
	// the provider's startup output; the real session replaces it on success.
	SessionStarting SessionState = "starting"
	// SessionUnknown means the provider lost the signal for a session (e.g. a
	// terminal capture failed or timed out) and reports it as unknowable rather
	// than keeping a stale last-known state. It is non-terminal.
	SessionUnknown SessionState = "unknown"
	// SessionStopped means a session is not running but can be started again —
	// either because the user stopped it, or because its terminal went away and the
	// provider could not bring it back (which the provider says in ObservationData, so
	// the two are still tellable apart). It is terminal (not active), distinct from
	// SessionDone, which is a run that ended on its own.
	//
	// A lost terminal reports *this* and not SessionIdle deliberately: an idle row
	// offers to be opened and prompted, and both would reach a terminal that is not
	// there — which is how a session with nothing to resume used to look perfectly
	// healthy while every prompt failed.
	SessionStopped SessionState = "stopped"
	SessionDone    SessionState = "done"
	// SessionError means the session never started: the provider's creation failed
	// and the session stands for the attempt, with the reason in Session.Error. It
	// is terminal — there is no runtime behind it, so it occupies no concurrency
	// slot and never blocks a workstream removal — and it is the user who disposes
	// of it (removing the session), because nothing else ever will: no provider
	// reports a session that was never created.
	SessionError SessionState = "error"
)

// Valid reports whether s is one of the normalized session states.
func (s SessionState) Valid() bool {
	switch s {
	case SessionIdle, SessionRunning, SessionWaiting, SessionStarting, SessionUnknown, SessionStopped, SessionDone, SessionError:
		return true
	}
	return false
}

// Busy reports whether a session is actively working or waiting for input, so
// tearing it down would interrupt live work.
//
// The daemon gates workstream removal on this: removing a workstream is refused
// while any of its sessions is busy unless the caller forces it
// (provider.RemoveOptions.Force). Removing a single session is deliberately not
// gated — see provider.Service.RemoveSession.
//
// A starting session counts as busy: tearing its workstream down mid-build
// would interrupt work the user just asked for, and the container the provider
// is about to hand back would outlive the workstream that was removed. A session
// whose creation *failed* does not: there is nothing behind it to interrupt, so
// a workstream left holding one is still removable without forcing.
func (s SessionState) Busy() bool {
	return s == SessionRunning || s == SessionWaiting || s == SessionStarting
}

// Terminal reports whether a session has ended — done (finished), stopped
// (deliberately halted), or error (it never started) — and so does not occupy a
// concurrency slot. The daemon enforces MaxConcurrentSessions by counting
// non-terminal sessions.
//
// A failed creation counts here for the same reason: the slot it reserved was
// released when the create returned, so leaving the row on screen must not charge
// the workstream for a session that does not exist.
func (s SessionState) Terminal() bool {
	return s == SessionDone || s == SessionStopped || s == SessionError
}

// Ended reports whether a session in this state has ended: done or stopped, a
// terminal state other than error. A failed start stays in view instead.
func (s SessionState) Ended() bool {
	return s == SessionDone || s == SessionStopped
}

// AllTerminal reports whether every session is in a terminal state, so none
// is left to stop; true for none.
func AllTerminal(sessions []Session) bool {
	for _, session := range sessions {
		if !session.State.Terminal() {
			return false
		}
	}
	return true
}

// LiveCount is the number of sessions that have not ended (SessionState.Ended).
func LiveCount(sessions []Session) int {
	live := 0
	for _, session := range sessions {
		if !session.State.Ended() {
			live++
		}
	}
	return live
}

// statePrecedence is attention-first: error > waiting > unknown > running >
// starting > idle > stopped. "Done" only surfaces once every child is terminal.
// Starting ranks below running because a workstream that already has a live agent
// is better described by that agent than by a second one still coming up, but
// above idle, since something is actively happening.
//
// Error leads for the same reason it leads runtimePrecedence: a create that
// failed is the one thing here that will not resolve itself, and it stays until
// the user disposes of it. A workstream reading "error" while its other sessions
// work is the intended reading — something in it needs a decision.
var statePrecedence = [...]SessionState{SessionError, SessionWaiting, SessionUnknown, SessionRunning, SessionStarting, SessionIdle, SessionStopped}

// Aggregate derives the state of a parent (workstream, project) from its
// children's states using attention-first precedence: error > waiting > unknown
// > running > starting > idle > stopped. "Done" is the fallback when every child
// is terminal. No children means idle: the parent is ready but nothing happens.
func Aggregate(states []SessionState) SessionState {
	if len(states) == 0 {
		return SessionIdle
	}
	present := make(map[SessionState]bool, len(states))
	for _, s := range states {
		present[s] = true
	}
	for _, s := range statePrecedence {
		if present[s] {
			return s
		}
	}
	return SessionDone
}

// ---------- Runtime states ----------

// RuntimeState describes the workspace container lifecycle as reported by a
// provider. It is orthogonal to session states.
type RuntimeState string

const (
	RuntimeIdle     RuntimeState = "idle"
	RuntimeUnknown  RuntimeState = "unknown"
	RuntimeStopped  RuntimeState = "stopped"
	RuntimeRunning  RuntimeState = "running"
	RuntimeStopping RuntimeState = "stopping"
	RuntimeStarting RuntimeState = "starting"
	RuntimeCreating RuntimeState = "creating"
	RuntimeError    RuntimeState = "error"
)

// ValidRuntime reports whether r is one of the recognized runtime states.
func (r RuntimeState) ValidRuntime() bool {
	switch r {
	case RuntimeIdle, RuntimeUnknown, RuntimeStopped, RuntimeRunning,
		RuntimeStopping, RuntimeStarting, RuntimeCreating, RuntimeError:
		return true
	}
	return false
}

// runtimePrecedence defines the attention-first ordering for runtime states.
// Higher-index entries win: error > creating > starting > stopping >
// running > stopped > unknown > idle.
var runtimePrecedence = map[RuntimeState]int{
	RuntimeIdle:     0,
	RuntimeUnknown:  1,
	RuntimeStopped:  2,
	RuntimeRunning:  3,
	RuntimeStopping: 4,
	RuntimeStarting: 5,
	RuntimeCreating: 6,
	RuntimeError:    7,
}

// AggregateRuntime derives a single runtime state from a set of them; the
// highest-precedence one wins. An empty slice yields RuntimeIdle.
func AggregateRuntime(states []RuntimeState) RuntimeState {
	if len(states) == 0 {
		return RuntimeIdle
	}
	best := states[0]
	for _, s := range states[1:] {
		if runtimePrecedence[s] > runtimePrecedence[best] {
			best = s
		}
	}
	return best
}

// EffectiveState combines a workstream's runtime state with its session
// aggregate into one display string. Error, creating, starting, stopping and
// stopped surface directly; running, idle and unknown defer to the session
// aggregate. The two sets partition runtimePrecedence.
func EffectiveState(runtime RuntimeState, sessionAggregate SessionState) string {
	switch runtime {
	case RuntimeError:
		return string(RuntimeError)
	case RuntimeCreating:
		return string(RuntimeCreating)
	case RuntimeStarting:
		return string(RuntimeStarting)
	case RuntimeStopping:
		return string(RuntimeStopping)
	case RuntimeStopped:
		return string(RuntimeStopped)
	default:
		return string(sessionAggregate)
	}
}

// The structs below are the wire model served to clients. Provider is the
// opaque ID of the owning provider; StrategyID is the machine-stable strategy
// identifier and Strategy the display label Recompute derives from it. Only
// StrategyID is persisted.

// Session is one agent conversation inside a workstream.
type Session struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Agent string `json:"agent"`
	// Provider is the ID of the session provider that owns the session. It is
	// persisted because the reconciler routes and retains by it.
	Provider string       `json:"provider,omitempty"`
	State    SessionState `json:"state"`
	// EndedAt is when the daemon saw the session become terminal (done, stopped or
	// error). It is nil while the session is live, and for one first seen already
	// ended, whose real end the daemon cannot know. Persisted, so a restart does not
	// restamp it.
	EndedAt *time.Time `json:"endedAt,omitempty"`
	// Ended says the session has ended (SessionState.Ended), so clients list and
	// count it apart from the live ones. Derived by Recompute from State; not
	// persisted.
	Ended bool `json:"ended"`
	// Preview is a plain-text tail of the session's terminal output, surfaced for
	// the UI. It is live, ephemeral state (recomputed on each capture, never
	// persisted), so it is omitted when empty.
	Preview string `json:"preview,omitempty"`
	// Error explains why a session in SessionError state never started — the
	// provider's own message, verbatim. It is set only alongside that state and
	// never cleared: it is the row's entire account of itself, so the way to be rid
	// of it is to remove the session (the same reasoning as a failed workstream's
	// StatusMessage, which is deliberately not dismissible either). Ephemeral, like
	// the placeholder it is written onto — a restart forgets the failed attempt.
	Error        string          `json:"error,omitempty"`
	ProviderData json.RawMessage `json:"providerData,omitempty"`
	// ObservationData is provider-specific live detail, never persisted.
	ObservationData json.RawMessage `json:"observationData,omitempty"`
	// PlaceholderID is the ID of the starting placeholder this session replaced,
	// so the client that asked for it can tell which row is its own. Not
	// persisted.
	PlaceholderID string `json:"placeholderId,omitempty"`
	// Placeholder marks the daemon's own row for a session create in flight, or
	// one that failed: no provider has heard of it, so stopping it is removing it,
	// which cancels the create. A session restarting is starting too, but is not
	// one. Not persisted, as placeholders are not.
	Placeholder bool `json:"placeholder,omitempty"`
	// Restartable is whether the session can be restarted with settings, as far
	// as the daemon can tell without asking its provider anything costly. Derived
	// during projection like Workstream.OpenModes; not persisted.
	Restartable bool `json:"restartable"`
	// OutdatedSettings names the provider settings whose value differs from what
	// the session would start with now: its provider's config changed since it
	// started, and a restart with the same settings would apply the change.
	// Sorted, in the provider's own names; empty when nothing changed or the
	// provider cannot tell. Live, like Preview: never persisted.
	OutdatedSettings []string `json:"outdatedSettings,omitempty"`
	// Capabilities are what a client may do with this session beyond its
	// provider's capabilities. Nil means the session is live (HomeShell sees and
	// types into it), offers no take-over and needs no stop warning. Derived by
	// the provider; never persisted.
	Capabilities *SessionCapabilities `json:"capabilities,omitempty"`
	// Replaces is the ID of the session this one goes on from under a new ID; the
	// store carries the old session's title and launch config over to it when it
	// is first listed. Set by the provider and kept; never serialized.
	Replaces string `json:"-"`
}

// SessionCapabilities are what a client may do with one session beyond its
// provider's capabilities. A session without them is live, offers no take-over
// and needs no stop warning.
type SessionCapabilities struct {
	// Live is whether HomeShell sees the session's screen and can type into it.
	Live bool `json:"live"`
	// TakeOver is whether the session runs outside HomeShell, so that a restart
	// brings it under HomeShell.
	TakeOver bool `json:"takeOver"`
	// StopWarning is what a confirmation before stopping or deleting says.
	StopWarning string `json:"stopWarning,omitempty"`
	// TakeOverWarning is what a confirmation before a take-over says.
	TakeOverWarning string `json:"takeOverWarning,omitempty"`
	// Note is what the session's row says about where it runs, such as why
	// HomeShell neither shows nor types into it.
	Note string `json:"note,omitempty"`
}

// LaunchConfig is what a session was last (re)started with: its provider, the
// option it was started as, and that option's create-form config, null when there
// is none.
type LaunchConfig struct {
	ProviderID string          `json:"providerId"`
	OptionID   string          `json:"optionId"`
	Config     json.RawMessage `json:"config"`
}

// Workstream is an isolated-or-not working environment for a project.
//
// WorkingDir is the public join key of the workstream/session divide: the
// directory the workstream's sessions run in, set once at creation time, which
// the daemon matches discovered sessions against. CreatorData is the workstream
// provider's own private teardown bookkeeping.
type Workstream struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Provider string `json:"provider"`
	// ProviderLabel is the display name of the workstream's provider (for an
	// adopted workstream, its session provider's); empty for the core provider
	// and for an ID no registered provider answers to. Derived during
	// projection like OpenModes.
	ProviderLabel string `json:"providerLabel,omitempty"`
	StrategyID    string `json:"strategyId"`
	Strategy      string `json:"strategy"`
	Branch        string `json:"branch,omitempty"`
	WorkingDir    string `json:"workingDir,omitempty"`
	// InProjectFolder says the workstream works, or will once created, in the
	// project's own folder, the one folder a project's workstreams can share only
	// one at a time. Derived by Recompute; not persisted.
	InProjectFolder bool            `json:"inProjectFolder,omitempty"`
	State           SessionState    `json:"state"`
	RuntimeState    RuntimeState    `json:"runtimeState"`
	Effective       string          `json:"effectiveState"`
	StatusMessage   string          `json:"statusMessage,omitempty"`
	ProviderData    json.RawMessage `json:"providerData,omitempty"`
	CreatorData     json.RawMessage `json:"creatorData,omitempty"`
	OpenModes       []OpenMode      `json:"openModes"`
	// Actions are the workstream-level actions available right now, derived
	// during projection like OpenModes and for the same reason: whether one is
	// possible can depend on the daemon's filesystem, which a client cannot see.
	Actions  []Action  `json:"actions"`
	Sessions []Session `json:"sessions"`
	// PlaceholderID is the ID of the creation placeholder this workstream
	// replaced, like Session.PlaceholderID. Not persisted.
	PlaceholderID string `json:"placeholderId,omitempty"`
	// CreateFailed marks the placeholder of a create that failed: it stands for
	// no workstream, so nothing is offered on it but its removal, and its
	// StatusMessage says why. Not persisted, as placeholders are not.
	CreateFailed bool `json:"createFailed,omitempty"`
	// FolderMissing marks a worktree workstream whose folder is gone, kept
	// listed while a session in it has not ended. Derived by the reconciler;
	// not persisted.
	FolderMissing bool `json:"folderMissing,omitempty"`
}

// OpenMode is one way a workstream can be handed to the user for interactive
// work. The set is derived per workstream and pushed as data: a client renders
// one action per mode and echoes the ID back to open with it. The daemon
// authors the Label.
type OpenMode struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	// OpensInBrowser reports that opening this mode answers with a URL for the
	// client to open in a tab. Declared so a client can prepare the tab inside
	// the user's click, without reading meaning into the mode id.
	OpensInBrowser bool `json:"opensInBrowser"`
	// Default marks the mode the plain open (POST …/open) runs. At most one mode
	// carries it, and none does when the plain open would fail (the working
	// directory is not on this host), so a client offers that shortcut only when
	// some mode carries it.
	Default bool `json:"default"`
}

// The open-mode IDs the daemon derives. They are a closed set in the daemon
// (each has an implementation behind it) but opaque to clients.
const (
	// OpenModeTerminal hands the workstream to a terminal: a shell in its working
	// directory on the daemon host, whatever its sessions run in.
	OpenModeTerminal = "terminal"
	// OpenModeOpenWith runs one entry from the user's "open with" list. Never
	// used bare: the mode is "open-with:<entryId>".
	OpenModeOpenWith = "open-with"
	// OpenModeOpenWithPrefix marks a mode naming one "open with" entry.
	OpenModeOpenWithPrefix = OpenModeOpenWith + ":"
	// OpenModeLinkPrefix marks a mode naming a browser-openable link a session
	// provider reported. The daemon resolves the URL at open time and the client
	// opens it; the daemon never proxies it.
	OpenModeLinkPrefix = "link:"
	// OpenModeSessionShellPrefix marks a mode naming one live session whose
	// provider runs it in an environment of its own (a container): the mode opens
	// a shell inside that environment. One per such session, since each has its
	// own.
	OpenModeSessionShellPrefix = "session-shell:"
)

// Action is one thing that can be done to a workstream as a whole right now,
// beside the open modes. Same contract as OpenMode: the set is derived per
// workstream and pushed as data, the daemon authors the words, and a client
// renders one control per action and echoes the ID back to run it. So a rule
// like "a note needs files on this host" is stated once, here, rather than
// re-derived by every client from a raw fact about the daemon's disk.
type Action struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	// Description says what running the action will do, for a tooltip. The
	// daemon authors it because the daemon is what knows: the note is handed to
	// whatever the desktop associates with the file type, which is not something
	// a client can name.
	Description string `json:"description,omitempty"`
}

// The action IDs the daemon derives. Closed in the daemon (each has an
// implementation and an endpoint behind it) but opaque to clients.
const (
	// ActionNote opens the workstream's note, creating it on first use.
	ActionNote = "note"
)

// OpenWithModeFor builds the open-mode ID naming one "open with" entry.
func OpenWithModeFor(entryID string) string { return OpenModeOpenWithPrefix + entryID }

// IsOpenWithMode reports whether mode runs an entry from the user's "open with"
// list, and returns the entry it names. The bare prefix names no entry, so it is
// not a mode.
func IsOpenWithMode(mode string) (entryID string, ok bool) {
	if after, found := strings.CutPrefix(mode, OpenModeOpenWithPrefix); found && after != "" {
		return after, true
	}
	return "", false
}

// SessionShellModeFor builds the open-mode ID naming a shell in one session's
// environment.
func SessionShellModeFor(sessionID string) string { return OpenModeSessionShellPrefix + sessionID }

// IsSessionShellMode reports whether mode opens a shell in a session's
// environment, and returns the session ID it names.
func IsSessionShellMode(mode string) (sessionID string, ok bool) {
	if after, found := strings.CutPrefix(mode, OpenModeSessionShellPrefix); found && after != "" {
		return after, true
	}
	return "", false
}

// LinkModeFor builds the open-mode ID naming one provider-reported link.
func LinkModeFor(linkID string) string { return OpenModeLinkPrefix + linkID }

// IsLinkMode reports whether mode opens a provider-reported link, and returns
// the link it names. The bare prefix names no link, so it is not a mode.
func IsLinkMode(mode string) (linkID string, ok bool) {
	if after, found := strings.CutPrefix(mode, OpenModeLinkPrefix); found && after != "" {
		return after, true
	}
	return "", false
}

// Project is a named, persisted reference to a codebase.
type Project struct {
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	Path         string       `json:"path"`
	GitBacked    bool         `json:"gitBacked"`
	State        SessionState `json:"state"`
	RuntimeState RuntimeState `json:"runtimeState"`
	Effective    string       `json:"effectiveState"`
	Workstreams  []Workstream `json:"workstreams"`
	// UntrackedWorktrees are the project's linked git worktrees no workstream
	// works in, suggested for adding. Derived by the reconciler; not persisted.
	UntrackedWorktrees []UntrackedWorktree `json:"untrackedWorktrees"`
}

// UntrackedWorktree is a linked worktree of a project's repository that no
// workstream works in: a folder HomeShell suggests adding as a workstream.
// Derived by the reconciler; not persisted.
type UntrackedWorktree struct {
	Name         string `json:"name"`
	Branch       string `json:"branch,omitempty"`
	Path         string `json:"path"`
	SessionCount int    `json:"sessionCount"` // Sessions not ended; derived by Recompute
	// Sessions are the ones reported in the folder, hidden until it is added.
	Sessions []Session `json:"-"`
}

// StrategyLabel returns the human-readable display label for a raw strategy ID.
// The label is derived at runtime; only the ID is persisted.
func StrategyLabel(strategyID string) string {
	switch strategyID {
	case "in-place":
		return "in place"
	case "copy":
		return "folder"
	}
	return strategyID
}

// SameFolder reports whether two working directories name the same folder. An
// empty one (a placeholder's) is no folder and matches nothing. It is the one
// test behind both the rule that a folder holds one workstream and
// Workstream.InProjectFolder.
func SameFolder(dir, other string) bool {
	return dir != "" && other != "" && filepath.Clean(dir) == filepath.Clean(other)
}

// Recompute derives whether each session has ended, the workstream states from
// their session states, whether each works in the project folder, the effective
// display state from the runtime and session aggregates, and the project-level
// aggregates from the workstream values. Call it after every mutation.
func (p *Project) Recompute() {
	workstreamStates := make([]SessionState, len(p.Workstreams))
	workstreamRuntimeStates := make([]RuntimeState, len(p.Workstreams))
	for i := range p.Workstreams {
		w := &p.Workstreams[i]
		w.Strategy = StrategyLabel(w.StrategyID)
		// An in-place create's placeholder has no folder yet, only its strategy.
		w.InProjectFolder = w.StrategyID == "in-place" || SameFolder(w.WorkingDir, p.Path)
		// Persistence and provider projection do not set a runtime state, so the
		// zero value means "unknown".
		if w.RuntimeState == "" {
			w.RuntimeState = RuntimeUnknown
		}
		// A workstream read back from persistence has no open modes or actions
		// until the first reconcile derives them; clients are promised arrays,
		// never null.
		if w.OpenModes == nil {
			w.OpenModes = []OpenMode{}
		}
		if w.Actions == nil {
			w.Actions = []Action{}
		}
		sessionStates := make([]SessionState, len(w.Sessions))
		for j := range w.Sessions {
			sessionStates[j] = w.Sessions[j].State
			w.Sessions[j].Ended = w.Sessions[j].State.Ended()
		}
		w.State = Aggregate(sessionStates)
		w.Effective = EffectiveState(w.RuntimeState, w.State)
		workstreamStates[i] = w.State
		workstreamRuntimeStates[i] = w.RuntimeState
	}
	if p.UntrackedWorktrees == nil {
		p.UntrackedWorktrees = []UntrackedWorktree{}
	}
	for i := range p.UntrackedWorktrees {
		p.UntrackedWorktrees[i].SessionCount = LiveCount(p.UntrackedWorktrees[i].Sessions)
	}
	p.State = Aggregate(workstreamStates)
	p.RuntimeState = AggregateRuntime(workstreamRuntimeStates)
	p.Effective = EffectiveState(p.RuntimeState, p.State)
}
