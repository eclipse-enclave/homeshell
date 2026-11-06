// Package fake is an in-memory demo provider. It plays both roles of the
// workstream/session divide at once, and when simulation is enabled cycles a
// session through running, waiting-for-input and idle so the live SSE updates
// and notifications can be exercised without a real backend.
package fake

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
	"github.com/eclipsesource/homeshell/daemon/internal/pathutil"
	"github.com/eclipsesource/homeshell/daemon/internal/provider"
	"github.com/eclipsesource/homeshell/daemon/internal/provider/enclave/sandboxschema"
)

// workstreamSchema is the JSON Schema for the fake provider's workstream
// creation form: a required branch and an optional copy-.env boolean.
const workstreamSchema = `{
  "type": "object",
  "additionalProperties": false,
  "required": ["branch"],
  "properties": {
    "branch": {"type": "string", "title": "Branch", "description": "Branch to base the workstream on", "minLength": 1},
    "copyEnv": {"type": "boolean", "title": "Copy .env", "description": "Copy the project's .env into the workstream", "default": false}
  }
}`

// sessionOptions is the fixed set of session types the fake provider offers.
//
// The two carry the two shapes a real provider's model knob has, so the e2e suite
// can drive both over the public API: claude's model has a default (the choice is
// made for you unless you change it), codex's is optional with none — the shape
// the Enclave provider uses, where leaving it unset means "whatever the container
// already runs" and the daemon must pass no model at all.
var sessionOptions = []provider.SessionSchemaOption{
	{
		ID: "claude", Label: "Claude Code",
		Schema: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"model":{"type":"string","enum":["opus","sonnet","haiku"],"default":"sonnet","title":"Model"}}}`),
	},
	{
		ID: "codex", Label: "Codex",
		Schema: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"model":{"type":"string","enum":["o3","o4-mini"],"title":"Model"}}}`),
	},
	{
		// A third option whose model enum carries no `default`, which is what makes
		// the form render it as a <select> with a leading "Default" entry rather
		// than as pills. Its own option rather than a `default` dropped from one of
		// the two above, so the specs that rely on those keep the schema they were
		// written against.
		ID: "opencode", Label: "OpenCode",
		Schema: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"model":{"type":"string","enum":["glm-4.6","kimi-k2","qwen3-coder"],"title":"Model"}}}`),
	},
	{
		// The shape the Enclave provider gives a container's sandbox settings: a
		// nested group of checklists, chip and row lists, with values inherited from
		// Enclave's own config shown alongside the user's. Its own option so the
		// e2e suite can drive that form — and a restart with it — without Enclave.
		ID: "sandboxed", Label: "Sandboxed demo",
		Schema: sandboxedSchema,
	},
}

// sandboxedSchema is the "sandboxed" option's create form: a model, and the
// Enclave provider's sandbox group over a demo Enclave config that lists a
// directory and a port of its own.
var sandboxedSchema = must(sandboxschema.With(
	json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"model":{"type":"string","enum":["opus","sonnet"],"default":"sonnet","title":"Model"}}}`),
	sandboxschema.Schema(sandboxschema.Base{
		Config: sandboxschema.EffectiveConfig{
			"features":          {Value: json.RawMessage(`["devtools","node-dev"]`), Source: "default"},
			"add_readonly_dirs": {Value: json.RawMessage(`["/home/demo/shared"]`), Source: "global"},
			"ports":             {Value: json.RawMessage(`["3000"]`), Source: "project"},
			"host_config":       {Value: json.RawMessage(`"none"`), Source: "default"},
		},
		Features: []sandboxschema.Feature{
			{Name: "devtools", Description: "Core development tools (vim, htop, ripgrep, etc.)"},
			{Name: "node-dev", Description: "Node.js development tools (typescript, eslint, prettier, etc.)"},
			{Name: "playwright-mcp", Description: "Playwright browsers and MCP server for UI testing"},
		},
	}),
))

