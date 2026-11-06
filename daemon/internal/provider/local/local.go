// Package local implements HomeShell's local-system session provider: agent
// sessions run directly as processes under the sbridge PTY bridge, with their
// state derived from the rendered terminal (daemon/internal/detect).
//
// It is a session provider only — it does not create, rename, or remove
// workstreams. The daemon's core workstream provider prepares the working
// directory; the local provider launches agent sessions in it (the directory is
// handed to CreateSession via CreateSessionRequest.WorkingDir) and reports the
// sessions it observes, each tagged with that directory, so the daemon can match
// them back to the workstream.
//
// Which agents it can launch is not decided here: the agents, their launch
// settings, and how each chosen value reaches the command line are user-authored
// rows in the agent catalog, read through an agentspec.Source (internal/agentspec,
// internal/agentcatalog). This provider offers one session option per described agent whose
// CLI is on PATH (SessionSchemas), with the option's schema generated from that
// agent's settings rows, and launches it by delivering the chosen values the way
// each row says to.
//
// The provider persists its sessions (each session's agent, generated session
// UUID, runtime target, model, and launch config) so that after a daemon restart
// it re-attaches to still-running bridge sessions and, when the bridge process
// itself is gone, a resumable agent (Claude, via `--resume <uuid>`) is resumed
// while a non-resumable one (Codex, Gemini) is reported gone.
package local

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/agentmodel"
	"github.com/eclipsesource/homeshell/daemon/internal/agentspec"
	"github.com/eclipsesource/homeshell/daemon/internal/detect"
	"github.com/eclipsesource/homeshell/daemon/internal/domain"
	"github.com/eclipsesource/homeshell/daemon/internal/hostbridge"
	"github.com/eclipsesource/homeshell/daemon/internal/provider"
	"github.com/eclipsesource/homeshell/daemon/internal/sbridge"
	"github.com/eclipsesource/homeshell/daemon/internal/terminal"
)

const (
	providerID    = "local"
	providerLabel = "Local System"
)

// watchInterval is how often the provider re-detects its sessions' states so
// the UI reflects them live. Detection is a cheap snapshot probe; the provider
// only asks the daemon to re-project when a session's state actually changed.
const watchInterval = time.Second

// Which agents can be launched, what a new session of each one offers, and how a
// chosen value reaches the agent's command line are **not** written here: they are
// user-authored rows in the agent catalog (internal/agentcatalog), read through the agentspec.Source this
// provider is given. They used to be the `agents` slice, three JSON-Schema string
// constants and one argv-building function per agent right here, which meant a flag
// a CLI added — or a wording fix — was a HomeShell release. What is left in this file is the *behavior*
// those rows drive: launching, resuming, detecting and reporting.

// agents is every described agent, in selector order. Empty when no source is wired
// (a provider built without persistence), which reads as "nothing to launch" — the
// same answer as a host with none of the agents installed.
func (p *Provider) agents() []agentspec.Agent {
	if p.specs == nil {
		return nil
	}
	return p.specs.Agents()
}

// agentFor resolves a session-option id to its agent description. It reports false
// for an id no agent answers to, so CreateSession can reject it before launching
// anything. The model is not part of the id — it arrives in the session config,
// which the option's schema has already validated.
func (p *Provider) agentFor(id string) (agentspec.Agent, bool) {
	if p.specs == nil {
		return agentspec.Agent{}, false
	}
	return p.specs.Agent(id)
}

// agentOf resolves the description a session was launched from, so a launch can be
// rebuilt for resume and restart. By id: agents are user rows, and a binary is
// neither unique (a `claude-yolo` agent may run `claude` too) nor fixed (a PATCH can
// change it). See agentIndex.of for the fallback.
func (p *Provider) agentOf(agentID, binary string) (agentspec.Agent, bool) {
	if agentID != "" {
		return p.agentFor(agentID)
	}
	return p.indexAgents().of("", binary)
}

// agentIndex is every agent description, read once and then looked up in memory. A
// capture pass resolves every session's agent, and every read of the catalog hands
// out a copy of it, so a pass reads it once and hands the index down rather than
// asking the catalog per session on every watch tick.
type agentIndex []agentspec.Agent

func (p *Provider) indexAgents() agentIndex { return p.agents() }

// of finds a session's agent: by id, or by binary for a session persisted before
// sessions recorded the id. That fallback is the only use of the binary, and it is
// unambiguous for those sessions because they can only have come from the seeded
// agents, whose binaries are distinct.
func (idx agentIndex) of(agentID, binary string) (agentspec.Agent, bool) {
	for _, a := range idx {
		match := a.ID == agentID
		if agentID == "" {
			match = a.Binary == binary
		}
		if match {
			return a, true
		}
	}
	return agentspec.Agent{}, false
}

// display is the user-facing agent name for a session, falling back to the binary it
// ran for an agent we no longer know — a session launched from a description the
// user has since deleted still has to be shown.
func (idx agentIndex) display(agentID, binary string) string {
	if a, ok := idx.of(agentID, binary); ok {
		return a.Display
	}
	return binary
}

// argv is the full command line for a launch: the environment prefix, the binary,
// then the arguments the agent's own settings deliver (internal/agentspec).
//
// The environment goes on as an `env VAR=value` prefix rather than as real process
// environment because the runtime execs this argv directly, with no shell to expand an
// assignment. It is how vibe's model is set — the one seeded setting whose CLI has no
// flag for it at all.
func argv(a agentspec.Agent, l agentspec.Launch) []string {
	cmd := agentspec.Deliver(a, l, agentspec.Host)
	var out []string
	if len(cmd.Env) > 0 {
		out = append(out, "env")
		for _, name := range sortedNames(cmd.Env) {
			out = append(out, name+"="+cmd.Env[name])
		}
	}
	out = append(out, a.Binary)
	return append(out, cmd.Args...)
}

