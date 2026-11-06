//go:build dev

package enclave

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
	"github.com/eclipsesource/homeshell/daemon/internal/provider"
	"github.com/eclipsesource/homeshell/daemon/internal/sbridge"
	"github.com/eclipsesource/homeshell/daemon/internal/store"
)

type testBridge struct {
	// mu serializes the methods: a poll samples sessions in parallel.
	mu           sync.Mutex
	live         bool
	snap         sbridge.Snapshot
	err          error
	startErr     error
	starts       [][]string
	dumps, stops int
	input, key   string
	inputTarget  string
}

func (b *testBridge) Available(context.Context, string) error { return nil }
func (b *testBridge) Start(_ context.Context, _, _ string, argv []string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.starts = append(b.starts, argv)
	if b.startErr != nil {
		return b.startErr
	}
	b.live = true
	return nil
}
func (b *testBridge) Snapshot(_ context.Context, target string) (sbridge.Snapshot, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.dumps++
	if b.err != nil {
		return sbridge.Snapshot{}, b.err
	}
	if !b.live {
		return sbridge.Snapshot{}, fmt.Errorf("sbridge dump %q: %w: dial unix: no such file or directory", target, sbridge.ErrNotServing)
	}
	return b.snap, nil
}
func (b *testBridge) Stop(context.Context, string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.stops++
	b.live = false
	return nil
}
func (b *testBridge) SendInput(_ context.Context, target, text string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.input = text
	b.inputTarget = target
	return nil
}
func (b *testBridge) SendKey(_ context.Context, _, key string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.key = key
	return nil
}
func (b *testBridge) AttachCommand(target string) []string {
	return []string{"homeshell", "bridge", "attach", "--sock", target}
}