// must is schema, or a panic: the demo's schemas are fixed, so one that cannot be
// built is a bug.
func must(schema json.RawMessage, err error) json.RawMessage {
	if err != nil {
		panic(fmt.Sprintf("building the demo's session schema: %v", err))
	}
	return schema
}

// SessionOptionIDs lists the agents this provider offers, for tests that assert
// on the set rather than pinning a count that adding one would break.
func SessionOptionIDs() []string {
	ids := make([]string, len(sessionOptions))
	for i, option := range sessionOptions {
		ids[i] = option.ID
	}
	return ids
}

func sessionOption(id string) (provider.SessionSchemaOption, bool) {
	for _, o := range sessionOptions {
		if o.ID == id {
			return o, true
		}
	}
	return provider.SessionSchemaOption{}, false
}

// optionOrDefault is optionID, or the first option for none.
func optionOrDefault(optionID string) string {
	if optionID == "" {
		return sessionOptions[0].ID
	}
	return optionID
}

// Config configures a fake provider.
type Config struct {
	// Seed pre-populates the sessions the fake owns, keyed by project ID and
	// bucketed by WorkingDir so DiscoverSessions reports them and the daemon
	// matches them to the seeded workstream records.
	Seed map[string][]domain.Workstream
	// CreateErr, when non-nil, makes Create return this error.
	CreateErr error
	// MaxConcurrentSessions caps how many non-terminal sessions may run at
	// once in a single workstream. 0 means unlimited.
	MaxConcurrentSessions int
	// Simulate turns on the demo session cycler.
	Simulate bool
	// SimProject and SimWorkingDir identify the workstream (by project and
	// working directory) the simulator drives. They must match a workstream the
	// daemon already knows about so the discovered sessions attach to it.
	SimProject    string
	SimWorkingDir string
	// SimInterval is the cadence of the simulator's state transitions.
	SimInterval time.Duration
	// OnCreate, when non-nil, is called during Create. Tests use it to observe
	// intermediate state while creation is in flight.
	OnCreate func(provider.CreateRequest)
	// SessionStartup, when > 0, makes CreateSession take about this long and emit
	// synthetic build output through the request's Progress sink as it goes.
	//
	// A real slow provider is one that builds a container image (Enclave); the fake
	// returns instantly, which leaves the starting-session placeholder and its log
	// tail with no window to be observed in. Setting this opens that window, so the
	// e2e suite can drive the feature over the public API instead of trusting a
	// unit test with it.
	SessionStartup time.Duration
}

// startupScript is the synthetic build output a delayed fake create emits, shaped
// like the buildx progress the Enclave provider really streams.
//
// Twice the ten lines the UI's output box shows at once, and no more: a real image
// build runs to hundreds of lines, so a script that fit on screen exactly would
// leave the scrollback — the thing the daemon retains those lines for —
// unobservable in the demo and untestable in the e2e suite. The cap is the
// client's own: SessionRow renders the last `previewLines` (20 by default), so a
// longer script would put its opening lines somewhere no user can scroll to.
var startupScript = []string{
	"#1 [internal] load build definition from Dockerfile",
	"#2 [internal] load metadata for docker.io/library/debian:bookworm",
	"#3 [internal] load .dockerignore",
	"#4 [base 1/6] FROM docker.io/library/debian:bookworm",
	"#4 sha256:9f2c0e1a resolve docker.io/library/debian:bookworm done",
	"#5 [base 2/6] RUN apt-get update && apt-get install -y git curl",
	"#5 DONE 11.2s",
	"#6 [base 3/6] RUN useradd -m agent",
	"#6 DONE 0.3s",
	"#7 [tool 4/6] RUN npm i -g @anthropic-ai/claude-code",
	"#7 added 1 package in 4s",
	"#7 DONE 4.1s",
	"#8 [tool 5/6] COPY entrypoint.sh /usr/local/bin/",
	"#9 [tool 6/6] RUN chmod +x /usr/local/bin/entrypoint.sh",
	"#10 exporting to image",
	"#10 exporting layers 2.6s done",
	"#10 writing image sha256:4b1d77e0 done",
	"#11 naming to docker.io/library/enclave-claude:latest",
	"starting container",
}

