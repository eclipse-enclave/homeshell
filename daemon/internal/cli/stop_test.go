package cli

import (
	"errors"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/daemonstate"
)

// writeStaleState records a daemon that is not reachable and whose PID is dead.
func writeStaleState(t *testing.T) {
	t.Helper()
	if err := daemonstate.Write(daemonstate.State{Address: "127.0.0.1:1", PID: 999999999}); err != nil {
		t.Fatal(err)
	}
}

func TestStopWithNoDaemon(t *testing.T) {
	isolateStateFile(t)

	code, _, stderr := runVerb(runStop, nil)
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr, "no daemon") {
		t.Errorf("stderr = %q, want it to report no daemon is running", stderr)
	}
	// "No daemon is running" alone reads as a fact about the machine; it is only
	// a fact about this data directory. A user chasing a daemon started under a
	// different -home-dir needs both the path checked and the reason it missed.
	if !strings.Contains(stderr, statePath()) {
		t.Errorf("stderr = %q, want it to name the state file it checked (%s)", stderr, statePath())
	}
	if !strings.Contains(stderr, "-home-dir") {
		t.Errorf("stderr = %q, want it to point at -home-dir", stderr)
	}
}

func TestRemoveStateIfOwnedBy(t *testing.T) {
	t.Run("removes the file when it still names the stopped pid", func(t *testing.T) {
		isolateStateFile(t)
		if err := daemonstate.Write(daemonstate.State{Address: "127.0.0.1:1", PID: 4242}); err != nil {
			t.Fatal(err)
		}
		removeStateIfOwnedBy(4242)
		if _, err := daemonstate.Read(); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("state file still present after cleanup: %v", err)
		}
	})

	t.Run("keeps a file a newer daemon wrote", func(t *testing.T) {
		isolateStateFile(t)
		if err := daemonstate.Write(daemonstate.State{Address: "127.0.0.1:2", PID: 5555}); err != nil {
			t.Fatal(err)
		}
		removeStateIfOwnedBy(4242) // the pid we stopped, not the one now recorded
		if _, err := daemonstate.Read(); err != nil {
			t.Errorf("cleanup removed a state file owned by a different pid: %v", err)
		}
	})

	t.Run("no-op when the file is already gone", func(t *testing.T) {
		isolateStateFile(t)
		removeStateIfOwnedBy(4242) // the daemon already removed it; must not error
	})
}

func TestStopRemovesStaleStateFile(t *testing.T) {
	isolateStateFile(t)
	writeStaleState(t)

	code, stdout, _ := runVerb(runStop, nil)
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if !strings.Contains(stdout, "stale") {
		t.Errorf("stdout = %q, want it to mention the stale state file", stdout)
	}
	if _, err := daemonstate.Read(); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("stale state file still present after stop: %v", err)
	}
}

// `ensure-running` refuses to respawn while a stale-but-alive daemon holds the
// port and tells the user to run `homeshell stop`. Only clearing the state file
// here would leave that process running, making the advice a dead end.
func TestStopSignalsAStaleButAliveDaemon(t *testing.T) {
	isolateStateFile(t)
	if err := daemonstate.Write(daemonstate.State{Address: "127.0.0.1:1", PID: 4242}); err != nil {
		t.Fatal(err)
	}
	// Discovery says stale while the process is still up; it goes on the signal.
	alive := true
	origAlive := processAlive
	t.Cleanup(func() { processAlive = origAlive })
	processAlive = func(int) bool { return alive }

	var signalled []int
	origSignal := signalStop
	t.Cleanup(func() { signalStop = origSignal })
	signalStop = func(pid int) error {
		signalled = append(signalled, pid)
		alive = false
		return nil
	}

	code, stdout, stderr := runVerb(runStop, nil)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
	}
	if len(signalled) != 1 || signalled[0] != 4242 {
		t.Errorf("signalled = %v, want the recorded pid 4242 to be signalled exactly once", signalled)
	}
	if !strings.Contains(stdout, "not responding") {
		t.Errorf("stdout = %q, want it to say the daemon was not responding", stdout)
	}
	if _, err := daemonstate.Read(); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("state file still present after stopping the daemon it named: %v", err)
	}
}

func TestStopReportsAStaleDaemonThatIgnoresTheSignal(t *testing.T) {
	isolateStateFile(t)
	if err := daemonstate.Write(daemonstate.State{Address: "127.0.0.1:1", PID: 4242}); err != nil {
		t.Fatal(err)
	}
	stubProcessAlive(t, true) // wedged: never exits, not even on the signal

	origSignal := signalStop
	t.Cleanup(func() { signalStop = origSignal })
	signalStop = func(int) error { return nil }
	origTimeout := stopExitTimeout
	t.Cleanup(func() { stopExitTimeout = origTimeout })
	stopExitTimeout = 50 * time.Millisecond

	code, _, stderr := runVerb(runStop, nil)
	if code != 1 {
		t.Errorf("exit code = %d, want 1 for a daemon that would not exit", code)
	}
	if !strings.Contains(stderr, "did not exit") {
		t.Errorf("stderr = %q, want it to report the daemon did not exit", stderr)
	}
	// The process still holds the port, so the record of it must survive: removing
	// it would make the next discovery report "no daemon" while one is still up.
	if _, err := daemonstate.Read(); err != nil {
		t.Errorf("state file removed for a daemon that is still alive: %v", err)
	}
}