// sortedNames orders a delivery's variables by name, so an argv carrying more than
// one is stable rather than at the mercy of Go's map iteration: a launch command that
// reshuffles between runs is untestable, and unreadable in a log.
func sortedNames(env map[string]string) []string {
	names := make([]string, 0, len(env))
	for name := range env {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Runtime is the seam over the session runtime: launching agent processes and
// reading their rendered state. Production uses internal/hostbridge; tests
// supply a fake. Every method takes a context so a wedged
// runtime call cannot block the daemon.
type Runtime interface {
	Available(ctx context.Context, binary string) error
	Start(ctx context.Context, target, dir string, argv []string) error
	// Probe returns nil while a bridge answers for target. Like State, only an
	// error wrapping sbridge.ErrNotServing permits a relaunch.
	Probe(ctx context.Context, target string) error
	// State observes liveness and state in one read. An error wrapping
	// sbridge.ErrNotServing means no bridge answers; any other means unknown,
	// never permission to resume.
	State(ctx context.Context, agent, target string) (detect.Result, error)
	Stop(ctx context.Context, target string) error
	// SendInput delivers a line of text to the target's terminal (typed as if by
	// the user) followed by Enter. Sending to an unknown target is an error.
	SendInput(ctx context.Context, target, text string) error
	// SendKey delivers a single named key event (a normalized provider.SessionKeys
	// name, e.g. "up"/"enter") to the target's terminal, interpreted as a key
	// press rather than literal text and with no trailing Enter. For navigating
	// an agent's interactive menu. Sending to an unknown target is an error.
	SendKey(ctx context.Context, target, key string) error
	// AttachCommand returns the argv a terminal runs to attach to the target
	// (e.g. `homeshell bridge attach --sock <sock>`); Open hands it to the terminal
	// launcher.
	AttachCommand(target string) []string

	// Output runs a one-off command and returns what it printed on stdout. Used to
	// ask an agent's CLI a question — today only "which models do you have?" — so
	// nothing about a session is involved. The caller bounds it with ctx.
	Output(ctx context.Context, argv []string) ([]byte, error)
}

// ExitRecorder is an optional Runtime capability: how a target's agent last
// exited (status and final screen), so a session that ended on its own can say
// why. recorded is the location persisted at launch, as for LocateElsewhere.
type ExitRecorder interface {
	LastExit(target, recorded string) (sbridge.Exit, bool)
}

// LocationRecorder is an optional Runtime capability for runtimes that reach a
// target through a filesystem location which can differ between daemon starts.
//
// Without it, a not-serving Probe is ambiguous: the agent may have died, or the
// runtime may be looking somewhere the session was never started. The provider
// treats those identically — it resumes — so the second case forks a second
// live agent into the working directory beside the one still running. A
// runtime that implements this lets the provider tell them apart.
type LocationRecorder interface {
	// Location is where this runtime currently resolves target. The provider
	// persists it alongside the session.
	Location(target string) string

	// LocateElsewhere returns the location, if any, where target is still
	// served but this runtime no longer looks. recorded is the location
	// persisted when the session started, empty for sessions written before it
	// was recorded — those are probed at the runtime's own legacy path instead.
	// A non-empty result means a not-serving Probe is not evidence the agent died,
	// *and* names the socket every other operation must address.
	//
	// It returns the location rather than a bool so callers cannot disagree
	// about which sessions count: an earlier split between "is it elsewhere?"
	// and "where is it?" let the resume refuse to relaunch a session that Stop
	// and Open then addressed at the wrong socket.
	LocateElsewhere(ctx context.Context, target, recorded string) string

	// StopAt shuts down whatever serves at an explicit location. Without it a
	// session found alive elsewhere could not be stopped or removed: the
	// target-addressed Stop aims at the current location, which answers as
	// already-dead, so the operation reports success while the real session
	// keeps running.
	StopAt(ctx context.Context, location string) error

	// AttachCommandAt is AttachCommand for an explicit location, so opening a
	// session found alive elsewhere attaches to the bridge that exists rather
	// than to the path this runtime would compute.
	AttachCommandAt(location string) []string
}

// Store persists the provider's sessions across daemon restarts. The production
// implementation (store_file.go) writes JSON under the user state directory;
// tests supply an in-memory fake.
type Store interface {
	Load() ([]PersistedWorkspace, error)
	Save([]PersistedWorkspace) error
}

// PersistedWorkspace / PersistedSession are the on-disk shape of the provider's
// state: the sessions running in one working directory of one project. The
// working directory is the join key the daemon matches on.
type PersistedWorkspace struct {
	ProjectID  string             `json:"projectId"`
	WorkingDir string             `json:"workingDir"`
	Sessions   []PersistedSession `json:"sessions"`
}

type PersistedSession struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	// Agent is the binary the session ran, which is also its detect rule-set label.
	Agent string `json:"agent"`
	// AgentID is the agent description the session was launched from (agents.id).
	// Empty for records written before it was tracked; those are resolved by Agent.
	AgentID        string          `json:"agentId,omitempty"`
	Target         string          `json:"target"`
	AgentSessionID string          `json:"agentSessionId"`
	Model          string          `json:"model"`
	Config         json.RawMessage `json:"config,omitempty"`
	Stopped        bool            `json:"stopped,omitempty"`
	// Sock is where the runtime resolved this session when it was started (see
	// LocationRecorder). Empty for records written before it was tracked, and
	// for runtimes that do not implement the capability; both fall back to
	// trusting a negative liveness probe, i.e. to the previous behavior.
	Sock string `json:"sock,omitempty"`
}

// session is one launched agent process, tracked by its multiplexer target and
// a generated session id (used to resume a resumable agent).
type session struct {
	id             string
	title          string
	agent          string // the agent binary / detect rule-set label (e.g. "claude")
	agentID        string // the agent description it was launched from (agents.id)
	target         string
	agentSessionID string
	model          string
	// config is the validated launch config (the per-agent knobs). Kept so a
	// resume/restart re-applies the same settings and a fresh restart of a
	// non-resumable agent re-runs its initial prompt.
	config json.RawMessage
	// sock is where the runtime resolved this session at launch (see
	// LocationRecorder). Persisted, so a later start with a different
	// environment can tell "the bridge died" from "I am looking elsewhere".
	sock string
	// resumeFailed marks a session whose terminal is gone and whose resume
	// attempt failed, so we stop relaunching it and report it as gone. Not
	// persisted: a fresh daemon start retries once.
	resumeFailed bool
	// relaunched marks a session we have just launched again — by a relaunch or a
	// Start — but have not yet seen alive, so a launch the runtime accepted and that
	// died anyway is not retried forever: the runtime's launch succeeds once the
	// bridge answers, so an agent that exits after that is indistinguishable from a
	// good launch until the next tick. Cleared as soon as the session is seen alive,
	// so every fresh loss of a terminal gets its own attempt. Not persisted.
	relaunched bool
	// hold is the published state and its idle confirmation. Not persisted.
	hold detect.Hold
	// revision rejects superseded observations; lifecycle also discards side
	// effects and blocks a capture's relaunch after a deliberate stop/start.
	revision  uint64
	lifecycle uint64
	observed  *domain.Session
	// stopped marks a session the user deliberately stopped (its runtime target
	// was killed). It overrides detection: a stopped session reports SessionStopped
	// and is never captured or resumed until StartSession clears it. Persisted so
	// a stopped session stays stopped across a restart.
	stopped bool
}

// workspace groups the sessions running in one working directory of a project.
// It is keyed by (projectID, dir) — the daemon owns workstream identity, so the
// provider tracks only the directory the sessions run in.
type workspace struct {
	projectID string
	dir       string
	sessions  []*session
}

// Provider is the local-system session provider.
type Provider struct {
	runtime    Runtime
	localStore Store
	term       terminal.Launcher
	provider.SettingsHolder

	mu        sync.Mutex
	byProject map[string][]*workspace

	// models remembers each CLI's answer to "which models do you have?" (models.go).
	models agentmodel.Cache

	// catalog is the user's authored model list, shared with every other provider
	// that launches the same agent (internal/agentcatalog). Injected after
	// construction (the provider is built from init(), before there is a catalog),
	// so it is nil until then, and nil offers no list rather than an old one.
	catalog agentmodel.Catalog

	// conversation reports whether a resumable agent has a conversation on disk for
	// a session id, which is what decides resume-versus-fresh (see resumes). A field
	// rather than a direct call so a test can answer it without a home directory.
	conversation func(agentSessionID string) bool

	// specs is the user's authored agent descriptions — which agents can be launched,
	// what settings each new session offers, and how each value reaches the command
	// line (internal/agentcatalog). Injected after construction for the same
	// reason as the catalog, and nil the same way: an agent nothing describes cannot
	// be launched, which is the same answer as one whose binary is not installed.
	specs agentspec.Source

	// launchMu guards launching, the per-target locks that serialize Start.
	// Separate from p.mu because the capture path deliberately runs *without*
	// it, so a wedged runtime cannot block the provider — see the
	// lock-discipline pattern.
	launchMu  sync.Mutex
	launching map[string]*targetLock
	// now is injectable for temporal-state tests.
	now func() time.Time
}

// targetLock serializes Start for one target. refs keeps it in launching only
// as long as someone is using it, so the map tracks in-flight launches rather
// than every target the daemon has ever seen.
type targetLock struct {
	mu   sync.Mutex
	refs int
}

// startOnce runs Start for target unless another goroutine is already starting
// it, in which case it waits for that one and skips its own.
//
// Serializing here rather than at each caller is deliberate: Start is reached
// from the capture path (unlocked), from ensureAliveLocked via Open and
// SessionState, and from StartSession — all three under different locking, and
// guarding only the first left a Connect click able to race the watcher. A
// second `bridge serve` on one socket does not fail; serve unlinks the socket
// before binding, so the loser's agent keeps running in the working directory,
// unreachable and un-stoppable.
//
// The wait is bounded by one Start (itself bounded), and the caller may hold
// p.mu across it — no worse in kind than ensureAliveLocked, which already runs
// Start under the lock.
func (p *Provider) startOnce(ctx context.Context, target, dir string, argv []string) error {
	lock := p.acquireTarget(target)
	defer p.releaseTarget(target, lock)

	// The winner may have finished as we waited, leaving nothing to launch.
	if err := p.runtime.Probe(ctx, target); err == nil {
		return nil
	} else if !errors.Is(err, sbridge.ErrNotServing) {
		return fmt.Errorf("%w: %w", errBridgeUnanswered, err)
	}
	return p.runtime.Start(ctx, target, dir, argv)
}

// errBridgeUnanswered is startOnce refusing to start beside a bridge that may
// still be serving: a timed-out probe is not a dead agent.
var errBridgeUnanswered = errors.New("bridge did not answer; not starting a second agent")

func (p *Provider) acquireTarget(target string) *targetLock {
	p.launchMu.Lock()
	if p.launching == nil {
		p.launching = map[string]*targetLock{}
	}
	lock, ok := p.launching[target]
	if !ok {
		lock = &targetLock{}
		p.launching[target] = lock
	}
	lock.refs++
	p.launchMu.Unlock()

	lock.mu.Lock()
	return lock
}

func (p *Provider) releaseTarget(target string, lock *targetLock) {
	lock.mu.Unlock()

	p.launchMu.Lock()
	defer p.launchMu.Unlock()
	lock.refs--
	if lock.refs == 0 {
		delete(p.launching, target)
	}
}

var (
	_ provider.WatchedDiscoverer   = (*Provider)(nil)
	_ provider.SessionProvider     = (*Provider)(nil)
	_ provider.Pruner              = (*Provider)(nil)
	_ provider.Watcher             = (*Provider)(nil)
	_ provider.SessionStartStopper = (*Provider)(nil)
	_ provider.SettingsAware       = (*Provider)(nil)
	_ provider.ModelCatalogAware   = (*Provider)(nil)
	_ provider.AgentSpecAware      = (*Provider)(nil)
)

// SetModelCatalog wires the user's model catalog (implements
// provider.ModelCatalogAware).
func (p *Provider) SetModelCatalog(c agentmodel.Catalog) { p.catalog = c }

// SetAgentSpecs wires the user's agent descriptions (implements
// provider.AgentSpecAware), then records the agent id on restored sessions that
// predate it (adoptLegacySessions).
func (p *Provider) SetAgentSpecs(s agentspec.Source) {
	p.specs = s
	p.adoptLegacySessions()
}

// adoptLegacySessions gives a restored session persisted before sessions recorded
// their agent id the id its binary resolves to now, and persists it. After this the
// session no longer depends on the binary, so a later PATCH of the agent's binary
// leaves it resolvable. It cannot happen in New: the provider is built from init(),
// before there are descriptions to ask.
func (p *Provider) adoptLegacySessions() {
	p.mu.Lock()
	defer p.mu.Unlock()
	var agents agentIndex
	adopted := false
	for _, workspaces := range p.byProject {
		for _, ws := range workspaces {
			for _, sess := range ws.sessions {
				if sess.agentID != "" {
					continue
				}
				if agents == nil {
					agents = p.indexAgents()
				}
				if a, ok := agents.of("", sess.agent); ok {
					sess.agentID = a.ID
					adopted = true
				}
			}
		}
	}
	if adopted {
		p.persistLocked()
	}
}

// New builds the local provider over a runtime and a store. It rehydrates
// persisted sessions best-effort: a load error is logged and the provider starts
// empty rather than blocking the daemon.
func New(runtime Runtime, localStore Store) *Provider {
	localProvider := &Provider{
		runtime: runtime, localStore: localStore,
		byProject: map[string][]*workspace{}, conversation: hasConversation,
	}
	localProvider.term = terminal.NewOSLauncher(localProvider.TerminalPreference)
	persisted, err := localStore.Load()
	if err != nil {
		slog.Warn("loading local provider state failed; starting empty", "error", err)
		return localProvider
	}
	for _, pw := range persisted {
		w := &workspace{projectID: pw.ProjectID, dir: pw.WorkingDir}
		for _, ps := range pw.Sessions {
			w.sessions = append(w.sessions, &session{
				id: ps.ID, title: ps.Title, agent: ps.Agent, agentID: ps.AgentID, target: ps.Target,
				agentSessionID: ps.AgentSessionID, model: ps.Model, config: ps.Config,
				hold: detect.Hold{State: domain.SessionIdle}, stopped: ps.Stopped, sock: ps.Sock,
			})
		}
		localProvider.byProject[pw.ProjectID] = append(localProvider.byProject[pw.ProjectID], w)
	}
	return localProvider
}

// resumes reports whether bringing a session back should resume its conversation
// or launch the agent fresh under the same session id.
//
// A resumable agent whose transcript is not on disk has nothing to resume: the CLI
// writes it on the first answer and not at startup, so a session that was created
// and never prompted has none — and `claude --resume <id>` on an id it has no
// conversation for prints "No conversation found" and exits. Resuming it anyway is
// how a session came back dead on every attempt, and the runtime reports that
// launch as a success once the bridge answers (see the relaunched guard), so nothing
// noticed.
//
// Launching fresh instead does not lose a conversation, because there is none; it
// re-runs the session's initial config, which is what a fresh launch of a
// non-resumable agent does too.
func (p *Provider) resumes(a agentspec.Agent, agentSessionID string) bool {
	if !a.Resumable() {
		return false
	}
	if a.Transcript && p.conversation != nil && !p.conversation(agentSessionID) {
		return false
	}
	return true
}

func (p *Provider) ID() string    { return providerID }
func (p *Provider) Label() string { return providerLabel }

// Available reports whether any agent can be launched right now. The provider is
// available when at least one agent CLI is on PATH; when
// none is, the last runtime error is reported so the reason reaches clients via
// the provider descriptor.
func (p *Provider) Available(ctx context.Context) (bool, error) {
	var lastErr error
	for _, a := range p.agents() {
		if err := p.runtime.Available(ctx, a.Binary); err != nil {
			lastErr = err
			continue
		}
		return true, nil
	}
	if lastErr == nil {
		// Either every described agent is missing from PATH, or nothing describes any:
		// a daemon with no agent rows has nothing to launch, and says so rather than
		// claiming to be ready.
		lastErr = errors.New("no local agent available")
	}
	return false, lastErr
}

func (p *Provider) Capabilities() provider.Capabilities {
	// MaxConcurrentSessions is 0 (unlimited): several bridge sessions can run in one
	// workstream. DetectsWaiting is true: the detect rules map blocked→waiting.
	return provider.Capabilities{OpenModes: []string{"terminal"}, DetectsWaiting: true}
}

// SessionSchemas returns the session types creatable in the workstream: one option
// per described agent whose CLI is on PATH, each carrying the schema generated from
// that agent's settings rows. The ref is ignored (what the host can launch does not
// vary per workstream); the set still varies between calls as agents are installed or
// removed — and now also as the user edits their descriptions.
func (p *Provider) SessionSchemas(ctx context.Context, _ provider.WorkstreamRef) ([]provider.SessionSchemaOption, error) {
	var opts []provider.SessionSchemaOption
	for _, a := range p.agents() {
		if err := p.runtime.Available(ctx, a.Binary); err != nil {
			continue
		}
		// An agent that can list its models is asked and offers what it answered; one
		// that cannot offers the user's authored rows (agentmodel.Offered).
		discovered := p.modelsFor(ctx, a)
		models := agentmodel.Offered(discovered, p.catalog, a.ID)
		opts = append(opts, provider.SessionSchemaOption{
			ID:     a.ID,
			Label:  a.Display,
			Schema: agentspec.Schema(a, models, agentspec.Host),
		})
	}
	return opts, nil
}

func (p *Provider) CreateSession(ctx context.Context, req provider.CreateSessionRequest) (domain.Session, error) {
	if req.WorkingDir == "" {
		return domain.Session{}, fmt.Errorf("local provider requires a working directory")
	}
	a, ok := p.agentFor(req.OptionID)
	if !ok {
		return domain.Session{}, fmt.Errorf("unknown session option %q", req.OptionID)
	}
	// provider.Service has already validated the config against this option's schema,
	// model enum included, before calling here. This re-check is for any other caller,
	// and leaves the model enum out only because building it can mean asking the
	// agent's CLI (modelsFor); every other field is checked against the rows.
	if err := provider.ValidateConfig(agentspec.Schema(a, nil, agentspec.Host), req.Config); err != nil {
		return domain.Session{}, err
	}
	// The model travels in the config; it is lifted out here so the session records
	// what it launched with, which is what a resume re-passes.
	model := agentspec.ModelOf(a, req.Config)
	p.mu.Lock()
	defer p.mu.Unlock()
	ws := p.ensureWorkspaceLocked(req.Workstream.ProjectID, req.WorkingDir)
	sess, err := p.launchLocked(ctx, ws, a, model, "", req.Config)
	if err != nil {
		return domain.Session{}, err
	}
	p.persistLocked()
	// Freshly launched: reads idle until the watcher refreshes it.
	return domain.Session{ID: sess.id, Title: sess.title, Agent: a.Display, State: domain.SessionIdle}, nil
}

// StopSession kills a session's terminal and marks it stopped. A stopped session
// reports SessionStopped and is not captured or resumed until StartSession.
func (p *Provider) StopSession(ctx context.Context, ref provider.SessionRef) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, s := p.findSessionLocked(ref.ProjectID, ref.SessionID)
	if s == nil {
		return fmt.Errorf("session %q not found", ref.SessionID)
	}
	if err := p.stopWherever(ctx, s); err != nil {
		return fmt.Errorf("stopping session: %w", err)
	}
	s.stopped = true
	s.resumeFailed = false
	s.relaunched = false
	s.hold = detect.Hold{State: domain.SessionStopped, RuleSet: s.hold.RuleSet}
	s.revision++
	s.lifecycle++
	s.observed = nil
	p.persistLocked()
	return nil
}