// bucket groups the sessions the fake tracks for one working directory, keyed on
// the working directory so discovered sessions match the daemon's workstream
// records regardless of ID. Like Enclave's containers, they belong to their
// directory rather than to a project record; projectID is the project that put
// them there, which finds them wherever its path is.
type bucket struct {
	projectID  string
	workingDir string
	sessions   []domain.Session
}

// Provider is an in-memory demo provider.
type Provider struct {
	mu      sync.Mutex
	buckets []*bucket
	counter int
	sim     Config
	// sink is captured when Watch starts so mutations outside the demo cycle
	// (e.g. Prompt) can broadcast a change to clients. Guarded by mu.
	sink provider.Sink
}

// Compile-time checks: the fake satisfies both provider contracts and the
// optional capabilities it advertises.
var (
	_ provider.WorkstreamProvider     = (*Provider)(nil)
	_ provider.SessionProvider        = (*Provider)(nil)
	_ provider.WorkstreamStartStopper = (*Provider)(nil)
	_ provider.SessionStartStopper    = (*Provider)(nil)
	_ provider.SessionRestarter       = (*Provider)(nil)
	_ provider.ContinueRefuser        = (*Provider)(nil)
	_ provider.Watcher                = (*Provider)(nil)
	_ provider.Prompter               = (*Provider)(nil)
	_ provider.ShellOpener            = (*Provider)(nil)
	_ provider.LinkOpener             = (*Provider)(nil)
)

// New builds a fake provider.
func New(cfg Config) *Provider {
	if cfg.SimInterval == 0 {
		cfg.SimInterval = 3 * time.Second
	}
	p := &Provider{sim: cfg}
	for projectID, workstreams := range cfg.Seed {
		for _, ws := range workstreams {
			if ws.WorkingDir == "" || len(ws.Sessions) == 0 {
				continue
			}
			b := p.ensureBucketLocked(projectID, ws.WorkingDir)
			b.sessions = append(b.sessions, ws.Sessions...)
		}
	}
	return p
}

func (p *Provider) ID() string    { return "fake" }
func (p *Provider) Label() string { return "Fake Provider" }

func (p *Provider) Available(context.Context) (bool, error) { return true, nil }

func (p *Provider) Capabilities() provider.Capabilities {
	return provider.Capabilities{
		OpenModes:             []string{"terminal"},
		MaxConcurrentSessions: p.sim.MaxConcurrentSessions,
	}
}

// --- WorkstreamProvider ---

func (p *Provider) CanCreate(context.Context, provider.ProjectRef) bool { return true }

func (p *Provider) Schema(context.Context, provider.ProjectRef) json.RawMessage {
	return json.RawMessage(workstreamSchema)
}

func (p *Provider) Create(_ context.Context, req provider.CreateRequest) (domain.Workstream, error) {
	if err := provider.ValidateConfig(json.RawMessage(workstreamSchema), req.Config); err != nil {
		return domain.Workstream{}, err
	}
	if req.OnProgress != nil {
		req.OnProgress("Fake provider working\u2026")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.sim.OnCreate != nil {
		p.sim.OnCreate(req)
	}
	if p.sim.CreateErr != nil {
		return domain.Workstream{}, p.sim.CreateErr
	}
	p.counter++
	name := req.Name
	if name == "" {
		name = fmt.Sprintf("workstream-%d", p.counter)
	}
	workingDir := filepath.Join(req.Project.Path, fmt.Sprintf(".fake-ws-%d", p.counter))
	return domain.Workstream{
		Name:       name,
		StrategyID: "worktree",
		Branch:     branchFromConfig(req.Config),
		WorkingDir: workingDir,
		Sessions:   []domain.Session{},
	}, nil
}

func (p *Provider) Remove(_ context.Context, project provider.ProjectRef, ws domain.Workstream, _ provider.RemoveOptions) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.dropBucketLocked(ws.WorkingDir)
	return nil
}

