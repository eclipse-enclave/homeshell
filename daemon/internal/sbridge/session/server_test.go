package session

import (
	"bytes"
	"io"
	"net"
	"syscall"
	"testing"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/sbridge/proto"
)

// startTestSession runs `cat` on a PTY: it stays alive and quiet until the
// test terminates it, so broadcasts are fully under test control.
func startTestSession(t *testing.T) *Session {
	t.Helper()
	sess, err := Start(Options{Argv: []string{"cat"}, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	t.Cleanup(func() {
		sess.KillGroup()
		sess.Wait()
		sess.ClosePTY()
	})
	return sess
}

// attachPipe connects an attach client to srv over an in-memory pipe and
// completes the handshake. Size 0x0 (no terminal) keeps adoptSize inert.
func attachPipe(t *testing.T, srv *Server) net.Conn {
	t.Helper()
	client, server := net.Pipe()
	go srv.handle(server)
	if err := proto.WriteHandshake(client, proto.Handshake{Kind: proto.KindAttach}); err != nil {
		t.Fatalf("attach handshake: %v", err)
	}
	if err := proto.ReadResponse(client); err != nil {
		t.Fatalf("attach response: %v", err)
	}
	return client
}

// readUntil drains conn until want appears in the stream or the deadline
// passes.
func readUntil(t *testing.T, conn net.Conn, want []byte) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var got []byte
	buf := make([]byte, 4096)
	for !bytes.Contains(got, want) {
		n, err := conn.Read(buf)
		got = append(got, buf[:n]...)
		if err != nil {
			t.Fatalf("stream ended before %q arrived (err %v, got %q)", want, err, got)
		}
	}
}

func TestAttachTakeover(t *testing.T) {
	srv := NewServer(startTestSession(t))

	first := attachPipe(t, srv)
	firstOut := make(chan []byte, 1)
	go func() {
		out, _ := io.ReadAll(first)
		firstOut <- out
	}()

	// handleAttach swaps the slot and finishes the old client before it
	// answers, so once the second handshake completes the first client's
	// stream is over.
	second := attachPipe(t, srv)
	defer second.Close()

	select {
	case out := <-firstOut:
		if !bytes.Contains(out, []byte(takeoverFarewell)) {
			t.Errorf("replaced client got %q, want the takeover farewell", out)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("replaced client's stream never closed")
	}

	// The slot now belongs to the new client: broadcasts reach it.
	srv.Broadcast([]byte("after-takeover"))
	readUntil(t, second, []byte("after-takeover"))
}

func TestBroadcastKicksStalledClient(t *testing.T) {
	srv := NewServer(startTestSession(t))

	client := attachPipe(t, srv)
	defer client.Close()

	// The client never reads, so the writer goroutine stalls on the repaint
	// and the queue fills; overflowing it must kick the client instead of
	// ever blocking the broadcaster.
	for i := 0; i < attachQueueLen+2; i++ {
		srv.Broadcast([]byte("x"))
	}

	srv.mu.Lock()
	kicked := srv.client == nil
	srv.mu.Unlock()
	if !kicked {
		t.Fatal("attach slot still occupied after queue overflow")
	}

	// The kicked client's connection is closed.
	_ = client.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadAll(client); err != nil {
		t.Fatalf("kicked client's connection not closed cleanly: %v", err)
	}
}

func TestFinishAttachFlushesPendingOutput(t *testing.T) {
	srv := NewServer(startTestSession(t))

	client := attachPipe(t, srv)
	out := make(chan []byte, 1)
	go func() {
		b, _ := io.ReadAll(client)
		out <- b
	}()

	srv.Broadcast([]byte("final-output"))
	srv.FinishAttach(5 * time.Second)

	select {
	case got := <-out:
		if !bytes.Contains(got, []byte("final-output")) {
			t.Errorf("shutdown stream %q missing pending output", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stream never closed after FinishAttach")
	}
}

func TestWaitMirrorsExit(t *testing.T) {
	t.Run("exit code", func(t *testing.T) {
		sess, err := Start(Options{Argv: []string{"sh", "-c", "exit 3"}, Cols: 80, Rows: 24})
		if err != nil {
			t.Fatalf("start session: %v", err)
		}
		defer sess.ClosePTY()
		if code := sess.Wait(); code != 3 {
			t.Errorf("Wait() = %d, want 3", code)
		}
	})
	t.Run("signal", func(t *testing.T) {
		sess, err := Start(Options{Argv: []string{"sleep", "30"}, Cols: 80, Rows: 24})
		if err != nil {
			t.Fatalf("start session: %v", err)
		}
		defer sess.ClosePTY()
		sess.Signal(syscall.SIGTERM)
		if code := sess.Wait(); code != 128+int(syscall.SIGTERM) {
			t.Errorf("Wait() = %d, want %d", code, 128+int(syscall.SIGTERM))
		}
	})
}
