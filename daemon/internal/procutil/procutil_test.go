package procutil

import (
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// The whole reason this helper exists: after the child exits, it must have been
// reaped. An unreaped child stays a zombie holding its PID, and a daemon that
// launches terminals for days accumulates them.
//
// Reaping is observed rather than asserted directly — there is no portable "is
// this PID a zombie" call. Signal 0 to a reaped PID fails (the entry is gone);
// to a zombie it succeeds (the entry is still there, awaiting its parent). So the
// test polls until the signal fails.
func TestStartAndReapReapsTheChild(t *testing.T) {
	cmd := exec.Command("true")
	if err := StartAndReap(cmd); err != nil {
		t.Fatalf("StartAndReap: %v", err)
	}
	pid := cmd.Process.Pid

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err != nil {
			return // the PID is gone, so the child was reaped
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("pid %d still in the process table; the child was never reaped", pid)
}

// StartAndReap must not wait for the child: a terminal window lives as long as
// the user keeps it open, so a hand-off that waited would block the request that
// asked for it.
func TestStartAndReapDoesNotWaitForTheChild(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	start := time.Now()
	if err := StartAndReap(cmd); err != nil {
		t.Fatalf("StartAndReap: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("StartAndReap blocked for %s, want an immediate return", elapsed)
	}
}

// A program that is not there is reported to the caller rather than swallowed by
// the goroutine: the user asked for a terminal and needs to be told there is not
// one.
func TestStartAndReapReportsAFailedStart(t *testing.T) {
	err := StartAndReap(exec.Command("homeshell-no-such-binary-exists"))
	if err == nil {
		t.Fatal("StartAndReap() = nil for a missing binary, want the start error")
	}
}
