package local

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/agentcatalog"
	"github.com/eclipsesource/homeshell/daemon/internal/agentspec"
	"github.com/eclipsesource/homeshell/daemon/internal/detect"
	"github.com/eclipsesource/homeshell/daemon/internal/domain"
	"github.com/eclipsesource/homeshell/daemon/internal/provider"
	"github.com/eclipsesource/homeshell/daemon/internal/sbridge"
	"github.com/eclipsesource/homeshell/daemon/internal/terminal"
)

// The default agent descriptions and model catalog, built once for the whole test
// binary.
//
// This keeps default-agent tests on the rows that ship and custom-agent tests on the
// same path a user edit takes. Read-only after setup, so one in-memory catalog
// serves every test.
var (
	seedOnce    sync.Once
	seedCatalog *agentcatalog.Store
)

func seeded(t *testing.T) (*agentcatalog.Store, *agentcatalog.Store) {
	t.Helper()
	seedOnce.Do(func() { seedCatalog = agentcatalog.NewMemory(agentcatalog.Defaults()) })
	return seedCatalog, seedCatalog
}

// newForTest builds a provider with the seeded rows wired, in the order production
// wires them: New first (it restores persisted sessions), then the descriptions and
// the catalog.
func newForTest(t *testing.T, runtime Runtime, localStore Store) *Provider {
	t.Helper()
	specs, models := seeded(t)
	p := New(runtime, localStore)
	p.SetAgentSpecs(specs)
	p.SetModelCatalog(models)
	// "Does this session have a conversation to resume?" is a question about the
	// developer's own ~/.claude in production, which no test may depend on. The
	// default here is yes — the ordinary state of a session that has been used, and
	// what the resume tests are about; a test about the other answer says so.
	p.conversation = func(string) bool { return true }
	return p
}

// describedAgent returns one default or explicitly inserted test description.
func describedAgent(t *testing.T, id string) agentspec.Agent {
	t.Helper()
	specs, _ := seeded(t)
	a, ok := specs.Agent(id)
	if !ok {
		t.Fatalf("no test description for agent %q", id)
	}
	return a
}

// seededModels is one agent's shipped model catalog (agentcatalog.Defaults).
func seededModels(t *testing.T, agent string) []string {
	t.Helper()
	_, models := seeded(t)
	return models.Models(agent)
}

// --- fakes ---

type started struct {
	target, dir string
	argv        []string
}

type fakeRuntime struct {
	// mu serializes the methods: a poll captures sessions in parallel.
	mu       sync.Mutex
	availErr error
	startErr error
	// startDies models a runtime that reports a launch as successful even though
	// the command it runs exits immediately and leaves no session behind. That is
	// the failure this provider has to survive, and a fake that only ever failed
	// loudly could not show it.
	startDies  bool
	starts     []started
	stops      []string
	state      map[string]detect.Result
	stateAgent string
	alive      map[string]bool
	// wedged targets have a bridge that accepts but never answers.
	wedged       map[string]bool
	lastInput    struct{ target, text string }
	lastKey      struct{ target, key string }
	sendInputErr error
	sendKeyErr   error
	// output is the canned stdout per command name (argv[0]); outputErr is returned
	// instead when set. outputCalls counts the runs, so a test can prove the answer
	// is cached rather than re-asked.
	output      map[string]string
	outputErr   error
	outputCalls map[string]int
	// startHook runs before Start takes mu, so it may call back into the provider.
	startHook func(target string)
	// exits are the recorded exits LastExit reports, keyed by the recorded
	// location, or by target for a session without one.
	exits map[string]sbridge.Exit
}

func newFakeRuntime() *fakeRuntime {
	return &fakeRuntime{state: map[string]detect.Result{}, alive: map[string]bool{}, wedged: map[string]bool{}}
}

func (f *fakeRuntime) Available(context.Context, string) error { return f.availErr }

func (f *fakeRuntime) Output(_ context.Context, argv []string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.outputCalls == nil {
		f.outputCalls = map[string]int{}
	}
	f.outputCalls[argv[0]]++
	if f.outputErr != nil {
		return nil, f.outputErr
	}
	return []byte(f.output[argv[0]]), nil
}

func (f *fakeRuntime) Start(_ context.Context, target, dir string, argv []string) error {
	if f.startHook != nil {
		f.startHook(target)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts = append(f.starts, started{target: target, dir: dir, argv: argv})
	if f.startErr != nil {
		return f.startErr
	}
	if f.startDies {
		return nil // accepted, and nothing is running
	}
	f.alive[target] = true
	return nil
}

func (f *fakeRuntime) Probe(_ context.Context, target string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.probeLocked(target)
}

func (f *fakeRuntime) probeLocked(target string) error {
	if f.wedged[target] {
		return context.DeadlineExceeded
	}
	if !f.alive[target] {
		return sbridge.ErrNotServing
	}
	return nil
}

func (f *fakeRuntime) LastExit(target, recorded string) (sbridge.Exit, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := recorded
	if key == "" {
		key = target
	}
	rec, ok := f.exits[key]
	return rec, ok
}

func (f *fakeRuntime) State(_ context.Context, agent, target string) (detect.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stateAgent = agent
	if err := f.probeLocked(target); err != nil {
		return detect.Result{}, err
	}
	if res, ok := f.state[target]; ok {
		return res, nil
	}
	return detect.Result{State: domain.SessionIdle}, nil
}

func (f *fakeRuntime) Stop(_ context.Context, target string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stops = append(f.stops, target)
	delete(f.alive, target)
	return nil
}

func (f *fakeRuntime) SendInput(_ context.Context, target, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastInput = struct{ target, text string }{target: target, text: text}
	return f.sendInputErr
}

func (f *fakeRuntime) SendKey(_ context.Context, target, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastKey = struct{ target, key string }{target: target, key: key}
	return f.sendKeyErr
}

func (f *fakeRuntime) AttachCommand(target string) []string {
	return []string{"fake-attach", target}
}

// locatingRuntime stands in for bridgeRuntime in the relocated-socket tests.
// The plain fakeRuntime deliberately does not implement LocationRecorder.
type locatingRuntime struct {
	*fakeRuntime
	location  string   // where this "daemon start" resolves every target
	elsewhere []string // recorded locations that still answer
	stoppedAt []string // locations StopAt was called with
	// legacyFor maps a recorded value to the location the runtime probes instead.
	legacyFor map[string]string
}

// Without this the fake could silently drop out of the interface and every test
// using it would quietly take the no-recorder path.
var _ LocationRecorder = (*locatingRuntime)(nil)

func (f *locatingRuntime) Location(string) string { return f.location }

func (f *locatingRuntime) LocateElsewhere(_ context.Context, _, recorded string) string {
	if mapped, ok := f.legacyFor[recorded]; ok {
		recorded = mapped
	}
	for _, live := range f.elsewhere {
		if recorded == live {
			return live
		}
	}
	return ""
}

func (f *locatingRuntime) StopAt(_ context.Context, location string) error {
	f.stoppedAt = append(f.stoppedAt, location)
	return nil
}

func (f *locatingRuntime) AttachCommandAt(location string) []string {
	return []string{"fake-attach", "--sock", location}
}

// countingRuntime is a mutex-guarded Runtime for the concurrency tests: racing
// the unlocked fakeRuntime would trip the detector on the fake. Start sleeps to
// widen the window two callers must both fit through.
type countingRuntime struct {
	*fakeRuntime
	mu     sync.Mutex
	starts int
}

func (f *countingRuntime) Probe(_ context.Context, target string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fakeRuntime.probeLocked(target)
}

func (f *countingRuntime) State(ctx context.Context, agent, target string) (detect.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fakeRuntime.State(ctx, agent, target)
}

func (f *countingRuntime) Start(_ context.Context, target, _ string, _ []string) error {
	f.mu.Lock()
	f.starts++
	f.mu.Unlock()
	time.Sleep(20 * time.Millisecond)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fakeRuntime.alive[target] = true
	return nil
}

func (f *countingRuntime) startCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.starts
}

type fakeStore struct {
	loaded  []PersistedWorkspace
	saved   []PersistedWorkspace
	loadErr error
	saves   int
}

func (f *fakeStore) Load() ([]PersistedWorkspace, error) { return f.loaded, f.loadErr }
func (f *fakeStore) Save(ws []PersistedWorkspace) error {
	f.saved = ws
	f.saves++
	return nil
}