// --- SessionProvider ---

func (p *Provider) SessionSchemas(_ context.Context, _ provider.WorkstreamRef) ([]provider.SessionSchemaOption, error) {
	return slices.Clone(sessionOptions), nil
}

func (p *Provider) CreateSession(_ context.Context, req provider.CreateSessionRequest) (domain.Session, error) {
	opt, ok := sessionOption(req.OptionID)
	if !ok {
		return domain.Session{}, fmt.Errorf("unknown session option %q", req.OptionID)
	}
	if err := provider.ValidateConfig(opt.Schema, req.Config); err != nil {
		return domain.Session{}, err
	}
	// Emit the synthetic build log before taking the lock, so the daemon's own
	// state pushes are not blocked behind a create that is pretending to be slow.
	if p.sim.SessionStartup > 0 && req.Progress != nil {
		step := p.sim.SessionStartup / time.Duration(len(startupScript))
		for _, line := range startupScript {
			req.Progress(line)
			time.Sleep(step)
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.counter++
	session := domain.Session{
		ID:    fmt.Sprintf("sess-fake-%d", p.counter),
		Title: opt.Label,
		Agent: opt.Label,
		State: domain.SessionIdle,
	}
	b := p.ensureBucketLocked(req.Workstream.ProjectID, req.WorkingDir)
	b.sessions = append(b.sessions, session)
	return session, nil
}

func (p *Provider) RemoveSession(_ context.Context, ref provider.SessionRef) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.removeSessionLocked(ref.SessionID) {
		return fmt.Errorf("session %q not found", ref.SessionID)
	}
	return nil
}

func (p *Provider) SessionState(_ context.Context, ref provider.SessionRef) (domain.Session, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if session := p.sessionLocked(ref.SessionID); session != nil {
		return *session, nil
	}
	return domain.Session{}, fmt.Errorf("session %q not found", ref.SessionID)
}

func (p *Provider) Open(_ context.Context, ref provider.SessionRef, mode string) (provider.OpenResult, error) {
	if mode != "terminal" {
		return provider.OpenResult{}, fmt.Errorf("unsupported open mode %q", mode)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.sessionLocked(ref.SessionID) != nil {
		return provider.OpenResult{Mode: mode, Data: json.RawMessage(`{}`)}, nil
	}
	return provider.OpenResult{}, fmt.Errorf("session %q not found", ref.SessionID)
}

// OpenShell stands in for a provider whose sessions run in an environment of
// their own (provider.ShellOpener). Like Open it launches nothing, so the
// workstream-open path can be exercised without spawning a window.
func (p *Provider) OpenShell(_ context.Context, ref provider.SessionRef) (provider.OpenResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.sessionLocked(ref.SessionID) == nil {
		// Mirrors a container that has gone away: a conflict, not a failure.
		return provider.OpenResult{}, fmt.Errorf("%w: session %q is not running", provider.ErrNoShellEnvironment, ref.SessionID)
	}
	return provider.OpenResult{Mode: "terminal", Data: json.RawMessage(
		`{"target":"container","container":"fake","shell":"bash"}`)}, nil
}

// Links stands in for a provider declaring browser-openable hand-offs
// (provider.LinkOpener), so the link open modes can be exercised without a
// real backend.
func (p *Provider) Links(session domain.Session) []provider.Link {
	if session.State == domain.SessionStopped {
		return nil
	}
	return []provider.Link{{ID: "web-ide", Label: "Web IDE"}}
}

// LinkURL resolves one of the declared links. The URL points at nothing.
func (p *Provider) LinkURL(_ context.Context, ref provider.SessionRef, linkID string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if linkID != "web-ide" {
		return "", fmt.Errorf("%w: unknown link %q", provider.ErrNoLink, linkID)
	}
	if p.sessionLocked(ref.SessionID) == nil {
		return "", fmt.Errorf("%w: session %q is not running", provider.ErrNoLink, ref.SessionID)
	}
	return "http://127.0.0.1:3000/?fake=" + ref.SessionID, nil
}

// DiscoverSessions reports the sessions of a project: those in its directory or
// below it, and those it put elsewhere. A project removed and added again at the
// same path finds the sessions it left running in the workstream directories the
// fake created there.
func (p *Provider) DiscoverSessions(_ context.Context, project provider.ProjectRef) ([]provider.DiscoveredSession, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []provider.DiscoveredSession
	for _, sessionBucket := range p.buckets {
		if sessionBucket.projectID != project.ID && !within(project.Path, sessionBucket.workingDir) {
			continue
		}
		for _, sess := range sessionBucket.sessions {
			out = append(out, provider.DiscoveredSession{WorkingDir: sessionBucket.workingDir, Session: sess})
		}
	}
	return out, nil
}

func (p *Provider) StartSession(_ context.Context, ref provider.SessionRef) error {
	return p.setSessionStateByRef(ref, domain.SessionRunning)
}

func (p *Provider) StopSession(_ context.Context, ref provider.SessionRef) error {
	return p.setSessionStateByRef(ref, domain.SessionStopped)
}

// CheckRestart refuses no session: any of them can be restarted as any option.
func (p *Provider) CheckRestart(provider.SessionRef, string) error { return nil }

// ContinueRefusal refuses, as the Enclave provider does, to continue a session's
// conversation while another session of the same option works in its directory.
// No option is its first one, as Enclave takes none as its default tool.
func (p *Provider) ContinueRefusal(optionID, workingDir string, neighbourOptionIDs []string) error {
	option, ok := sessionOption(optionOrDefault(optionID))
	if !ok {
		return nil
	}
	for _, neighbourOptionID := range neighbourOptionIDs {
		if optionOrDefault(neighbourOptionID) == option.ID {
			return fmt.Errorf("%w: another %s session works in %s, and continuing may pick up its conversation; restart it fresh",
				provider.ErrCannotContinue, option.Label, workingDir)
		}
	}
	return nil
}

// PrepareRestart accepts any restart of a session it has: in the demo, the same
// session comes back running. The restart takes as long as a create does
// (Config.SessionStartup), so the window in which it is in flight can be observed.
//
// A restart that runs to the end brings the session back under its ID even when
// it was removed meanwhile, so it is the service cancelling the restart on removal
// that keeps a removed session gone, and the tests exercising that would notice it
// missing. It takes the session down as soon as it runs, so one cancelled at any
// point leaves the session starting, and returns provider.ErrRestartInterrupted
// for the session to be removed.
func (p *Provider) PrepareRestart(_ context.Context, req provider.RestartSessionRequest) (provider.RestartFunc, error) {
	p.mu.Lock()
	found := p.sessionLocked(req.Session.SessionID)
	if found == nil {
		p.mu.Unlock()
		return nil, fmt.Errorf("%w: %q", provider.ErrSessionNotFound, req.Session.SessionID)
	}
	session := *found
	session.OutdatedSettings = nil
	p.mu.Unlock()
	return func(ctx context.Context) error {
		p.mu.Lock()
		p.updateSimSessionLocked(req.Session.SessionID, func(simSession *domain.Session) {
			simSession.State = domain.SessionStarting
			simSession.OutdatedSettings = nil
		})
		p.mu.Unlock()
		p.notifyChanged()
		select {
		case <-time.After(p.sim.SessionStartup):
		case <-ctx.Done():
		}
		// Rechecked rather than left to the select, which picks either case when the
		// startup ends just as the restart is cancelled.
		if ctx.Err() != nil {
			return provider.ErrRestartInterrupted
		}
		session.State = domain.SessionRunning
		if p.setSessionStateByRef(req.Session, domain.SessionRunning) != nil {
			p.mu.Lock()
			bucket := p.ensureBucketLocked(req.Session.ProjectID, req.WorkingDir)
			bucket.sessions = append(bucket.sessions, session)
			p.mu.Unlock()
		}
		p.notifyChanged()
		return nil
	}, nil
}

// --- WorkstreamStartStopper (no-ops for the demo) ---

func (p *Provider) StartWorkstream(context.Context, provider.WorkstreamRef) error { return nil }
func (p *Provider) StopWorkstream(context.Context, provider.WorkstreamRef) error  { return nil }

// Watch runs the demo session cycler until ctx is cancelled, notifying the
// sink after each change so the daemon re-projects.
func (p *Provider) Watch(ctx context.Context, sink provider.Sink) {
	if !p.sim.Simulate {
		return
	}
	// Capture the sink so mutations outside this loop (Prompt) can broadcast.
	p.mu.Lock()
	p.sink = sink
	p.mu.Unlock()
	for n := 1; ; n++ {
		id := fmt.Sprintf("sess-demo-%d", n)
		session := domain.Session{
			ID:      id,
			Title:   fmt.Sprintf("Demo run #%d", n),
			Agent:   "Demo",
			State:   domain.SessionRunning,
			Preview: demoPreview(n, "running"),
		}
		steps := []func(){
			func() { p.addSimSession(session) },
			// A title change with an unchanged state, plus an advancing preview.
			func() {
				p.setSimSessionTitle(id, fmt.Sprintf("Demo run #%d · editing files", n))
				p.setSimSessionPreview(id, demoPreview(n, "editing files"))
			},
			// Waiting-for-input, so the notification monitor fires on the
			// running→waiting transition.
			func() { p.setSimSessionState(id, domain.SessionWaiting) },
			// Idle, not done: the daemon never emits `done`, which is a UI-only
			// overlay a client computes itself.
			func() { p.setSimSessionState(id, domain.SessionIdle) },
			func() { p.removeSimSession(id) },
		}
		for _, step := range steps {
			step()
			sink.Changed()
			if !sleep(ctx, p.sim.SimInterval) {
				return
			}
		}
	}
}

func (p *Provider) addSimSession(session domain.Session) {
	p.mu.Lock()
	defer p.mu.Unlock()
	b := p.ensureBucketLocked(p.sim.SimProject, p.sim.SimWorkingDir)
	b.sessions = append(b.sessions, session)
}

func (p *Provider) setSimSessionTitle(sessionID, title string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.updateSimSessionLocked(sessionID, func(s *domain.Session) { s.Title = title })
}

func (p *Provider) setSimSessionState(sessionID string, state domain.SessionState) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.updateSimSessionLocked(sessionID, func(s *domain.Session) { s.State = state })
}

func (p *Provider) updateSimSessionLocked(sessionID string, mutate func(*domain.Session)) {
	if session := p.sessionLocked(sessionID); session != nil {
		mutate(session)
	}
}

func (p *Provider) setSimSessionPreview(sessionID, preview string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.updateSimSessionLocked(sessionID, func(s *domain.Session) { s.Preview = preview })
}

// Prompt delivers a one-off prompt to a session by echoing it into the
// session's preview and broadcasting the change, so demo mode advertises
// oneOffPrompt and the UI shows the send-text input.
func (p *Provider) Prompt(_ context.Context, ref provider.SessionRef, text string) error {
	return p.echoIntoPreview(ref, "> "+text)
}

// SendKey echoes a bracketed key name into the session's preview, the same
// observable way Prompt does.
func (p *Provider) SendKey(_ context.Context, ref provider.SessionRef, key string) error {
	return p.echoIntoPreview(ref, "[key: "+key+"]")
}

// echoIntoPreview appends a line to a session's preview and broadcasts, so the
// effect is observable on the next projection.
func (p *Provider) echoIntoPreview(ref provider.SessionRef, line string) error {
	p.mu.Lock()
	session := p.sessionLocked(ref.SessionID)
	found := session != nil
	if found {
		session.Preview = appendPreviewLine(session.Preview, line)
	}
	sink := p.sink
	p.mu.Unlock()
	if !found {
		return fmt.Errorf("session %q not found in project %q", ref.SessionID, ref.ProjectID)
	}
	if sink != nil {
		sink.Changed()
	}
	return nil
}

// notifyChanged nudges the sink Watch captured, if any, so a change made
// outside the demo cycle (a restart) shows without waiting for the next poll.
func (p *Provider) notifyChanged() {
	p.mu.Lock()
	sink := p.sink
	p.mu.Unlock()
	if sink != nil {
		sink.Changed()
	}
}

func (p *Provider) removeSimSession(sessionID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.removeSessionLocked(sessionID)
}

func (p *Provider) setSessionStateByRef(ref provider.SessionRef, state domain.SessionState) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	session := p.sessionLocked(ref.SessionID)
	if session == nil {
		return fmt.Errorf("session %q not found", ref.SessionID)
	}
	session.State = state
	return nil
}

