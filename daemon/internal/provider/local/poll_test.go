package local

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/detect"
	"github.com/eclipsesource/homeshell/daemon/internal/domain"
	"github.com/eclipsesource/homeshell/daemon/internal/provider"
	"github.com/eclipsesource/homeshell/daemon/internal/settings"
)

type probingRuntime struct {
	*fakeRuntime
	probes     int
	probeError error
	delay      time.Duration
}

func (r *probingRuntime) State(ctx context.Context, agent, target string) (detect.Result, error) {
	r.mu.Lock()
	r.probes++
	r.mu.Unlock()
	if r.delay > 0 {
		timer := time.NewTimer(r.delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return detect.Result{}, ctx.Err()
		case <-timer.C:
		}
	}
	if r.probeError != nil {
		return detect.Result{}, r.probeError
	}
	return r.fakeRuntime.State(ctx, agent, target)
}

func TestWatcherProjectsOneObservation(t *testing.T) {
	rt := &probingRuntime{fakeRuntime: newFakeRuntime()}
	p := newForTest(t, rt, &fakeStore{})
	createSession(t, p, "proj1", testDir)
	last := map[string]sessionMark{}
	for tick := 1; tick <= 3; tick++ {
		rt.state[rt.starts[0].target] = detect.Result{State: domain.SessionRunning, Title: "live", Tail: "output"}
		p.detectChanged(context.Background(), last)
		for repeat := 0; repeat < 2; repeat++ {
			sessions, err := p.DiscoverWatchedSessions(context.Background(), projectRef())
			if err != nil || len(sessions) != 1 || sessions[0].Session.Title != "live" || sessions[0].Session.Preview != "output" {
				t.Fatalf("sessions=%+v error=%v", sessions, err)
			}
		}
		if rt.probes != tick {
			t.Fatalf("reads=%d after tick %d", rt.probes, tick)
		}
	}
}

func TestProbeFailureDoesNotResumeLiveAgent(t *testing.T) {
	for _, err := range []error{context.DeadlineExceeded, errors.New("capture broken"), errors.New("invalid JSON"), context.Canceled} {
		rt := &probingRuntime{fakeRuntime: newFakeRuntime(), probeError: err}
		p := newForTest(t, rt, &fakeStore{})
		createSession(t, p, "proj1", testDir)
		sessions, _ := p.DiscoverSessions(context.Background(), projectRef())
		if len(rt.starts) != 1 || sessions[0].Session.State != domain.SessionUnknown {
			t.Fatalf("error=%v starts=%d sessions=%+v", err, len(rt.starts), sessions)
		}
	}
}

func TestLocalIdleHoldKeepsLiveObservations(t *testing.T) {
	rt := newFakeRuntime()
	p := newForTest(t, rt, &fakeStore{})
	createSession(t, p, "proj1", testDir)
	target := rt.starts[0].target
	now := time.Unix(100, 0)
	p.now = func() time.Time { return now }
	read := func(result detect.Result, want domain.SessionState) {
		t.Helper()
		rt.state[target] = result
		sessions, _ := p.DiscoverSessions(context.Background(), projectRef())
		if sessions[0].Session.State != want {
			t.Fatalf("state=%s want %s", sessions[0].Session.State, want)
		}
	}
	read(detect.Result{State: domain.SessionRunning}, domain.SessionRunning)
	read(detect.Result{State: domain.SessionIdle, Title: "new title", Tail: "new output"}, domain.SessionRunning)
	observed, _ := p.DiscoverWatchedSessions(context.Background(), projectRef())
	if observed[0].Session.Title != "new title" || observed[0].Session.Preview != "new output" {
		t.Fatal(observed)
	}
	now = now.Add(200 * time.Millisecond)
	read(detect.Result{State: domain.SessionIdle}, domain.SessionRunning)
	read(detect.Result{State: domain.SessionRunning}, domain.SessionRunning)
	now = now.Add(time.Second)
	read(detect.Result{State: domain.SessionIdle}, domain.SessionRunning) // new pending period
	now = now.Add(300 * time.Millisecond)
	read(detect.Result{State: domain.SessionIdle}, domain.SessionIdle)
	read(detect.Result{State: domain.SessionRunning}, domain.SessionRunning)
	read(detect.Result{State: domain.SessionIdle, VisibleIdle: true}, domain.SessionIdle)
}