type fakeTerminal struct {
	launched [][]string
	err      error
}

func (f *fakeTerminal) Launch(_ context.Context, cmd []string, _ string) error {
	if f.err != nil {
		return f.err
	}
	f.launched = append(f.launched, cmd)
	return nil
}

const testDir = "/tmp/proj"

func projectRef() provider.ProjectRef {
	return provider.ProjectRef{ID: "proj1", Path: testDir}
}

// testOption is the session option used across the tests — an agent, not an
// agent+model — and testModel the model its config asks for, which is what the CLI
// then receives.
const testOption = "claude"
const testModel = "claude-opus-5"

// testConfig is the session config carrying testModel, since the model is a field
// now rather than part of the option id.
var testConfig = json.RawMessage(`{"model":"` + testModel + `"}`)

// createSession launches one session and returns a ref to it.
func createSession(t *testing.T, p *Provider, projectID, dir string) provider.SessionRef {
	t.Helper()
	wsRef := provider.WorkstreamRef{ProjectID: projectID, WorkstreamID: "ws-" + projectID}
	sess, err := p.CreateSession(context.Background(), provider.CreateSessionRequest{
		Workstream: wsRef, WorkingDir: dir, OptionID: testOption, Config: testConfig,
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	return provider.SessionRef{ProjectID: projectID, WorkstreamID: wsRef.WorkstreamID, SessionID: sess.ID}
}

func argvValue(argv []string, flag string) string {
	for i, a := range argv {
		if a == flag && i+1 < len(argv) {
			return argv[i+1]
		}
	}
	return ""
}

func argvHas(argv []string, want string) bool {
	for _, a := range argv {
		if a == want {
			return true
		}
	}
	return false
}

// --- tests ---

func TestAvailablePropagatesRuntimeError(t *testing.T) {
	rt := newFakeRuntime()
	rt.availErr = errors.New("claude not found on PATH")
	p := newForTest(t, rt, &fakeStore{})
	if ok, err := p.Available(context.Background()); ok || err == nil {
		t.Fatalf("Available = (%v, %v), want (false, error)", ok, err)
	}
}

func TestCreateSession(t *testing.T) {
	rt := newFakeRuntime()
	st := &fakeStore{}
	p := newForTest(t, rt, st)

	wsRef := provider.WorkstreamRef{ProjectID: "proj1", WorkstreamID: "ws1"}
	sess, err := p.CreateSession(context.Background(), provider.CreateSessionRequest{Workstream: wsRef, WorkingDir: testDir, OptionID: testOption, Config: testConfig})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if len(rt.starts) != 1 {
		t.Fatalf("want 1 Start, got %d", len(rt.starts))
	}
	launch := rt.starts[0]
	if launch.dir != testDir {
		t.Errorf("launch dir = %q, want %q", launch.dir, testDir)
	}
	if launch.argv[0] != "claude" || argvValue(launch.argv, "--model") != testModel {
		t.Errorf("argv = %v", launch.argv)
	}
	if sess.Agent != "Claude Code" || sess.Title != "Session 1" {
		t.Errorf("session = %+v", sess)
	}
	if st.saves == 0 {
		t.Error("CreateSession must persist")
	}

	sess2, err := p.CreateSession(context.Background(), provider.CreateSessionRequest{Workstream: wsRef, WorkingDir: testDir, OptionID: testOption})
	if err != nil {
		t.Fatalf("second CreateSession: %v", err)
	}
	if len(rt.starts) != 2 {
		t.Fatalf("want 2 Starts, got %d", len(rt.starts))
	}
	if sess2.Title != "Session 2" {
		t.Errorf("session = %+v", sess2)
	}
}

func TestDiscoverDerivesLiveState(t *testing.T) {
	rt := newFakeRuntime()
	p := newForTest(t, rt, &fakeStore{})
	createSession(t, p, "proj1", testDir)
	target := rt.starts[0].target
	rt.state[target] = detect.Result{State: domain.SessionWaiting, MatchedRule: "synthetic_blocker", Confident: true, VisibleBlocker: true}

	ds, err := p.DiscoverSessions(context.Background(), projectRef())
	if err != nil {
		t.Fatal(err)
	}
	if len(ds) != 1 {
		t.Fatalf("unexpected discovery: %+v", ds)
	}
	if ds[0].WorkingDir != testDir {
		t.Errorf("discovered dir = %q, want %q", ds[0].WorkingDir, testDir)
	}
	sess := ds[0].Session
	if sess.State != domain.SessionWaiting {
		t.Errorf("state = %v, want waiting", sess.State)
	}
	if len(sess.ProviderData) != 0 {
		t.Fatal("live detection detail leaked into durable provider data")
	}
	var detail sessionDetail
	if err := json.Unmarshal(sess.ObservationData, &detail); err != nil {
		t.Fatalf("provider data: %v", err)
	}
	if detail.MatchedRule != "synthetic_blocker" || !detail.VisibleBlocker {
		t.Errorf("detail = %+v", detail)
	}
}

func TestDiscoverDerivesPreview(t *testing.T) {
	rt := newFakeRuntime()
	p := newForTest(t, rt, &fakeStore{})
	createSession(t, p, "proj1", testDir)
	target := rt.starts[0].target
	rt.state[target] = detect.Result{State: domain.SessionRunning, Tail: "the last visible line"}

	ds, err := p.DiscoverSessions(context.Background(), projectRef())
	if err != nil {
		t.Fatal(err)
	}
	if len(ds) != 1 {
		t.Fatalf("unexpected discovery: %+v", ds)
	}
	if got := ds[0].Session.Preview; got != "the last visible line" {
		t.Errorf("preview = %q, want %q", got, "the last visible line")
	}
}

func TestSkipStateUpdateKeepsPriorState(t *testing.T) {
	rt := newFakeRuntime()
	p := newForTest(t, rt, &fakeStore{})
	createSession(t, p, "proj1", testDir)
	target := rt.starts[0].target

	rt.state[target] = detect.Result{State: domain.SessionWaiting, MatchedRule: "synthetic_blocker"}
	ds, _ := p.DiscoverSessions(context.Background(), projectRef())
	if ds[0].Session.State != domain.SessionWaiting {
		t.Fatalf("setup: state = %v, want waiting", ds[0].Session.State)
	}

	rt.state[target] = detect.Result{State: domain.SessionIdle, MatchedRule: "synthetic_viewer", SkipStateUpdate: true}
	ds, _ = p.DiscoverSessions(context.Background(), projectRef())
	if ds[0].Session.State != domain.SessionWaiting {
		t.Errorf("skip should retain prior waiting, got %v", ds[0].Session.State)
	}

	rt.state[target] = detect.Result{State: domain.SessionRunning}
	ds, _ = p.DiscoverSessions(context.Background(), projectRef())
	if ds[0].Session.State != domain.SessionRunning {
		t.Errorf("normal reading should update, got %v", ds[0].Session.State)
	}
}

func TestRemoveSessionStops(t *testing.T) {
	rt := newFakeRuntime()
	st := &fakeStore{}
	p := newForTest(t, rt, st)
	sref := createSession(t, p, "proj1", testDir)
	target := rt.starts[0].target
	if err := p.RemoveSession(context.Background(), sref); err != nil {
		t.Fatal(err)
	}
	if len(rt.stops) != 1 || rt.stops[0] != target {
		t.Errorf("stops = %v, want [%s]", rt.stops, target)
	}
	ds, _ := p.DiscoverSessions(context.Background(), projectRef())
	if len(ds) != 0 {
		t.Errorf("session should be gone, got %d", len(ds))
	}
}

func TestOpenLaunchesTerminal(t *testing.T) {
	rt := newFakeRuntime()
	p := newForTest(t, rt, &fakeStore{})
	ft := &fakeTerminal{}
	p.term = ft
	sref := createSession(t, p, "proj1", testDir)
	target := rt.starts[0].target

	res, err := p.Open(context.Background(), sref, "terminal")
	if err != nil {
		t.Fatal(err)
	}
	if len(ft.launched) != 1 {
		t.Fatalf("terminal launched %d times, want 1", len(ft.launched))
	}
	// Held on failure: an attach that cannot dial would otherwise close its
	// window before the error can be read.
	wantCmd := terminal.HoldOnFailure("fake-attach", target)
	if !slices.Equal(ft.launched[0], wantCmd) {
		t.Errorf("terminal launched = %q, want %q", ft.launched[0], wantCmd)
	}
	var data map[string]string
	if err := json.Unmarshal(res.Data, &data); err != nil {
		t.Fatal(err)
	}
	if data["target"] != target {
		t.Errorf("open target = %q, want %q", data["target"], target)
	}
	if _, err := p.Open(context.Background(), sref, "ide"); err == nil {
		t.Error("expected error for unsupported mode")
	}
	unknown := provider.SessionRef{ProjectID: "proj1", WorkstreamID: sref.WorkstreamID, SessionID: "nope"}
	if _, err := p.Open(context.Background(), unknown, "terminal"); err == nil {
		t.Error("expected error opening an unknown session")
	}
}

func TestDetectChangedSignalsOnlyOnStateFlip(t *testing.T) {
	rt := newFakeRuntime()
	p := newForTest(t, rt, &fakeStore{})
	createSession(t, p, "proj1", testDir)
	target := rt.starts[0].target

	last := map[string]sessionMark{}
	if !p.detectChanged(context.Background(), last) {
		t.Fatal("first detection should register a change")
	}
	if p.detectChanged(context.Background(), last) {
		t.Error("steady state should not signal a change")
	}
	rt.state[target] = detect.Result{State: domain.SessionWaiting}
	if !p.detectChanged(context.Background(), last) {
		t.Error("a state flip should signal a change")
	}
	if p.detectChanged(context.Background(), last) {
		t.Error("no further change after the flip settled")
	}
}

func TestPersistAndRehydrate(t *testing.T) {
	rt := newFakeRuntime()
	st := &fakeStore{}
	a := newForTest(t, rt, st)
	createSession(t, a, "proj1", testDir)

	rt2 := newFakeRuntime()
	for _, s := range st.saved[0].Sessions {
		rt2.alive[s.Target] = true // still-alive bridge sessions survive the restart
	}
	b := newForTest(t, rt2, &fakeStore{loaded: st.saved})
	ds, err := b.DiscoverSessions(context.Background(), projectRef())
	if err != nil {
		t.Fatal(err)
	}
	if len(ds) != 1 {
		t.Fatalf("rehydrated = %+v", ds)
	}
	if len(rt2.starts) != 0 {
		t.Errorf("a still-alive session must not be relaunched; starts = %v", rt2.starts)
	}
}

func TestResumeWhenTerminalGone(t *testing.T) {
	rt := newFakeRuntime() // nothing alive
	loaded := []PersistedWorkspace{{
		ProjectID: "proj1", WorkingDir: testDir,
		Sessions: []PersistedSession{{ID: "sess-1", Title: "w", Agent: "claude", Target: "hs-abc", AgentSessionID: "abc", Model: "sonnet"}},
	}}
	p := newForTest(t, rt, &fakeStore{loaded: loaded})

	if _, err := p.DiscoverSessions(context.Background(), projectRef()); err != nil {
		t.Fatal(err)
	}
	if len(rt.starts) != 1 {
		t.Fatalf("a dead session must be resumed; starts = %v", rt.starts)
	}
	got := rt.starts[0]
	if got.target != "hs-abc" || got.dir != testDir {
		t.Errorf("resume target/dir = %q/%q", got.target, got.dir)
	}
	if got.argv[0] != "claude" || !argvHas(got.argv, "--resume") || argvValue(got.argv, "--resume") != "abc" {
		t.Errorf("resume argv = %v, want claude --resume abc", got.argv)
	}
	if argvValue(got.argv, "--model") != "sonnet" {
		t.Errorf("resume should re-pass the model, argv = %v", got.argv)
	}
}

// The socket the daemon looks at now is silent, but the session is still served
// where it started; resuming would leave two live agents in one directory.
func TestNoResumeWhenAliveAtRecordedLocation(t *testing.T) {
	rt := &locatingRuntime{
		fakeRuntime: newFakeRuntime(), // nothing alive at the current location
		location:    "/run/user/1000/homeshell-new/hs-abc.sock",
		elsewhere:   []string{"/run/user/1000/homeshell-old/hs-abc.sock"},
	}
	loaded := []PersistedWorkspace{{
		ProjectID: "proj1", WorkingDir: testDir,
		Sessions: []PersistedSession{{
			ID: "sess-1", Title: "w", Agent: "claude", Target: "hs-abc",
			AgentSessionID: "abc", Model: "sonnet",
			Sock: "/run/user/1000/homeshell-old/hs-abc.sock",
		}},
	}}
	p := New(rt, &fakeStore{loaded: loaded})

	ds, err := p.DiscoverSessions(context.Background(), projectRef())
	if err != nil {
		t.Fatal(err)
	}
	if len(rt.starts) != 0 {
		t.Fatalf("a session alive at its recorded socket must not be resumed; starts = %v", rt.starts)
	}
	if len(ds) != 1 {
		t.Fatalf("expected the session to still be reported, got %d", len(ds))
	}
	if ds[0].Session.State != domain.SessionUnknown {
		t.Errorf("state = %q, want unknown (reachable nowhere this daemon looks)", ds[0].Session.State)
	}
}

// The recorded socket is silent too, so the negative probe is trustworthy and
// the guard must not suppress the resume.
func TestResumeWhenRecordedLocationIsAlsoDead(t *testing.T) {
	rt := &locatingRuntime{
		fakeRuntime: newFakeRuntime(),
		location:    "/run/user/1000/homeshell-new/hs-abc.sock",
		elsewhere:   nil, // nothing answers anywhere
	}
	loaded := []PersistedWorkspace{{
		ProjectID: "proj1", WorkingDir: testDir,
		Sessions: []PersistedSession{{
			ID: "sess-1", Title: "w", Agent: "claude", Target: "hs-abc",
			AgentSessionID: "abc", Model: "sonnet",
			Sock: "/run/user/1000/homeshell-old/hs-abc.sock",
		}},
	}}
	p := newForTest(t, rt, &fakeStore{loaded: loaded})

	if _, err := p.DiscoverSessions(context.Background(), projectRef()); err != nil {
		t.Fatal(err)
	}
	if len(rt.starts) != 1 {
		t.Fatalf("a genuinely dead session must still resume; starts = %v", rt.starts)
	}
}

func TestLaunchRecordsAndPersistsLocation(t *testing.T) {
	rt := &locatingRuntime{fakeRuntime: newFakeRuntime(), location: "/run/user/1000/homeshell-abc/hs-x.sock"}
	st := &fakeStore{}
	p := newForTest(t, rt, st)

	createSession(t, p, "proj1", testDir)
	if len(st.saved) != 1 || len(st.saved[0].Sessions) != 1 {
		t.Fatalf("expected one persisted session, got %+v", st.saved)
	}
	if got := st.saved[0].Sessions[0].Sock; got != rt.location {
		t.Errorf("persisted sock = %q, want the launch location %q", got, rt.location)
	}
}

// captureSession runs without p.mu and is reached from several goroutines. A
// second `bridge serve` on one socket does not fail: it rebinds and orphans
// the first agent.
func TestConcurrentCaptureResumesOnce(t *testing.T) {
	rt := &countingRuntime{fakeRuntime: newFakeRuntime()}
	p := newForTest(t, rt, &fakeStore{})
	ref := createSession(t, p, "proj1", testDir)
	p.mu.Lock()
	ws, s := p.findSessionLocked(ref.ProjectID, ref.SessionID)
	snap := snapshotSession(s, ws)
	p.mu.Unlock()
	rt.mu.Lock()
	rt.alive[snap.target] = false
	rt.starts = 0
	rt.mu.Unlock()

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.captureSession(context.Background(), snap, p.indexAgents())
		}()
	}
	wg.Wait()

	if got := rt.startCount(); got != 1 {
		t.Errorf("Start called %d times for one dead target; want exactly 1 (each extra call orphans a live agent)", got)
	}
}

