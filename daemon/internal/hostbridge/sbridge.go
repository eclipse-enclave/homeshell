package hostbridge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/detect"
	"github.com/eclipsesource/homeshell/daemon/internal/provider"
	"github.com/eclipsesource/homeshell/daemon/internal/sbridge"
)

// bridgeExe is the executable that runs `bridge serve` and `bridge attach`:
// the daemon's own. A self-update replaces the file before re-executing the
// daemon, so a bridge started afterwards runs the new build too.
func (r Runtime) bridgeExe() (string, error) {
	if r.exe != "" {
		return r.exe, nil
	}
	return os.Executable()
}

// serveCommand is the `bridge serve` process Start launches. On Linux it execs
// /proc/self/exe, which stays pinned to the running image after a package
// upgrade replaces the file, so a bridge always comes from the build that
// started it.
func (r Runtime) serveCommand(args ...string) (*exec.Cmd, error) {
	bin, err := r.bridgeExe()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(bin, append([]string{"bridge", "serve"}, args...)...)
	if r.exe == "" && runtime.GOOS == "linux" {
		cmd.Path = "/proc/self/exe" // argv[0] keeps the readable path for ps
	}
	return cmd, nil
}

// sbridgeTimeout bounds dump, liveness and send calls so a wedged bridge
// cannot block a reconcile or an API call. Start polls the socket for
// sbridgeReadyTimeout in sbridgeReadyStep increments.
const (
	sbridgeTimeout      = sbridge.DefaultTimeout
	sbridgeReadyTimeout = 2 * time.Second
	sbridgeReadyStep    = 50 * time.Millisecond
	sbridgeReapDelay    = 1 * time.Second
)

// Runtime runs host PTY sessions under detached `homeshell bridge serve`
// processes.
// The socket path derives from the persisted target handle, so restarts
// re-find running bridges. Sessions survive daemon restarts (the serve process
// is its own session leader), but not a cgroup-killing supervisor (systemd
// `KillMode=control-group`).
type Runtime struct {
	runDir string // sockets, one per target
	exe    string // runs the bridge (tests); empty means this executable

	// readyTimeout overrides sbridgeReadyTimeout in Start (tests); zero
	// means the default.
	readyTimeout time.Duration
}

// New returns the sbridge-backed Runtime, its socket directory
// namespaced by stateBase — the daemon's resolved home directory.
//
// Sockets stay under XDG_RUNTIME_DIR (tmpfs: reboots garbage-collect stale
// sockets, per-user, short paths, and the directory the spec reserves for
// exactly this; a networked home cannot host a unix socket at all, so the data
// directory is not an option) with a state-dir fallback. The namespace is what
// makes -home-dir mean the same thing for sockets as it already does for the
// session store: two daemons with different home directories keep separate
// stores, so they must keep separate socket namespaces or they collide on a
// target name and each answers liveness probes for the other's sessions.
func New(stateBase string) Runtime {
	return Runtime{runDir: filepath.Join(runRoot(), "homeshell-"+shortHash(stateBase))}
}

// runRoot is the directory socket namespaces live in. Split from the namespace
// itself so legacyRunDir can reproduce the pre-namespacing layout.
func runRoot() string {
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return os.TempDir()
	}
	return filepath.Join(home, ".local", "state")
}

// legacyRunDir reproduces, exactly, the un-namespaced socket directory used
// before New took a stateBase. A session started by such a daemon
// is still serving there, so LocateElsewhere has to look before the reconciler
// concludes the agent died and resumes a second copy of it.
func legacyRunDir() string {
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		return filepath.Join(dir, "homeshell")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "homeshell-bridge")
	}
	return filepath.Join(home, ".local", "state", "homeshell", "run")
}

// shortHash is a stable 12-hex-char digest of a path, the same shape Enclave
// uses for its project hashes. Keeps the socket path well inside the ~108-byte
// sun_path limit even for a deep -home-dir.
func shortHash(s string) string {
	if abs, err := filepath.Abs(s); err == nil {
		s = abs
	}
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:12]
}

