// Package sbridgetest supplies a wire-level peer for daemon tests.
package sbridgetest

import (
	"bufio"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TempDir keeps socket paths below the Unix sun_path limit, even with long
// Go test names. The directory is still private and removed by the test.
func TempDir(t testing.TB) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "hs-sock-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// Listen serves connections until cleanup. Each handler has a bounded lifetime.
func Listen(t testing.TB, sock string, handle func(net.Conn)) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(sock), 0o700); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
				handle(conn)
			}()
		}
	}()
	t.Cleanup(func() { _ = ln.Close(); wg.Wait() })
}

// Serve verifies the actual dump handshake and counts every snapshot read.
func Serve(t testing.TB, sock string, payload func() string) *atomic.Int64 {
	t.Helper()
	return &Bridge(t, sock, payload).Dumps
}

// Request is a send or stop a fake bridge received.
type Request struct {
	Line  string // the request line, without the newline
	Input string // what a send typed
}

// Fake answers dump, send and stop as a bridge would.
type Fake struct {
	Dumps atomic.Int64

	mu       sync.Mutex
	requests []Request
}

// Requests returns the sends and stops received so far.
func (f *Fake) Requests() []Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Request(nil), f.requests...)
}

// Bridge serves sock like a bridge whose screen is payload.
func Bridge(t testing.TB, sock string, payload func() string) *Fake {
	t.Helper()
	f := &Fake{}
	Listen(t, sock, func(conn net.Conn) {
		r := bufio.NewReader(conn)
		line, err := r.ReadString('\n')
		line = strings.TrimSuffix(line, "\n")
		switch {
		case err != nil:
			t.Errorf("request line = %q, %v", line, err)
		case line == "sbridge-1 dump":
			f.Dumps.Add(1)
			_, _ = io.WriteString(conn, payload())
		case line == "sbridge-1 send":
			input, _ := io.ReadAll(r)
			f.record(Request{Line: line, Input: string(input)})
			_, _ = io.WriteString(conn, "ok\n")
		case strings.HasPrefix(line, "sbridge-1 stop "):
			f.record(Request{Line: line})
			_, _ = io.WriteString(conn, "ok\n")
		default:
			t.Errorf("unexpected request line %q", line)
		}
	})
	return f
}

func (f *Fake) record(req Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, req)
}
