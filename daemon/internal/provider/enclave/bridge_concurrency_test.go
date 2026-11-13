//go:build dev

package enclave

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/detect"
	"github.com/eclipsesource/homeshell/daemon/internal/domain"
	"github.com/eclipsesource/homeshell/daemon/internal/provider"
	"github.com/eclipsesource/homeshell/daemon/internal/sbridge"
)

type controlledBridge struct {
	BridgeRuntime
	snapshot func(context.Context, string) (sbridge.Snapshot, error)
	start    func(context.Context, string, string, []string) error
	stop     func(context.Context, string) error
}

func (b controlledBridge) Snapshot(ctx context.Context, id string) (sbridge.Snapshot, error) {
	if b.snapshot != nil {
		return b.snapshot(ctx, id)
	}
	return b.BridgeRuntime.Snapshot(ctx, id)
}
func (b controlledBridge) Start(ctx context.Context, id, dir string, argv []string) error {
	if b.start != nil {
		return b.start(ctx, id, dir, argv)
	}
	return b.BridgeRuntime.Start(ctx, id, dir, argv)
}
func (b controlledBridge) Stop(ctx context.Context, id string) error {
	if b.stop != nil {
		return b.stop(ctx, id)
	}
	return b.BridgeRuntime.Stop(ctx, id)
}

type controlledCLI struct {
	CLI
	inventory func(context.Context, string) ([]Container, error)
	stop      func(context.Context, string) error
}

func (c controlledCLI) ListAllSessions(ctx context.Context, dir string) ([]Container, error) {
	return c.inventory(ctx, dir)
}

func (c controlledCLI) Stop(ctx context.Context, id string) error {
	if c.stop != nil {
		return c.stop(ctx, id)
	}
	return c.CLI.Stop(ctx, id)
}

func TestSlowBridgeIOLeavesOtherSessionOperationsResponsive(t *testing.T) {
	for _, operation := range []string{"capture", "inventory", "attach", "stop"} {
		t.Run(operation, func(t *testing.T) {
			p, cli, runtime, project := bridgeProvider(t)
			first := createBridge(t, p, project)
			cli.runResult = "enclave-codex-0123456789ab-hs-second"
			second := createBridge(t, p, project)
			firstTarget := bridgeTarget(first.ID)
			entered, release := make(chan struct{}), make(chan struct{})
			blocked := func() { close(entered); <-release }
			// The blocked external call returns fixed data, avoiding races in the
			// deliberately simple sequential fake while other operations execute.
			fixed := sbridge.Snapshot{Screen: "Ready", OSCTitle: "✳ Codex"}
			bridge := controlledBridge{BridgeRuntime: runtime}
			bridge.snapshot = func(_ context.Context, id string) (sbridge.Snapshot, error) {
				if id == firstTarget {
					if operation == "capture" {
						blocked()
						return fixed, nil
					}
					if operation == "inventory" {
						return sbridge.Snapshot{}, sbridge.ErrNotServing
					}
				}
				return fixed, nil
			}
			bridge.start = func(_ context.Context, id, dir string, argv []string) error {
				if operation == "attach" && id == firstTarget {
					blocked()
				}
				return nil
			}
			bridge.stop = func(_ context.Context, id string) error {
				if operation == "stop" && id == firstTarget {
					blocked()
				}
				return nil
			}
			p.bridges.runtime = bridge
			p.cli = controlledCLI{CLI: cli, inventory: func(context.Context, string) ([]Container, error) { blocked(); return nil, errors.New("Docker down") }}
			finished := make(chan error, 1)
			go func() {
				switch operation {
				case "capture", "inventory":
					p.sampleBridgeAndWait(context.Background(), first.ID, nil, true)
					finished <- nil
				case "attach":
					finished <- p.registerBridge(context.Background(), bridgeRecord{Container: Container{Name: first.ID, Tool: "codex", Directory: project.Path}})
				case "stop":
					finished <- p.removeBridge(context.Background(), first.ID, false)
				}
			}()
			defer func() { close(release); <-finished }()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("external call never started")
			}
			done := make(chan error, 1)
			go func() {
				ref := provider.SessionRef{ProjectID: project.ID, SessionID: second.ID}
				if err := p.Prompt(context.Background(), ref, "hello"); err != nil {
					done <- err
					return
				}
				if err := p.SendKey(context.Background(), ref, "up"); err != nil {
					done <- err
					return
				}
				if _, err := p.DiscoverWatchedSessions(context.Background(), project); err != nil {
					done <- err
					return
				}
				done <- p.StopSession(context.Background(), ref)
			}()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("one target's I/O blocked another session")
			}
		})
	}
}