func (r Runtime) sock(target string) string {
	return filepath.Join(r.runDir, target+".sock")
}

// exitFile is where the bridge serving sock records how its agent exited.
func exitFile(sock string) string {
	return strings.TrimSuffix(sock, ".sock") + ".exit.json"
}

// LastExit implements ExitRecorder. The recorded socket wins over the current
// one, since the run dir can differ between daemon starts.
func (r Runtime) LastExit(target, recorded string) (sbridge.Exit, bool) {
	sock := recorded
	if sock == "" {
		sock = r.sock(target)
	}
	rec, err := sbridge.ReadExit(exitFile(sock))
	return rec, err == nil
}

// describeExit is the agent's own account of a failed start, which went to
// the PTY rather than serve's stderr.
func describeExit(sock string) string {
	rec, err := sbridge.ReadExit(exitFile(sock))
	if err != nil {
		return ""
	}
	return " (agent exited with " + rec.Summary() + ")"
}

// Location implements LocationRecorder: where this runtime resolves target
// right now. The provider persists it per session.
func (r Runtime) Location(target string) string { return r.sock(target) }

// LocateElsewhere implements LocationRecorder. It answers the question a bare
// liveness probe cannot: a bridge that does not answer at the current socket
// path may be dead, or may be alive at a path a previous daemon start used —
// XDG_RUNTIME_DIR unset this time, a different -home-dir, or a session created
// before either was recorded. Resuming in the second case forks a second live
// agent into the same working directory, which is exactly what the reconciler
// must not do; every other operation has to address the socket returned here
// rather than the one this runtime computes.
func (r Runtime) LocateElsewhere(ctx context.Context, target, recorded string) string {
	current := r.sock(target)
	candidates := []string{recorded}
	if recorded == "" {
		// Persisted before locations were recorded: the only other place it can
		// be is the layout that daemon used.
		candidates = []string{filepath.Join(legacyRunDir(), target+".sock")}
	}
	for _, candidate := range candidates {
		if candidate == "" || candidate == current {
			continue
		}
		// An unresponsive or unreadable recorded socket is not proof that its
		// agent died. Preserve the no-duplicate-resume guard on uncertain probes.
		if err := r.probe(ctx, candidate); !errors.Is(err, sbridge.ErrNotServing) {
			return candidate
		}
	}
	return ""
}

// Available reports why the agent cannot be launched, such as the agent binary
// missing from PATH.
func (r Runtime) Available(_ context.Context, binary string) error {
	if _, err := r.bridgeExe(); err != nil {
		return fmt.Errorf("cannot locate the homeshell executable: %w", err)
	}
	if binary == "" {
		return fmt.Errorf("no agent binary configured")
	}
	if _, err := exec.LookPath(binary); err != nil {
		return fmt.Errorf("%s not found on PATH", binary)
	}
	return nil
}

