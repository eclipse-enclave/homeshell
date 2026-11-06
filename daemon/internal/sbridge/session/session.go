// Package session implements the bridge engine: it runs the agent on a PTY,
// feeds a copy of the output into the capture layer, and exposes the
// primitives the verbs compose — input writes, resizes, signal forwarding,
// and exit-code mirroring. It also hosts the snapshot/attach socket server
// (server.go).
package session

import (
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/sbridge/capture"

	"github.com/creack/pty"
)

// Options configures the agent process.
type Options struct {
	Argv []string
	Dir  string   // working directory ("" = inherit)
	Env  []string // full environment (nil = inherit)
	Cols int
	Rows int
}

// Session is one agent process on a PTY plus its capture state.
type Session struct {
	cmd  *exec.Cmd
	ptmx *os.File
	cap  *capture.Capture

	// inputMu serializes writes to the PTY master. Each mode has a single
	// input source today; the lock keeps that a local invariant instead of
	// a global one.
	inputMu sync.Mutex

	sizeMu sync.Mutex
	cols   int
	rows   int

	waitOnce sync.Once
	exitCode int
	done     chan struct{} // closed once the agent has been reaped

	// endedByBridge is set once the bridge asks the agent to end (a stop, or a
	// forwarded signal), so an exit the agent chose can be told apart.
	endedByBridge atomic.Bool

	// answering gates the replies to the agent's terminal queries, which
	// replyLoop writes so the read loop never blocks on the PTY's input side.
	answering func() bool
	replies   chan []byte
}

// replyQueueLen bounds pending query replies; a full queue drops replies, as
// a terminal the agent does not read from would lose them too.
const replyQueueLen = 16

// Start launches the agent on a new PTY at the given size.
func Start(opts Options) (*Session, error) {
	cmd := exec.Command(opts.Argv[0], opts.Argv[1:]...) // #nosec G204 -- argv is the caller's agent command, passed through by design.
	cmd.Dir = opts.Dir
	cmd.Env = opts.Env
	ptmx, err := pty.StartWithSize(cmd, winsize(opts.Cols, opts.Rows))
	if err != nil {
		return nil, err
	}
	return &Session{
		cmd:  cmd,
		ptmx: ptmx,
		cap:  capture.New(opts.Cols, opts.Rows),
		cols: opts.Cols,
		rows: opts.Rows,
		done: make(chan struct{}),
	}, nil
}

// AnswerQueriesWhen makes the session answer the agent's terminal queries
// whenever answering reports true: while no terminal is attached that would
// answer them itself. Call it before ReadLoop starts.
func (s *Session) AnswerQueriesWhen(answering func() bool) {
	s.cap.AnswerQueries()
	s.answering = answering
	s.replies = make(chan []byte, replyQueueLen)
	go s.replyLoop()
}

func (s *Session) replyLoop() {
	for {
		select {
		case r := <-s.replies:
			_ = s.WriteInput(r)
		case <-s.done:
			return
		}
	}
}

// feed passes output to the capture and queues the replies it produced.
func (s *Session) feed(p []byte) {
	replies := s.cap.Feed(p)
	if len(replies) == 0 || s.answering == nil || !s.answering() {
		return
	}
	select {
	case s.replies <- replies:
	default:
	}
}

// Capture exposes the snapshot state for the socket server.
func (s *Session) Capture() *capture.Capture { return s.cap }

// WriteInput writes keystrokes to the agent's PTY, atomically per call.
func (s *Session) WriteInput(p []byte) error {
	s.inputMu.Lock()
	defer s.inputMu.Unlock()
	_, err := s.ptmx.Write(p)
	return err
}

// InputWriter adapts WriteInput for io.Copy.
func (s *Session) InputWriter() *inputWriter { return &inputWriter{s} }

type inputWriter struct{ s *Session }

func (w *inputWriter) Write(p []byte) (int, error) {
	if err := w.s.WriteInput(p); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Resize propagates a new terminal size to the PTY (SIGWINCH for the agent)
// and the emulator.
func (s *Session) Resize(cols, rows int) {
	if cols <= 0 || rows <= 0 {
		return
	}
	s.sizeMu.Lock()
	defer s.sizeMu.Unlock()
	_ = pty.Setsize(s.ptmx, winsize(cols, rows))
	s.cap.Resize(cols, rows)
	s.cols, s.rows = cols, rows
}

// Size returns the last size set (initial size until the first Resize).
func (s *Session) Size() (cols, rows int) {
	s.sizeMu.Lock()
	defer s.sizeMu.Unlock()
	return s.cols, s.rows
}

// Signal forwards a signal to the agent process.
func (s *Session) Signal(sig os.Signal) {
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Signal(sig)
	}
}

// ForwardSignals relays termination signals to the agent so a kill of the
// bridge reaches it promptly.
func (s *Session) ForwardSignals() {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP, syscall.SIGQUIT)
	go func() {
		for sig := range sigs {
			s.endedByBridge.Store(true)
			s.Signal(sig)
		}
	}()
}

// KillGroup SIGKILLs the agent's process group. The agent is a session
// leader (creack/pty starts it with Setsid), so its pgid is its pid and the
// kill reaches its whole tree.
func (s *Session) KillGroup() {
	if s.cmd.Process != nil {
		_ = syscall.Kill(-s.cmd.Process.Pid, syscall.SIGKILL)
	}
}

// Terminate asks the agent to exit (SIGTERM) and escalates to a group
// SIGKILL when it is still running after grace.
func (s *Session) Terminate(grace time.Duration) {
	s.endedByBridge.Store(true)
	s.Signal(syscall.SIGTERM)
	select {
	case <-s.done:
	case <-time.After(grace):
		s.KillGroup()
	}
}

// ReadLoop pumps the PTY output into the capture, then the sink, until the
// PTY reaches EOF (agent exited). Capture first, so a repaint snapshot can
// never lag the attach stream.
func (s *Session) ReadLoop(sink func([]byte)) {
	buf := make([]byte, 32*1024)
	for {
		n, err := s.ptmx.Read(buf)
		if n > 0 {
			s.feed(buf[:n])
			sink(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

// Wait reaps the agent and mirrors its exit status: the exit code for a
// normal exit, 128+signum when signal-killed (shell convention). Safe to
// call more than once.
func (s *Session) Wait() int {
	s.waitOnce.Do(func() {
		err := s.cmd.Wait()
		s.exitCode = exitCode(s.cmd, err)
		close(s.done)
	})
	return s.exitCode
}

// EndedByBridge reports whether the bridge ended the agent (Terminate or a
// forwarded signal) rather than the agent exiting on its own.
func (s *Session) EndedByBridge() bool { return s.endedByBridge.Load() }

// Done is closed once the agent has been reaped; Wait's result is valid
// after that.
func (s *Session) Done() <-chan struct{} { return s.done }

// ClosePTY releases the master side (after Wait).
func (s *Session) ClosePTY() { _ = s.ptmx.Close() }

func exitCode(cmd *exec.Cmd, waitErr error) int {
	if state := cmd.ProcessState; state != nil {
		if ws, ok := state.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return 128 + int(ws.Signal())
		}
		if code := state.ExitCode(); code >= 0 {
			return code
		}
	}
	if waitErr != nil {
		return 1
	}
	return 0
}

// winsize converts terminal dimensions for the PTY ioctls.
func winsize(cols, rows int) *pty.Winsize {
	// #nosec G115 -- terminal dimensions reported by the kernel fit in uint16.
	return &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)}
}