// relocatedProvider holds one session alive at the socket it was launched on,
// while this "daemon start" computes a different one.
func relocatedProvider(t *testing.T) (*Provider, *locatingRuntime, provider.SessionRef) {
	t.Helper()
	const recorded = "/run/user/1000/homeshell-old/hs-abc.sock"
	rt := &locatingRuntime{
		fakeRuntime: newFakeRuntime(), // nothing alive at the current location
		location:    "/run/user/1000/homeshell-new/hs-abc.sock",
		elsewhere:   []string{recorded},
	}
	loaded := []PersistedWorkspace{{
		ProjectID: "proj1", WorkingDir: testDir,
		Sessions: []PersistedSession{{
			ID: "sess-1", Title: "w", Agent: "claude", Target: "hs-abc",
			AgentSessionID: "abc", Model: testModel, Sock: recorded,
		}},
	}}
	p := New(rt, &fakeStore{loaded: loaded})
	return p, rt, provider.SessionRef{ProjectID: "proj1", WorkstreamID: "ws1", SessionID: "sess-1"}
}

// A bridge stop succeeds on a dead socket, so stopping the wrong one would
// report success while the session ran on.
func TestStopAddressesTheRecordedLocation(t *testing.T) {
	p, rt, ref := relocatedProvider(t)

	if err := p.StopSession(context.Background(), ref); err != nil {
		t.Fatalf("StopSession: %v", err)
	}
	if len(rt.stoppedAt) != 1 || rt.stoppedAt[0] != "/run/user/1000/homeshell-old/hs-abc.sock" {
		t.Errorf("stopped at %v, want the recorded socket", rt.stoppedAt)
	}
	if len(rt.stops) != 0 {
		t.Errorf("also stopped by target (%v), which addresses the wrong socket", rt.stops)
	}
}

