package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/eclipsesource/homeshell/daemon/internal/persist"
)

// spawnLogName is the file a spawned daemon's stdout/stderr are redirected to,
// inside the data directory the daemon itself uses. Named for the daemon rather
// than for the spawn because that is what it holds: the child inherits the
// handle and logs to it for its whole lifetime, not just while starting.
const spawnLogName = "daemon.log"

// spawnLogRotateAt is the size above which the previous daemon's log is moved
// aside at the next spawn instead of being appended to.
//
// This bounds the file *across* spawns only. A single long-lived daemon at
// -log-level=debug still grows it without limit, and the parent cannot prevent
// that: the daemon owns the inherited handle, so bounding one run's output is
// the daemon's own logging concern, not this seam's.
const spawnLogRotateAt = 8 << 20 // 8 MiB

// spawnedDaemon reports on a daemon this process started.
//
// Both fields exist because a detached daemon's startup failure is otherwise
// invisible: the child writes it to stderr and exits, and if the parent
// discards that stream and releases the process handle, the only symptom left
// is `ensure-running` burning its whole -timeout with nothing to go on. The
// common case is a port already held by something else. So the child's output
// goes to a log file, and the parent keeps waiting on the process so it can
// fail as soon as the child dies rather than at the deadline.
type spawnedDaemon struct {
	// exited is closed once the child process has terminated.
	exited chan struct{}
	// logPath is the file the child's stdout/stderr were redirected to.
	logPath string
	// logOffset is where in that file this spawn's output begins. The log is
	// appended to across daemon runs, so reporting a failure without it quotes
	// every previous run as well.
	logOffset int64

	mu      sync.Mutex
	waitErr error
}

// spawnLog is the opened daemon log plus where this spawn's output starts in it.
type spawnLog struct {
	file   *os.File
	path   string
	offset int64
}

// serveArgs builds the argument list a spawned daemon is started with. -home-dir
// makes it share the caller's data directory; -addr pins its listen address.
func serveArgs(homeDir, addr string) []string {
	args := []string{"serve"}
	if homeDir != "" {
		args = append(args, "-home-dir", homeDir)
	}
	if addr != "" {
		args = append(args, "-addr", addr)
	}
	return args
}

// openSpawnLog opens the daemon's log inside homeDir, rotating an oversized one
// aside first. It is 0600 because the daemon's output can name local paths.
//
// homeDir is a parameter, not persist.DefaultDir(), so the log lands beside the
// data of the daemon this call is about to spawn even when the caller has not
// touched the package-level default. Empty means "no -home-dir given", i.e. the
// platform default, which is what the daemon will use too.
//
// It appends rather than truncates, because the previous daemon may still hold
// this file open at a nonzero offset.
func openSpawnLog(homeDir string) (*spawnLog, error) {
	dir := homeDir
	if dir == "" {
		defaultDir, err := persist.DefaultDir()
		if err != nil {
			return nil, err
		}
		dir = defaultDir
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("creating data directory %s: %w", dir, err)
	}
	path := filepath.Join(dir, spawnLogName)
	rotateSpawnLog(path)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	// Where this spawn's output will start. Nothing has been written yet, so the
	// current size is the boundary between the previous daemon's lines and ours.
	var offset int64
	if info, statErr := file.Stat(); statErr == nil {
		offset = info.Size()
	}
	return &spawnLog{file: file, path: path, offset: offset}, nil
}

// rotateSpawnLog moves an oversized log aside, keeping one previous generation.
// Best-effort: a log that cannot be rotated is not a reason to refuse to start a
// daemon, and appending to an overlarge file still works.
func rotateSpawnLog(path string) {
	info, err := os.Stat(path)
	if err != nil || info.Size() < spawnLogRotateAt {
		return
	}
	_ = os.Rename(path, path+".1")
}

// watchSpawned reaps the child in the background, closing exited when it goes.
// Reaping (rather than releasing the handle) is what lets the caller notice a
// daemon that died instantly; the log file is closed at the same time, since
// only the child writes to it.
func watchSpawned(cmd *exec.Cmd, log *spawnLog) *spawnedDaemon {
	spawned := &spawnedDaemon{
		exited:    make(chan struct{}),
		logPath:   log.path,
		logOffset: log.offset,
	}
	go func() {
		err := cmd.Wait()
		log.file.Close()
		spawned.mu.Lock()
		spawned.waitErr = err
		spawned.mu.Unlock()
		close(spawned.exited)
	}()
	return spawned
}

