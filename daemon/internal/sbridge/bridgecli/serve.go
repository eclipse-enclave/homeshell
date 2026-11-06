package bridgecli

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/sbridge"
	"github.com/eclipsesource/homeshell/daemon/internal/sbridge/session"
)

// attachDrainTimeout bounds delivery of the final output to an attached
// client on shutdown.
const attachDrainTimeout = 2 * time.Second

// cmdServe runs the agent headless in the foreground: no stdio passthrough,
// terminals attach and detach through the socket. The daemon spawns it in a
// new session (Setsid) and polls the socket. A stale socket left by a SIGKILLed bridge is
// replaced, a live one is refused, and a socket deleted while serving is
// recreated (socket.go).
func cmdServe(args []string) int {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	sock := fs.String("sock", "", "unix socket to serve on (required)")
	dir := fs.String("dir", "", "agent working directory (default: current)")
	size := fs.String("size", "80x24", "initial PTY size as COLSxROWS")
	termName := fs.String("term", "xterm-256color", "TERM for the agent (empty = inherit)")
	exitFile := fs.String("exit-file", "", "write the agent's exit code and final screen here as JSON when it exits")
	_ = fs.Parse(args)
	if fs.NArg() == 0 || *sock == "" {
		fmt.Fprintln(os.Stderr, "usage: homeshell bridge serve --sock PATH [--dir DIR] [--size COLSxROWS] [--term NAME] [--exit-file PATH] -- CMD [ARG...]")
		return 2
	}
	argv := fs.Args()

	cols, rows, err := parseSize(*size)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sbridge: %v\n", err)
		return 2
	}
	// Resolve paths before proto.Listen's long-path fallback can chdir.
	for _, p := range []*string{sock, dir, exitFile} {
		if *p != "" {
			if abs, err := filepath.Abs(*p); err == nil {
				*p = abs
			}
		}
	}
	if *dir == "" {
		*dir, _ = os.Getwd()
	}

	keeper, err := listenSocket(*sock)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sbridge: serve: listen: %v\n", err)
		return 1
	}
	defer keeper.close()
	if *exitFile != "" {
		_ = os.Remove(*exitFile) // a previous run's record must not describe this one
	}

	sess, err := session.Start(session.Options{
		Argv: argv, Dir: *dir, Env: envWithTERM(*termName), Cols: cols, Rows: rows,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "sbridge: serve: start agent: %v\n", err)
		return 1
	}
	defer sess.ClosePTY()

	sess.ForwardSignals()
	srv := session.NewServer(sess)
	keeper.serve(srv, sess.Done())

	// Capture first: an attach's repaint snapshot must never lag the stream
	// it is about to join.
	readDone := make(chan struct{})
	go func() {
		sess.ReadLoop(srv.Broadcast)
		close(readDone)
	}()

	code := sess.Wait()
	<-readDone // the last output is queued before the stream ends
	if *exitFile != "" {
		// Before the socket goes: a caller that finds it gone reads this next.
		ended := sess.EndedByBridge()
		rec := sbridge.Exit{Code: code, Screen: sess.Capture().Snapshot().Screen, EndedByBridge: &ended}
		if err := writeExit(*exitFile, rec); err != nil {
			fmt.Fprintf(os.Stderr, "sbridge: serve: exit file: %v\n", err)
		}
	}
	srv.FinishAttach(attachDrainTimeout)
	return code
}

// writeExit records the agent's exit atomically, so a reader never sees a
// partial file.
func writeExit(path string, rec sbridge.Exit) error {
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// parseSize parses "COLSxROWS".
func parseSize(s string) (cols, rows int, err error) {
	c, r, found := strings.Cut(s, "x")
	if found {
		cols, err = strconv.Atoi(c)
		if err == nil {
			rows, err = strconv.Atoi(r)
		}
	}
	if !found || err != nil || cols <= 0 || rows <= 0 {
		return 0, 0, fmt.Errorf("invalid --size %q (want COLSxROWS, e.g. 120x40)", s)
	}
	return cols, rows, nil
}

// envWithTERM returns the inherited environment with TERM forced to name.
// A daemon parent may have no TERM at all; TUI agents need one.
func envWithTERM(name string) []string {
	if name == "" {
		return nil // inherit as-is
	}
	env := os.Environ()
	out := env[:0]
	for _, kv := range env {
		if !strings.HasPrefix(kv, "TERM=") {
			out = append(out, kv)
		}
	}
	return append(out, "TERM="+name)
}
