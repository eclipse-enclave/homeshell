package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"syscall"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/daemonstate"
	"github.com/eclipsesource/homeshell/daemon/internal/persist"
)

func init() {
	register(Command{
		Name:    "stop",
		Summary: "Stop the running daemon",
		Run:     runStop,
	})
}

// signalStop and stopExitTimeout are the seams runStop drives, so tests can
// cover the stale-but-alive and wedged-daemon paths without a real process to
// signal. signalStopProcess is defined per-OS.
var (
	signalStop      = signalStopProcess
	stopExitTimeout = 10 * time.Second
)

// runStop discovers the daemon and asks it to exit. A running daemon is sent a
// stop signal (its clean-drain path removes the state file), and so is one whose
// state file looks stale while its process is still alive — that process is what
// holds the port. A stale file whose process is gone is simply cleared so a
// fresh daemon can start; with no daemon there is nothing to do. It waits for a
// signalled daemon to exit before returning.
func runStop(env Env, args []string) int {
	fs := flag.NewFlagSet("stop", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	homeDirFlag := fs.String("home-dir", "", "data directory (must match the running daemon's -home-dir)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *homeDirFlag != "" {
		persist.SetHomeDir(*homeDirFlag)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	st, phase, err := discover(ctx)
	if err != nil {
		fmt.Fprintf(env.Stderr, "homeshell: %v\n", err)
		return 1
	}

	switch phase {
	case daemonNone:
		// Name the file that was checked. "No daemon is running" on its own reads
		// as a fact about the machine, when it is only a fact about this data
		// directory — a daemon started with a different -home-dir is invisible
		// here, and may well be the one holding the port the user cares about.
		fmt.Fprintf(env.Stderr, "homeshell: no daemon is running for %s\n", statePath())
		fmt.Fprintln(env.Stderr, "homeshell: a daemon started with a different -home-dir has its own"+
			" state file; pass that -home-dir to stop it")
		return 1
	case daemonStale:
		// A stale state file does not mean the process is gone: it may be mid
		// startup or wedged, still holding the port. Only clearing the file
		// leaves that process running, so the next `ensure-running` collides
		// with it on the port — and `stop` is what its error tells the user to
		// run. So signal a stale-but-alive daemon just like a responding one,
		// and fall through to the shared signal-and-wait path below.
		if !processAlive(st.PID) {
			if err := daemonstate.Remove(); err != nil {
				fmt.Fprintf(env.Stderr, "homeshell: removing stale state file: %v\n", err)
				return 1
			}
			fmt.Fprintf(env.Stdout, "removed stale state file (pid %d was not running)\n", st.PID)
			return 0
		}
		fmt.Fprintf(env.Stdout, "daemon (pid %d) is not responding; stopping it anyway\n", st.PID)
	}

	// Running: signal the daemon to drain and exit, then wait for it to go.
	if err := signalStop(st.PID); err != nil {
		fmt.Fprintf(env.Stderr, "homeshell: signaling pid %d: %v\n", st.PID, err)
		return 1
	}
	if !waitForExit(st.PID, stopExitTimeout) {
		fmt.Fprintf(env.Stderr, "homeshell: daemon (pid %d) did not exit within the timeout\n", st.PID)
		return 1
	}
	// A daemon that exits without draining leaves its state file behind, so
	// clear a file that still names the daemon we just stopped.
	removeStateIfOwnedBy(st.PID)
	fmt.Fprintf(env.Stdout, "stopped daemon (pid %d)\n", st.PID)
	return 0
}

// removeStateIfOwnedBy deletes the state file when it still records pid. The PID
// check keeps the cleanup from clobbering a file a newer daemon may have written
// after the one we stopped exited.
func removeStateIfOwnedBy(pid int) {
	if st, err := daemonstate.Read(); err != nil || st.PID != pid {
		return
	}
	daemonstate.Remove()
}

// waitForExit polls until the process is gone or the timeout elapses, reporting
// whether it exited.
func waitForExit(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return !processAlive(pid)
}

// signalStopProcess asks the daemon to shut down gracefully. SIGTERM triggers
// the daemon's clean-drain path (in-flight requests finish, event streams close,
// the state file is removed).
func signalStopProcess(pid int) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return proc.Signal(syscall.SIGTERM)
}
