package updater

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeUpdater is an in-memory Updater for exercising the orchestration without
// touching the network or the filesystem.
type fakeUpdater struct {
	latest    Release
	latestErr error

	latestCalls int
	applyErr    error
	appliedWith Release
	applyCalls  int
}

func (f *fakeUpdater) Latest(context.Context) (Release, error) {
	f.latestCalls++
	return f.latest, f.latestErr
}

func (f *fakeUpdater) Apply(_ context.Context, rel Release, _ string) error {
	f.applyCalls++
	f.appliedWith = rel
	return f.applyErr
}

// withStubs replaces the process-level restart and executable lookup so Apply
// is observable without spawning or exiting. It returns a pointer to a flag set
// when the restart is invoked, and restores the originals via t.Cleanup.
func withStubs(t *testing.T) *bool {
	t.Helper()
	origRestart := restartFunc
	origExe := executablePath
	restarted := false
	restartFunc = func(string, []string, []string) error {
		restarted = true
		return nil // do not os.Exit in tests
	}
	executablePath = func() (string, error) { return "/tmp/homeshell", nil }
	t.Cleanup(func() {
		restartFunc = origRestart
		executablePath = origExe
	})
	return &restarted
}

func TestServiceCheckUpdateAvailable(t *testing.T) {
	svc := NewService(&fakeUpdater{latest: Release{Version: "1.2.0"}}, "1.1.0")
	status, err := svc.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !status.UpdateAvailable {
		t.Error("expected UpdateAvailable = true")
	}
	if status.Current != "1.1.0" || status.Latest != "1.2.0" {
		t.Errorf("status = %+v", status)
	}
}

func TestServiceCheckUpToDate(t *testing.T) {
	svc := NewService(&fakeUpdater{latest: Release{Version: "1.1.0"}}, "1.1.0")
	status, err := svc.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.UpdateAvailable {
		t.Error("expected UpdateAvailable = false")
	}
}

func TestServiceCheckNoReleaseIsNotFatal(t *testing.T) {
	svc := NewService(&fakeUpdater{latestErr: ErrNoRelease}, "1.1.0")
	status, err := svc.Check(context.Background())
	if err != nil {
		t.Fatalf("ErrNoRelease should not surface as an error: %v", err)
	}
	if status.UpdateAvailable || status.Latest != "1.1.0" {
		t.Errorf("status = %+v, want current as latest and no update", status)
	}
}

func TestServiceApplyUpdatesAndRestarts(t *testing.T) {
	restarted := withStubs(t)
	fake := &fakeUpdater{latest: Release{Version: "2.0.0", rel: nil}}
	// rel is nil here but the fake's Apply ignores it; that's fine for the
	// orchestration test.
	svc := NewService(fake, "1.0.0")

	var readyVersion string
	version, updated, err := svc.Apply(context.Background(), func(v string) {
		readyVersion = v
	})
	if err != nil {
		t.Fatal(err)
	}
	if !updated || version != "2.0.0" {
		t.Errorf("Apply = (%q, %v), want (2.0.0, true)", version, updated)
	}
	if fake.applyCalls != 1 {
		t.Errorf("Apply called %d times, want 1", fake.applyCalls)
	}
	if readyVersion != "2.0.0" {
		t.Errorf("onReady version = %q, want 2.0.0", readyVersion)
	}
	if !*restarted {
		t.Error("expected restart to be invoked")
	}
}

func TestServiceApplyUpToDateDoesNothing(t *testing.T) {
	restarted := withStubs(t)
	fake := &fakeUpdater{latest: Release{Version: "1.0.0"}}
	svc := NewService(fake, "1.0.0")

	readyCalled := false
	_, updated, err := svc.Apply(context.Background(), func(string) { readyCalled = true })
	if err != nil {
		t.Fatal(err)
	}
	if updated {
		t.Error("expected updated = false")
	}
	if fake.applyCalls != 0 {
		t.Error("Apply must not download when up to date")
	}
	if readyCalled || *restarted {
		t.Error("no onReady/restart when up to date")
	}
}

func TestServiceApplyVerificationFailureLeavesBinary(t *testing.T) {
	restarted := withStubs(t)
	fake := &fakeUpdater{
		latest:   Release{Version: "2.0.0"},
		applyErr: errors.New("checksum mismatch"),
	}
	svc := NewService(fake, "1.0.0")

	_, updated, err := svc.Apply(context.Background(), nil)
	if err == nil {
		t.Fatal("expected an error on verification failure")
	}
	if updated {
		t.Error("updated must be false on failure")
	}
	if *restarted {
		t.Error("must not restart when the swap failed")
	}
}

// withClock makes now() controllable so cache-expiry can be tested without
// sleeping. It restores the real clock via t.Cleanup.
func withClock(t *testing.T) *time.Time {
	t.Helper()
	orig := now
	clock := time.Now()
	now = func() time.Time { return clock }
	t.Cleanup(func() { now = orig })
	return &clock
}

func TestServiceCheckCachesWithinTTL(t *testing.T) {
	withClock(t)
	fake := &fakeUpdater{latest: Release{Version: "2.0.0"}}
	svc := NewService(fake, "1.0.0")

	if _, err := svc.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fake.latestCalls != 1 {
		t.Errorf("Latest called %d times within TTL, want 1", fake.latestCalls)
	}
}

func TestServiceCheckRefreshesAfterTTL(t *testing.T) {
	clock := withClock(t)
	fake := &fakeUpdater{latest: Release{Version: "2.0.0"}}
	svc := NewService(fake, "1.0.0")

	if _, err := svc.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	*clock = clock.Add(checkTTL + time.Second)
	if _, err := svc.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fake.latestCalls != 2 {
		t.Errorf("Latest called %d times after TTL, want 2", fake.latestCalls)
	}
}

func TestServiceCheckCachesErrorBriefly(t *testing.T) {
	clock := withClock(t)
	fake := &fakeUpdater{latestErr: errors.New("github unreachable")}
	svc := NewService(fake, "1.0.0")

	if _, err := svc.Check(context.Background()); err == nil {
		t.Fatal("expected error")
	}
	// Within the (shorter) error TTL the failure is served from cache.
	if _, err := svc.Check(context.Background()); err == nil {
		t.Fatal("expected cached error")
	}
	if fake.latestCalls != 1 {
		t.Errorf("Latest called %d times within error TTL, want 1", fake.latestCalls)
	}
	// After the error TTL the source is retried.
	*clock = clock.Add(checkErrorTTL + time.Second)
	if _, err := svc.Check(context.Background()); err == nil {
		t.Fatal("expected error")
	}
	if fake.latestCalls != 2 {
		t.Errorf("Latest called %d times after error TTL, want 2", fake.latestCalls)
	}
}

func TestServiceApplyInvalidatesCache(t *testing.T) {
	withClock(t)
	withStubs(t)
	fake := &fakeUpdater{latest: Release{Version: "2.0.0"}}
	svc := NewService(fake, "1.0.0")

	if _, err := svc.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Apply(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	// The post-Apply Check must hit the source again, not the stale cache.
	if _, err := svc.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	// 1 (initial Check) + 1 (Apply's own Latest) + 1 (post-Apply Check).
	if fake.latestCalls != 3 {
		t.Errorf("Latest called %d times, want 3 (cache invalidated by Apply)", fake.latestCalls)
	}
}