// Remove drops the record, so a bridge stopped at the wrong socket leaks with
// nothing left pointing at it.
func TestRemoveAddressesTheRecordedLocation(t *testing.T) {
	p, rt, ref := relocatedProvider(t)

	if err := p.RemoveSession(context.Background(), ref); err != nil {
		t.Fatalf("RemoveSession: %v", err)
	}
	if len(rt.stoppedAt) != 1 || rt.stoppedAt[0] != "/run/user/1000/homeshell-old/hs-abc.sock" {
		t.Errorf("stopped at %v, want the recorded socket", rt.stoppedAt)
	}
}

// A session persisted before sockets were recorded has an empty Sock, and the
// runtime finds it at its legacy path.
func TestStopAddressesALegacyLocationWithNoRecordedSocket(t *testing.T) {
	const legacy = "/run/user/1000/homeshell/hs-abc.sock"
	rt := &locatingRuntime{
		fakeRuntime: newFakeRuntime(),
		location:    "/run/user/1000/homeshell-ns/hs-abc.sock",
		elsewhere:   []string{legacy},
	}
	// bridgeRuntime.LocateElsewhere resolves an empty recorded value this way.
	rt.legacyFor = map[string]string{"": legacy}
	loaded := []PersistedWorkspace{{
		ProjectID: "proj1", WorkingDir: testDir,
		Sessions: []PersistedSession{{
			ID: "sess-1", Title: "w", Agent: "claude", Target: "hs-abc",
			AgentSessionID: "abc", Model: testModel, // no Sock recorded
		}},
	}}
	p := New(rt, &fakeStore{loaded: loaded})

	ref := provider.SessionRef{ProjectID: "proj1", WorkstreamID: "ws1", SessionID: "sess-1"}
	if err := p.StopSession(context.Background(), ref); err != nil {
		t.Fatalf("StopSession: %v", err)
	}
	if len(rt.stoppedAt) != 1 || rt.stoppedAt[0] != legacy {
		t.Errorf("stopped at %v, want the legacy socket %q", rt.stoppedAt, legacy)
	}
	if len(rt.stops) != 0 {
		t.Errorf("also stopped by target (%v), which addresses the wrong socket", rt.stops)
	}
}

// Attaching to the computed socket opens a terminal that cannot dial and
// closes at once, while Open still reports success.
func TestOpenAttachesAtTheRecordedLocation(t *testing.T) {
	p, _, ref := relocatedProvider(t)
	term := &fakeTerminal{}
	p.term = term

	if _, err := p.Open(context.Background(), ref, "terminal"); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if len(term.launched) != 1 {
		t.Fatalf("terminal launches = %d, want 1", len(term.launched))
	}
	if got := strings.Join(term.launched[0], " "); !strings.Contains(got, "homeshell-old") {
		t.Errorf("attached with %q, want the recorded socket", got)
	}
}

// Open resumes through ensureAliveLocked while holding p.mu, so serializing
// only the capture path would still let it race the watcher.
func TestConcurrentOpenAndCaptureResumeOnce(t *testing.T) {
	rt := &countingRuntime{fakeRuntime: newFakeRuntime()} // nothing alive
	loaded := []PersistedWorkspace{{
		ProjectID: "proj1", WorkingDir: testDir,
		Sessions: []PersistedSession{{
			ID: "sess-1", Title: "w", Agent: "claude", Target: "hs-abc",
			AgentSessionID: "abc", Model: testModel,
		}},
	}}
	// newForTest, not New: resuming needs the agent descriptions wired, which are
	// data now rather than Go constants — without them "claude" resolves to no
	// agent and the resume is skipped entirely.
	p := newForTest(t, rt, &fakeStore{loaded: loaded})
	p.term = &fakeTerminal{}
	ref := provider.SessionRef{ProjectID: "proj1", WorkstreamID: "ws1", SessionID: "sess-1"}
	snap := sessionSnapshot{
		id: "sess-1", title: "w", agent: "claude", target: "hs-abc", dir: testDir,
		agentSessionID: "abc", model: testModel, hold: detect.Hold{State: domain.SessionIdle},
	}

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.captureSession(context.Background(), snap, p.indexAgents())
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = p.Open(context.Background(), ref, "terminal")
		}()
	}
	wg.Wait()

	if got := rt.startCount(); got != 1 {
		t.Errorf("Start called %d times across Open and capture; want exactly 1", got)
	}
}

func TestResumeFailureStopsRetryingAndMarksGone(t *testing.T) {
	rt := newFakeRuntime()
	rt.startErr = errors.New("no such session to resume")
	loaded := []PersistedWorkspace{{
		ProjectID: "proj1", WorkingDir: testDir,
		Sessions: []PersistedSession{{ID: "sess-1", Title: "w", Agent: "claude", Target: "hs-abc", AgentSessionID: "abc", Model: "sonnet"}},
	}}
	p := newForTest(t, rt, &fakeStore{loaded: loaded})

	if _, err := p.DiscoverSessions(context.Background(), projectRef()); err != nil {
		t.Fatal(err)
	}
	ds, err := p.DiscoverSessions(context.Background(), projectRef())
	if err != nil {
		t.Fatal(err)
	}
	if len(rt.starts) != 1 {
		t.Fatalf("a failed resume must be attempted only once, got %d attempts", len(rt.starts))
	}
	sess := ds[0].Session
	// Stopped, not idle: the session is not running, and an idle row offers an open
	// and a prompt that would both reach a terminal that is not there. The gone
	// marker is what still says it was not the user who stopped it.
	if sess.State != domain.SessionStopped {
		t.Errorf("gone session state = %v, want stopped", sess.State)
	}
	var detail sessionDetail
	if err := json.Unmarshal(sess.ObservationData, &detail); err != nil {
		t.Fatal(err)
	}
	if !detail.Gone {
		t.Error("expected gone marker on an unresumable session")
	}
}

// goneSessionProvider is a provider holding one restored claude session whose
// terminal is not there, with a runtime that accepts a launch and leaves nothing
// running: an agent that exits right after its bridge came up.
func goneSessionProvider(t *testing.T) (*Provider, *fakeRuntime) {
	t.Helper()
	rt := newFakeRuntime()
	rt.startDies = true
	loaded := []PersistedWorkspace{{
		ProjectID: "proj1", WorkingDir: testDir,
		Sessions: []PersistedSession{{ID: "sess-1", Title: "w", Agent: "claude", Target: "hs-abc", AgentSessionID: "abc", Model: "sonnet"}},
	}}
	return newForTest(t, rt, &fakeStore{loaded: loaded}), rt
}