// Start launches argv under a bridge serving on the target's socket. serve
// is foreground-only, so Start daemonizes it: a new session (Setsid) severs it
// from the daemon's controlling terminal and signals, a goroutine reaps it,
// and Start returns once a dump probe answers on the socket — from then on
// the session is usable and survives daemon restarts. On startup failure the child's real error
// (e.g. "start agent: exec: claude: not found") is in the returned error.
func (r Runtime) Start(ctx context.Context, target, dir string, argv []string) error {
	if len(argv) == 0 {
		return fmt.Errorf("empty launch command")
	}

	if err := os.MkdirAll(r.runDir, 0o700); err != nil {
		return fmt.Errorf("bridge run dir: %w", err)
	}
	// serve replaces a socket left by a bridge that died without cleanup and
	// refuses one a live bridge serves, so it is not removed here.
	sock := r.sock(target)
	// The live bridge would also answer the readiness probe below, so refuse
	// here or Start reports a launch that serve refused.
	if r.probe(ctx, sock) == nil {
		return fmt.Errorf("sbridge serve %q: socket %s is served by another running bridge", target, sock)
	}
	cmd, err := r.serveCommand(append([]string{"--sock", sock, "--exit-file", exitFile(sock), "--dir", dir, "--"}, argv...)...)
	if err != nil {
		return fmt.Errorf("sbridge serve %q: %w", target, err)
	}
	detachFromDaemon(cmd)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	// stderr is a buffer, so os/exec pipes it through a copier goroutine and
	// Wait blocks until that pipe reaches EOF — which needs *every* inheritor
	// of the write end to exit, not just the serve leader we kill. Without a
	// bound, the timeout path below waits out the agent's whole lifetime while
	// waiting for process startup. WaitDelay caps that: once the process is gone
	// Wait gives the copier this long, then closes the pipe and returns.
	cmd.WaitDelay = sbridgeReapDelay
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("sbridge serve %q: %w", target, err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }() // reap; on success serve outlives Start

	timeout := r.readyTimeout
	if timeout == 0 {
		timeout = sbridgeReadyTimeout
	}
	deadline := time.Now().Add(timeout)
	for {
		select {
		case err := <-exited:
			// serve ended before serving: a startup failure, whose story is
			// on stderr (or an agent that exited immediately).
			if err == nil {
				err = fmt.Errorf("exited before the socket became ready")
			}
			return fmt.Errorf("sbridge serve %q: %w: %s%s", target, err, stderr.Bytes(), describeExit(sock))
		case <-ctx.Done():
			_ = cmd.Process.Kill()
			return fmt.Errorf("sbridge serve %q: %w", target, ctx.Err())
		default:
		}
		// Bounded like every other runtime call: a bridge that accepts the
		// connection but never answers must not wedge Start, which waits for readiness.
		if r.probe(ctx, sock) == nil {
			return nil
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			<-exited // reap first: the stderr buffer is safe to read only after Wait returned
			return fmt.Errorf("sbridge serve %q: socket %s not ready after %v: %s", target, sock, timeout, stderr.Bytes())
		}
		time.Sleep(sbridgeReadyStep)
	}
}

// probe asks the socket for a dump, the readiness and liveness question both
// Start and Probe ask. Bounded by sbridgeTimeout and by the caller's context.
func (r Runtime) probe(ctx context.Context, sock string) error {
	_, err := sbridge.Dump(ctx, sock)
	return err
}

// Probe returns nil while a bridge serves the target's socket. Only an error
// wrapping sbridge.ErrNotServing means none does.
func (r Runtime) Probe(ctx context.Context, target string) error {
	return r.probe(ctx, r.sock(target))
}

func detectionSnapshot(agent string, snap sbridge.Snapshot) detect.Snapshot {
	progress := ""
	if snap.OSCProgress != nil {
		progress = *snap.OSCProgress
	}
	return detect.Snapshot{
		Agent:       agent,
		Screen:      snap.Screen,
		OSCTitle:    strings.TrimRight(snap.OSCTitle, "\n"),
		OSCProgress: progress,
	}
}

// Classify detects a bridge snapshot as agent, the same way for every provider.
func Classify(agent string, snap sbridge.Snapshot) detect.Result {
	return detect.Detect(detectionSnapshot(agent, snap))
}

// State captures the target's snapshot and classifies it as agent, in one read.
// Only ErrNotServing permits the provider to treat the bridge as gone.
func (r Runtime) State(ctx context.Context, agent, target string) (detect.Result, error) {
	snap, err := sbridge.Dump(ctx, r.sock(target))
	if err != nil {
		return detect.Result{}, err
	}
	if snap.Error != "" {
		return detect.Result{}, fmt.Errorf("sbridge capture %q: %s", target, snap.Error)
	}
	return Classify(agent, snap), nil
}

// Stop shuts the bridge down (SIGTERM to the agent, SIGKILL escalation
// server-side). An already-dead bridge is not an error.
func (r Runtime) Stop(ctx context.Context, target string) error {
	return r.StopAt(ctx, r.sock(target))
}

