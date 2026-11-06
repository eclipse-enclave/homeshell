package cli

import (
	"context"
	"encoding/json"
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
		Name:    "ensure-running",
		Summary: "Ensure a daemon is running and print its URL (attach or spawn)",
		Run:     runEnsureRunning,
	})
}

// discoverPhase and spawnDaemon are the seams ensureRunning drives. They are
// package vars so tests can substitute fakes instead of touching a real daemon
// or forking a process. spawnDetachedServe is defined per-OS.
var (
	discoverPhase = discover
	spawnDaemon   = spawnDetachedServe
	processAlive  = daemonstate.ProcessAlive
)

// ensureResult is the JSON payload printed by `ensure-running --json`.
type ensureResult struct {
	URL     string `json:"url"`
	Spawned bool   `json:"spawned"`
}

// runEnsureRunning is the CLI entry point. It attaches to a live daemon or
// spawns one, then prints the loopback URL a browser or webview should load.
func runEnsureRunning(env Env, args []string) int {
	fs := flag.NewFlagSet("ensure-running", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	homeDirFlag := fs.String("home-dir", "", "data directory (must match the running daemon's -home-dir)")
	jsonFlag := fs.Bool("json", false, "print the result as JSON")
	timeoutFlag := fs.Duration("timeout", 15*time.Second, "how long to wait for a spawned daemon to become ready")
	addrFlag := fs.String("addr", "", "listen address for a spawned daemon (host:port); empty uses the serve default")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *homeDirFlag != "" {
		persist.SetHomeDir(*homeDirFlag)
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeoutFlag)
	defer cancel()

	st, spawned, err := ensureRunning(ctx, *homeDirFlag, *addrFlag)
	if err != nil {
		fmt.Fprintf(env.Stderr, "homeshell: %v\n", err)
		return 1
	}
	writeEnsureResult(env.Stdout, ensureResult{URL: daemonURL(st), Spawned: spawned}, *jsonFlag)
	return 0
}

// ensureRunning attaches to a discovered live daemon, else clears a stale state
// file, spawns a daemon, and waits until it answers. The bool reports whether
// this call spawned the daemon. addr, if non-empty, is forwarded to a spawned
// daemon's -addr flag (ignored when attaching to an already-running one).
func ensureRunning(ctx context.Context, homeDir, addr string) (daemonstate.State, bool, error) {
	st, phase, err := discoverPhase(ctx)
	if err != nil {
		return daemonstate.State{}, false, err
	}
	if phase == daemonRunning {
		return st, false, nil
	}
	if phase == daemonStale {
		if processAlive(st.PID) {
			return daemonstate.State{}, false, fmt.Errorf(
				"a daemon (pid %d) holds %s but is not responding; run `homeshell stop` and retry",
				st.PID, st.Address)
		}
		if err := daemonstate.Remove(); err != nil {
			return daemonstate.State{}, false, fmt.Errorf("removing stale state file: %w", err)
		}
	}
	child, err := spawnDaemon(homeDir, addr)
	if err != nil {
		return daemonstate.State{}, false, fmt.Errorf("spawning daemon: %w", err)
	}
	st, err = waitForRunning(ctx, child)
	if err != nil {
		return daemonstate.State{}, false, err
	}
	return st, true, nil
}

// waitForRunning polls the discovery seam until a reachable daemon is found, the
// spawned daemon dies, or the context deadline elapses. Watching the child means
// a daemon that refuses to start (a held port, most often) reports its own error
// straight away instead of hiding behind the full -timeout.
func waitForRunning(ctx context.Context, child *spawnedDaemon) (daemonstate.State, error) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if st, phase, err := discoverPhase(ctx); err == nil && phase == daemonRunning {
			return st, nil
		}
		select {
		case <-ctx.Done():
			return daemonstate.State{}, fmt.Errorf("daemon did not become ready: %w%s", ctx.Err(), child.logHint())
		case <-child.died():
			return daemonstate.State{}, fmt.Errorf("daemon exited before it was ready: %s%s",
				child.exitReason(), child.logHint())
		case <-ticker.C:
		}
	}
}

// daemonURL builds the loopback URL the UI is served from. state.Address is
// host:port (e.g. 127.0.0.1:8420).
func daemonURL(state daemonstate.State) string {
	return "http://" + state.Address + "/"
}

// writeEnsureResult prints the result as pretty JSON or as a bare URL line.
func writeEnsureResult(writer io.Writer, result ensureResult, asJSON bool) {
	if asJSON {
		enc := json.NewEncoder(writer)
		enc.SetIndent("", "  ")
		_ = enc.Encode(result)
		return
	}
	fmt.Fprintln(writer, result.URL)
}