// --- helpers (caller holds p.mu) ---

// ensureBucketLocked returns the bucket of a working directory, created for
// projectID if there is none.
func (p *Provider) ensureBucketLocked(projectID, workingDir string) *bucket {
	for _, sessionBucket := range p.buckets {
		if sessionBucket.workingDir == workingDir {
			return sessionBucket
		}
	}
	sessionBucket := &bucket{projectID: projectID, workingDir: workingDir}
	p.buckets = append(p.buckets, sessionBucket)
	return sessionBucket
}

func (p *Provider) dropBucketLocked(workingDir string) {
	for i, sessionBucket := range p.buckets {
		if sessionBucket.workingDir == workingDir {
			p.buckets = append(p.buckets[:i], p.buckets[i+1:]...)
			return
		}
	}
}

// sessionLocked returns the session with the given ID, nil if there is none.
// Session IDs are the fake's own, unique across its working directories.
func (p *Provider) sessionLocked(sessionID string) *domain.Session {
	for _, sessionBucket := range p.buckets {
		for i := range sessionBucket.sessions {
			if sessionBucket.sessions[i].ID == sessionID {
				return &sessionBucket.sessions[i]
			}
		}
	}
	return nil
}

func (p *Provider) removeSessionLocked(sessionID string) bool {
	for _, sessionBucket := range p.buckets {
		for i := range sessionBucket.sessions {
			if sessionBucket.sessions[i].ID == sessionID {
				sessionBucket.sessions = append(sessionBucket.sessions[:i], sessionBucket.sessions[i+1:]...)
				return true
			}
		}
	}
	return false
}

// within reports whether dir is projectDir or below it.
func within(projectDir, dir string) bool {
	if projectDir == "" {
		return false
	}
	return pathutil.Within(dir, projectDir)
}

// demoPreview builds a canned terminal tail for a demo session, embedding the
// cycle counter and phase so it visibly changes as the simulator advances.
func demoPreview(n int, phase string) string {
	return fmt.Sprintf("$ demo build\n... run #%d: %s", n, phase)
}

// appendPreviewLine appends line to an existing preview on its own line.
func appendPreviewLine(preview, line string) string {
	if preview == "" {
		return line
	}
	return preview + "\n" + line
}

func branchFromConfig(config json.RawMessage) string {
	if len(config) == 0 {
		return ""
	}
	var c struct {
		Branch string `json:"branch"`
	}
	_ = json.Unmarshal(config, &c)
	return c.Branch
}

func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