// died returns a channel that fires when the child process exits. A nil
// spawnedDaemon yields a nil channel, which blocks forever in a select — the
// right behavior for tests that stub the spawn seam and have no real child.
func (d *spawnedDaemon) died() <-chan struct{} {
	if d == nil {
		return nil
	}
	return d.exited
}

// exitReason describes how the child exited, for use once died() has fired.
func (d *spawnedDaemon) exitReason() string {
	if d == nil {
		return ""
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.waitErr == nil {
		return "it exited successfully without serving"
	}
	return d.waitErr.Error()
}

// logHint points at the spawn log so a failure is diagnosable. It reads the
// child's output back rather than only naming the file, because the reason the
// daemon refused to start is usually a single line and worth showing inline.
// When that reason is a taken port it adds the guidance below, since the port's
// holder is most often another daemon this home dir cannot see.
func (d *spawnedDaemon) logHint() string {
	if d == nil || d.logPath == "" {
		return ""
	}
	output := d.ownOutput()
	if len(output) == 0 {
		return fmt.Sprintf("; see %s", d.logPath)
	}
	return fmt.Sprintf("; the daemon wrote:\n%s\n(full output: %s)%s",
		output, d.logPath, addressInUseHint(string(output)))
}

// ownOutput reads only what this spawn wrote, from the offset the log stood at
// when it was opened. The file is appended to across daemon runs, so the offset
// is what keeps an earlier run's lines — including a bind error of its own, which
// addressInUseHint would act on — out of this failure's report.
func (d *spawnedDaemon) ownOutput() []byte {
	file, err := os.Open(d.logPath)
	if err != nil {
		return nil
	}
	defer file.Close()
	if _, err := file.Seek(d.logOffset, io.SeekStart); err != nil {
		return nil
	}
	output, err := io.ReadAll(file)
	if err != nil {
		return nil
	}
	return output
}

// addressInUseHint explains what to do about a port another process holds.
//
// It exists because the obvious reading of "address already in use" — that some
// unrelated program took the port — is usually wrong. The usual holder is
// another HomeShell daemon started under a *different* `-home-dir`, which this
// call cannot discover: every verb reads the state file belonging to the home
// dir it was given, so `homeshell stop` with no `-home-dir` reports "no daemon
// is running" while that daemon keeps serving. Naming that trap here is the
// difference between a one-line fix and a dead end.
func addressInUseHint(daemonOutput string) string {
	if !mentionsAddressInUse(daemonOutput) {
		return ""
	}
	return "\n\nSomething already holds that address. If it is another HomeShell daemon, it was" +
		"\nstarted under a different -home-dir, and every verb only sees the daemon for the" +
		"\n-home-dir it is given — which is why `homeshell stop` can report no daemon while" +
		"\nthat one keeps serving. Any of these resolves it:" +
		"\n  * attach to it instead, by passing its -home-dir to this command" +
		"\n  * stop it with `homeshell stop -home-dir <its data directory>`" +
		"\n  * leave it alone and take another port with -addr 127.0.0.1:<port>"
}

// mentionsAddressInUse reports whether the daemon's output is a bind conflict.
// The child reports it as text, so this matches on it rather than on a syscall
// error; each supported platform words it differently.
func mentionsAddressInUse(daemonOutput string) bool {
	lowered := strings.ToLower(daemonOutput)
	for _, signature := range []string{
		"address already in use", // Linux, macOS
		"address in use",         // shorter phrasings
	} {
		if strings.Contains(lowered, signature) {
			return true
		}
	}
	return false
}

// spawnDetachedServe starts a background `homeshell serve` in its own session so
// it outlives this short-lived process. It re-execs the current binary and
// forwards -home-dir when set so the daemon shares the caller's data
// directory, and -addr when set so it binds a specific listen address. The
// child's output goes to the spawn log so a startup failure is diagnosable.
func spawnDetachedServe(homeDir, addr string) (*spawnedDaemon, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	log, err := openSpawnLog(homeDir)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(exe, serveArgs(homeDir, addr)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdin = nil
	cmd.Stdout, cmd.Stderr = log.file, log.file
	if err := cmd.Start(); err != nil {
		log.file.Close()
		return nil, err
	}
	return watchSpawned(cmd, log), nil
}
