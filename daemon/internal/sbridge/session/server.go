package session

import (
	"io"
	"net"
	"sync"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/sbridge/proto"
)

// handshakeTimeout bounds the request line and a dump response so a wedged
// client cannot pin a connection handler.
const handshakeTimeout = 3 * time.Second

// sendTimeout bounds a whole send request (stream + ack). Sends are
// programmatic one-shots; the generous bound only guards against a wedged
// sender pinning a handler.
const sendTimeout = 30 * time.Second

// sizeJiggleDelay separates the two resizes that force a repaint-SIGWINCH
// when an attaching client already matches the PTY size.
const sizeJiggleDelay = 20 * time.Millisecond

// Attach backpressure bounds. The PTY read loop must never block on the
// attach client: output goes through a bounded chunk queue drained by a
// writer goroutine; a client whose queue overflows or whose writes stall is
// kicked (connection closed, slot freed).
const (
	attachQueueLen      = 256
	attachWriteDeadline = 30 * time.Second
)

// takeoverDrainTimeout bounds delivery of the farewell line to a client
// being replaced by a new attach.
const takeoverDrainTimeout = time.Second

// takeoverFarewell is injected into a replaced client's output stream before
// its connection closes — the stream is frameless, so this line is the only
// way its terminal learns the session itself is still running. Raw mode
// needs the explicit \r.
const takeoverFarewell = "\r\n[sbridge] taken over by a new client\r\n"

// Server answers dump/attach/stop connections for one Session. At most one
// attach client is connected at a time; a new attach takes the slot over and
// the previous client is detached (last attach wins, as `tmux attach -d`).
type Server struct {
	sess     *Session
	stopOnce sync.Once

	mu     sync.Mutex
	client *attachClient // the single attach slot; nil when free
}

// NewServer wires a Server to sess, and has sess answer the agent's terminal
// queries while no terminal is attached.
func NewServer(sess *Session) *Server {
	s := &Server{sess: sess}
	sess.AnswerQueriesWhen(s.noTerminalAttached)
	return s
}

// noTerminalAttached reports whether nobody would answer the agent's queries:
// no client is attached, or the attached one has no terminal (it reported
// 0x0, e.g. a piped attach).
func (s *Server) noTerminalAttached() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.client == nil || !s.client.terminal
}

// Serve answers connections on ln. It returns immediately; the accept loop
// ends when ln is closed.
func (s *Server) Serve(ln net.Listener) {
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go s.handle(conn)
		}
	}()
}

func (s *Server) handle(conn net.Conn) {
	_ = conn.SetDeadline(time.Now().Add(handshakeTimeout))
	h, err := proto.ReadHandshake(conn)
	if err != nil {
		_ = proto.WriteErr(conn, "unsupported handshake")
		_ = conn.Close()
		return
	}
	switch h.Kind {
	case proto.KindDump:
		_ = conn.SetDeadline(time.Now().Add(handshakeTimeout))
		_, _ = conn.Write(s.sess.Capture().SnapshotJSON())
		_ = conn.Close()
	case proto.KindAttach:
		s.handleAttach(conn, h)
	case proto.KindResize:
		// One-shot out-of-band resize (the attach client sends one per
		// SIGWINCH).
		s.sess.Resize(h.Cols, h.Rows)
		_ = proto.WriteOK(conn)
		_ = conn.Close()
	case proto.KindSend:
		// One-shot input injection (the tmux send-keys analogue): raw bytes
		// until the client half-closes, acknowledged once they reached the
		// PTY. Allowed while a client is attached: WriteInput serializes the
		// input sources.
		_ = conn.SetDeadline(time.Now().Add(sendTimeout))
		if _, err := io.Copy(s.sess.InputWriter(), conn); err != nil {
			_ = proto.WriteErr(conn, "send failed")
		} else {
			_ = proto.WriteOK(conn)
		}
		_ = conn.Close()
	case proto.KindStop:
		_ = conn.SetDeadline(time.Time{})
		_ = proto.WriteOK(conn)
		s.stopOnce.Do(func() {
			go s.sess.Terminate(time.Duration(h.GraceMs) * time.Millisecond)
		})
		// Hold the connection: the client waits for EOF, which the process
		// exit delivers once the agent is gone and the server shuts down.
		_, _ = io.Copy(io.Discard, conn)
		_ = conn.Close()
	}
}