func TestStoppedSessionRejectsInFlightCapture(t *testing.T) {
	rt := newFakeRuntime()
	p := newForTest(t, rt, &fakeStore{})
	ref := createSession(t, p, "proj1", testDir)
	p.mu.Lock()
	ws, sess := p.findSessionLocked(ref.ProjectID, ref.SessionID)
	snap := snapshotSession(sess, ws)
	p.mu.Unlock()
	rt.state[snap.target] = detect.Result{State: domain.SessionRunning, Title: "stale"}
	capture := p.captureSession(context.Background(), snap, p.indexAgents())
	if err := p.StopSession(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	p.applyCapturesLocked(map[string]sessionCapture{snap.id: capture})
	p.mu.Unlock()
	sessions, _ := p.DiscoverWatchedSessions(context.Background(), projectRef())
	if sessions[0].Session.State != domain.SessionStopped || sessions[0].Session.Title == "stale" {
		t.Fatal(sessions)
	}
	if err := p.StartSession(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	sessions, _ = p.DiscoverWatchedSessions(context.Background(), projectRef())
	if sessions[0].Session.State != domain.SessionRunning {
		t.Fatal("restart reused a stopped observation")
	}
}

func TestOverlappingObservationPreservesResumeSideEffects(t *testing.T) {
	rt := &locatingRuntime{fakeRuntime: newFakeRuntime(), location: "first.sock"}
	p := newForTest(t, rt, &fakeStore{})
	ref := createSession(t, p, "proj1", testDir)
	p.mu.Lock()
	ws, s := p.findSessionLocked(ref.ProjectID, ref.SessionID)
	old := snapshotSession(s, ws)
	p.mu.Unlock()
	rt.alive[old.target] = false
	rt.location = "resumed.sock"
	resumed := p.captureSession(context.Background(), old, p.indexAgents())
	if !resumed.markRelaunched {
		t.Fatal("test did not resume the session")
	}
	// Both captures began from the same metadata. The second saw the resumed
	// process alive, and publishes its newer screen before the resume returns.
	observed := p.captureSession(context.Background(), old, p.indexAgents())
	observed.sess.Preview = "newer screen"
	p.mu.Lock()
	p.applyCapturesLocked(map[string]sessionCapture{old.id: observed})
	p.applyCapturesLocked(map[string]sessionCapture{old.id: resumed})
	if !s.relaunched || s.sock != "resumed.sock" || s.observed.Preview != "newer screen" {
		t.Fatalf("lost resume effects or accepted stale screen: %+v", s)
	}
	p.mu.Unlock()
	rt.alive[old.target] = false
	sessions, err := p.DiscoverSessions(context.Background(), projectRef())
	if err != nil || sessions[0].Session.State != domain.SessionStopped || len(rt.starts) != 2 {
		t.Fatalf("failed resume looped: sessions=%+v starts=%d err=%v", sessions, len(rt.starts), err)
	}
}

func TestOverlappingObservationPreservesResumeFailure(t *testing.T) {
	rt := newFakeRuntime()
	p := newForTest(t, rt, &fakeStore{})
	ref := createSession(t, p, "proj1", testDir)
	p.mu.Lock()
	ws, s := p.findSessionLocked(ref.ProjectID, ref.SessionID)
	snap := snapshotSession(s, ws)
	p.mu.Unlock()
	newer := p.captureSession(context.Background(), snap, p.indexAgents())
	rt.alive[snap.target] = false
	rt.startErr = errors.New("cannot resume")
	failed := p.captureSession(context.Background(), snap, p.indexAgents())
	p.mu.Lock()
	defer p.mu.Unlock()
	p.applyCapturesLocked(map[string]sessionCapture{snap.id: newer})
	p.applyCapturesLocked(map[string]sessionCapture{snap.id: failed})
	if !s.resumeFailed {
		t.Fatal("a newer observation discarded a failed resume")
	}
}

type stalledProbeRuntime struct{ *fakeRuntime }

func (r stalledProbeRuntime) State(ctx context.Context, agent, target string) (detect.Result, error) {
	<-ctx.Done()
	return detect.Result{}, ctx.Err()
}

func TestStalledPollUsesShortDeadlineWithoutResumingAgent(t *testing.T) {
	rt := stalledProbeRuntime{newFakeRuntime()}
	p := newForTest(t, rt, &fakeStore{})
	createSession(t, p, "proj1", testDir)
	done := make(chan error, 1)
	go func() {
		sessions, err := p.DiscoverSessions(context.Background(), projectRef())
		if err == nil && sessions[0].Session.State != domain.SessionUnknown {
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
	if len(rt.starts) != 1 {
		t.Fatal("timeout was treated as evidence permitting resume")
	}
}

func TestFastPollingAllowsSlowerCapturesWithoutLosingIdleHold(t *testing.T) {
	rt := &probingRuntime{fakeRuntime: newFakeRuntime(), delay: 150 * time.Millisecond}
	p := newForTest(t, rt, &fakeStore{})
	st, err := settings.Load(filepath.Join(t.TempDir(), "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	next := settings.Defaults()
	next.Providers.RefreshIntervalSec = 0.1
	if _, err := st.Set(next); err != nil {
		t.Fatal(err)
	}
	p.SetSettings(st)
	createSession(t, p, "proj1", testDir)
	target := rt.starts[0].target
	now := time.Unix(100, 0)
	p.now = func() time.Time { return now }
	for _, step := range []struct {
		after          time.Duration
		observed, want domain.SessionState
	}{
		{0, domain.SessionRunning, domain.SessionRunning},
		{100 * time.Millisecond, domain.SessionIdle, domain.SessionRunning},
		{300 * time.Millisecond, domain.SessionIdle, domain.SessionIdle},
	} {
		now = now.Add(step.after)
		rt.state[target] = detect.Result{State: step.observed, Confident: true}
		sessions, err := p.DiscoverSessions(context.Background(), projectRef())
		if err != nil || len(sessions) != 1 || sessions[0].Session.State != step.want {
			t.Fatalf("slow capture lost idle hold: %+v want %s err=%v", sessions, step.want, err)
		}
	}
	if len(rt.starts) != 1 {
		t.Fatal("slow healthy capture caused an agent resume")
	}
}

// One failed read mid-turn reports unknown but does not cancel the idle hold:
// the next idle-looking frame is still held as running.
func TestFailedCaptureKeepsIdleHold(t *testing.T) {
	rt := &probingRuntime{fakeRuntime: newFakeRuntime()}
	p := newForTest(t, rt, &fakeStore{})
	createSession(t, p, "proj1", testDir)
	target := rt.starts[0].target
	now := time.Unix(100, 0)
	p.now = func() time.Time { return now }
	read := func(want domain.SessionState) {
		t.Helper()
		sessions, _ := p.DiscoverSessions(context.Background(), projectRef())
		if sessions[0].Session.State != want {
			t.Fatalf("state=%s want %s", sessions[0].Session.State, want)
		}
	}
	rt.state[target] = detect.Result{State: domain.SessionRunning}
	read(domain.SessionRunning)
	rt.probeError = context.DeadlineExceeded
	read(domain.SessionUnknown)
	rt.probeError = nil
	rt.state[target] = detect.Result{State: domain.SessionIdle}
	read(domain.SessionRunning)
	now = now.Add(300 * time.Millisecond)
	read(domain.SessionIdle)
}

// barrierRuntime answers only once every session's read is in flight, so a
// poll that reads sessions one at a time times each of them out.
type barrierRuntime struct {
	*fakeRuntime
	want    int32
	arrived atomic.Int32
	all     chan struct{}
}

func (r *barrierRuntime) State(ctx context.Context, agent, target string) (detect.Result, error) {
	if r.arrived.Add(1) == r.want {
		close(r.all)
	}
	select {
	case <-r.all:
		return detect.Result{State: domain.SessionRunning}, nil
	case <-ctx.Done():
		return detect.Result{}, ctx.Err()
	}
}

func TestPollReadsSessionsInParallel(t *testing.T) {
	rt := &barrierRuntime{fakeRuntime: newFakeRuntime(), want: 3, all: make(chan struct{})}
	p := newForTest(t, rt, &fakeStore{})
	for range 3 {
		createSession(t, p, "proj1", testDir)
	}
	sessions, err := p.DiscoverSessions(context.Background(), projectRef())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range sessions {
		if s.Session.State != domain.SessionRunning {
			t.Fatalf("session %s=%s; a serial poll timed it out", s.Session.ID, s.Session.State)
		}
	}
}

func TestStaleCaptureDoesNotRelaunchStoppedOrRemovedSession(t *testing.T) {
	for _, tc := range []struct {
		name   string
		action func(*Provider, provider.SessionRef) error
	}{
		{"stop", func(p *Provider, ref provider.SessionRef) error { return p.StopSession(context.Background(), ref) }},
		{"remove", func(p *Provider, ref provider.SessionRef) error { return p.RemoveSession(context.Background(), ref) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt := newFakeRuntime()
			p := newForTest(t, rt, &fakeStore{})
			ref := createSession(t, p, "proj1", testDir)
			p.mu.Lock()
			ws, s := p.findSessionLocked(ref.ProjectID, ref.SessionID)
			snap := snapshotSession(s, ws)
			p.mu.Unlock()
			// The capture sampled the session before the action and reaches the relaunch after it.
			if err := tc.action(p, ref); err != nil {
				t.Fatal(err)
			}
			p.captureSession(context.Background(), snap, p.indexAgents())
			rt.mu.Lock()
			defer rt.mu.Unlock()
			if len(rt.starts) != 1 || rt.alive[snap.target] {
				t.Fatalf("stale capture relaunched the agent: starts=%d alive=%v", len(rt.starts), rt.alive[snap.target])
			}
		})
	}
}

// The relaunch runs without p.mu, so a stop or removal can land while it
// launches; the agent it missed must not keep running. A start that follows
// the stop adopts the relaunched agent instead.
func TestRelaunchSupersededWhileLaunchingIsStoppedUnlessAdopted(t *testing.T) {
	ctx := context.Background()
	for _, tt := range []struct {
		name      string
		action    func(*Provider, provider.SessionRef, string, chan<- error)
		wantAlive bool
	}{
		{"stop", func(p *Provider, ref provider.SessionRef, _ string, done chan<- error) {
			done <- p.StopSession(ctx, ref)
		}, false},
		{"remove", func(p *Provider, ref provider.SessionRef, _ string, done chan<- error) {
			done <- p.RemoveSession(ctx, ref)
		}, false},
		{"stop then start", func(p *Provider, ref provider.SessionRef, target string, done chan<- error) {
			if err := p.StopSession(ctx, ref); err != nil {
				done <- err
				return
			}
			go func() { done <- p.StartSession(ctx, ref) }()
			// Return once the start, holding p.mu, waits on this launch.
			for {
				p.launchMu.Lock()
				waiting := p.launching[target] != nil && p.launching[target].refs == 2
				p.launchMu.Unlock()
				if waiting {
					return
				}
				time.Sleep(time.Millisecond)
			}
		}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rt := newFakeRuntime()
			p := newForTest(t, rt, &fakeStore{})
			ref := createSession(t, p, "proj1", testDir)
			p.mu.Lock()
			ws, s := p.findSessionLocked(ref.ProjectID, ref.SessionID)
			snap := snapshotSession(s, ws)
			p.mu.Unlock()
			rt.mu.Lock()
			delete(rt.alive, snap.target)
			rt.mu.Unlock()
			var once sync.Once
			done := make(chan error, 1)
			rt.startHook = func(string) { once.Do(func() { tt.action(p, ref, snap.target, done) }) }
			capture := p.captureSession(ctx, snap, p.indexAgents())
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if capture.sess.State != domain.SessionUnknown || capture.markRelaunched {
				t.Fatalf("superseded relaunch was reported as current: %+v", capture)
			}
			rt.mu.Lock()
			defer rt.mu.Unlock()
			if len(rt.starts) != 2 || rt.alive[snap.target] != tt.wantAlive {
				t.Fatalf("starts=%d alive=%v; want alive=%v", len(rt.starts), rt.alive[snap.target], tt.wantAlive)
			}
		})
	}
}

// A poll relaunching one lost session must not hold up actions on the others.
func TestRelaunchDoesNotBlockOtherSessions(t *testing.T) {
	rt := newFakeRuntime()
	p := newForTest(t, rt, &fakeStore{})
	lost := createSession(t, p, "proj1", testDir)
	healthy := createSession(t, p, "proj1", testDir)
	p.mu.Lock()
	ws, s := p.findSessionLocked(lost.ProjectID, lost.SessionID)
	snap := snapshotSession(s, ws)
	p.mu.Unlock()
	rt.mu.Lock()
	delete(rt.alive, snap.target)
	rt.mu.Unlock()
	entered, release := make(chan struct{}), make(chan struct{})
	rt.startHook = func(target string) {
		if target == snap.target {
			close(entered)
			<-release
		}
	}
	captured := make(chan sessionCapture, 1)
	go func() { captured <- p.captureSession(context.Background(), snap, p.indexAgents()) }()
	<-entered
	prompted := make(chan error, 1)
	go func() { prompted <- p.Prompt(context.Background(), healthy, "still responsive") }()
	select {
	case err := <-prompted:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("a relaunch blocked another session's prompt")
	}
	close(release)
	if capture := <-captured; !capture.markRelaunched {
		t.Fatalf("relaunch was not reported: %+v", capture)
	}
}

// A bridge that times out is unknown, not dead: Open must not start a second agent beside it.
func TestOpenDoesNotResumeAWedgedBridge(t *testing.T) {
	rt := newFakeRuntime()
	p := newForTest(t, rt, &fakeStore{})
	p.term = &fakeTerminal{}
	ref := createSession(t, p, "proj1", testDir)
	rt.wedged[rt.starts[0].target] = true
	if _, err := p.Open(context.Background(), ref, "terminal"); err != nil {
		t.Fatal(err)
	}
	if len(rt.starts) != 1 {
		t.Fatalf("Open started %d agents beside a wedged bridge", len(rt.starts)-1)
	}
}

// startOnce re-probes after waiting for a concurrent start; a timeout there must not launch either.
func TestStartOnceRefusesBesideAWedgedBridge(t *testing.T) {
	rt := newFakeRuntime()
	p := newForTest(t, rt, &fakeStore{})
	rt.wedged["hs-wedged"] = true
	if err := p.startOnce(context.Background(), "hs-wedged", testDir, []string{"claude"}); !errors.Is(err, errBridgeUnanswered) {
		t.Fatalf("startOnce error = %v, want errBridgeUnanswered", err)
	}
	if len(rt.starts) != 0 {
		t.Fatalf("starts = %v", rt.starts)
	}
}

func TestFailedReadKeepsThePreview(t *testing.T) {
	rt := &probingRuntime{fakeRuntime: newFakeRuntime()}
	p := newForTest(t, rt, &fakeStore{})
	createSession(t, p, "proj1", testDir)
	rt.state[rt.starts[0].target] = detect.Result{State: domain.SessionRunning, Tail: "output"}
	if _, err := p.DiscoverSessions(context.Background(), projectRef()); err != nil {
		t.Fatal(err)
	}
	rt.probeError = context.DeadlineExceeded
	sessions, _ := p.DiscoverSessions(context.Background(), projectRef())
	if got := sessions[0].Session; got.State != domain.SessionUnknown || got.Preview != "output" {
		t.Fatalf("after a failed read: state=%s preview=%q", got.State, got.Preview)
	}
}
