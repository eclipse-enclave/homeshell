package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/daemonstate"
	"github.com/eclipsesource/homeshell/daemon/internal/persist"
)

func init() {
	register(Command{
		Name:    "status",
		Summary: "Report whether the daemon is running and where",
		Run:     runStatus,
	})
}

// runStatus reports the discovered daemon. It exits 0 when a live daemon is
// found and 1 otherwise (no daemon, a stale state file, or a discovery fault),
// so scripts can branch on the exit code.
func runStatus(env Env, args []string) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
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
	case daemonRunning:
		writeRunning(env.Stdout, st)
		return 0
	case daemonStale:
		fmt.Fprintf(env.Stdout, "not running (stale state file for pid %d at %s)\n", st.PID, statePath())
		return 1
	default:
		fmt.Fprintln(env.Stdout, "not running")
		return 1
	}
}

// writeRunning prints the human-readable description of a running daemon,
// including the discovery state file and the persisted state's path, to w. It is shared by
// `status` and by `serve` at start-up so both report the daemon identically.
func writeRunning(w io.Writer, st daemonstate.State) {
	fmt.Fprintf(w, "running\n  address:    %s\n  pid:        %d\n  version:    %s\n  started at: %s\n  daemon:     %s\n",
		st.Address, st.PID, st.Version, st.StartedAt.Format(time.RFC3339), statePath())
	if st.StatePath != "" {
		fmt.Fprintf(w, "  state:      %s\n", st.StatePath)
	}
}

// statePath is the state file location for messages, or a placeholder if it
// cannot be resolved (never fatal to a status report).
func statePath() string {
	p, err := daemonstate.Path()
	if err != nil {
		return "the state file"
	}
	return p
}