// A relaunch the runtime accepts and that dies anyway is the case that used to
// run forever: the launch reports success whatever the command does, so every
// tick relaunched a session that could never come up, the row went on reading
// like a live one, and the user got a terminal window that closed itself.
func TestRelaunchThatDidNotSurviveIsReportedGone(t *testing.T) {
	p, rt := goneSessionProvider(t)

	if _, err := p.DiscoverSessions(context.Background(), projectRef()); err != nil {
		t.Fatal(err)
	}
	if len(rt.starts) != 1 {
		t.Fatalf("first pass should relaunch once, starts = %v", rt.starts)
	}
	ds, err := p.DiscoverSessions(context.Background(), projectRef())
	if err != nil {
		t.Fatal(err)
	}
	if len(rt.starts) != 1 {
		t.Errorf("a relaunch that did not survive must not be retried, got %d attempts", len(rt.starts))
	}
	if ds[0].Session.State != domain.SessionStopped {
		t.Errorf("state = %v, want stopped", ds[0].Session.State)
	}
	var detail sessionDetail
	if err := json.Unmarshal(ds[0].Session.ObservationData, &detail); err != nil {
		t.Fatal(err)
	}
	if !detail.Gone {
		t.Error("expected the gone marker")
	}
	// And it stays that way rather than starting the cycle over.
	if _, err := p.DiscoverSessions(context.Background(), projectRef()); err != nil {
		t.Fatal(err)
	}
	if len(rt.starts) != 1 {
		t.Errorf("gone session relaunched again, starts = %v", rt.starts)
	}
}

// A session reported gone shows the screen its agent left, the only account of
// why it ended.
func TestGoneSessionPreviewsTheAgentsLastScreen(t *testing.T) {
	p, rt := goneSessionProvider(t)
	rt.exits = map[string]sbridge.Exit{"hs-abc": {Code: 1, Screen: "\n● Resuming abc\nNo conversation found with session ID: abc\n\n\n"}}

	for range 2 { // relaunch, then find the relaunch gone
		if _, err := p.DiscoverSessions(context.Background(), projectRef()); err != nil {
			t.Fatal(err)
		}
	}
	ds, err := p.DiscoverSessions(context.Background(), projectRef())
	if err != nil {
		t.Fatal(err)
	}
	want := "● Resuming abc\nNo conversation found with session ID: abc"
	if got := ds[0].Session.Preview; got != want {
		t.Errorf("preview %q, want %q", got, want)
	}
}

// The exit record is read where the session was started, not where this
// daemon start would put it: the run dir can change between daemon starts.
func TestGoneSessionPreviewsTheExitAtItsRecordedLocation(t *testing.T) {
	recorded := "/run/user/1000/homeshell-old/hs-abc.sock"
	rt := &locatingRuntime{fakeRuntime: newFakeRuntime(), location: "/run/user/1000/homeshell-new/hs-abc.sock"}
	rt.startErr = errors.New("agent exited")
	rt.exits = map[string]sbridge.Exit{
		recorded:    {Code: 1, Screen: "panic: old run dir\n"},
		rt.location: {Code: 1, Screen: "wrong record\n"},
	}
	loaded := []PersistedWorkspace{{
		ProjectID: "proj1", WorkingDir: testDir,
		Sessions: []PersistedSession{{
			ID: "sess-1", Title: "w", Agent: "claude", Target: "hs-abc",
			AgentSessionID: "abc", Model: "sonnet", Sock: recorded,
		}},
	}}
	p := newForTest(t, rt, &fakeStore{loaded: loaded})

	ds, err := p.DiscoverSessions(context.Background(), projectRef())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := ds[0].Session.Preview, "panic: old run dir"; got != want {
		t.Errorf("preview %q, want %q", got, want)
	}
}

func endedByBridge(v bool) *bool { return &v }

// quitSessionProvider holds one restored claude session whose bridge is gone,
// with rec as the exit record its bridge left.
func quitSessionProvider(t *testing.T, rec *sbridge.Exit) (*Provider, *fakeRuntime, *fakeStore) {
	t.Helper()
	rt := newFakeRuntime()
	if rec != nil {
		rt.exits = map[string]sbridge.Exit{"hs-abc": *rec}
	}
	store := &fakeStore{loaded: []PersistedWorkspace{{
		ProjectID: "proj1", WorkingDir: testDir,
		Sessions: []PersistedSession{{ID: "sess-1", Title: "w", Agent: "claude", Target: "hs-abc", AgentSessionID: "abc", Model: "sonnet"}},
	}}}
	return newForTest(t, rt, store), rt, store
}

// The user's /quit ends the agent on its own; bringing it back made a local
// session impossible to end from inside the agent (#115). This also covers a
// quit while the daemon was down, as the session here is a restored one.
func TestAgentThatQuitIsStoppedNotRelaunched(t *testing.T) {
	p, rt, store := quitSessionProvider(t, &sbridge.Exit{Code: 0, Screen: "bye\n", EndedByBridge: endedByBridge(false)})

	for range 2 { // and it stays that way, its last screen included
		ds, err := p.DiscoverSessions(context.Background(), projectRef())
		if err != nil {
			t.Fatal(err)
		}
		if got := ds[0].Session; got.State != domain.SessionStopped || got.Preview != "bye" {
			t.Errorf("session = %v %q, want stopped showing the agent's last screen", got.State, got.Preview)
		}
		var detail sessionDetail
		_ = json.Unmarshal(ds[0].Session.ObservationData, &detail)
		if detail.Gone {
			t.Error("a session the user quit is stopped, not gone")
		}
	}
	if len(rt.starts) != 0 {
		t.Errorf("an agent that quit was relaunched: %v", rt.starts)
	}
	if len(store.saved) != 1 || !store.saved[0].Sessions[0].Stopped {
		t.Errorf("saved %+v, want the session persisted as stopped", store.saved)
	}
}

// Only an exit the agent chose is a quit: a bridge that was killed leaves no
// record, a stop or forwarded signal is the bridge's doing, and a crash or
// kill -9 of the agent alone is not the user's.
func TestAgentNotKnownToHaveQuitIsRelaunched(t *testing.T) {
	for _, tc := range []struct {
		name string
		rec  *sbridge.Exit
	}{
		{"no record", nil},
		{"ended by the bridge", &sbridge.Exit{Code: 143, EndedByBridge: endedByBridge(true)}},
		{"record from an older bridge", &sbridge.Exit{Code: 0}},
		{"agent killed by a signal", &sbridge.Exit{Code: 137, EndedByBridge: endedByBridge(false)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, rt, _ := quitSessionProvider(t, tc.rec)
			if _, err := p.DiscoverSessions(context.Background(), projectRef()); err != nil {
				t.Fatal(err)
			}
			if len(rt.starts) != 1 {
				t.Errorf("starts = %v, want one relaunch", rt.starts)
			}
		})
	}
}