// Prompt delivers a one-off line of text to a session's terminal (see
// provider.Prompter). A stopped session has no live terminal, so it is rejected —
// and so is one whose terminal is gone (ErrSessionGone), which the provider would
// otherwise type into and get an unexplained `bridge send` failure for.
func (p *Provider) Prompt(ctx context.Context, ref provider.SessionRef, text string) error {
	p.mu.Lock()
	_, s := p.findSessionLocked(ref.ProjectID, ref.SessionID)
	if s == nil {
		p.mu.Unlock()
		return fmt.Errorf("session %q not found", ref.SessionID)
	}
	if s.stopped {
		p.mu.Unlock()
		return fmt.Errorf("session %q: %w", ref.SessionID, provider.ErrSessionStopped)
	}
	if s.resumeFailed {
		p.mu.Unlock()
		return fmt.Errorf("session %q: %w", ref.SessionID, provider.ErrSessionGone)
	}
	target := s.target
	p.mu.Unlock() // release before the runtime call (it may block up to sbridgeTimeout)
	return p.runtime.SendInput(ctx, target, text)
}

// SendKey delivers a single named key event to a session's terminal (see
// provider.Prompter). Like Prompt, a stopped or gone session is rejected.
func (p *Provider) SendKey(ctx context.Context, ref provider.SessionRef, key string) error {
	p.mu.Lock()
	_, s := p.findSessionLocked(ref.ProjectID, ref.SessionID)
	if s == nil {
		p.mu.Unlock()
		return fmt.Errorf("session %q not found", ref.SessionID)
	}
	if s.stopped {
		p.mu.Unlock()
		return fmt.Errorf("session %q: %w", ref.SessionID, provider.ErrSessionStopped)
	}
	if s.resumeFailed {
		p.mu.Unlock()
		return fmt.Errorf("session %q: %w", ref.SessionID, provider.ErrSessionGone)
	}
	target := s.target
	p.mu.Unlock() // release before the runtime call (it may block up to sbridgeTimeout)
	return p.runtime.SendKey(ctx, target, key)
}