func TestLifecycleChangeRejectsInFlightObservation(t *testing.T) {
	for _, mutation := range []string{"stop", "remove", "restart"} {
		for _, missing := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/missing=%v", mutation, missing), func(t *testing.T) {
				p, cli, runtime, project := bridgeProvider(t)
				created := createBridge(t, p, project)
				entered, release := make(chan struct{}), make(chan struct{})
				p.bridges.runtime = controlledBridge{BridgeRuntime: runtime, snapshot: func(context.Context, string) (sbridge.Snapshot, error) {
					close(entered)
					<-release
					if missing {
						return sbridge.Snapshot{}, sbridge.ErrNotServing
					}
					return sbridge.Snapshot{Screen: "old", OSCTitle: "⠋ Codex"}, nil
				}}
				finished := make(chan error, 1)
				go func() { p.sampleBridgeAndWait(context.Background(), created.ID, nil, true); finished <- nil }()
				<-entered
				changed := make(chan error, 1)
				go func() {
					ref := provider.SessionRef{ProjectID: project.ID, SessionID: created.ID}
					switch mutation {
					case "stop":
						changed <- p.StopSession(context.Background(), ref)
					case "remove":
						changed <- p.RemoveSession(context.Background(), ref)
					case "restart":
						run, err := p.PrepareRestart(context.Background(), provider.RestartSessionRequest{Session: ref, WorkingDir: project.Path, OptionID: "codex", Fresh: true})
						if err == nil {
							err = run(context.Background())
						}
						changed <- err
					}
				}()
				select {
				case err := <-changed:
					if err != nil {
						close(release)
						<-finished
						t.Fatal(err)
					}
				case <-time.After(time.Second):
					close(release)
					<-finished
					<-changed
					t.Fatal("capture blocked lifecycle change")
				}
				close(release)
				if err := <-finished; err != nil {
					t.Fatal(err)
				}
				if cli.allCalls != 0 {
					t.Fatal("stale missing-socket capture queried inventory after lifecycle change")
				}
				p.bridges.runtime = runtime
				out, err := p.DiscoverSessions(context.Background(), project)
				if err != nil {
					t.Fatal(err)
				}
				if mutation == "remove" {
					if len(out) != 0 {
						t.Fatal("stale capture restored removed session")
					}
					return
				}
				want := domain.SessionStopped
				if mutation == "restart" {
					want = domain.SessionIdle
				}
				if len(out) != 1 || out[0].Session.State != want || out[0].Session.Preview == "old" {
					t.Fatalf("stale observation survived %s: %+v", mutation, out)
				}
			})
		}
	}
}

func TestSpinnerFramesDoNotRewriteBridgeOwnership(t *testing.T) {
	p, _, runtime, project := bridgeProvider(t)
	created := createBridge(t, p, project)
	for i, title := range []string{"⠋ Codex", "⠙ Codex", "⠹ Codex", "⠸ Codex"} {
		runtime.snap.OSCTitle = title
		got := discoverBridge(t, p, project)
		if got.State != domain.SessionRunning {
			t.Fatalf("spinner lost detection meaning: %+v", got)
		}
		if i == 0 {
			if err := os.Chtimes(p.bridges.path, time.Unix(100, 0), time.Unix(100, 0)); err != nil {
				t.Fatal(err)
			}
			continue
		}
		info, err := os.Stat(p.bridges.path)
		if err != nil {
			t.Fatal(err)
		}
		// A fixed mtime gives an independent witness that save/fsync/rename did
		// not happen, even if identical JSON could have been rewritten.
		if !info.ModTime().Equal(time.Unix(100, 0)) {
			t.Fatal("spinner-only update rewrote ownership")
		}
	}
	runtime.snap.OSCTitle = "✳ Codex"
	// The idle hold must first expire; resetting history isolates persistence.
	p.bridges.mu.Lock()
	p.bridges.guard(created.ID).hold = detect.Hold{}
	p.bridges.mu.Unlock()
	if got := discoverBridge(t, p, project); got.State != domain.SessionIdle {
		t.Fatalf("idle title transition lost: %+v", got)
	}
	restored, err := NewWithBridge(p.cli, runtime, p.bridges.path)
	if err != nil {
		t.Fatal(err)
	}
	restored.SetStore(p.store)
	runtime.snap.OSCTitle = ""
	if got := discoverBridge(t, restored, project); got.State != domain.SessionIdle {
		t.Fatalf("persisted representative title lost idle: %+v", got)
	}
}