func (s *Server) handleAttach(conn net.Conn, h proto.Handshake) {
	c := &attachClient{
		conn:     conn,
		ch:       make(chan []byte, attachQueueLen),
		quit:     make(chan struct{}),
		done:     make(chan struct{}),
		terminal: h.Cols > 0 && h.Rows > 0,
	}
	s.mu.Lock()
	old := s.client
	s.client = c
	s.mu.Unlock()
	if old != nil {
		// Take over the slot before answering: the previous terminal gets
		// the farewell and closes, then the new one starts clean.
		old.finish([]byte(takeoverFarewell), takeoverDrainTimeout)
	}

	_ = conn.SetDeadline(time.Time{})
	if err := proto.WriteOK(conn); err != nil {
		s.detach(c)
		return
	}
	// The queue is fresh and buffered: the initial repaint never blocks and
	// is the first thing the writer sends.
	c.ch <- s.sess.Capture().RepaintBytes()
	go c.writeLoop()
	adoptSize(s.sess, h.Cols, h.Rows)
	// Input: raw keystrokes until the client disconnects.
	_, _ = io.Copy(s.sess.InputWriter(), conn)
	s.detach(c)
}

// Broadcast queues an output chunk for the attach client, if any. The chunk
// is copied (p aliases the PTY read buffer). A full queue means the client
// cannot keep up — it is kicked rather than ever blocking the PTY loop.
func (s *Server) Broadcast(p []byte) {
	s.mu.Lock()
	c := s.client
	s.mu.Unlock()
	if c == nil {
		return
	}
	select {
	case c.ch <- append([]byte(nil), p...):
	default:
		s.detach(c)
	}
}

// FinishAttach ends the attach stream gracefully on shutdown: pending output
// is flushed (bounded by timeout), then the connection closes so the client
// sees EOF only after the final bytes.
func (s *Server) FinishAttach(timeout time.Duration) {
	s.mu.Lock()
	c := s.client
	s.client = nil
	s.mu.Unlock()
	if c == nil {
		return
	}
	c.finish(nil, timeout)
}

// detach frees the slot (when still owned by c) and disconnects the client.
func (s *Server) detach(c *attachClient) {
	s.mu.Lock()
	if s.client == c {
		s.client = nil
	}
	s.mu.Unlock()
	c.drop()
}

// attachClient is one attached terminal: a connection plus the bounded
// output queue its writer goroutine drains.
type attachClient struct {
	conn     net.Conn
	terminal bool        // a real terminal, which answers the agent's queries itself
	ch       chan []byte // never closed; nil chunk = end of stream
	quit     chan struct{}
	done     chan struct{}
	dropOnce sync.Once
}

func (c *attachClient) drop() {
	c.dropOnce.Do(func() {
		close(c.quit)
		_ = c.conn.Close()
	})
}

// finish ends the attach stream gracefully: an optional farewell line is
// queued behind pending output, the end-of-stream sentinel follows, delivery
// is waited out (bounded by timeout), then the connection closes. A full
// queue — the client is not draining anyway — skips straight to the close.
func (c *attachClient) finish(farewell []byte, timeout time.Duration) {
	if farewell != nil {
		select {
		case c.ch <- farewell:
		default:
		}
	}
	select {
	case c.ch <- nil:
		select {
		case <-c.done:
		case <-time.After(timeout):
		}
	default:
	}
	c.drop()
}

// writeLoop drains the queue to the connection as raw bytes. It exits on the
// end-of-stream sentinel, a kick (quit), or a failed/stalled write.
func (c *attachClient) writeLoop() {
	defer close(c.done)
	for {
		select {
		case p := <-c.ch:
			if p == nil {
				return
			}
			_ = c.conn.SetWriteDeadline(time.Now().Add(attachWriteDeadline))
			if _, err := c.conn.Write(p); err != nil {
				c.drop()
				return
			}
		case <-c.quit:
			return
		}
	}
}

// adoptSize applies an attaching client's terminal size. When it already
// matches, the size is jiggled (rows−1, then back) so the agent still
// receives a SIGWINCH and repaints in full fidelity on the new client.
// A client without a terminal reports 0x0 and adopts nothing.
func adoptSize(sess *Session, cols, rows int) {
	if cols <= 0 || rows <= 0 {
		return
	}
	curCols, curRows := sess.Size()
	if cols == curCols && rows == curRows {
		sess.Resize(cols, rows-1)
		time.Sleep(sizeJiggleDelay)
	}
	sess.Resize(cols, rows)
}