// StartSession brings a session back. A resumable agent (Claude) resumes its
// conversation by session UUID; an agent whose session id we do not control (Codex,
// Gemini) — or one with no conversation on disk to resume (resumes) — is relaunched
// fresh, re-running its initial config.
//
// It accepts both kinds of not-running session, because both report SessionStopped
// and the button that reaches this is the same one: a session the user stopped, and
// a session whose terminal went away and could not be brought back. For the latter
// this is the only way back, so treating it as "already active" left the user with a
// row nothing could revive.
func (p *Provider) StartSession(ctx context.Context, ref provider.SessionRef) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	ws, s := p.findSessionLocked(ref.ProjectID, ref.SessionID)
	if s == nil {
		return fmt.Errorf("session %q not found", ref.SessionID)
	}
	if !s.stopped && !s.resumeFailed {
		return nil // already active
	}
	a, ok := p.agentOf(s.agentID, s.agent)
	if !ok {
		return fmt.Errorf("unknown agent %q for session %q", s.agentID, s.id)
	}
	if err := p.startOnce(ctx, s.target, ws.dir, argv(a, s.launch(p.resumes(a, s.agentSessionID)))); err != nil {
		return fmt.Errorf("starting session: %w", err)
	}
	p.recordLocationLocked(s)
	s.stopped = false
	s.resumeFailed = false
	// Start is itself a launch not yet seen alive: an agent that still dies on it
	// is reported gone on the next tick rather than relaunched once more, so a
	// Start costs one launch.
	s.relaunched = true
	s.hold = detect.Hold{State: domain.SessionIdle, RuleSet: s.hold.RuleSet}
	s.revision++
	s.lifecycle++
	s.observed = nil
	p.persistLocked()
	return nil
}

// RemoveSession drops a session, tearing down its terminal first (a no-op for an
// already-stopped or gone session). Allowed in any state.
func (p *Provider) RemoveSession(ctx context.Context, ref provider.SessionRef) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	ws, s := p.findSessionLocked(ref.ProjectID, ref.SessionID)
	if s == nil {
		return fmt.Errorf("session %q not found", ref.SessionID)
	}
	if err := p.stopWherever(ctx, s); err != nil {
		slog.Warn("stopping session terminal on remove failed", "target", s.target, "error", err)
	}
	for i, candidate := range ws.sessions {
		if candidate.id == ref.SessionID {
			ws.sessions = append(ws.sessions[:i], ws.sessions[i+1:]...)
			break
		}
	}
	p.persistLocked()
	return nil
}

