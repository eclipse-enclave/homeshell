// Package procutil holds the process-handling detail the desktop hand-offs share
// (internal/terminal, internal/opener, and the "open with" command runner in
// internal/provider), which hand interaction off rather than embed it.
//
// All three do the same thing: launch a program that talks to the user, and
// return without waiting for it. That is the right shape — a terminal window
// lives as long as the user keeps it open, so waiting would block a request for
// minutes — but it leaves the child to be reaped, which is what this package is
// for.
package procutil

import "os/exec"

// StartAndReap starts cmd and returns as soon as it is running, having arranged
// for the child to be reaped when it exits.
//
// The reaping is the point. A hand-off returns immediately by design, so nothing
// on the request path can call cmd.Wait() — and on Unix a child whose parent
// never waits stays a zombie, holding its PID, until the parent dies. The daemon
// is the parent and it runs for days, so `Start()` alone leaks one zombie per
// terminal, editor, and file the user ever opens. A goroutine parked in Wait() costs
// nothing and reaps each child exactly once.
//
// The wait error is deliberately dropped: the child's exit status says nothing
// useful about a hand-off. A terminal emulator exits non-zero when the user
// closes the window, and by then the request it belonged to is long answered.
func StartAndReap(cmd *exec.Cmd) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