func TestPollStartedDuringStopCannotRecoverStoppedSession(t *testing.T) {
	for _, status := range []string{"running", "exited", "missing"} {
		t.Run(status, func(t *testing.T) {
			p, cli, runtime, project := bridgeProvider(t)
			created := createBridge(t, p, project)
			ref := provider.SessionRef{ProjectID: project.ID, SessionID: created.ID}
			if err := p.store.SetLaunchConfig(project.ID, created.ID, domain.LaunchConfig{ProviderID: "enclave", OptionID: "codex"}); err != nil {
				t.Fatal(err)
			}
			stopEntered, releaseStop, inventoried := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var firstStop sync.Once
			p.bridges.runtime = controlledBridge{BridgeRuntime: runtime, snapshot: func(context.Context, string) (sbridge.Snapshot, error) {
				return sbridge.Snapshot{}, sbridge.ErrNotServing
			}}
			p.cli = controlledCLI{CLI: cli, stop: func(context.Context, string) error {
				firstStop.Do(func() { close(stopEntered); <-releaseStop })
				return nil
			}, inventory: func(context.Context, string) ([]Container, error) {
				close(inventoried)
				if status == "missing" {
					return nil, nil
				}
				return []Container{{Name: created.ID, Status: status}}, nil
			}}
			stopped, sampled := make(chan error, 1), make(chan error, 1)
			go func() { stopped <- p.StopSession(context.Background(), ref) }()
			<-stopEntered
			go func() { p.sampleBridgeAndWait(context.Background(), created.ID, nil, true); sampled <- nil }()
			<-inventoried // This poll captured the revision after stop began.
			close(releaseStop)
			if err := <-stopped; err != nil {
				t.Fatal(err)
			}
			if err := <-sampled; err != nil {
				t.Fatal(err)
			}
			if len(runtime.starts) != 1 {
				t.Fatal("poll during stop reattached the stopped container")
			}
			if got := discoverBridge(t, p, project); got.State != domain.SessionStopped {
				t.Fatalf("poll during stop overwrote Stopped: %+v", got)
			}
			p.cli, p.bridges.runtime = cli, runtime
			if err := startBridge(t, p, ref, project.Path); err != nil {
				t.Fatalf("stopped session cannot be started: %v", err)
			}
		})
	}
}