func (p *Provider) SessionState(ctx context.Context, ref provider.SessionRef) (domain.Session, error) {
	p.mu.Lock()
	ws, s := p.findSessionLocked(ref.ProjectID, ref.SessionID)
	if s == nil {
		p.mu.Unlock()
		return domain.Session{}, fmt.Errorf("session %q not found", ref.SessionID)
	}
	snap := snapshotSession(s, ws)
	p.mu.Unlock()
	agents := p.indexAgents()
	c := p.captureSession(ctx, snap, agents)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.applyCapturesLocked(map[string]sessionCapture{snap.id: c})
	_, current := p.findSessionLocked(ref.ProjectID, ref.SessionID)
	if current == nil {
		return domain.Session{}, fmt.Errorf("session %q not found", ref.SessionID)
	}
	return observedSession(current, agents), nil
}

// DiscoverSessions reports every session the provider is tracking for a project,
// each tagged with the working directory it runs in.
func (p *Provider) DiscoverSessions(ctx context.Context, project provider.ProjectRef) ([]provider.DiscoveredSession, error) {
	return p.discoverSessions(ctx, project, false)
}

// DiscoverWatchedSessions projects the watcher's last accepted observations,
// without recapturing the sockets that triggered this reconciliation.
func (p *Provider) DiscoverWatchedSessions(ctx context.Context, project provider.ProjectRef) ([]provider.DiscoveredSession, error) {
	return p.discoverSessions(ctx, project, true)
}

func (p *Provider) discoverSessions(ctx context.Context, project provider.ProjectRef, watched bool) ([]provider.DiscoveredSession, error) {
	agents := p.indexAgents()
	p.mu.Lock()
	var snaps []sessionSnapshot
	for _, ws := range p.byProject[project.ID] {
		for _, sess := range ws.sessions {
			if !watched || sess.observed == nil {
				snaps = append(snaps, snapshotSession(sess, ws))
			}
		}
	}
	p.mu.Unlock()
	caps := p.captureAll(ctx, snaps, agents)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.applyCapturesLocked(caps)
	// Build from the current sessions, not the out-of-lock input: a stop/start
	// or removal may have superseded a capture while the socket was answering.
	var out []provider.DiscoveredSession
	for _, ws := range p.byProject[project.ID] {
		for _, sess := range ws.sessions {
			out = append(out, provider.DiscoveredSession{WorkingDir: ws.dir, Session: observedSession(sess, agents)})
		}
	}
	return out, nil
}

// observedSession is called under p.mu. Lifecycle mutations clear observations.
func observedSession(s *session, agents agentIndex) domain.Session {
	result := domain.Session{ID: s.id, Title: s.title, State: s.hold.State}
	if s.observed != nil {
		result = *s.observed
		result.ProviderData = bytes.Clone(result.ProviderData)
		result.ObservationData = bytes.Clone(result.ObservationData)
	}
	result.Agent = agents.display(s.agentID, s.agent)
	return result
}