// StopAt implements LocationRecorder: the same shutdown addressed at an
// explicit socket, for a session serving somewhere this runtime no longer
// computes.
func (r Runtime) StopAt(ctx context.Context, location string) error {
	ctx, cancel := context.WithTimeout(ctx, sbridge.StopTimeout)
	defer cancel()
	if err := sbridge.Stop(ctx, location); err != nil {
		return err
	}
	_ = os.Remove(exitFile(location)) // a deliberate stop needs no account
	return nil
}

// SendInput types text into the target's terminal and submits it with Enter.
// It works while a terminal is attached. A dead or absent bridge is an error
// (the interface promises one for unknown targets).
func (r Runtime) SendInput(ctx context.Context, target, text string) error {
	return r.send(ctx, target, text+"\r")
}

// SendKey delivers a single named key event as the raw byte sequence a
// terminal emits for it, with no trailing Enter, for navigating an agent's
// interactive menu.
func (r Runtime) SendKey(ctx context.Context, target, key string) error {
	seq, ok := provider.SessionKeyBytes[key]
	if !ok {
		return fmt.Errorf("unknown key %q", key)
	}
	return r.send(ctx, target, seq)
}

func (r Runtime) send(ctx context.Context, target, input string) error {
	ctx, cancel := context.WithTimeout(ctx, sbridgeTimeout)
	defer cancel()
	return sbridge.Send(ctx, r.sock(target), []byte(input))
}

// AttachCommand attaches a terminal to the bridge: a plain PTY passthrough
// with native terminal behavior (no prefix key, no copy-mode). Attaching
// while another terminal is attached takes the session over — the bridge
// detaches the previous client (last attach wins), so re-opening a session
// moves it into the new terminal instead of failing.
//
// The argv carries the daemon's executable path: the terminal emulator runs it
// in its own environment, whose PATH need not contain it.
func (r Runtime) AttachCommand(target string) []string {
	return r.AttachCommandAt(r.sock(target))
}

// AttachCommandAt implements LocationRecorder: attach addressed at an explicit
// socket, so a session serving where an earlier daemon start put it can still
// be opened.
func (r Runtime) AttachCommandAt(location string) []string {
	bin, err := r.bridgeExe()
	if err != nil {
		bin = "homeshell" // let the terminal report it; Open already failed Available
	}
	return []string{bin, "bridge", "attach", "--sock", location}
}

// Output runs a one-off command and returns its stdout. Unlike every other
// method here it does not go through the bridge: the question ("which models do
// you have?") is for the agent's CLI itself, not for a session. The caller's ctx
// is the only bound, and stderr is discarded — a CLI that explains itself there
// has still failed to answer.
func (Runtime) Output(ctx context.Context, argv []string) ([]byte, error) {
	if len(argv) == 0 {
		return nil, fmt.Errorf("no command to run")
	}
	out, err := exec.CommandContext(ctx, argv[0], argv[1:]...).Output()
	if err != nil {
		if ctx.Err() != nil {
			// A deadline we imposed reads as "signal: killed" otherwise, which sends
			// the reader looking for a crash that never happened.
			return nil, fmt.Errorf("%s %v: %w", argv[0], argv[1:], ctx.Err())
		}
		return nil, fmt.Errorf("%s %v: %w", argv[0], argv[1:], err)
	}
	return out, nil
}

// Snapshot reads the raw terminal signals for providers that own their detector.
func (r Runtime) Snapshot(ctx context.Context, target string) (sbridge.Snapshot, error) {
	return sbridge.Dump(ctx, r.sock(target))
}

// detachFromDaemon puts the bridge in its own session so it survives the
// daemon: a new session severs it from the daemon's controlling terminal and
// signal group, which is what makes `bridge serve` outlive the process that
// spawned it. Under a cgroup-killing supervisor (systemd `KillMode=control-group`)
// it still dies with the daemon.
func detachFromDaemon(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
