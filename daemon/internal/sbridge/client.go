// Package sbridge is HomeShell's session bridge: each session's agent runs on
// a PTY under its own `homeshell bridge serve` process (bridgecli), and the
// daemon reads and drives it over a unix socket with this client. The wire
// protocol is specified in PROTOCOL.md.
package sbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/detect"
	"github.com/eclipsesource/homeshell/daemon/internal/sbridge/proto"
)

// ErrNotServing is returned only when the socket is absent or refuses a
// connection. Other failures are not evidence that an agent can be relaunched.
var ErrNotServing = errors.New("no bridge serving")

// Snapshot is the JSON payload served for a `dump` request.
type Snapshot struct {
	// Screen is the visible screen as plain text, one line per row, trailing
	// whitespace stripped. A wide rune stands for both its cells.
	Screen string `json:"screen"`
	// OSCTitle is the title last set with OSC 0/2, empty once the agent sets
	// an empty one.
	OSCTitle string `json:"osc_title"`
	// OSCProgress is the last OSC 9;4 report as "state[;percent]", or null
	// when none is set or the agent cleared it.
	OSCProgress *string `json:"osc_progress"`
	// Error is set when the emulator failed; the agent keeps running.
	Error string `json:"error,omitempty"`
}

// Exit is the record `bridge serve --exit-file` writes when the agent exits,
// so a caller that finds the socket gone can tell why the session ended.
type Exit struct {
	// Code is the agent's exit status, 128+signal when a signal killed it.
	Code int `json:"code"`
	// Screen is the last rendered screen, as in Snapshot.
	Screen string `json:"screen"`
	// EndedByBridge is true when the bridge ended the agent (a stop or a
	// forwarded signal), false when the agent exited on its own, and nil in a
	// record from a bridge that predates the field.
	EndedByBridge *bool `json:"ended_by_bridge"`
}

// QuitOnItsOwn reports whether the agent ended without the bridge ending it,
// e.g. the user's /quit. A record that does not say counts as no, and so does
// a signal death (code 128+n): an OOM kill or crash is not the user quitting.
func (e Exit) QuitOnItsOwn() bool {
	return e.EndedByBridge != nil && !*e.EndedByBridge && e.Code < 128
}

// ReadExit reads an exit record. An error wrapping fs.ErrNotExist means there
// is none: the agent still runs, or the bridge itself was killed.
func ReadExit(path string) (Exit, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Exit{}, err
	}
	var rec Exit
	if err := json.Unmarshal(data, &rec); err != nil {
		return Exit{}, fmt.Errorf("sbridge exit record %s: %w", path, err)
	}
	return rec, nil
}

// exitLines is how much of the final screen Summary quotes.
const exitLines = 5

// Summary is the exit status and the last non-empty screen lines, for an error
// or a log line.
func (e Exit) Summary() string {
	lines := strings.Split(detect.ScreenTail(e.Screen, exitLines), "\n")
	for i, line := range lines {
		lines[i] = strings.TrimSpace(line)
	}
	return fmt.Sprintf("status %d: %s", e.Code, strings.Join(lines, " / "))
}

// DefaultTimeout is the outer safety limit for a dump. Polling callers supply
// a shorter deadline suited to their refresh interval.
const DefaultTimeout = 5 * time.Second

// SendTimeout matches the bound the bridge puts on a send.
const SendTimeout = 30 * time.Second

// StopTimeout bounds a stop when the caller sets no deadline.
const StopTimeout = 10 * time.Second

// stopReserve is kept back from the stop deadline to see the bridge exit
// after the agent's grace runs out.
const stopReserve = 2 * time.Second

const maxSnapshotBytes = 8 << 20

// Dump reads one snapshot. Cancellation closes an established connection
// too, not just an in-progress dial.
func Dump(ctx context.Context, sock string) (Snapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, DefaultTimeout)
	defer cancel()
	var data []byte
	err := exchange(ctx, "dump", sock, func(conn net.Conn) error {
		if err := proto.WriteHandshake(conn, proto.Handshake{Kind: proto.KindDump}); err != nil {
			return err
		}
		var err error
		data, err = io.ReadAll(io.LimitReader(conn, maxSnapshotBytes+1))
		return err
	})
	if err != nil {
		return Snapshot{}, err
	}
	if len(data) > maxSnapshotBytes {
		return Snapshot{}, fmt.Errorf("sbridge dump %q: snapshot exceeds %d bytes", sock, maxSnapshotBytes)
	}
	// Decode into a pointer to reject null as well as malformed/empty payloads.
	var snap *Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return Snapshot{}, fmt.Errorf("sbridge dump %q: decode: %w", sock, err)
	}
	if snap == nil {
		return Snapshot{}, fmt.Errorf("sbridge dump %q: null snapshot", sock)
	}
	return *snap, nil
}

// Send types input into the session and returns once it reached the PTY. It
// works while a terminal is attached.
func Send(ctx context.Context, sock string, input []byte) error {
	ctx, cancel := context.WithTimeout(ctx, SendTimeout)
	defer cancel()
	return exchange(ctx, "send", sock, func(conn net.Conn) error {
		if err := proto.WriteHandshake(conn, proto.Handshake{Kind: proto.KindSend}); err != nil {
			return err
		}
		if _, err := conn.Write(input); err != nil {
			return err
		}
		// EOF tells the bridge the input is complete; the ack comes back on the
		// still-open read side.
		if uc, ok := conn.(*net.UnixConn); ok {
			if err := uc.CloseWrite(); err != nil {
				return err
			}
		}
		return proto.ReadResponse(conn)
	})
}

// Stop shuts the bridge down: the agent gets SIGTERM, and its process group
// SIGKILL once the grace (the deadline less stopReserve) runs out. A bridge
// that is not serving is already stopped.
func Stop(ctx context.Context, sock string) error {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, StopTimeout)
		defer cancel()
	}
	deadline, _ := ctx.Deadline()
	grace := max(time.Until(deadline)-stopReserve, time.Second)
	err := exchange(ctx, "stop", sock, func(conn net.Conn) error {
		if err := proto.WriteHandshake(conn, proto.Handshake{Kind: proto.KindStop, GraceMs: int(grace.Milliseconds())}); err != nil {
			return err
		}
		if err := proto.ReadResponse(conn); err != nil {
			return err
		}
		// The bridge holds the connection until it exits; EOF is the ack.
		_, err := io.Copy(io.Discard, conn)
		return err
	})
	if errors.Is(err, ErrNotServing) {
		return nil
	}
	return err
}

// exchange runs one request on its own connection, bounded by ctx.
func exchange(ctx context.Context, verb, sock string, run func(net.Conn) error) error {
	conn, err := proto.DialContext(ctx, sock)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED) {
			err = fmt.Errorf("%w: %w", ErrNotServing, err)
		}
		return fmt.Errorf("sbridge %s %q: %w", verb, sock, err)
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	deadline, _ := ctx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		return err
	}
	err = run(conn)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		// The socket deadline can fire just before the context timer's goroutine.
		if !time.Now().Before(deadline) {
			return context.DeadlineExceeded
		}
		return fmt.Errorf("sbridge %s %q: %w", verb, sock, err)
	}
	return nil
}