func TestStartResumesASessionThatQuit(t *testing.T) {
	p, rt, _ := quitSessionProvider(t, &sbridge.Exit{EndedByBridge: endedByBridge(false)})
	ref := provider.SessionRef{ProjectID: "proj1", WorkstreamID: "ws1", SessionID: "sess-1"}
	if _, err := p.DiscoverSessions(context.Background(), projectRef()); err != nil {
		t.Fatal(err)
	}
	if err := p.StartSession(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	if len(rt.starts) != 1 || !slices.Contains(rt.starts[0].argv, "--resume") {
		t.Fatalf("starts = %v, want one resume", rt.starts)
	}
	ds, err := p.DiscoverSessions(context.Background(), projectRef())
	if err != nil {
		t.Fatal(err)
	}
	if ds[0].Session.State == domain.SessionStopped {
		t.Error("session still stopped after Start")
	}
}

// Opening a session whose agent quit, before any capture noticed, must not
// bring it back either.
func TestOpenOnASessionThatQuitIsRefused(t *testing.T) {
	p, rt, store := quitSessionProvider(t, &sbridge.Exit{EndedByBridge: endedByBridge(false)})
	term := &fakeTerminal{}
	p.term = term
	ref := provider.SessionRef{ProjectID: "proj1", WorkstreamID: "ws1", SessionID: "sess-1"}
	if _, err := p.Open(context.Background(), ref, "terminal"); !errors.Is(err, provider.ErrSessionStopped) {
		t.Errorf("Open = %v, want ErrSessionStopped", err)
	}
	if len(rt.starts) != 0 || len(term.launched) != 0 {
		t.Errorf("starts = %v, terminals = %v; want neither", rt.starts, term.launched)
	}
	if len(store.saved) != 1 || !store.saved[0].Sessions[0].Stopped {
		t.Errorf("saved %+v, want the session persisted as stopped", store.saved)
	}
}

// The one-attempt guard is per loss of the terminal, not per session: a session
// that comes back and later dies again gets its own attempt, or a long-lived
// session would be revivable only once.
func TestEveryLossOfTheTerminalGetsAnAttempt(t *testing.T) {
	rt := newFakeRuntime()
	loaded := []PersistedWorkspace{{
		ProjectID: "proj1", WorkingDir: testDir,
		Sessions: []PersistedSession{{ID: "sess-1", Title: "w", Agent: "claude", Target: "hs-abc", AgentSessionID: "abc", Model: "sonnet"}},
	}}
	p := newForTest(t, rt, &fakeStore{loaded: loaded})

	// Lost, relaunched (this runtime's launches do come up), then seen alive.
	if _, err := p.DiscoverSessions(context.Background(), projectRef()); err != nil {
		t.Fatal(err)
	}
	if _, err := p.DiscoverSessions(context.Background(), projectRef()); err != nil {
		t.Fatal(err)
	}
	// Lost again.
	delete(rt.alive, "hs-abc")
	ds, err := p.DiscoverSessions(context.Background(), projectRef())
	if err != nil {
		t.Fatal(err)
	}
	if len(rt.starts) != 2 {
		t.Fatalf("a second loss must get its own relaunch, starts = %v", rt.starts)
	}
	if ds[0].Session.State == domain.SessionStopped {
		t.Error("a session that was successfully relaunched must not read as gone")
	}
}

// A session with no conversation on disk cannot be resumed — the CLI exits on an
// id it has never heard of — so it is launched fresh under the same session id
// instead. Nothing is lost: a session that has not had a turn has no conversation.
func TestNoConversationLaunchesFreshInsteadOfResuming(t *testing.T) {
	rt := newFakeRuntime()
	loaded := []PersistedWorkspace{{
		ProjectID: "proj1", WorkingDir: testDir,
		Sessions: []PersistedSession{{ID: "sess-1", Title: "w", Agent: "claude", Target: "hs-abc", AgentSessionID: "abc", Model: "sonnet"}},
	}}
	p := newForTest(t, rt, &fakeStore{loaded: loaded})
	p.conversation = func(string) bool { return false }

	ds, err := p.DiscoverSessions(context.Background(), projectRef())
	if err != nil {
		t.Fatal(err)
	}
	if len(rt.starts) != 1 {
		t.Fatalf("starts = %v", rt.starts)
	}
	got := rt.starts[0].argv
	if argvHas(got, "--resume") {
		t.Errorf("nothing to resume, yet argv resumes: %v", got)
	}
	if argvValue(got, "--session-id") != "abc" {
		t.Errorf("a fresh launch must keep the session id, argv = %v", got)
	}
	if ds[0].Session.State == domain.SessionStopped {
		t.Error("a session that came back must not read as gone")
	}
}

// Prompting a session whose terminal is gone is a state conflict the client can
// act on (start it), not a failed `bridge send` arriving as a 500 with nothing a
// user can do about it.
func TestPromptAndKeyOnAGoneSessionAreRefused(t *testing.T) {
	p, _ := goneSessionProvider(t)
	ref := provider.SessionRef{ProjectID: "proj1", WorkstreamID: "ws1", SessionID: "sess-1"}

	for range 2 { // relaunch, then give up on it
		if _, err := p.DiscoverSessions(context.Background(), projectRef()); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.Prompt(context.Background(), ref, "hi"); !errors.Is(err, provider.ErrSessionGone) {
		t.Errorf("Prompt error = %v, want ErrSessionGone", err)
	}
	if err := p.SendKey(context.Background(), ref, "enter"); !errors.Is(err, provider.ErrSessionGone) {
		t.Errorf("SendKey error = %v, want ErrSessionGone", err)
	}
	if _, err := p.Open(context.Background(), ref, "terminal"); !errors.Is(err, provider.ErrSessionGone) {
		t.Errorf("Open error = %v, want ErrSessionGone", err)
	}
}

// Start is the way back for a gone session. It used to answer "already active"
// (the session was never marked stopped), which left the row with no route to a
// live agent at all — the user could only remove it.
func TestStartRevivesAGoneSession(t *testing.T) {
	p, rt := goneSessionProvider(t)
	ref := provider.SessionRef{ProjectID: "proj1", WorkstreamID: "ws1", SessionID: "sess-1"}

	for range 2 {
		if _, err := p.DiscoverSessions(context.Background(), projectRef()); err != nil {
			t.Fatal(err)
		}
	}
	rt.startDies = false // the user has fixed whatever it was
	if err := p.StartSession(context.Background(), ref); err != nil {
		t.Fatalf("starting a gone session: %v", err)
	}
	ds, err := p.DiscoverSessions(context.Background(), projectRef())
	if err != nil {
		t.Fatal(err)
	}
	if ds[0].Session.State == domain.SessionStopped {
		t.Errorf("state after start = %v, want a live state", ds[0].Session.State)
	}
	if err := p.Prompt(context.Background(), ref, "hi"); err != nil {
		t.Errorf("prompting the revived session: %v", err)
	}
}

// A Start is a launch not yet seen alive, the same as a relaunch: when the agent
// still dies on it, the session goes back to gone on the next tick instead of
// being relaunched once more, so one Start costs one launch.
func TestStartThatDoesNotSurviveIsNotRelaunched(t *testing.T) {
	p, rt := goneSessionProvider(t)
	ref := provider.SessionRef{ProjectID: "proj1", WorkstreamID: "ws1", SessionID: "sess-1"}

	for range 2 { // relaunch, then give up on it
		if _, err := p.DiscoverSessions(context.Background(), projectRef()); err != nil {
			t.Fatal(err)
		}
	}
	before := len(rt.starts)
	if err := p.StartSession(context.Background(), ref); err != nil {
		t.Fatalf("starting a gone session: %v", err)
	}
	var ds []provider.DiscoveredSession
	for range 2 {
		var err error
		if ds, err = p.DiscoverSessions(context.Background(), projectRef()); err != nil {
			t.Fatal(err)
		}
	}
	if got := len(rt.starts) - before; got != 1 {
		t.Errorf("a Start whose agent dies made %d launches, want 1; starts = %v", got, rt.starts)
	}
	if ds[0].Session.State != domain.SessionStopped {
		t.Errorf("state = %v, want stopped", ds[0].Session.State)
	}
	if err := p.Prompt(context.Background(), ref, "hi"); !errors.Is(err, provider.ErrSessionGone) {
		t.Errorf("Prompt error = %v, want ErrSessionGone", err)
	}
}

func TestPruneDropsOrphanedProjects(t *testing.T) {
	rt := newFakeRuntime()
	p := newForTest(t, rt, &fakeStore{})

	createSession(t, p, "proj1", "/tmp/proj1")
	createSession(t, p, "proj2", "/tmp/proj2")

	if err := p.Prune(context.Background(), []string{"proj1"}); err != nil {
		t.Fatal(err)
	}

	l1, _ := p.DiscoverSessions(context.Background(), provider.ProjectRef{ID: "proj1", Path: "/tmp/proj1"})
	l2, _ := p.DiscoverSessions(context.Background(), provider.ProjectRef{ID: "proj2", Path: "/tmp/proj2"})
	if len(l1) != 1 {
		t.Errorf("kept project should retain its session, got %d", len(l1))
	}
	if len(l2) != 0 {
		t.Errorf("orphaned project's session should be pruned, got %d", len(l2))
	}
	if len(rt.stops) != 1 {
		t.Errorf("pruned session should be stopped, stops = %v", rt.stops)
	}
}

func TestSelfRegisters(t *testing.T) {
	reg := provider.NewRegistry()
	if err := provider.RegisterSessionFactories(reg); err != nil {
		t.Fatalf("RegisterSessionFactories: %v", err)
	}
	prov, ok := reg.SessionProvider("local")
	if !ok {
		t.Fatal("local provider did not self-register via init()")
	}
	if prov.Label() != "Local System" {
		t.Errorf("label = %q", prov.Label())
	}
}

// The selector lists **agents**, one entry each, and the models live inside each
// agent's schema. That split is the point: picking what to run and picking which
// model are two decisions, so each described agent is one entry rather than one per
// agent-and-model combination.
func TestSessionSchemasOfferOneOptionPerAgent(t *testing.T) {
	p := newForTest(t, newFakeRuntime(), &fakeStore{})

	opts, err := p.SessionSchemas(context.Background(), provider.WorkstreamRef{})
	if err != nil {
		t.Fatalf("SessionSchemas: %v", err)
	}
	offered := map[string]string{} // option id → label
	for _, o := range opts {
		offered[o.ID] = o.Label
	}

	want := map[string]string{
		"amp":          "Amp",
		"agy":          "Antigravity",
		"claude":       "Claude Code",
		"cline":        "Cline",
		"codex":        "Codex",
		"copilot":      "GitHub Copilot",
		"cursor":       "Cursor",
		"devin":        "Devin",
		"droid":        "Droid",
		"gemini":       "Gemini",
		"grok":         "Grok",
		"kiro":         "Kiro",
		"letta":        "Letta",
		"maki":         "Maki",
		"muse":         "Muse",
		"opencode":     "OpenCode",
		"pi":           "Pi",
		"qodercli":     "Qoder CLI",
		"qwen":         "Qwen Code",
		"mistral-vibe": "Mistral Vibe",
	}
	if len(offered) != len(want) {
		t.Errorf("offered %d options, want one per agent (%d): %v", len(offered), len(want), offered)
	}
	for id, label := range want {
		if offered[id] != label {
			t.Errorf("option %q label = %q, want %q", id, offered[id], label)
		}
		if _, ok := p.agentFor(id); !ok {
			t.Errorf("agentFor(%q) did not resolve, so a create with it would fail", id)
		}
	}
	// The old shape must be gone: an agent+model key names nothing now.
	if _, ok := p.agentFor("anthropic/claude-opus-5"); ok {
		t.Error(`agentFor("anthropic/claude-opus-5") resolved; the model is not part of the option id`)
	}
}

// Each agent's Model field offers its curated catalog, and a create is held to it:
// the whole catalog is choosable and nothing outside it is.
func TestEachAgentOffersItsCatalogAsTheModelChoices(t *testing.T) {
	p := newForTest(t, newFakeRuntime(), &fakeStore{})

	opts, err := p.SessionSchemas(context.Background(), provider.WorkstreamRef{})
	if err != nil {
		t.Fatalf("SessionSchemas: %v", err)
	}
	schemas := map[string]json.RawMessage{}
	for _, o := range opts {
		schemas[o.ID] = o.Schema
	}

	for _, id := range []string{"claude", "codex", "gemini"} {
		t.Run(id, func(t *testing.T) {
			want := seededModels(t, id)
			if len(want) == 0 {
				t.Fatalf("agent %q has no seeded catalog to offer", id)
			}
			if got := modelEnumOf(t, schemas[id]); !slices.Equal(got, want) {
				t.Errorf("model choices = %v, want the catalog %v", got, want)
			}
			// Validation uses the schema with no model enum, so the agent stays the
			// authority on a model it is handed.
			schema := agentspec.Schema(describedAgent(t, id), nil, agentspec.Host)
			for _, model := range want {
				cfg := json.RawMessage(`{"model":"` + model + `"}`)
				if err := provider.ValidateConfig(schema, cfg); err != nil {
					t.Errorf("catalog model %q was refused: %v", model, err)
				}
			}
			// Unset stays a real answer: the CLI then picks its own.
			if err := provider.ValidateConfig(schema, json.RawMessage(`{}`)); err != nil {
				t.Errorf("a session with no model chosen was refused: %v", err)
			}
		})
	}
}

// Every catalog model has to reach the CLI as --model, whichever agent it belongs
// to: this is the step between "the form offered it" and "the session runs it".
func TestCreateSessionLaunchesTheChosenModel(t *testing.T) {
	for _, tt := range []struct {
		option, binary string
	}{
		{"claude", "claude"},
		{"codex", "codex"},
		{"gemini", "gemini"},
	} {
		for _, model := range seededModels(t, tt.option) {
			t.Run(tt.option+"/"+model, func(t *testing.T) {
				rt := newFakeRuntime()
				p := newForTest(t, rt, &fakeStore{})

				if _, err := p.CreateSession(context.Background(), provider.CreateSessionRequest{
					Workstream: provider.WorkstreamRef{ProjectID: "proj1", WorkstreamID: "ws1"},
					WorkingDir: testDir,
					OptionID:   tt.option,
					Config:     json.RawMessage(`{"model":"` + model + `"}`),
				}); err != nil {
					t.Fatalf("CreateSession: %v", err)
				}
				if len(rt.starts) != 1 {
					t.Fatalf("want 1 Start, got %d", len(rt.starts))
				}
				argv := rt.starts[0].argv
				if argv[0] != tt.binary {
					t.Errorf("argv[0] = %q, want %q", argv[0], tt.binary)
				}
				if got := argvValue(argv, "--model"); got != model {
					t.Errorf("--model = %q, want %q (argv = %v)", got, model, argv)
				}
			})
		}
	}
}

// Choosing nothing must launch the agent as the agent would launch itself — no
// --model at all, rather than a model HomeShell picked on the user's behalf.
func TestCreateSessionWithoutAModelPassesNoModelFlag(t *testing.T) {
	rt := newFakeRuntime()
	p := newForTest(t, rt, &fakeStore{})

	if _, err := p.CreateSession(context.Background(), provider.CreateSessionRequest{
		Workstream: provider.WorkstreamRef{ProjectID: "proj1", WorkstreamID: "ws1"},
		WorkingDir: testDir,
		OptionID:   "claude",
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if len(rt.starts) != 1 {
		t.Fatalf("want 1 Start, got %d", len(rt.starts))
	}
	if argvHas(rt.starts[0].argv, "--model") {
		t.Errorf("argv = %v, want no --model when none was chosen", rt.starts[0].argv)
	}
}

// Each agent's model reaches it the way that agent takes it: a flag for most, the
// environment for vibe, whose CLI has no model flag at all.
func TestCreateSessionPassesATypedModelPerAgent(t *testing.T) {
	tests := []struct {
		option   string
		config   string
		wantArgv []string
	}{
		{"opencode", `{"model":"anthropic/claude-sonnet-5"}`, []string{"opencode", "--model", "anthropic/claude-sonnet-5"}},
		{"opencode", `{}`, []string{"opencode"}},
		// pi takes a display name like Claude does, so the daemon's default session
		// title travels with the launch.
		{"pi", `{"model":"openai/gpt-5.6","thinking":"high"}`, []string{"pi", "--model", "openai/gpt-5.6", "--thinking", "high", "--name", "Session 1"}},
		{"pi", `{"prompt":"run the tests"}`, []string{"pi", "--name", "Session 1", "run the tests"}},
		{"mistral-vibe", `{"model":"devstral-small-latest"}`, []string{"env", "VIBE_ACTIVE_MODEL=devstral-small-latest", "vibe"}},
		{"mistral-vibe", `{"autoApprove":true}`, []string{"vibe", "--auto-approve"}},
		{"mistral-vibe", `{}`, []string{"vibe"}},
	}
	for _, tt := range tests {
		t.Run(tt.option+" "+tt.config, func(t *testing.T) {
			rt := newFakeRuntime()
			p := newForTest(t, rt, &fakeStore{})

			if _, err := p.CreateSession(context.Background(), provider.CreateSessionRequest{
				Workstream: provider.WorkstreamRef{ProjectID: "proj1", WorkstreamID: "ws1"},
				WorkingDir: testDir,
				OptionID:   tt.option,
				Config:     json.RawMessage(tt.config),
			}); err != nil {
				t.Fatalf("CreateSession: %v", err)
			}
			if len(rt.starts) != 1 {
				t.Fatalf("want 1 Start, got %d", len(rt.starts))
			}
			if got := rt.starts[0].argv; !slices.Equal(got, tt.wantArgv) {
				t.Errorf("argv = %v, want %v", got, tt.wantArgv)
			}
		})
	}
}

// A typed model is free text, but the rest of the config is not: a knob an agent
// does not have must still be refused, or a create would silently drop it.
func TestAgentSchemasRefuseForeignKnobs(t *testing.T) {
	tests := []struct {
		option  string
		config  string
		wantErr bool
	}{
		{"opencode", `{"model":"anything/at-all"}`, false},
		{"opencode", `{"thinking":"high"}`, true},
		{"pi", `{"model":"x/y:high","thinking":"max","prompt":"go"}`, false},
		{"pi", `{"thinking":"ludicrous"}`, true},
		{"mistral-vibe", `{"model":"local","autoApprove":true}`, false},
		{"mistral-vibe", `{"model":5}`, true},
		{"claude", `{"model":"claude-opus-5","effort":"max"}`, false},
		{"claude", `{"autoApprove":true}`, true},
	}
	for _, tt := range tests {
		t.Run(tt.option+" "+tt.config, func(t *testing.T) {
			schema := agentspec.Schema(describedAgent(t, tt.option), nil, agentspec.Host)
			err := provider.ValidateConfig(schema, json.RawMessage(tt.config))
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateConfig(%s) error = %v, wantErr = %v", tt.config, err, tt.wantErr)
			}
		})
	}
}

// schemaHasProperty reports whether a schema declares a top-level property.
func schemaHasProperty(t *testing.T, schema, name string) bool {
	t.Helper()
	var doc struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal([]byte(schema), &doc); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
	_, ok := doc.Properties[name]
	return ok
}

// --- agent identity ---

// editableSource is the seeded descriptions plus whatever a test adds or edits, the
// way the user edits them over /api/agents. It counts the reads, so a test can tell
// how often the provider asked.
type editableSource struct {
	mu        sync.Mutex
	agents    []agentspec.Agent
	listReads int
	oneReads  int
}

func newEditableSource(t *testing.T) *editableSource {
	t.Helper()
	specs, _ := seeded(t)
	return &editableSource{agents: specs.Agents()}
}

func (s *editableSource) Agents() []agentspec.Agent {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listReads++
	return slices.Clone(s.agents)
}

func (s *editableSource) Agent(id string) (agentspec.Agent, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.oneReads++
	for _, a := range s.agents {
		if a.ID == id {
			return a, true
		}
	}
	return agentspec.Agent{}, false
}

// put adds an agent, or replaces the one with the same id.
func (s *editableSource) put(agent agentspec.Agent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.agents {
		if s.agents[i].ID == agent.ID {
			s.agents[i] = agent
			return
		}
	}
	s.agents = append(s.agents, agent)
}

func (s *editableSource) resetReads() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listReads, s.oneReads = 0, 0
}

// newWithSource is newForTest over an editable set of descriptions.
func newWithSource(t *testing.T, runtime Runtime, localStore Store, source agentspec.Source) *Provider {
	t.Helper()
	_, models := seeded(t)
	p := New(runtime, localStore)
	p.SetAgentSpecs(source)
	p.SetModelCatalog(models)
	p.conversation = func(string) bool { return true }
	return p
}

// restart stops a session and starts it again, returning the argv of the relaunch.
func restart(t *testing.T, p *Provider, rt *fakeRuntime, ref provider.SessionRef) []string {
	t.Helper()
	if err := p.StopSession(context.Background(), ref); err != nil {
		t.Fatalf("StopSession: %v", err)
	}
	delete(rt.alive, rt.starts[len(rt.starts)-1].target)
	if err := p.StartSession(context.Background(), ref); err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	return rt.starts[len(rt.starts)-1].argv
}

// Agents are user rows and a binary is not unique: a second description may run the
// same CLI with different settings. A session is the agent it was launched from, so
// it has to come back as that one, not as whichever agent happens to share its binary.
func TestASessionKeepsTheAgentItWasLaunchedFrom(t *testing.T) {
	source := newEditableSource(t)
	yolo := describedAgent(t, "claude")
	yolo.ID, yolo.Display = "claude-yolo", "Claude YOLO"
	yolo.Settings = append(slices.Clone(yolo.Settings), agentspec.Setting{
		ID: "agent-setting-claude-yolo-skip", Agent: "claude-yolo", Field: "skip",
		Type: agentspec.TypeBoolean, Title: "Skip permissions",
		Delivery: agentspec.BoolFlag, DeliveryArg: "--dangerously-skip-permissions",
	})
	source.put(yolo)
	rt := newFakeRuntime()
	p := newWithSource(t, rt, &fakeStore{}, source)

	wsRef := provider.WorkstreamRef{ProjectID: "proj1", WorkstreamID: "ws-proj1"}
	sess, err := p.CreateSession(context.Background(), provider.CreateSessionRequest{
		Workstream: wsRef, WorkingDir: testDir, OptionID: "claude-yolo",
		Config: json.RawMessage(`{"skip":true}`),
	})
	if err != nil {
		t.Fatalf("CreateSession(claude-yolo): %v", err)
	}
	ref := provider.SessionRef{ProjectID: "proj1", WorkstreamID: wsRef.WorkstreamID, SessionID: sess.ID}

	ds, err := p.DiscoverSessions(context.Background(), projectRef())
	if err != nil {
		t.Fatal(err)
	}
	if ds[0].Session.Agent != "Claude YOLO" {
		t.Errorf("agent = %q, want the description it was launched from", ds[0].Session.Agent)
	}
	if argv := restart(t, p, rt, ref); !argvHas(argv, "--dangerously-skip-permissions") {
		t.Errorf("restart argv = %v, want claude-yolo's own settings re-applied", argv)
	}
}

// A PATCH that changes an agent's binary must not orphan its sessions: they are that
// agent's sessions whatever it now runs, and a restart runs the new binary.
func TestChangingAnAgentsBinaryKeepsItsSessions(t *testing.T) {
	source := newEditableSource(t)
	rt := newFakeRuntime()
	p := newWithSource(t, rt, &fakeStore{}, source)
	ref := createSession(t, p, "proj1", testDir)

	claude := describedAgent(t, "claude")
	claude.Binary = "claude-beta"
	source.put(claude)

	ds, err := p.DiscoverSessions(context.Background(), projectRef())
	if err != nil {
		t.Fatal(err)
	}
	if ds[0].Session.Agent != "Claude Code" {
		t.Errorf("agent = %q, want it still resolved to Claude Code", ds[0].Session.Agent)
	}
	if argv := restart(t, p, rt, ref); len(argv) == 0 || argv[0] != "claude-beta" {
		t.Errorf("restart argv = %v, want the agent's current binary", argv)
	}
}

// A session persisted before sessions recorded their agent id is resolved by its
// binary once, and from then on carries the id — so it survives a later binary edit
// the same way a new session does.
func TestALegacySessionIsGivenItsAgentID(t *testing.T) {
	rt := newFakeRuntime()
	rt.alive["hs-abc"] = true
	store := &fakeStore{loaded: []PersistedWorkspace{{
		ProjectID: "proj1", WorkingDir: testDir,
		Sessions: []PersistedSession{{ID: "sess-1", Title: "w", Agent: "claude", Target: "hs-abc", AgentSessionID: "abc"}},
	}}}
	newWithSource(t, rt, store, newEditableSource(t))

	if len(store.saved) != 1 || len(store.saved[0].Sessions) != 1 {
		t.Fatalf("saved = %+v, want the restored session persisted", store.saved)
	}
	if got := store.saved[0].Sessions[0].AgentID; got != "claude" {
		t.Errorf("persisted agent id = %q, want claude", got)
	}
}

// The watch tick runs every second over every session. Resolving each session's agent
// against the database would be two queries per session per tick, on a database
// pinned to one connection, so a pass reads the descriptions once.
func TestACapturePassReadsTheAgentsOnce(t *testing.T) {
	source := newEditableSource(t)
	rt := newFakeRuntime()
	p := newWithSource(t, rt, &fakeStore{}, source)
	for range 3 {
		createSession(t, p, "proj1", testDir)
	}
	// One dead session too, so the not-alive path is on the pass.
	delete(rt.alive, rt.starts[0].target)
	source.resetReads()

	p.detectChanged(context.Background(), map[string]sessionMark{})

	source.mu.Lock()
	defer source.mu.Unlock()
	if source.listReads != 1 || source.oneReads != 0 {
		t.Errorf("one pass read the descriptions %d times (plus %d single reads), want once",
			source.listReads, source.oneReads)
	}
}
