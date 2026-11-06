package updater

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// fakeUpdater is an in-memory Updater for exercising the orchestration without
// touching the network or the filesystem.
type fakeUpdater struct {
	status      Status
	checkErr    error
	unsupported error
	installErr  error

	checkCalls   int
	installCalls int
	installed    Status
}

func (f *fakeUpdater) Supported() error { return f.unsupported }

func (f *fakeUpdater) Check(context.Context) (Status, error) {
	f.checkCalls++
	return f.status, f.checkErr
}

func (f *fakeUpdater) Install(_ context.Context, status Status, _ string) error {
	f.installCalls++
	f.installed = status
	return f.installErr
}

func available(current, latest string) Status {
	return Status{Channel: ChannelStable, Current: current, Latest: latest, UpdateAvailable: current != latest, build: latest}
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

func TestServiceCheckReportsTheUpdatersStatus(t *testing.T) {
	want := available("1.1.0", "1.2.0")
	svc := NewService(&fakeUpdater{status: want})
	status, err := svc.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status != want {
		t.Errorf("status = %+v, want %+v", status, want)
	}
}

func TestServiceApplyUpdatesAndRestarts(t *testing.T) {
	restarted := withStubs(t)
	fake := &fakeUpdater{status: available("1.0.0", "2.0.0")}
	svc := NewService(fake)

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
	if fake.installCalls != 1 || fake.installed.build != "2.0.0" {
		t.Errorf("Install called %d times with %+v, want once with 2.0.0", fake.installCalls, fake.installed)
	}
	if readyVersion != "2.0.0" {
		t.Errorf("onReady version = %q, want 2.0.0", readyVersion)
	}
	if !*restarted {
		t.Error("expected restart to be invoked")
	}
}

func TestServiceApplyWithoutInstalling(t *testing.T) {
	for _, tc := range []struct {
		name    string
		fake    *fakeUpdater
		wantErr error
	}{
		{name: "up to date", fake: &fakeUpdater{status: available("1.0.0", "1.0.0")}},
		{name: "unsupported build", fake: &fakeUpdater{unsupported: fmt.Errorf("%w: source build", ErrUnsupported)}, wantErr: ErrUnsupported},
		{name: "check fails", fake: &fakeUpdater{checkErr: errors.New("offline")}},
		{name: "verification fails", fake: &fakeUpdater{status: available("1.0.0", "2.0.0"), installErr: errors.New("checksum mismatch")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			restarted := withStubs(t)
			readyCalled := false
			_, updated, err := NewService(tc.fake).Apply(context.Background(), func(string) { readyCalled = true })
			wantFailure := tc.fake.unsupported != nil || tc.fake.checkErr != nil || tc.fake.installErr != nil
			if (err != nil) != wantFailure {
				t.Fatalf("Apply error = %v, want failure %v", err, wantFailure)
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("Apply error = %v, want %v", err, tc.wantErr)
			}
			if updated || readyCalled || *restarted {
				t.Errorf("updated=%v ready=%v restarted=%v, want none", updated, readyCalled, *restarted)
			}
			if tc.fake.installErr == nil && tc.fake.installCalls != 0 {
				t.Error("Install must not run without an available update")
			}
		})
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
	fake := &fakeUpdater{status: available("1.0.0", "2.0.0")}
	svc := NewService(fake)

	for range 2 {
		if _, err := svc.Check(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if fake.checkCalls != 1 {
		t.Errorf("Check called %d times within TTL, want 1", fake.checkCalls)
	}
}

func TestServiceCheckRefreshesAfterTTL(t *testing.T) {
	clock := withClock(t)
	fake := &fakeUpdater{status: available("1.0.0", "2.0.0")}
	svc := NewService(fake)

	if _, err := svc.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	*clock = clock.Add(checkTTL + time.Second)
	if _, err := svc.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fake.checkCalls != 2 {
		t.Errorf("Check called %d times after TTL, want 2", fake.checkCalls)
	}
}

func TestServiceCheckCachesErrorBriefly(t *testing.T) {
	clock := withClock(t)
	fake := &fakeUpdater{checkErr: errors.New("download server unreachable")}
	svc := NewService(fake)

	if _, err := svc.Check(context.Background()); err == nil {
		t.Fatal("expected error")
	}
	// Within the (shorter) error TTL the failure is served from cache.
	if _, err := svc.Check(context.Background()); err == nil {
		t.Fatal("expected cached error")
	}
	if fake.checkCalls != 1 {
		t.Errorf("Check called %d times within error TTL, want 1", fake.checkCalls)
	}
	// After the error TTL the source is retried.
	*clock = clock.Add(checkErrorTTL + time.Second)
	if _, err := svc.Check(context.Background()); err == nil {
		t.Fatal("expected error")
	}
	if fake.checkCalls != 2 {
		t.Errorf("Check called %d times after error TTL, want 2", fake.checkCalls)
	}
}

func TestServiceApplyInvalidatesCache(t *testing.T) {
	withClock(t)
	withStubs(t)
	fake := &fakeUpdater{status: available("1.0.0", "2.0.0")}
	svc := NewService(fake)

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
	// 1 (initial Check) + 1 (Apply's own Check) + 1 (post-Apply Check).
	if fake.checkCalls != 3 {
		t.Errorf("Check called %d times, want 3 (cache invalidated by Apply)", fake.checkCalls)
	}
}