func bridgeProvider(t *testing.T) (*Provider, *fakeCLI, *testBridge, provider.ProjectRef) {
	t.Helper()
	dir := t.TempDir()
	cli := &fakeCLI{available: true, runResult: "enclave-codex-0123456789ab-hs-134"}
	bridge := &testBridge{snap: sbridge.Snapshot{Screen: "Ready", OSCTitle: "✳ Codex"}}
	p, err := NewWithBridge(cli, bridge, filepath.Join(t.TempDir(), "enclave-provider.json"))
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "workstreams")
	p.workstreamsRoot = func() (string, error) { return root, nil }
	specs, models := seeded(t)
	p.SetAgentSpecs(specs)
	p.SetModelCatalog(models)
	project := provider.ProjectRef{ID: "p", Path: dir}
	p.SetStore(store.NewTestStore(t, []domain.Project{{ID: project.ID, Path: dir, Workstreams: []domain.Workstream{{ID: "ws", WorkingDir: dir, Sessions: []domain.Session{}}}}}))
	return p, cli, bridge, project
}
func createBridge(t *testing.T, p *Provider, project provider.ProjectRef) domain.Session {
	t.Helper()
	session, err := p.CreateSession(context.Background(), provider.CreateSessionRequest{WorkingDir: project.Path, OptionID: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	return session
}

// startBridge does what the service does for an Enclave start: a restart with
// the recorded launch config, once StartSession has accepted the session.
func startBridge(t *testing.T, p *Provider, ref provider.SessionRef, dir string) error {
	t.Helper()
	if err := p.StartSession(context.Background(), ref); !errors.Is(err, provider.ErrStartIsRestart) {
		return err
	}
	launch, _ := p.store.LaunchConfig(ref.SessionID)
	run, err := p.PrepareRestart(context.Background(), provider.RestartSessionRequest{Session: ref, WorkingDir: dir, OptionID: launch.OptionID, Config: launch.Config})
	if err != nil {
		return err
	}
	return run(context.Background())
}

// pastStartup makes id look attached by an earlier daemon, so its exit is an end rather than a failed start.
func pastStartup(p *Provider, id string) {
	p.bridges.mu.Lock()
	defer p.bridges.mu.Unlock()
	p.bridges.guard(id).attachedAt = time.Time{}
}

func discoverBridge(t *testing.T, p *Provider, project provider.ProjectRef) domain.Session {
	t.Helper()
	p.sampleBridges(context.Background(), p.detectionAgents(), func(dir string) bool { return p.ownsDir(project, dir) }, true)
	p.waitForBridgeSamples()
	out, err := p.DiscoverWatchedSessions(context.Background(), project)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("sessions=%+v", out)
	}
	return out[0].Session
}

// Synchronous tests join the per-target background recovery before changing
// their fakes or asserting its effects. Watch itself never waits for recovery.
func (p *Provider) waitForBridgeSamples() {
	for {
		p.bridges.mu.Lock()
		busy := slices.ContainsFunc(slices.Collect(maps.Values(p.bridges.guards)), func(g *bridgeGuard) bool { return g.sampling })
		p.bridges.mu.Unlock()
		if !busy {
			return
		}
		time.Sleep(time.Millisecond)
	}
}

func (p *Provider) sampleBridgeAndWait(ctx context.Context, id string, agents map[string]string, recoverMissing bool) {
	p.sampleBridge(ctx, id, agents, recoverMissing)
	p.waitForBridgeSamples()
}
func TestHostBridgeCaptureReuseRecoveryAndDaemonRestart(t *testing.T) {
	p, cli, b, project := bridgeProvider(t)
	created := createBridge(t, p, project)
	if len(cli.runCalls) != 1 || !cli.runCalls[0].opts.Background || len(b.starts) != 1 || !reflect.DeepEqual(b.starts[0], []string{"enclave", "attach", created.ID}) {
		t.Fatalf("run=%+v bridge=%+v", cli.runCalls, b.starts)
	}
	cli.all = map[string][]Container{project.Path: {{Name: created.ID, Tool: "codex", Directory: project.Path, Status: "running"}}}
	got := discoverBridge(t, p, project)
	if got.State != domain.SessionIdle {
		t.Fatalf("state=%s", got.State)
	}
	before := b.dumps
	if _, err := p.DiscoverWatchedSessions(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	if b.dumps != before || cli.listCalls != 0 {
		t.Fatalf("projection recaptured: dumps=%d->%d inventory=%d", before, b.dumps, cli.listCalls)
	}
	b.live = false
	b.snap.OSCTitle = ""
	if got := discoverBridge(t, p, project); got.State != domain.SessionIdle {
		t.Fatalf("reattached state=%s", got.State)
	}
	if len(b.starts) != 2 || len(cli.runCalls) != 1 {
		t.Fatalf("reattach restarted agent: bridge=%d run=%d", len(b.starts), len(cli.runCalls))
	}
	again, err := NewWithBridge(cli, b, p.bridges.path)
	if err != nil {
		t.Fatal(err)
	}
	again.SetStore(p.store)
	if got := discoverBridge(t, again, project); got.State != domain.SessionIdle {
		t.Fatalf("daemon restart lost title/idle: %+v", got)
	}
	if len(b.starts) != 2 || len(cli.runCalls) != 1 {
		t.Fatal("daemon restart launched an extra bridge or agent")
	}
}
func TestHostBridgeUncertainFailureNeverReattaches(t *testing.T) {
	for _, failure := range []error{context.DeadlineExceeded, os.ErrPermission, errors.New("malformed snapshot")} {
		t.Run(failure.Error(), func(t *testing.T) {
			p, cli, b, project := bridgeProvider(t)
			createBridge(t, p, project)
			b.err = failure
			if got := discoverBridge(t, p, project); got.State != domain.SessionUnknown {
				t.Fatalf("state=%s", got.State)
			}
			if len(b.starts) != 1 || len(cli.stopped) != 0 {
				t.Fatal("uncertain read caused lifecycle changes")
			}
		})
	}
}
func TestHostBridgeExitCleansContainerAndGatewayOnce(t *testing.T) {
	p, cli, b, project := bridgeProvider(t)
	advance := recoveryClock(p)
	created := createBridge(t, p, project)
	pastStartup(p, created.ID)
	b.live = false
	cli.all = map[string][]Container{project.Path: {{Name: created.ID, Status: "exited"}}}
	cli.stopErr = errors.New("Docker unavailable")
	if got := discoverBridge(t, p, project); got.State == domain.SessionDone {
		t.Fatal("failed cleanup published as complete")
	}
	cli.stopErr = nil
	advance(recoveryRetryMin)
	if got := discoverBridge(t, p, project); got.State != domain.SessionDone {
		t.Fatalf("state=%s", got.State)
	}
	stops := len(cli.stopped)
	discoverBridge(t, p, project)
	if len(cli.stopped) != stops || len(b.starts) != 1 {
		t.Fatal("completed exit reattached or repeated cleanup")
	}
	if err := p.RemoveSession(context.Background(), provider.SessionRef{SessionID: created.ID}); err != nil {
		t.Fatal(err)
	}
	again, err := NewWithBridge(cli, b, p.bridges.path)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.bridges.records) != 0 {
		t.Fatal("removed session restored")
	}
}
func TestHostBridgeExternalRemovalAndInventoryFailure(t *testing.T) {
	p, cli, b, project := bridgeProvider(t)
	advance := recoveryClock(p)
	createBridge(t, p, project)
	b.live = false
	cli.allErr = errors.New("Docker down")
	discoverBridge(t, p, project)
	if len(b.starts) != 1 || len(cli.stopped) != 0 {
		t.Fatal("failed inventory changed lifecycle")
	}
	cli.allErr = nil
	advance(recoveryRetryMin)
	if got := discoverBridge(t, p, project); got.State != domain.SessionDone {
		t.Fatal("externally removed container not ended")
	}
	if len(cli.stopped) != 0 {
		t.Fatal("attempted stop on provably absent container")
	}
}
func TestHostBridgeInputDoesNotUseContainerExec(t *testing.T) {
	p, cli, b, project := bridgeProvider(t)
	created := createBridge(t, p, project)
	ref := provider.SessionRef{ProjectID: project.ID, SessionID: created.ID}
	if err := p.Prompt(context.Background(), ref, "hello $(literal)"); err != nil {
		t.Fatal(err)
	}
	if err := p.SendKey(context.Background(), ref, "up"); err != nil {
		t.Fatal(err)
	}
	if b.input != "hello $(literal)" || b.key != "up" || cli.listCalls != 0 {
		t.Fatal("input did not use host bridge")
	}
	if err := p.StopSession(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	if got := discoverBridge(t, p, project); got.State != domain.SessionStopped {
		t.Fatalf("state=%s", got.State)
	}
	if err := p.Prompt(context.Background(), ref, "after stop"); err == nil {
		t.Fatal("prompted stopped session")
	}
	if b.stops != 1 || len(cli.stopped) != 1 {
		t.Fatalf("stop bridge=%d container=%v", b.stops, cli.stopped)
	}
}

func TestHostBridgeFailedAttachCleansCreatedContainer(t *testing.T) {
	p, cli, b, project := bridgeProvider(t)
	b.startErr = errors.New("attach refused")
	_, err := p.CreateSession(context.Background(), provider.CreateSessionRequest{WorkingDir: project.Path, OptionID: "codex"})
	if err == nil || !strings.Contains(err.Error(), "attach refused") || len(cli.stopped) != 1 || len(p.bridges.records) != 0 {
		t.Fatalf("error=%v stopped=%v records=%v", err, cli.stopped, p.bridges.records)
	}
}
func TestHostBridgeCorruptOwnershipFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "enclave-provider.json")
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewWithBridge(&fakeCLI{}, &testBridge{}, path); err == nil {
		t.Fatal("corrupt records silently lost")
	}
}
func TestHostBridgeRestartAndWithdrawal(t *testing.T) {
	for _, withdraw := range []bool{false, true} {
		t.Run(fmt.Sprint(withdraw), func(t *testing.T) {
			p, cli, b, project := bridgeProvider(t)
			created := createBridge(t, p, project)
			ref := provider.SessionRef{ProjectID: project.ID, WorkstreamID: "ws", SessionID: created.ID}
			if withdraw {
				cli.onRun = func() {
					if err := p.RemoveSession(context.Background(), ref); err != nil {
						t.Fatal(err)
					}
				}
			}
			run, err := p.PrepareRestart(context.Background(), provider.RestartSessionRequest{Session: ref, WorkingDir: project.Path, OptionID: "codex", Fresh: true})
			if err != nil {
				t.Fatal(err)
			}
			if err := run(context.Background()); err != nil {
				t.Fatal(err)
			}
			if withdraw {
				if len(p.bridges.records) != 0 || b.live {
					t.Fatal("withdrawn restart left a bridge or record")
				}
			} else {
				if len(b.starts) != 2 || len(cli.runCalls) != 2 || b.stops != 1 {
					t.Fatalf("starts=%d runs=%d stops=%d", len(b.starts), len(cli.runCalls), b.stops)
				}
				if got := discoverBridge(t, p, project); got.State != domain.SessionIdle {
					t.Fatalf("state=%s", got.State)
				}
			}
		})
	}
}
func TestHostBridgeRestartFailureAndRetry(t *testing.T) {
	p, cli, _, project := bridgeProvider(t)
	created := createBridge(t, p, project)
	request := provider.RestartSessionRequest{Session: provider.SessionRef{ProjectID: project.ID, WorkstreamID: "ws", SessionID: created.ID}, WorkingDir: project.Path, OptionID: "codex", Fresh: true}
	cli.runErr = errors.New("mount refused")
	run, err := p.PrepareRestart(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := discoverBridge(t, p, project); got.State != domain.SessionError || !strings.Contains(got.Error, "mount refused") {
		t.Fatalf("failed restart=%+v", got)
	}
	cli.runErr = nil
	cli.stopErr = errors.New("not found")
	run, err = p.PrepareRestart(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := discoverBridge(t, p, project); got.State != domain.SessionIdle {
		t.Fatalf("retried restart=%+v", got)
	}
}
func TestHostBridgeStopStartUsesLaunchConfigAndSensitiveDir(t *testing.T) {
	p, cli, _, project := bridgeProvider(t)
	root, _ := p.workstreamsRoot()
	dir := filepath.Join(root, "ws")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	project.Path = dir
	p.store = store.NewTestStore(t, []domain.Project{{ID: project.ID, Path: dir, Workstreams: []domain.Workstream{{ID: "ws", WorkingDir: dir}}}})
	created := createBridge(t, p, project)
	if !slices.Equal(cli.runCalls[0].opts.SensitiveDirs, []string{dir}) {
		t.Fatal("create missing exception")
	}
	if err := p.store.SetLaunchConfig(project.ID, created.ID, domain.LaunchConfig{ProviderID: "enclave", OptionID: "codex"}); err != nil {
		t.Fatal(err)
	}
	ref := provider.SessionRef{ProjectID: project.ID, WorkstreamID: "ws", SessionID: created.ID}
	if err := p.StopSession(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	if err := startBridge(t, p, ref, dir); err != nil {
		t.Fatal(err)
	}
	if len(cli.runCalls) != 2 || !cli.runCalls[1].opts.Continue || !slices.Equal(cli.runCalls[1].opts.SensitiveDirs, []string{dir}) {
		t.Fatalf("restart opts=%+v", cli.runCalls)
	}
}
func TestHostBridgePersistenceFailureCleansCreatedContainer(t *testing.T) {
	p, cli, _, project := bridgeProvider(t)
	p.bridges.path = t.TempDir() // atomic rename cannot replace a directory
	_, err := p.CreateSession(context.Background(), provider.CreateSessionRequest{WorkingDir: project.Path, OptionID: "codex"})
	if err == nil || len(cli.stopped) != 1 || len(p.bridges.records) != 0 {
		t.Fatalf("err=%v stopped=%v records=%v", err, cli.stopped, p.bridges.records)
	}
}

func TestHostBridgeOpenUsesHostSocket(t *testing.T) {
	p, _, bridge, project := bridgeProvider(t)
	created := createBridge(t, p, project)
	launcher := &fakeLauncher{}
	p.term = launcher
	if _, err := p.Open(context.Background(), provider.SessionRef{ProjectID: "other", SessionID: created.ID}, "terminal"); err == nil {
		t.Fatal("opened a session through a project that does not own it")
	}
	bridge.dumps = 0
	ref := provider.SessionRef{ProjectID: project.ID, SessionID: created.ID}
	if _, err := p.Open(context.Background(), ref, "terminal"); err != nil {
		t.Fatal(err)
	}
	if bridge.dumps != 1 {
		t.Fatalf("open read the bridge %d times, want 1", bridge.dumps)
	}
	if len(launcher.launched) != 1 || !strings.Contains(strings.Join(launcher.launched[0], " "), "'bridge' 'attach'") || !strings.Contains(strings.Join(launcher.launched[0], " "), "--sock") {
		t.Fatalf("terminal=%v", launcher.launched)
	}
}

func TestHostBridgeOpenDoesNotLaunchOnMissingBridge(t *testing.T) {
	p, _, bridge, project := bridgeProvider(t)
	created := createBridge(t, p, project)
	launcher := &fakeLauncher{}
	p.term = launcher
	bridge.live = false
	if _, err := p.Open(context.Background(), provider.SessionRef{ProjectID: project.ID, SessionID: created.ID}, "terminal"); err == nil || !strings.Contains(err.Error(), "sbridge dump") {
		t.Fatalf("open missing bridge: %v", err)
	}
	if len(launcher.launched) != 0 {
		t.Fatal("opened a terminal on an exited session")
	}
}

func TestHostBridgeOpenAllowsUncertainCapture(t *testing.T) {
	for _, failure := range []error{context.DeadlineExceeded, errors.New(sbridge.ErrNotServing.Error())} {
		t.Run(failure.Error(), func(t *testing.T) {
			p, _, bridge, project := bridgeProvider(t)
			created := createBridge(t, p, project)
			launcher := &fakeLauncher{}
			p.term = launcher
			bridge.err = failure
			if _, err := p.Open(context.Background(), provider.SessionRef{ProjectID: project.ID, SessionID: created.ID}, "terminal"); err != nil {
				t.Fatalf("uncertain capture blocked a potentially live bridge: %v", err)
			}
			if len(launcher.launched) != 1 {
				t.Fatal("did not open the potentially live bridge")
			}
		})
	}
}

func TestDiscoveryOnlyObservesOwnedBridges(t *testing.T) {
	p, cli, bridge, project := bridgeProvider(t)
	cli.containers = map[string][]Container{project.Path: {{Name: "external", Tool: "codex", Directory: project.Path, Status: "running"}}}
	poll := p.changeDetector()
	for range 10 {
		poll(context.Background())
		out, err := p.DiscoverSessions(context.Background(), project)
		if err != nil || len(out) != 0 {
			t.Fatalf("unowned sessions discovered: %+v err=%v", out, err)
		}
	}
	if bridge.dumps != 0 || cli.listCalls != 0 || cli.allCalls != 0 {
		t.Fatalf("empty provider queried runtime: dumps=%d ps=%d psall=%d", bridge.dumps, cli.listCalls, cli.allCalls)
	}
	created := createBridge(t, p, project)
	for range 10 {
		poll(context.Background())
	}
	out, err := p.DiscoverWatchedSessions(context.Background(), project)
	if err != nil || len(out) != 1 || out[0].Session.ID != created.ID {
		t.Fatalf("owned session=%+v err=%v", out, err)
	}
	if cli.listCalls != 0 || cli.allCalls != 0 {
		t.Fatal("healthy bridge queried container inventory")
	}
	if err := p.Prompt(context.Background(), provider.SessionRef{ProjectID: project.ID, SessionID: "external"}, "must not send"); err == nil {
		t.Fatal("unowned external session accepted input")
	}
	if err := p.RemoveSession(context.Background(), provider.SessionRef{ProjectID: project.ID, SessionID: "external"}); err == nil {
		t.Fatal("unowned external session accepted removal")
	}
	if len(cli.stopped) != 0 || bridge.input != "" {
		t.Fatal("unowned external session was modified")
	}
}

// An ended state can come from a listing that missed the container, so remove
// still asks Enclave to stop it and its gateway.
func TestRemoveStopsContainerRecordedAsEnded(t *testing.T) {
	p, cli, _, project := bridgeProvider(t)
	created := createBridge(t, p, project)
	p.bridges.mu.Lock()
	r := p.bridges.records[created.ID]
	r.State = domain.SessionDone
	p.bridges.records[created.ID] = r
	p.bridges.mu.Unlock()
	if err := p.RemoveSession(context.Background(), provider.SessionRef{ProjectID: project.ID, SessionID: created.ID}); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(cli.stopped, created.ID) {
		t.Fatalf("stopped=%v; want the ended container stopped", cli.stopped)
	}
}

// Persisting a cosmetic observation is retried later; it must not hide every
// Enclave session from discovery.
func TestObservationSaveFailureKeepsDiscovery(t *testing.T) {
	p, _, bridge, project := bridgeProvider(t)
	createBridge(t, p, project)
	p.bridges.path = t.TempDir() // atomic rename cannot replace a directory
	bridge.snap.OSCTitle = "✳ A new title"
	out, err := p.DiscoverSessions(context.Background(), project)
	if err != nil || len(out) != 1 || out[0].Session.Title != "A new title" {
		t.Fatalf("sessions=%+v err=%v", out, err)
	}
}

func TestInputIsRefusedWhileRestarting(t *testing.T) {
	p, cli, bridge, project := bridgeProvider(t)
	created := createBridge(t, p, project)
	ref := provider.SessionRef{ProjectID: project.ID, SessionID: created.ID}
	running, release := make(chan struct{}), make(chan struct{})
	cli.onRun = func() { close(running); <-release }
	run, err := p.PrepareRestart(context.Background(), provider.RestartSessionRequest{Session: ref, WorkingDir: project.Path, OptionID: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- run(context.Background()) }()
	<-running
	if err := p.Prompt(context.Background(), ref, "hello"); !errors.Is(err, provider.ErrSessionStopped) {
		t.Errorf("prompt during restart=%v; want ErrSessionStopped", err)
	}
	if err := p.StopSession(context.Background(), ref); err == nil {
		t.Error("stop during restart was accepted")
	}
	if bridge.input != "" {
		t.Errorf("input reached the old bridge: %q", bridge.input)
	}
	cli.onRun = nil
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestReattachDoesNotReplayAWorkingTitleOverTheScreen(t *testing.T) {
	p, cli, runtime, project := bridgeProvider(t)
	created := createBridge(t, p, project)
	runtime.snap = sbridge.Snapshot{Screen: "Working", OSCTitle: "⠋ Codex"}
	if got := discoverBridge(t, p, project); got.State != domain.SessionRunning {
		t.Fatalf("spinner state=%s", got.State)
	}
	// The turn ends in an approval prompt while the daemon is down; the new bridge has no title yet.
	restored, err := NewWithBridge(cli, runtime, p.bridges.path)
	if err != nil {
		t.Fatal(err)
	}
	restored.SetAgentSpecs(p.specs)
	restored.SetStore(p.store)
	cli.all = map[string][]Container{project.Path: {{Name: created.ID, Tool: "codex", Directory: project.Path, Status: "running"}}}
	runtime.snap = sbridge.Snapshot{Screen: "• Proposed command\n\n  Allow command?\n    cargo test\n\n  press enter to confirm or esc to cancel\n"}
	if got := discoverBridge(t, restored, project); got.State != domain.SessionWaiting {
		t.Fatalf("replayed spinner hid the approval prompt: state=%s", got.State)
	}
}

func TestChangeDetectorSamplesSharedDirectoryOnce(t *testing.T) {
	p, _, runtime, project := bridgeProvider(t)
	created := createBridge(t, p, project)
	// A second project whose workstream runs in the first project's directory.
	p.SetStore(store.NewTestStore(t, []domain.Project{
		{ID: project.ID, Path: project.Path, Workstreams: []domain.Workstream{{ID: "ws", WorkingDir: project.Path, Sessions: []domain.Session{}}}},
		{ID: "q", Path: t.TempDir(), Workstreams: []domain.Workstream{{ID: "ws-q", WorkingDir: project.Path, Sessions: []domain.Session{}}}},
	}))
	runtime.mu.Lock()
	runtime.dumps = 0
	runtime.mu.Unlock()
	if !p.changeDetector()(context.Background()) {
		t.Fatal("first pass reported no change")
	}
	if runtime.dumps != 1 {
		t.Fatalf("dumps = %d; want one per target per tick", runtime.dumps)
	}
	for _, id := range []string{project.ID, "q"} {
		if got := p.projectBridges(provider.ProjectRef{ID: id, Path: project.Path}, p.storeProjects()); len(got) != 1 || got[0].Session.ID != created.ID {
			t.Fatalf("project %s: %+v", id, got)
		}
	}
}

// bridgeReadError is why an unknown session's terminal could not be read.
func bridgeReadError(session domain.Session) string {
	var detail bridgeDetail
	_ = json.Unmarshal(session.ObservationData, &detail)
	return detail.ReadError
}

// Polling an unchanged screen must not fsync and rename the record file every tick.
func TestUnchangedObservationDoesNotRewriteRecords(t *testing.T) {
	p, _, _, project := bridgeProvider(t)
	created := createBridge(t, p, project)
	p.sampleBridgeAndWait(context.Background(), created.ID, nil, true)
	before, err := os.Stat(p.bridges.path)
	if err != nil {
		t.Fatal(err)
	}
	p.sampleBridgeAndWait(context.Background(), created.ID, nil, true)
	after, err := os.Stat(p.bridges.path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("an unchanged observation rewrote the record file")
	}
}

func TestObservationWritesTitleOutsideMetadataLock(t *testing.T) {
	p, _, bridge, project := bridgeProvider(t)
	created := createBridge(t, p, project)
	discoverBridge(t, p, project)
	const title = "✳ Fix the login bug"
	bridge.mu.Lock()
	bridge.snap.OSCTitle = title
	bridge.mu.Unlock()
	p.bridges.writeMu.Lock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.sampleBridge(context.Background(), created.ID, p.detectionAgents(), false)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for published := false; !published; {
		if time.Now().After(deadline) {
			p.bridges.writeMu.Unlock()
			t.Fatal("a pending record write kept the metadata lock")
		}
		if p.bridges.mu.TryLock() {
			published = p.bridges.records[created.ID].LastTitle == title
			p.bridges.mu.Unlock()
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case <-done:
		t.Fatal("observation finished while its record write was blocked")
	default:
	}
	p.bridges.writeMu.Unlock()
	<-done
	data, err := os.ReadFile(p.bridges.path)
	if err != nil || !strings.Contains(string(data), title) {
		t.Fatalf("record file=%s err=%v", data, err)
	}
}

func TestRecordWriteKeepsTheNewerFile(t *testing.T) {
	p, _, _, project := bridgeProvider(t)
	created := createBridge(t, p, project)
	b := p.bridges
	encodeTitle := func(title string) recordFile {
		b.mu.Lock()
		defer b.mu.Unlock()
		record := b.records[created.ID]
		record.LastTitle = title
		b.records[created.ID] = record
		file, err := b.encode()
		if err != nil {
			t.Fatal(err)
		}
		return file
	}
	older, newer := encodeTitle("older"), encodeTitle("newer")
	if err := b.write(newer); err != nil {
		t.Fatal(err)
	}
	if err := b.write(older); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(b.path)
	if err != nil || !strings.Contains(string(data), `"newer"`) {
		t.Fatalf("record file=%s err=%v", data, err)
	}
}

// locatingBridge resolves every target under location; elsewhere lists recorded sockets that still answer.
type locatingBridge struct {
	*testBridge
	location  string
	elsewhere []string
}

func (b *locatingBridge) Location(target string) string { return b.location + "/" + target + ".sock" }
func (b *locatingBridge) LocateElsewhere(_ context.Context, _, recorded string) string {
	if slices.Contains(b.elsewhere, recorded) {
		return recorded
	}
	return ""
}

// A daemon resolving another socket directory must not attach a container its old bridge still holds.
func TestReattachRefusesWhileTheRecordedBridgeAnswers(t *testing.T) {
	p, cli, b, project := bridgeProvider(t)
	locator := &locatingBridge{testBridge: b, location: "/run/old"}
	p.bridges.runtime = locator
	created := createBridge(t, p, project)
	first := locator.Location(bridgeTarget(created.ID))
	if record, _ := p.bridgeRecordFor(created.ID); record.Sock != first {
		t.Fatalf("recorded sock = %q, want %q", record.Sock, first)
	}
	cli.all = map[string][]Container{project.Path: {{Name: created.ID, Tool: "codex", Directory: project.Path, Status: "running"}}}

	locator.location, locator.elsewhere, b.live = "/run/new", []string{first}, false
	if got := discoverBridge(t, p, project); got.State != domain.SessionUnknown || !strings.Contains(bridgeReadError(got), first) {
		t.Fatalf("state=%s readError=%q", got.State, bridgeReadError(got))
	}
	if len(b.starts) != 1 {
		t.Fatalf("attached a second bridge beside the recorded one: starts=%d", len(b.starts))
	}

	locator.elsewhere = nil
	p.bridges.mu.Lock()
	p.bridges.guard(created.ID).retryAt = time.Time{}
	p.bridges.mu.Unlock()
	discoverBridge(t, p, project)
	if record, _ := p.bridgeRecordFor(created.ID); len(b.starts) != 2 || record.Sock != locator.Location(bridgeTarget(created.ID)) {
		t.Fatalf("starts=%d sock=%q after the old bridge went away", len(b.starts), record.Sock)
	}
}
