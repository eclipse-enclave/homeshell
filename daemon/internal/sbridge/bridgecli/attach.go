package bridgecli

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/sbridge/proto"

	"golang.org/x/term"
)

// dialTimeout bounds the attach connect.
const dialTimeout = 3 * time.Second

// cmdAttach connects the current terminal to a bridge: raw mode,
// keystrokes stream up and PTY output streams down, both unframed; resizes
// go as one-shot side connections. Attaching takes the session over from a
// previously attached terminal (last attach wins, as `tmux attach -d`). It
// ends when the stream closes — the agent exited, the bridge was stopped, or
// a newer client took over; the stream is frameless, so the causes are
// indistinguishable here (a takeover is announced by a server-injected
// farewell line just before EOF) and the trailer stays cause-agnostic.
// Attach exits 0 in all cases. Detaching = closing the terminal (or the
// connection).
func cmdAttach(args []string) int {
	fs := flag.NewFlagSet("attach", flag.ExitOnError)
	sock := fs.String("sock", "", "unix socket of the running bridge (required)")
	_ = fs.Parse(args)
	if *sock == "" {
		fmt.Fprintln(os.Stderr, "usage: homeshell bridge attach --sock PATH")
		return 2
	}

	conn, err := proto.Dial(*sock, dialTimeout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sbridge: attach: %v\n", err)
		return 1
	}
	defer func() { _ = conn.Close() }()

	// 0x0 tells the server "no terminal": it will not adopt a size.
	cols, rows := 0, 0
	isTTY := term.IsTerminal(stdinFd())
	if isTTY {
		if w, h, err := term.GetSize(stdinFd()); err == nil {
			cols, rows = w, h
		}
	}
	if err := proto.WriteHandshake(conn, proto.Handshake{Kind: proto.KindAttach, Cols: cols, Rows: rows}); err != nil {
		fmt.Fprintf(os.Stderr, "sbridge: attach: %v\n", err)
		return 1
	}
	if err := proto.ReadResponse(conn); err != nil {
		fmt.Fprintf(os.Stderr, "sbridge: attach: %v\n", err)
		return 1
	}

	restore := func() {}
	if isTTY {
		if old, err := term.MakeRaw(stdinFd()); err == nil {
			var once sync.Once
			restore = func() { once.Do(func() { _ = term.Restore(stdinFd(), old) }) }
		}
	}
	defer restore()

	// Resize: one short-lived connection per SIGWINCH (resizes are
	// out-of-band requests, not part of the attach stream).
	if isTTY {
		winch := make(chan os.Signal, 1)
		signal.Notify(winch, syscall.SIGWINCH)
		go func() {
			for range winch {
				if w, h, err := term.GetSize(stdinFd()); err == nil {
					sendResize(*sock, w, h)
				}
			}
		}()
	}

	// Input: terminal → bridge, raw. Stdin EOF (piped callers) stops input
	// but keeps the output stream alive (no half-close).
	go func() { _, _ = io.Copy(conn, os.Stdin) }()

	// Output: bridge → terminal, raw, until the stream ends.
	out := &bgWatcher{w: os.Stdout}
	_, _ = io.Copy(out, conn)
	if out.set {
		// The agent's own reset, if any, went to the bridge's PTY, not here.
		_, _ = os.Stdout.WriteString("\x1b]111\x07")
	}
	restore()
	fmt.Fprintln(os.Stderr, "\n[sbridge] detached")
	return 0
}

// sendResize performs a one-shot resize request; failures are ignored (the
// attach stream is the session, a lost resize only delays a repaint until
// the next SIGWINCH).
func sendResize(sock string, cols, rows int) {
	conn, err := proto.Dial(sock, dialTimeout)
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(dialTimeout))
	if proto.WriteHandshake(conn, proto.Handshake{Kind: proto.KindResize, Cols: cols, Rows: rows}) == nil {
		_ = proto.ReadResponse(conn)
	}
}

// bgWatcher passes output through and notes whether it set the terminal's
// background colour (OSC 11 with a colour, not a query).
type bgWatcher struct {
	w    io.Writer
	set  bool
	tail []byte // end of the last write, for a sequence split across writes
}

var oscSetBG = []byte("\x1b]11;")

func (b *bgWatcher) Write(p []byte) (int, error) {
	if !b.set {
		// Only the seam between writes needs joining; p is searched in place.
		seam := append(b.tail, p[:min(len(p), len(oscSetBG))]...)
		b.set = setsBG(seam) || setsBG(p)
		if len(p) >= len(oscSetBG) {
			seam = p
		}
		b.tail = append(b.tail[:0], seam[max(0, len(seam)-len(oscSetBG)):]...)
	}
	return b.w.Write(p)
}

// setsBG reports whether p holds an OSC 11 that sets a colour.
func setsBG(p []byte) bool {
	for i := 0; ; {
		j := bytes.Index(p[i:], oscSetBG)
		if j < 0 {
			return false
		}
		i += j + len(oscSetBG)
		if i < len(p) && p[i] != '?' {
			return true
		}
	}
}

// stdinFd returns stdin's descriptor for the terminal helpers.
func stdinFd() int {
	return int(os.Stdin.Fd()) // #nosec G115 -- file descriptor from Fd() fits in int on all supported platforms.
}