// Prune drops sessions whose project is not in keepProjectIDs, tearing down
// their terminals. Directory cleanup is the workstream provider's job.
func (p *Provider) Prune(ctx context.Context, keepProjectIDs []string) error {
	keep := make(map[string]bool, len(keepProjectIDs))
	for _, id := range keepProjectIDs {
		keep[id] = true
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	changed := false
	for projectID, workspaces := range p.byProject {
		if keep[projectID] {
			continue
		}
		for _, w := range workspaces {
			for _, s := range w.sessions {
				if err := p.stopWherever(ctx, s); err != nil {
					slog.Warn("stopping orphaned session failed", "target", s.target, "error", err)
				}
			}
		}
		delete(p.byProject, projectID)
		changed = true
	}
	if changed {
		p.persistLocked()
	}
	return nil
}

// sessionMark is what Watch remembers per session between ticks to decide when a
// re-projection is worth broadcasting: the normalized state, the live title, and
// the terminal-tail preview. Tracking the title and preview too means a
// detected-title change or a changed tail reaches clients even when the state is
// unchanged (e.g. a still-running session renames its terminal or emits output).
type sessionMark struct {
	state   domain.SessionState
	title   string
	preview string
	detail  string
}

// Watch drives live session updates: it re-detects every session at the
// configured refresh interval (falling back to watchInterval) and asks the
// daemon to re-project (which broadcasts to clients) only when a session's
// state, title, or terminal-tail preview (or the set of sessions) actually
// changed, so the UI reflects the detection mechanism without polling the store
// or spamming SSE. It runs until ctx is cancelled.
func (p *Provider) Watch(ctx context.Context, sink provider.Sink) {
	last := map[string]sessionMark{}
	provider.WatchLoop(ctx, sink, p.RefreshInterval, watchInterval, func(ctx context.Context) bool {
		return p.detectChanged(ctx, last)
	})
}

// detectChanged re-detects every session and reports whether any session's
// state, title, or terminal-tail preview (or the set of sessions) changed since
// the last tick, updating last in place. It also drives resume of sessions
// whose terminal was lost.
//
// The captures run WITHOUT holding p.mu: it snapshots the sessions under a
// brief lock, captures outside it, then re-acquires only to apply resume-failure
// marks and diff against last. This keeps a wedged runtime on the watch tick
// from blocking create/open/remove for up to the per-call timeout.
func (p *Provider) detectChanged(ctx context.Context, last map[string]sessionMark) bool {
	p.mu.Lock()
	var snaps []sessionSnapshot
	for _, workspaces := range p.byProject {
		for _, w := range workspaces {
			for _, s := range w.sessions {
				snaps = append(snaps, snapshotSession(s, w))
			}
		}
	}
	p.mu.Unlock()

	marks := make(map[string]sessionMark, len(snaps))
	// Read once for the whole pass: this runs every tick, for every session.
	agents := p.indexAgents()
	caps := p.captureAll(ctx, snaps, agents)

	p.mu.Lock()
	p.applyCapturesLocked(caps)
	for _, workspaces := range p.byProject {
		for _, ws := range workspaces {
			for _, sess := range ws.sessions {
				observed := observedSession(sess, agents)
				marks[sess.id] = sessionMark{state: observed.State, title: observed.Title, preview: observed.Preview, detail: string(observed.ObservationData)}
			}
		}
	}
	p.mu.Unlock()
	changed := false
	seen := make(map[string]bool, len(marks))
	for id, m := range marks {
		seen[id] = true
		if prev, ok := last[id]; !ok || prev != m {
			last[id] = m
			changed = true
		}
	}
	for id := range last {
		if !seen[id] {
			delete(last, id)
			changed = true
		}
	}
	return changed
}

// Open launches a local terminal emulator attached to the given session, so the
// user can interact with the running agent. It ensures the session's terminal
// is alive (resuming it if the bridge process had died) before attaching.
// Opening a session that already has a terminal attached moves it into the
// new window: the bridge detaches the previous client (last attach wins).
func (p *Provider) Open(ctx context.Context, ref provider.SessionRef, mode string) (provider.OpenResult, error) {
	if mode != "terminal" {
		return provider.OpenResult{}, fmt.Errorf("unsupported open mode %q", mode)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	ws, s := p.findSessionLocked(ref.ProjectID, ref.SessionID)
	if s == nil {
		return provider.OpenResult{}, fmt.Errorf("session %q not found", ref.SessionID)
	}
	if quit := p.ensureAliveLocked(ctx, ws, s); quit {
		return provider.OpenResult{}, fmt.Errorf("session %q: %w", ref.SessionID, provider.ErrSessionStopped)
	}
	if s.resumeFailed {
		return provider.OpenResult{}, provider.ErrSessionGone
	}
	// Attach where the session actually is. Addressing the current location for
	// one serving elsewhere opens a terminal that fails to dial and closes
	// immediately, while this call still reports success.
	cmd := p.runtime.AttachCommand(s.target)
	if elsewhere := p.reachableAt(ctx, s); elsewhere != "" {
		cmd = p.runtime.(LocationRecorder).AttachCommandAt(elsewhere)
	}
	if err := p.term.Launch(ctx, terminal.HoldOnFailure(cmd...), s.title); err != nil {
		return provider.OpenResult{}, fmt.Errorf("could not open a terminal: %w", err)
	}
	data, err := json.Marshal(map[string]string{"target": s.target})
	if err != nil {
		return provider.OpenResult{}, err
	}
	return provider.OpenResult{Mode: mode, Data: data}, nil
}

// launchLocked starts a fresh agent session in ws.dir with a new session UUID
// and the supplied launch config, appending it to the workspace. The caller must
// hold p.mu and persist.
func (p *Provider) launchLocked(ctx context.Context, ws *workspace, a agentspec.Agent, model, title string, config json.RawMessage) (*session, error) {
	uuid, err := newUUID()
	if err != nil {
		return nil, err
	}
	if title == "" {
		title = fmt.Sprintf("Session %d", len(ws.sessions)+1)
	}
	sess := &session{
		id:             "sess-" + uuid,
		title:          title,
		agent:          a.Binary,
		agentID:        a.ID,
		target:         "hs-" + uuid,
		agentSessionID: uuid,
		model:          model,
		config:         config,
		hold:           detect.Hold{State: domain.SessionIdle},
	}
	if err := p.startOnce(ctx, sess.target, ws.dir, argv(a, sess.launch(false))); err != nil {
		return nil, fmt.Errorf("launching %s: %w", a.Binary, err)
	}
	p.recordLocationLocked(sess)
	ws.sessions = append(ws.sessions, sess)
	return sess, nil
}

// launch builds the delivery parameters for this session; resume selects a resume
// launch (continue by id) over a fresh one (seed title + initial prompt). The binary
// is not among them — it comes from the agent's description, which the caller has
// already resolved.
func (s *session) launch(resume bool) agentspec.Launch {
	return agentspec.Launch{
		Model: s.model, SessionID: s.agentSessionID,
		Title: s.title, Config: s.config, Resume: resume,
	}
}

// ensureAliveLocked re-attaches or brings back a session whose terminal is gone. A
// resumable agent (Claude) is resumed by UUID, or launched fresh under the same id
// when it has no conversation to resume (resumes); a non-resumable agent is marked
// gone rather than silently restarted. A relaunch is attempted only once per loss.
// An agent that quit on its own is stopped instead, reported by quit.
func (p *Provider) ensureAliveLocked(ctx context.Context, ws *workspace, s *session) (quit bool) {
	if err := p.runtime.Probe(ctx, s.target); err == nil {
		s.relaunched = false
		return false
	} else if !errors.Is(err, sbridge.ErrNotServing) {
		return false // unknown, not gone: a relaunch could fork a second agent
	}
	if p.aliveElsewhereLocked(ctx, s) {
		return false
	}
	if s.resumeFailed {
		return false
	}
	if _, ok := p.quitOnItsOwn(s.target, s.sock, s.relaunched); ok {
		p.markQuitLocked(s)
		p.persistLocked()
		return true
	}
	a, ok := p.agentOf(s.agentID, s.agent)
	if !ok || !a.Resumable() {
		s.resumeFailed = true // cannot resume this agent by id; report it gone
		return false
	}
	if s.relaunched {
		s.resumeFailed = true // the relaunch we already made did not survive
		slog.Warn("relaunched session is gone again; not trying further", "target", s.target, "session", s.agentSessionID)
		return false
	}
	if err := p.startOnce(ctx, s.target, ws.dir, argv(a, s.launch(p.resumes(a, s.agentSessionID)))); errors.Is(err, errBridgeUnanswered) {
		return false
	} else if err != nil {
		s.resumeFailed = true
		slog.Warn("resuming session failed; marking it gone", "target", s.target, "session", s.agentSessionID, "error", err)
		return false
	}
	s.relaunched = true
	p.recordLocationLocked(s)
	return false
}

// quitOnItsOwn returns the exit record of an agent that ended without the
// bridge ending it (e.g. the user's /quit). A relaunch not yet seen alive that
// dies is a failed relaunch, not a quit.
func (p *Provider) quitOnItsOwn(target, sock string, relaunched bool) (sbridge.Exit, bool) {
	if relaunched {
		return sbridge.Exit{}, false
	}
	rec, ok := p.lastExit(target, sock)
	return rec, ok && rec.QuitOnItsOwn()
}

// markQuitLocked stops a session whose agent quit, as StopSession would but
// without a runtime call: its bridge is already gone. The caller persists.
func (p *Provider) markQuitLocked(s *session) {
	s.stopped = true
	s.hold = detect.Hold{State: domain.SessionStopped, RuleSet: s.hold.RuleSet}
}

// aliveElsewhereLocked reports whether a failed liveness probe is untrustworthy
// because the session is still served where a previous daemon start put it. It
// is the guard between "the agent died" and "I looked in the wrong place": only
// the first may be resumed, since resuming the second leaves two live agents in
// one working directory. The session is deliberately left alone rather than
// re-adopted — the runtime would still address the current location for every
// later call — so it reads as unreachable until the environment is consistent
// again, which is the degrade-don't-break side of the trade. The caller
// must hold p.mu.
func (p *Provider) aliveElsewhereLocked(ctx context.Context, s *session) bool {
	elsewhere := p.reachableAt(ctx, s)
	if elsewhere == "" {
		return false
	}
	slog.Warn("session is alive at a socket this daemon no longer uses; not resuming it (a resume would fork a second agent)",
		"target", s.target, "found", elsewhere, "current", p.locationOf(s.target))
	return true
}

// reachableAt returns the location a session is actually served at when that
// differs from where the runtime would look — i.e. when the daemon resolved a
// different socket directory than the start that launched it. The empty string
// means the current location is the right one to address.
//
// Resume, Stop, Remove and Open all consult this one answer. Splitting it into
// "is it elsewhere?" for the resume and "where?" for the rest let the two
// disagree: a session with no recorded socket was refused a resume (the runtime
// probes its legacy path for those) yet still addressed at the current socket
// by everything else — so Stop reported success as the agent ran on, Remove
// leaked the bridge, and Open launched a terminal that died on dial. Hence a
// location, not a bool, and no short-circuit on an empty recorded value.
func (p *Provider) reachableAt(ctx context.Context, s *session) string {
	recorder, ok := p.runtime.(LocationRecorder)
	if !ok {
		return ""
	}
	return recorder.LocateElsewhere(ctx, s.target, s.sock)
}

// stopWherever shuts a session down at whichever location actually serves it.
func (p *Provider) stopWherever(ctx context.Context, s *session) error {
	if elsewhere := p.reachableAt(ctx, s); elsewhere != "" {
		slog.Info("stopping a session at the socket it was started on, not the one this daemon computes",
			"target", s.target, "recorded", elsewhere)
		return p.runtime.(LocationRecorder).StopAt(ctx, elsewhere)
	}
	return p.runtime.Stop(ctx, s.target)
}

// recordLocationLocked stamps the session with where the runtime just resolved
// it, so a later start can tell a dead bridge from a relocated one. No-op for a
// runtime without the capability. The caller must hold p.mu.
func (p *Provider) recordLocationLocked(s *session) {
	s.sock = p.locationOf(s.target)
}

// locationOf is where the runtime currently resolves target, or "" for a
// runtime that does not record locations.
func (p *Provider) locationOf(target string) string {
	if recorder, ok := p.runtime.(LocationRecorder); ok {
		return recorder.Location(target)
	}
	return ""
}

// sessionSnapshot is a lock-free copy of a session's identity, taken under the
// lock so the captures can run without holding it.
type sessionSnapshot struct {
	projectID, id, title, agent, agentID, target, dir, agentSessionID, model string
	config                                                                   json.RawMessage
	resumeFailed                                                             bool
	relaunched                                                               bool
	stopped                                                                  bool
	hold                                                                     detect.Hold
	revision                                                                 uint64
	lifecycle                                                                uint64
	// sock is the location recorded at launch (see LocationRecorder), carried
	// so the out-of-lock capture can tell a dead bridge from a relocated one.
	sock string
	// preview is the last observed one, kept across a failed read.
	preview string
}

// launch builds the delivery parameters for this snapshot (see session.launch).
func (snap sessionSnapshot) launch(resume bool) agentspec.Launch {
	return agentspec.Launch{
		Model: snap.model, SessionID: snap.agentSessionID,
		Title: snap.title, Config: snap.config, Resume: resume,
	}
}

// captureAll captures snaps outside the lock, in parallel.
func (p *Provider) captureAll(ctx context.Context, snaps []sessionSnapshot, agents agentIndex) map[string]sessionCapture {
	results := make([]sessionCapture, len(snaps))
	provider.Fanout(len(snaps), provider.CaptureConcurrency, func(i int) {
		results[i] = p.captureSession(ctx, snaps[i], agents)
	})
	caps := make(map[string]sessionCapture, len(snaps))
	for i, snap := range snaps {
		caps[snap.id] = results[i]
	}
	return caps
}

// sessionCapture is the result of capturing one session outside the lock.
type sessionCapture struct {
	revision  uint64
	lifecycle uint64
	hold      detect.Hold
	// classified is set once a screen was detected; only then does the capture
	// move the idle hold, so one failed read cannot cancel it.
	classified       bool
	sess             domain.Session
	markResumeFailed bool
	// markStopped stops a session whose agent quit on its own (markQuitLocked).
	markStopped bool
	// newSock/setSock carry the location a resume just started the session at,
	// written back under the lock so it is persisted like any other launch.
	newSock string
	setSock bool

	// markRelaunched / clearRelaunched carry the relaunch guard back to the live
	// session: set when this pass launched the agent again, cleared the moment the
	// session is seen alive, which is what makes the guard one-attempt-per-loss
	// rather than one-attempt-ever.
	markRelaunched  bool
	clearRelaunched bool
	newTitle        string
	setTitle        bool
}

// sessionDetail is the provider-specific detection detail carried in
// domain.Session.ObservationData.
type sessionDetail struct {
	detect.Detail
	Gone bool `json:"gone,omitempty"`
}

// snapshotSession copies a session for lock-free capture. The caller must hold
// p.mu. config is cloned so the snapshot never shares the live backing array.
func snapshotSession(s *session, w *workspace) sessionSnapshot {
	snap := sessionSnapshot{
		projectID: w.projectID, id: s.id, title: s.title, agent: s.agent, agentID: s.agentID, target: s.target, dir: w.dir,
		agentSessionID: s.agentSessionID, model: s.model, config: bytes.Clone(s.config),
		resumeFailed: s.resumeFailed, relaunched: s.relaunched, stopped: s.stopped,
		hold: s.hold, revision: s.revision, lifecycle: s.lifecycle, sock: s.sock,
	}
	if s.observed != nil {
		snap.preview = s.observed.Preview
	}
	return snap
}

// captureSession derives a session's live state WITHOUT holding p.mu. A stopped
// session short-circuits to SessionStopped; a lost terminal is brought back
// (Claude) or reported gone — which is also SessionStopped, see gone; a capture
// error reports SessionUnknown rather than a stale state (#6). It also lifts the
// agent's live terminal title. agents is the pass's read of the descriptions
// (agentIndex), so capturing does not touch the catalog.
func (p *Provider) captureSession(ctx context.Context, snap sessionSnapshot, agents agentIndex) sessionCapture {
	c := sessionCapture{revision: snap.revision, lifecycle: snap.lifecycle, sess: domain.Session{ID: snap.id, Title: snap.title, Agent: agents.display(snap.agentID, snap.agent), State: domain.SessionIdle}}
	if snap.stopped {
		c.sess.State = domain.SessionStopped
		// Stop clears the record, so one left is the screen of an agent that quit.
		if rec, ok := p.lastExit(snap.target, snap.sock); ok {
			c.sess.Preview = detect.ScreenTail(rec.Screen, detect.TailLines)
		}
		return c
	}
	ruleSetID := snap.agent
	if agent, ok := agents.of(snap.agentID, snap.agent); ok {
		ruleSetID = agent.DetectionID()
	}
	captureCtx, cancel := p.CaptureContext(ctx)
	res, err := p.runtime.State(captureCtx, ruleSetID, snap.target)
	cancel()
	if err != nil && !errors.Is(err, sbridge.ErrNotServing) {
		// Like Enclave: one timed-out read is not a reason to blank the screen.
		c.sess.State, c.sess.Preview = domain.SessionUnknown, snap.preview
		return c
	}
	alive := err == nil
	if !alive {
		// A negative probe is not proof the agent died: the bridge may be
		// serving where an earlier daemon start put it. Resuming then forks a
		// second agent into snap.dir beside the running one, so report the
		// session unreachable and leave it alone instead.
		if recorder, ok := p.runtime.(LocationRecorder); ok {
			locateCtx, cancel := p.CaptureContext(ctx)
			elsewhere := recorder.LocateElsewhere(locateCtx, snap.target, snap.sock)
			cancel()
			if elsewhere != "" {
				slog.Warn("session is alive at a socket this daemon no longer uses; not resuming it (a resume would fork a second agent)",
					"target", snap.target, "found", elsewhere, "current", recorder.Location(snap.target))
				c.sess.State = domain.SessionUnknown
				return c
			}
		}
		if !snap.resumeFailed {
			if rec, ok := p.quitOnItsOwn(snap.target, snap.sock, snap.relaunched); ok {
				c.markStopped = true
				c.sess.State = domain.SessionStopped
				c.sess.Preview = detect.ScreenTail(rec.Screen, detect.TailLines)
				return c
			}
		}
		a, ok := agents.of(snap.agentID, snap.agent)
		if snap.resumeFailed || !ok || !a.Resumable() {
			return p.gone(c, snap)
		}
		// A relaunch this provider already made and that the session did not survive
		// is the end of it: the runtime's launch succeeds once the bridge answers,
		// so an agent that exits after that is reported as a success and only a
		// later tick can tell the two apart. Without this the daemon relaunched a session
		// that could never come up on every single tick, and the user got a terminal
		// window that closed itself and prompts that failed with no reason given.
		if snap.relaunched {
			c.markResumeFailed = true
			attrs := []any{"target", snap.target, "session", snap.agentSessionID}
			if rec, ok := p.lastExit(snap.target, snap.sock); ok {
				attrs = append(attrs, "exit", rec.Summary())
			}
			slog.Warn("relaunched session is gone again; reporting it gone", attrs...)
			return p.gone(c, snap)
		}
		current, err := p.relaunchIfCurrent(ctx, snap, argv(a, snap.launch(p.resumes(a, snap.agentSessionID))))
		if !current || errors.Is(err, errBridgeUnanswered) {
			c.sess.State = domain.SessionUnknown
			return c
		}
		if err != nil {
			c.markResumeFailed = true
			slog.Warn("resuming session failed; marking it gone", "target", snap.target, "session", snap.agentSessionID, "error", err)
			return p.gone(c, snap)
		}
		c.newSock = p.locationOf(snap.target)
		c.setSock = true
		c.markRelaunched = true
	} else if snap.relaunched {
		c.clearRelaunched = true
	}
	if alive && snap.sock == "" {
		if recorder, ok := p.runtime.(LocationRecorder); ok {
			c.newSock = recorder.Location(snap.target)
			c.setSock = true
		}
	}

	// A successful read already contains the detection; a resumed agent needs one.
	if !alive {
		captureCtx, cancel := p.CaptureContext(ctx)
		res, err = p.runtime.State(captureCtx, ruleSetID, snap.target)
		cancel()
	}
	if err != nil {
		slog.Warn("detecting session state failed; reporting unknown", "target", snap.target, "error", err)
		c.sess.State = domain.SessionUnknown
		return c
	}
	if title := detect.CleanTitle(res.Title); title != "" && title != snap.title {
		c.sess.Title = title
		c.newTitle = title
		c.setTitle = true
	}
	now := p.clock()
	c.hold = snap.hold
	c.sess.State = c.hold.Observe(ruleSetID, res, now, p.RefreshInterval())
	c.classified = true
	c.sess.Preview = res.Tail
	if data, err := json.Marshal(sessionDetail{Detail: res.Detail()}); err == nil {
		c.sess.ObservationData = data
	}
	return c
}

// relaunchIfCurrent resumes a capture's session unless a stop, start or removal
// superseded it. The launch runs without p.mu, so a stop or removal that lands
// meanwhile is caught afterwards and the agent it missed is stopped.
func (p *Provider) relaunchIfCurrent(ctx context.Context, snap sessionSnapshot, argv []string) (bool, error) {
	if !p.captureCurrent(snap) {
		return false, nil
	}
	// startOnce serializes per target, so concurrent captures produce one relaunch.
	err := p.startOnce(ctx, snap.target, snap.dir, argv)
	p.mu.Lock()
	defer p.mu.Unlock()
	_, s := p.findSessionLocked(snap.projectID, snap.id)
	if s != nil && s.lifecycle == snap.lifecycle && !s.stopped {
		return true, err
	}
	// A start that superseded the capture adopted the running agent; only a stop or removal disowns it.
	if err == nil && (s == nil || s.stopped) {
		if stopErr := p.runtime.Stop(context.WithoutCancel(ctx), snap.target); stopErr != nil {
			slog.Warn("stopping a relaunch that lost to a stop failed", "target", snap.target, "error", stopErr)
		}
	}
	return false, nil
}

// captureCurrent reports whether no stop, start or removal has superseded snap.
func (p *Provider) captureCurrent(snap sessionSnapshot) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, s := p.findSessionLocked(snap.projectID, snap.id)
	return s != nil && s.lifecycle == snap.lifecycle && !s.stopped
}

// applyCapturesLocked writes the state changes from a capture pass back onto the
// live sessions, keyed by session id. The caller must hold p.mu.
func (p *Provider) applyCapturesLocked(caps map[string]sessionCapture) {
	if len(caps) == 0 {
		return
	}
	quit := false
	defer func() {
		if quit {
			p.persistLocked()
		}
	}()
	for _, workspaces := range p.byProject {
		for _, w := range workspaces {
			for _, s := range w.sessions {
				c, ok := caps[s.id]
				if !ok || c.lifecycle != s.lifecycle {
					continue
				}
				// Runtime side effects survive a competing observation, but never
				// a deliberate stop/start. Discard only the superseded screen.
				if c.markResumeFailed {
					s.resumeFailed = true
				}
				if c.markStopped {
					p.markQuitLocked(s)
					quit = true
				}
				if c.markRelaunched {
					s.relaunched = true
				}
				if c.setSock {
					s.sock = c.newSock
				}
				if c.revision != s.revision {
					continue
				}
				s.revision++
				if c.classified {
					s.hold = c.hold
				}
				observation := c.sess
				s.observed = &observation
				if c.clearRelaunched {
					s.relaunched = false
				}
				if c.setTitle {
					s.title = c.newTitle
				}
			}
		}
	}
}

// gone finishes a capture for a session whose terminal is not there and is not
// coming back on its own. It reports SessionStopped — not SessionIdle: the session
// is not running, and calling it idle offered the user an open and a prompt that
// both reached a terminal that does not exist. Stopped is also what makes the
// existing Start action the way back (StartSession), and the gone marker is what
// tells a client it was not the user who stopped it. When the runtime recorded
// how the agent exited, the preview is the screen it left behind.
func (p *Provider) gone(c sessionCapture, snap sessionSnapshot) sessionCapture {
	c.sess.State = domain.SessionStopped
	c.sess.ObservationData = goneDetail()
	if rec, ok := p.lastExit(snap.target, snap.sock); ok {
		c.sess.Preview = detect.ScreenTail(rec.Screen, detect.TailLines)
	}
	return c
}

// lastExit is how target's agent last exited, if the runtime records it; sock
// is the location recorded at launch.
func (p *Provider) lastExit(target, sock string) (sbridge.Exit, bool) {
	if recorder, ok := p.runtime.(ExitRecorder); ok {
		return recorder.LastExit(target, sock)
	}
	return sbridge.Exit{}, false
}

func goneDetail() json.RawMessage {
	data, _ := json.Marshal(sessionDetail{Gone: true})
	return data
}

// persistLocked snapshots the current sessions to the store. The caller must
// hold p.mu. A save error is logged, not propagated.
func (p *Provider) persistLocked() {
	var out []PersistedWorkspace
	for _, workspaces := range p.byProject {
		for _, w := range workspaces {
			pw := PersistedWorkspace{ProjectID: w.projectID, WorkingDir: w.dir}
			for _, s := range w.sessions {
				pw.Sessions = append(pw.Sessions, PersistedSession{
					ID: s.id, Title: s.title, Agent: s.agent, AgentID: s.agentID, Target: s.target,
					AgentSessionID: s.agentSessionID, Model: s.model, Config: s.config, Stopped: s.stopped,
					Sock: s.sock,
				})
			}
			out = append(out, pw)
		}
	}
	if err := p.localStore.Save(out); err != nil {
		slog.Warn("persisting local provider state failed", "error", err)
	}
}

// ensureWorkspaceLocked returns the workspace for (projectID, dir), creating it
// if needed. The caller must hold p.mu.
func (p *Provider) ensureWorkspaceLocked(projectID, dir string) *workspace {
	dir = filepath.Clean(dir)
	for _, w := range p.byProject[projectID] {
		if w.dir == dir {
			return w
		}
	}
	w := &workspace{projectID: projectID, dir: dir}
	p.byProject[projectID] = append(p.byProject[projectID], w)
	return w
}

// findSessionLocked returns the workspace and session for a session ID within a
// project. The caller must hold p.mu.
func (p *Provider) findSessionLocked(projectID, sessionID string) (*workspace, *session) {
	for _, w := range p.byProject[projectID] {
		for _, s := range w.sessions {
			if s.id == sessionID {
				return w, s
			}
		}
	}
	return nil, nil
}

// newUUID returns a random RFC 4122 version-4 UUID string.
func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generating uuid: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// init self-registers the local session provider so it joins the daemon by
// import alone. It is always registered; Available reports it unavailable when
// no agent CLI is on PATH, so it degrades cleanly.
func init() {
	provider.RegisterSessionFactory(func() (provider.SessionProvider, error) {
		_, storePath := defaultStatePaths()
		// The socket namespace keys off the same directory the session store
		// lives in, so -home-dir moves both together.
		return New(hostbridge.New(filepath.Dir(storePath)), NewFileStore(storePath)), nil
	})
}

func (p *Provider) clock() time.Time {
	if p.now != nil {
		return p.now()
	}
	return time.Now()
}
