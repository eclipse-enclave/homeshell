package proto

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHandshakeRoundTrip(t *testing.T) {
	tests := []Handshake{
		{Kind: KindDump},
		{Kind: KindAttach, Cols: 190, Rows: 48},
		{Kind: KindAttach, Cols: 0, Rows: 0},
		{Kind: KindResize, Cols: 213, Rows: 58},
		{Kind: KindSend},
		{Kind: KindStop, GraceMs: 5000},
	}
	for _, want := range tests {
		var buf bytes.Buffer
		if err := WriteHandshake(&buf, want); err != nil {
			t.Fatalf("%+v: write: %v", want, err)
		}
		got, err := ReadHandshake(&buf)
		if err != nil {
			t.Fatalf("%+v: read: %v", want, err)
		}
		if got != want {
			t.Errorf("round trip: got %+v want %+v", got, want)
		}
	}
}

func TestHandshakeRejects(t *testing.T) {
	lines := []string{
		"sbridge-0 dump\n",       // wrong version
		"nonsense\n",             // no kind
		"sbridge-1 reboot\n",     // unknown kind
		"sbridge-1 attach 80\n",  // missing rows
		"sbridge-1 attach a b\n", // non-numeric
		"sbridge-1 resize 80\n",  // missing rows
		"sbridge-1 stop\n",       // missing grace
		strings.Repeat("x", 300), // over line cap, never terminated
		"sbridge-1 dump",         // EOF before newline
	}
	for _, line := range lines {
		if _, err := ReadHandshake(strings.NewReader(line)); err == nil {
			t.Errorf("accepted %q", line)
		}
	}
}

func TestResponseLines(t *testing.T) {
	var buf bytes.Buffer
	_ = WriteOK(&buf)
	if err := ReadResponse(&buf); err != nil {
		t.Fatalf("ok response: %v", err)
	}
	buf.Reset()
	_ = WriteErr(&buf, "send failed\nagain")
	err := ReadResponse(&buf)
	if err == nil || !strings.Contains(err.Error(), "send failed again") {
		t.Fatalf("err response: %v", err)
	}
}

func TestListenDial(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "t.sock")
	ln, err := Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	info, err := os.Stat(sock)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("socket perms %v, want 0600", perm)
	}
	done := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err == nil {
			_, err = io.WriteString(conn, "hi")
			_ = conn.Close()
		}
		done <- err
	}()
	conn, err := Dial(sock, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	data, _ := io.ReadAll(conn)
	if string(data) != "hi" || <-done != nil {
		t.Fatalf("read %q", data)
	}
}

func TestListenLongPath(t *testing.T) {
	// Build a path well past sun_path and check the chdir fallback binds it.
	dir := t.TempDir()
	deep := filepath.Join(dir, strings.Repeat("d", 80), strings.Repeat("e", 80))
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(deep, "long.sock")
	if len(sock) <= maxSunPath {
		t.Skipf("path unexpectedly short: %d", len(sock))
	}
	origWd, _ := os.Getwd()
	defer func() { _ = os.Chdir(origWd) }()
	ln, err := Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		if conn, err := ln.Accept(); err == nil {
			_ = conn.Close()
		}
	}()
	conn, err := Dial(sock, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	if _, err := os.Stat(sock); err != nil {
		t.Fatalf("socket not at expected path: %v", err)
	}
}