func TestStopDuringInventoryPreventsLateReattach(t *testing.T) {
	p, cli, runtime, project := bridgeProvider(t)
	created := createBridge(t, p, project)
	p.bridges.runtime = controlledBridge{BridgeRuntime: runtime, snapshot: func(context.Context, string) (sbridge.Snapshot, error) {
		return sbridge.Snapshot{}, sbridge.ErrNotServing
	}}
	entered, release := make(chan struct{}), make(chan struct{})
	p.cli = controlledCLI{CLI: cli, inventory: func(context.Context, string) ([]Container, error) {
		close(entered)
		<-release
		return []Container{{Name: created.ID, Status: "running"}}, nil
	}}
	finished := make(chan error, 1)
	go func() { p.sampleBridgeAndWait(context.Background(), created.ID, nil, true); finished <- nil }()
	<-entered
	stopped := make(chan error, 1)
	go func() {
		stopped <- p.StopSession(context.Background(), provider.SessionRef{ProjectID: project.ID, SessionID: created.ID})
	}()
	select {
	case err := <-stopped:
		if err != nil {
			close(release)
			<-finished
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		close(release)
		<-finished
		<-stopped
		t.Fatal("read-only inventory blocked stop")
	}
	close(release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if len(runtime.starts) != 1 {
		t.Fatal("late inventory response reattached a stopped container")
	}
	if got := discoverBridge(t, p, project); got.State != domain.SessionStopped {
		t.Fatal(got)
	}
}

func TestAbandonedRestartRestoresObservationAndInvalidatesStartingCapture(t *testing.T) {
	p, cli, _, project := bridgeProvider(t)
	created := createBridge(t, p, project)
	ref := provider.SessionRef{ProjectID: project.ID, SessionID: created.ID}
	previous := created
	previous.State, previous.Preview = domain.SessionWaiting, "known permission prompt"
	p.bridges.observed[created.ID] = previous
	var startingRevision uint64
	p.cli = controlledCLI{CLI: cli, stop: func(ctx context.Context, id string) error {
		p.sampleBridgeAndWait(ctx, id, nil, true)
		p.bridges.mu.Lock()
		startingRevision = p.bridges.guard(id).revision
		p.bridges.mu.Unlock()
		return errors.New("cannot stop old container")
	}}
	run, err := p.PrepareRestart(context.Background(), provider.RestartSessionRequest{Session: ref, WorkingDir: project.Path, OptionID: "codex", Fresh: true})
	if err != nil {
		t.Fatal(err)
	}
	if err = run(context.Background()); err == nil {
		t.Fatal("stop failure was hidden")
	}
	got, err := p.DiscoverWatchedSessions(context.Background(), project)
	if err != nil || len(got) != 1 || got[0].Session.State != previous.State || got[0].Session.Preview != previous.Preview {
		t.Fatalf("abandoned restart kept a starting observation: %+v err=%v", got, err)
	}
	if p.bridges.guardFor(created.ID).revision == startingRevision {
		t.Fatal("a late starting observation could overwrite the restored session")
	}
}

func TestStalledPollUsesShortDeadlineWithoutContainerRecovery(t *testing.T) {
	p, cli, runtime, project := bridgeProvider(t)
	createBridge(t, p, project)
	p.bridges.runtime = controlledBridge{BridgeRuntime: runtime, snapshot: func(ctx context.Context, _ string) (sbridge.Snapshot, error) {
		<-ctx.Done()
		return sbridge.Snapshot{}, ctx.Err()
	}}
	done := make(chan error, 1)
	go func() {
		out, err := p.DiscoverSessions(context.Background(), project)
		if err == nil && out[0].Session.State != domain.SessionUnknown {
			err = errors.New("stalled capture was not unknown")
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("poll waited for the five-second wire timeout")
	}
	if cli.allCalls != 0 || len(runtime.starts) != 1 {
		t.Fatal("capture timeout triggered container recovery")
	}
}

// A poll that finds no socket while create is still attaching must not attach a
// second bridge: bridge serve unlinks the live socket, orphaning the first.
func TestPollDuringAttachDoesNotAttachAgain(t *testing.T) {
	p, cli, runtime, project := bridgeProvider(t)
	id := cli.runResult
	attaching, releaseAttach, inventoried := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var starts int
	var startsMu sync.Mutex
	p.bridges.runtime = controlledBridge{BridgeRuntime: runtime, snapshot: func(context.Context, string) (sbridge.Snapshot, error) {
		return sbridge.Snapshot{}, sbridge.ErrNotServing
	}, start: func(context.Context, string, string, []string) error {
		startsMu.Lock()
		starts++
		first := starts == 1
		startsMu.Unlock()
		if first {
			close(attaching)
			<-releaseAttach
		}
		return nil
	}}
	p.cli = controlledCLI{CLI: cli, inventory: func(context.Context, string) ([]Container, error) {
		close(inventoried)
		return []Container{{Name: id, Status: "running"}}, nil
	}}
	created := make(chan error, 1)
	go func() {
		_, err := p.CreateSession(context.Background(), provider.CreateSessionRequest{WorkingDir: project.Path, OptionID: "codex"})
		created <- err
	}()
	<-attaching
	sampled := make(chan error, 1)
	go func() { p.sampleBridgeAndWait(context.Background(), id, nil, true); sampled <- nil }()
	<-inventoried
	close(releaseAttach)
	if err := <-created; err != nil {
		t.Fatal(err)
	}
	if err := <-sampled; err != nil {
		t.Fatal(err)
	}
	if starts != 1 {
		t.Fatalf("bridge starts = %d; want 1", starts)
	}
}

// A restart while another is bringing the container up is refused rather than
// sharing its entry, which on failure made the first withdraw the session.
func TestOverlappingRestartIsRefused(t *testing.T) {
	p, cli, _, project := bridgeProvider(t)
	created := createBridge(t, p, project)
	request := provider.RestartSessionRequest{Session: provider.SessionRef{ProjectID: project.ID, SessionID: created.ID}, WorkingDir: project.Path, OptionID: "codex"}
	running, release := make(chan struct{}), make(chan struct{})
	cli.onRun = func() { close(running); <-release }
	first, err := p.PrepareRestart(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	firstDone := make(chan error, 1)
	go func() { firstDone <- first(context.Background()) }()
	<-running
	second, err := p.PrepareRestart(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := second(context.Background()); !errors.Is(err, provider.ErrSessionStarting) {
		t.Fatalf("overlapping restart = %v; want ErrSessionStarting", err)
	}
	cli.onRun = nil
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if got := discoverBridge(t, p, project); got.State != domain.SessionIdle {
		t.Fatalf("session after the first restart = %+v; want idle", got)
	}
}

// A poll that needs recovery while a stop holds the lifecycle lock must not wait
// for it: Fanout would hold every other session's poll behind that stop.
func TestRecoveryDuringStopDoesNotWaitForIt(t *testing.T) {
	p, cli, runtime, project := bridgeProvider(t)
	created := createBridge(t, p, project)
	ref := provider.SessionRef{ProjectID: project.ID, SessionID: created.ID}
	stopEntered, releaseStop := make(chan struct{}), make(chan struct{})
	p.bridges.runtime = controlledBridge{BridgeRuntime: runtime, snapshot: func(context.Context, string) (sbridge.Snapshot, error) {
		return sbridge.Snapshot{}, sbridge.ErrNotServing
	}}
	p.cli = controlledCLI{CLI: cli, stop: func(context.Context, string) error {
		close(stopEntered)
		<-releaseStop
		return nil
	}, inventory: func(context.Context, string) ([]Container, error) {
		return []Container{{Name: created.ID, Status: "running"}}, nil
	}}
	stopped := make(chan error, 1)
	go func() { stopped <- p.StopSession(context.Background(), ref) }()
	<-stopEntered
	defer func() { close(releaseStop); <-stopped }()
	sampled := make(chan error, 1)
	go func() { p.sampleBridgeAndWait(context.Background(), created.ID, nil, true); sampled <- nil }()
	select {
	case err := <-sampled:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("recovery waited for an in-progress stop")
	}
	if len(runtime.starts) != 1 {
		t.Fatal("recovery during stop reattached the container")
	}
}

// Inventory and reattach share the polling budget; cleanup must not inherit it,
// or a stop cut short after removing the container leaks its gateway.
func TestRecoveryBudgetsInventoryAndReattachButNotCleanup(t *testing.T) {
	for _, status := range []string{"running", "exited"} {
		t.Run(status, func(t *testing.T) {
			p, cli, runtime, project := bridgeProvider(t)
			created := createBridge(t, p, project)
			pastStartup(p, created.ID)
			var inventoryDeadline, effectDeadline time.Time
			var inventoryBudget time.Duration
			p.cli = controlledCLI{CLI: cli, inventory: func(ctx context.Context, _ string) ([]Container, error) {
				inventoryDeadline, _ = ctx.Deadline()
				inventoryBudget = time.Until(inventoryDeadline)
				return []Container{{Name: created.ID, Status: status}}, nil
			}, stop: func(ctx context.Context, _ string) error {
				effectDeadline, _ = ctx.Deadline()
				return context.DeadlineExceeded
			}}
			p.bridges.runtime = controlledBridge{BridgeRuntime: runtime, snapshot: func(context.Context, string) (sbridge.Snapshot, error) {
				return sbridge.Snapshot{}, sbridge.ErrNotServing
			}, start: func(ctx context.Context, _, _ string, _ []string) error {
				effectDeadline, _ = ctx.Deadline()
				return context.DeadlineExceeded
			}}
			p.sampleBridgeAndWait(context.Background(), created.ID, nil, true)
			if inventoryDeadline.IsZero() || inventoryBudget <= 0 || inventoryBudget > recoveryTimeout {
				t.Fatalf("inventory deadline=%v; want one within %v", inventoryDeadline, recoveryTimeout)
			}
			if shared := effectDeadline.Equal(inventoryDeadline); shared != (status == "running") {
				t.Fatalf("%s effect deadline=%v inventory=%v; want shared only for reattach", status, effectDeadline, inventoryDeadline)
			}
			if got := p.projectBridges(project, p.storeProjects())[0].Session; got.State != domain.SessionUnknown {
				t.Fatalf("failed recovery reported as complete: %+v", got)
			}
		})
	}
}

func TestExplicitReadsNeverRecoverMissingBridge(t *testing.T) {
	for _, operation := range []string{"state", "open", "shell", "discover"} {
		t.Run(operation, func(t *testing.T) {
			p, cli, bridge, project := bridgeProvider(t)
			created := createBridge(t, p, project)
			bridge.live = false
			p.term = &fakeLauncher{}
			ref := provider.SessionRef{ProjectID: project.ID, SessionID: created.ID}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch operation {
			case "state":
				got, err := p.SessionState(ctx, ref)
				if err != nil || got.State != domain.SessionUnknown {
					t.Fatalf("state=%+v err=%v", got, err)
				}
			case "open":
				if _, err := p.Open(ctx, ref, "terminal"); err == nil {
					t.Fatal("opened a missing host bridge")
				}
			case "shell":
				_, _ = p.OpenShell(ctx, ref)
			case "discover":
				if _, err := p.DiscoverSessions(ctx, project); err != nil {
					t.Fatal(err)
				}
			}
			cancel()
			if cli.allCalls != 0 || len(cli.stopped) != 0 || len(bridge.starts) != 1 {
				t.Fatal("request-scoped read initiated lifecycle recovery")
			}
			g := p.bridges.guardFor(created.ID)
			if !g.retryAt.IsZero() {
				t.Fatal("explicit read put watch recovery in cooldown")
			}
		})
	}
}

func TestChangeDetectorDropsRemovedProjects(t *testing.T) {
	p, _, _, project := bridgeProvider(t)
	createBridge(t, p, project)
	tick := p.changeDetector()
	if !tick(context.Background()) || tick(context.Background()) {
		t.Fatal("initial sample should change exactly once")
	}
	saved := p.store.Projects()[0]
	if _, err := p.store.RemoveProject(project.ID); err != nil {
		t.Fatal(err)
	}
	tick(context.Background())
	// Re-adding the same project must publish its observations again, even when
	// identical to those from before removal; the old baseline was discarded.
	if err := p.store.AddProject(&saved); err != nil {
		t.Fatal(err)
	}
	if !tick(context.Background()) {
		t.Fatal("removed project's stale baseline suppressed its new observation")
	}
}

func TestRecoveryLeavesEveryWatchTickResponsive(t *testing.T) {
	for _, stage := range []string{"inventory", "cleanup", "attach"} {
		t.Run(stage, func(t *testing.T) {
			p, cli, runtime, project := bridgeProvider(t)
			lost := createBridge(t, p, project)
			pastStartup(p, lost.ID)
			cli.runResult = "enclave-codex-0123456789ab-hs-healthy"
			healthy := createBridge(t, p, project)
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			block := func() { once.Do(func() { close(entered) }); <-release }
			var inventories atomic.Int32
			snapshot := sbridge.Snapshot{Screen: "Ready", OSCTitle: "✳ Codex"}
			p.bridges.runtime = controlledBridge{BridgeRuntime: runtime, snapshot: func(_ context.Context, target string) (sbridge.Snapshot, error) {
				if target == bridgeTarget(lost.ID) {
					return sbridge.Snapshot{}, fmt.Errorf("socket missing: %w", sbridge.ErrNotServing)
				}
				return snapshot, nil
			}, start: func(context.Context, string, string, []string) error {
				if stage == "attach" {
					block()
				}
				return nil
			}}
			p.cli = controlledCLI{CLI: cli, inventory: func(context.Context, string) ([]Container, error) {
				inventories.Add(1)
				if stage == "inventory" {
					block()
				}
				status := "running"
				if stage == "cleanup" {
					status = "exited"
				}
				return []Container{{Name: lost.ID, Status: status}}, nil
			}, stop: func(context.Context, string) error { block(); return nil }}
			defer func() { close(release); p.waitForBridgeSamples() }()
			tick := p.changeDetector()
			poll := func() {
				t.Helper()
				done := make(chan bool, 1)
				go func() { done <- tick(context.Background()) }()
				select {
				case changed := <-done:
					if !changed {
						t.Fatal("healthy transition was not published")
					}
				case <-time.After(time.Second):
					t.Fatal("background recovery blocked a watch tick")
				}
			}
			poll()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("recovery did not reach the blocked stage")
			}
			for _, want := range []domain.SessionState{domain.SessionRunning, domain.SessionIdle, domain.SessionRunning} {
				snapshot.OSCTitle = "⠋ Codex"
				if want == domain.SessionIdle {
					snapshot.OSCTitle = "✳ Codex"
				}
				poll()
				p.bridges.mu.Lock()
				got := p.bridges.observed[healthy.ID].State
				p.bridges.mu.Unlock()
				if got != want {
					t.Fatalf("healthy state=%s; want %s while recovery remains blocked", got, want)
				}
			}
			if got := inventories.Load(); got != 1 {
				t.Fatalf("inventories=%d; want a single in-flight recovery", got)
			}
		})
	}
}

func TestBackgroundRecoveryStopsWithWatcherContext(t *testing.T) {
	p, cli, runtime, project := bridgeProvider(t)
	created := createBridge(t, p, project)
	runtime.live = false
	entered := make(chan struct{})
	p.cli = controlledCLI{CLI: cli, inventory: func(ctx context.Context, _ string) ([]Container, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.sampleBridge(ctx, created.ID, nil, true)
	<-entered
	cancel()
	done := make(chan struct{})
	go func() { p.waitForBridgeSamples(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("watch cancellation left recovery running")
	}
	if len(runtime.starts) != 1 {
		t.Fatal("canceled recovery attached a bridge")
	}
}

func TestPollDuringAttachDoesNotOutliveCreate(t *testing.T) {
	p, _, runtime, project := bridgeProvider(t)
	entered, release := make(chan struct{}), make(chan struct{})
	p.bridges.runtime = controlledBridge{BridgeRuntime: runtime, start: func(ctx context.Context, id, dir string, argv []string) error {
		close(entered)
		<-release
		return runtime.Start(ctx, id, dir, argv)
	}}
	created := make(chan error, 1)
	go func() {
		_, err := p.CreateSession(context.Background(), provider.CreateSessionRequest{WorkingDir: project.Path, OptionID: "codex"})
		created <- err
	}()
	<-entered
	// The record exists but nothing serves its socket until the attach returns.
	p.sampleBridges(context.Background(), p.detectionAgents(), func(Container) bool { return true }, true)
	p.waitForBridgeSamples()
	close(release)
	if err := <-created; err != nil {
		t.Fatal(err)
	}
	out, err := p.DiscoverWatchedSessions(context.Background(), project)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Session.State == domain.SessionUnknown || out[0].Session.Error != "" {
		t.Fatalf("pre-attach poll outlived create: %+v", out)
	}
}
