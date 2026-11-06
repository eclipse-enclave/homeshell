// Package proto implements the sbridge socket protocol (PROTOCOL.md): a
// one-line text handshake selecting the request kind, followed by a raw
// stream. The handshake is a versioned ASCII line and nothing on the wire is
// ever framed, so a whole session can be driven with `socat - UNIX:<sock>`.
//
//	sbridge-1 dump\n                 -> one JSON snapshot, then close
//	sbridge-1 attach <cols> <rows>\n -> "ok\n", then the attach stream
//	sbridge-1 resize <cols> <rows>\n -> "ok\n", size applied, then close
//	sbridge-1 send\n                 -> raw input until client half-close,
//	                                    then "ok\n" once it reached the PTY
//	sbridge-1 stop <grace_ms>\n      -> "ok\n", server shuts down (EOF)
//
// The attach stream is raw PTY bytes both ways: output down, keystrokes up;
// connection close means session over (server side) or detach (client
// side). Out-of-band events are their own short-lived connections instead
// of in-band frames — resize is a one-shot request the attach client sends
// on SIGWINCH, send injects input without occupying the attach slot.
package proto

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Version starts every request line. PROTOCOL.md says when it changes.
const Version = "sbridge-1"

// Handshake kinds.
const (
	KindDump   = "dump"
	KindAttach = "attach"
	KindResize = "resize"
	KindSend   = "send"
	KindStop   = "stop"
)

// maxLineLen bounds the handshake/response line, terminator included.
const maxLineLen = 256

// Handshake is a parsed request line.
type Handshake struct {
	Kind       string
	Cols, Rows int // attach/resize: terminal size (attach 0,0 = no terminal)
	GraceMs    int // stop: SIGTERM→SIGKILL grace for the agent
}

// String renders the wire line without the trailing newline.
func (h Handshake) String() string {
	switch h.Kind {
	case KindAttach, KindResize:
		return fmt.Sprintf("%s %s %d %d", Version, h.Kind, h.Cols, h.Rows)
	case KindStop:
		return fmt.Sprintf("%s %s %d", Version, KindStop, h.GraceMs)
	default:
		return Version + " " + h.Kind
	}
}

// WriteHandshake sends the request line.
func WriteHandshake(w io.Writer, h Handshake) error {
	_, err := io.WriteString(w, h.String()+"\n")
	return err
}

// ReadHandshake parses a request line. The caller bounds it with a
// connection deadline.
func ReadHandshake(r io.Reader) (Handshake, error) {
	line, err := readLine(r)
	if err != nil {
		return Handshake{}, err
	}
	fields := strings.Fields(line)
	if len(fields) < 2 || fields[0] != Version {
		return Handshake{}, fmt.Errorf("unsupported handshake %q", line)
	}
	h := Handshake{Kind: fields[1]}
	switch h.Kind {
	case KindDump, KindSend:
		return h, nil
	case KindAttach, KindResize:
		if len(fields) != 4 {
			return Handshake{}, fmt.Errorf("malformed %s handshake %q", h.Kind, line)
		}
		if h.Cols, err = strconv.Atoi(fields[2]); err != nil {
			return Handshake{}, fmt.Errorf("malformed %s cols %q", h.Kind, fields[2])
		}
		if h.Rows, err = strconv.Atoi(fields[3]); err != nil {
			return Handshake{}, fmt.Errorf("malformed %s rows %q", h.Kind, fields[3])
		}
		return h, nil
	case KindStop:
		if len(fields) != 3 {
			return Handshake{}, fmt.Errorf("malformed stop handshake %q", line)
		}
		if h.GraceMs, err = strconv.Atoi(fields[2]); err != nil {
			return Handshake{}, fmt.Errorf("malformed stop grace %q", fields[2])
		}
		return h, nil
	default:
		return Handshake{}, fmt.Errorf("unknown request kind %q", h.Kind)
	}
}

// WriteOK / WriteErr send the response line that precedes any stream.
func WriteOK(w io.Writer) error {
	_, err := io.WriteString(w, "ok\n")
	return err
}

func WriteErr(w io.Writer, msg string) error {
	msg = strings.ReplaceAll(msg, "\n", " ")
	_, err := io.WriteString(w, "err "+msg+"\n")
	return err
}

// ReadResponse consumes the server's response line and returns an error
// carrying the server's message when it is not "ok".
func ReadResponse(r io.Reader) error {
	line, err := readLine(r)
	if err != nil {
		return err
	}
	if line == "ok" {
		return nil
	}
	if msg, found := strings.CutPrefix(line, "err "); found {
		return errors.New(msg)
	}
	return fmt.Errorf("unexpected response %q", line)
}

// readLine reads a \n-terminated line byte-by-byte (the stream switches to
// raw bytes right after the line, so no read-ahead is allowed).
func readLine(r io.Reader) (string, error) {
	var line []byte
	buf := make([]byte, 1)
	for len(line) < maxLineLen {
		if _, err := io.ReadFull(r, buf); err != nil {
			return "", err
		}
		if buf[0] == '\n' {
			return strings.TrimSuffix(string(line), "\r"), nil
		}
		line = append(line, buf[0])
	}
	return "", fmt.Errorf("line exceeds %d bytes", maxLineLen)
}
