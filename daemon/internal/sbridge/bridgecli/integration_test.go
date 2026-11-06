package bridgecli_test

import (
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/sbridge"
	"github.com/eclipsesource/homeshell/daemon/internal/sbridge/bridgecli"
	"github.com/eclipsesource/homeshell/daemon/internal/sbridge/proto"
)

// binPath is this test binary; TestMain runs it as the bridge when it is
// invoked as `bridge <verb>`, the way the daemon runs itself.
var binPath string

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "bridge" {
		os.Exit(bridgecli.Main(os.Args[2:]))
	}
	exe, err := os.Executable()
	if err != nil {
		panic(err)
	}
	binPath = exe
	os.Exit(m.Run())
}

// sockDir returns a short-lived directory under /tmp so socket paths stay
// well inside sun_path.
func sockDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "sb-it")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func run(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	cmd := exec.Command(binPath, append([]string{"bridge"}, args...)...)
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code = 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			t.Fatalf("%v: %v", args, err)
		}
	}
	return out.String(), errb.String(), code
}

// startServe spawns `bridge serve` detached the way real callers do —
// a new session (Setsid) — and waits until the socket accepts connections.
// The returned wait func blocks for process exit and yields the exit code.
func startServe(t *testing.T, sock string, extra []string, argv ...string) (wait func() int) {
	t.Helper()
	_, wait = startServeCmd(t, sock, extra, argv...)
	return wait
}

// startServeCmd is startServe that also returns the bridge process.
func startServeCmd(t *testing.T, sock string, extra []string, argv ...string) (*exec.Cmd, func() int) {
	t.Helper()
	args := append([]string{"bridge", "serve", "--sock", sock}, extra...)
	args = append(append(args, "--"), argv...)
	cmd := exec.Command(binPath, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	var errb strings.Builder
	cmd.Stderr = &errb
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan int, 1)
	go func() {
		code := 0
		if err := cmd.Wait(); err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				code = ee.ExitCode()
			} else {
				code = -1
			}
		}
		exited <- code
	}()
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	})
	waitFor(t, "serve socket ("+errb.String()+")", func() bool {
		conn, err := net.DialTimeout("unix", sock, 100*time.Millisecond)
		if err != nil {
			return false
		}
		_ = conn.Close()
		return true
	})
	return cmd, func() int {
		select {
		case code := <-exited:
			return code
		case <-time.After(5 * time.Second):
			t.Fatalf("serve did not exit (stderr: %s)", errb.String())
			return -1
		}
	}
}

func dumpSnapshot(t *testing.T, sock string) sbridge.Snapshot {
	t.Helper()
	out, errb, code := run(t, "dump", "--sock", sock)
	if code != 0 {
		t.Fatalf("dump exit %d: %s", code, errb)
	}
	var snap sbridge.Snapshot
	if err := json.Unmarshal([]byte(out), &snap); err != nil {
		t.Fatalf("dump payload %q: %v", out, err)
	}
	return snap
}

// waitFor polls cond until it holds or the deadline passes.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// attach performs the attach handshake and returns the connection with the
// raw output stream pending.
func attach(t *testing.T, sock string, cols, rows int) (net.Conn, error) {
	t.Helper()
	conn, err := proto.Dial(sock, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := proto.WriteHandshake(conn, proto.Handshake{Kind: proto.KindAttach, Cols: cols, Rows: rows}); err != nil {
		t.Fatal(err)
	}
	if err := proto.ReadResponse(conn); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

// request performs a one-shot handshake (resize, stop) and returns the
// server's response.
func request(t *testing.T, sock string, h proto.Handshake) error {
	t.Helper()
	conn, err := proto.Dial(sock, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	if err := proto.WriteHandshake(conn, h); err != nil {
		t.Fatal(err)
	}
	return proto.ReadResponse(conn)
}

func TestServeLifecycle(t *testing.T) {
	dir := sockDir(t)
	sock := filepath.Join(dir, "s.sock")

	// Agent: sets an OSC title, prints a line, then echoes stdin (cat).
	script := `printf '\033]0;` + "✳" + ` Test Agent\007'; echo hello-screen; exec cat`
	startServe(t, sock, []string{"--size", "100x30"}, "sh", "-c", script)

	snap := dumpSnapshot(t, sock)
	if snap.Error != "" {
		t.Fatalf("snapshot error: %s", snap.Error)
	}
	waitFor(t, "agent output in snapshot", func() bool {
		return strings.Contains(dumpSnapshot(t, sock).Screen, "hello-screen")
	})
	if got := dumpSnapshot(t, sock).OSCTitle; got != "✳ Test Agent" {
		t.Errorf("title %q", got)
	}

	// Attach: expect the initial repaint, then the echo of our input, both
	// as raw bytes.
	conn, err := attach(t, sock, 100, 30)
	if err != nil {
		t.Fatalf("attach refused: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write([]byte("ping-input\r")); err != nil {
		t.Fatal(err)
	}
	var seen strings.Builder
	buf := make([]byte, 32*1024)
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for !strings.Contains(seen.String(), "hello-screen") || !strings.Contains(seen.String(), "ping-input") {
		n, err := conn.Read(buf)
		if n > 0 {
			seen.Write(buf[:n])
		}
		if err != nil {
			t.Fatalf("attach stream ended early (saw %q): %v", seen.String(), err)
		}
	}

	// A second attach takes the session over (last attach wins): the first
	// client gets a farewell line and EOF, the new one the initial repaint.
	conn2, err := attach(t, sock, 80, 24)
	if err != nil {
		t.Fatalf("takeover attach refused: %v", err)
	}
	defer func() { _ = conn2.Close() }()
	var tail strings.Builder
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			tail.Write(buf[:n])
		}
		if err != nil {
			break
		}
	}
	if !strings.Contains(tail.String(), "taken over by a new client") {
		t.Errorf("replaced client tail %q, want the takeover farewell", tail.String())
	}
	_ = conn.Close()
	seen.Reset()
	_ = conn2.SetReadDeadline(time.Now().Add(5 * time.Second))
	for !strings.Contains(seen.String(), "hello-screen") {
		n, err := conn2.Read(buf)
		if n > 0 {
			seen.Write(buf[:n])
		}
		if err != nil {
			t.Fatalf("takeover stream ended early (saw %q): %v", seen.String(), err)
		}
	}

	// Resize is out-of-band: a one-shot request on its own connection,
	// accepted even while the attach slot is taken.
	if err := request(t, sock, proto.Handshake{Kind: proto.KindResize, Cols: 120, Rows: 40}); err != nil {
		t.Fatalf("resize refused: %v", err)
	}

	// send injects input without occupying the attach slot: the injected
	// text echoes back on the still-attached stream.
	if _, errb, code := run(t, "send", "--sock", sock, "--text", "sent-input", "--enter"); code != 0 {
		t.Fatalf("send exit %d: %s", code, errb)
	}
	seen.Reset()
	_ = conn2.SetReadDeadline(time.Now().Add(5 * time.Second))
	for !strings.Contains(seen.String(), "sent-input") {
		n, err := conn2.Read(buf)
		if n > 0 {
			seen.Write(buf[:n])
		}
		if err != nil {
			t.Fatalf("attach stream ended before send echo (saw %q): %v", seen.String(), err)
		}
	}
	_ = conn2.Close()

	// Attach still works after a client-initiated detach.
	waitFor(t, "attach after detach", func() bool {
		conn, err := attach(t, sock, 0, 0)
		if err != nil {
			return false
		}
		_ = conn.Close()
		return true
	})

	// Stop: exit 0, socket cleaned up.
	_, errb, code := run(t, "stop", "--sock", sock)
	if code != 0 {
		t.Fatalf("stop exit %d: %s", code, errb)
	}
	waitFor(t, "socket removal", func() bool {
		_, err := os.Stat(sock)
		return os.IsNotExist(err)
	})

	// Stopping again is not an error (already dead).
	_, _, code = run(t, "stop", "--sock", sock)
	if code != 0 {
		t.Fatalf("stop on dead socket exit %d (want 0)", code)
	}
}

func TestServeReportsExecFailure(t *testing.T) {
	dir := sockDir(t)
	sock := filepath.Join(dir, "s.sock")
	_, errb, code := run(t, "serve", "--sock", sock, "--", "/nonexistent-agent-binary")
	if code != 1 {
		t.Fatalf("exit %d (want 1): %s", code, errb)
	}
	if !strings.Contains(errb, "start agent") {
		t.Errorf("stderr misses the startup error: %q", errb)
	}
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Errorf("socket left behind after failed start")
	}
}

func TestServeTakesOverStaleSocket(t *testing.T) {
	dir := sockDir(t)
	sock := filepath.Join(dir, "s.sock")
	// Leftover of a SIGKILLed bridge: a socket file nothing listens on.
	if err := os.WriteFile(sock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	startServe(t, sock, nil, "sh", "-c", "exec cat")
	defer run(t, "stop", "--sock", sock)
	if snap := dumpSnapshot(t, sock); snap.Error != "" {
		t.Fatalf("snapshot error: %s", snap.Error)
	}
}

func TestServeMirrorsExitAndClosesStream(t *testing.T) {
	dir := sockDir(t)
	sock := filepath.Join(dir, "s.sock")
	wait := startServe(t, sock, nil, "sh", "-c", "sleep 0.4; exit 7")
	conn, err := attach(t, sock, 0, 0)
	if err != nil {
		t.Fatalf("attach refused: %v", err)
	}
	defer func() { _ = conn.Close() }()
	// The stream must end (EOF) once the agent exits.
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.Copy(io.Discard, conn); err != nil {
		t.Fatalf("attach stream: %v (want clean EOF)", err)
	}
	if code := wait(); code != 7 {
		t.Fatalf("serve exit %d (want 7, mirroring the agent)", code)
	}
}

// With no terminal attached, the bridge answers the agent's queries the way a
// terminal would; agents otherwise wait out a timeout at startup.
func TestServeAnswersQueriesWhileDetached(t *testing.T) {
	sock := filepath.Join(sockDir(t), "s.sock")
	script := `stty raw -echo; printf '\033[c'; r=$(head -c 9 | tr '\033' E); printf '\r\ngot %s\r\n' "$r"; exec cat`
	startServe(t, sock, nil, "sh", "-c", script)
	defer run(t, "stop", "--sock", sock)
	waitFor(t, "the device attributes reply", func() bool {
		return strings.Contains(dumpSnapshot(t, sock).Screen, "got E[?62;22c")
	})
}

// An attached terminal answers for itself, so the bridge must stay quiet or
// the agent reads two replies.
func TestServeLeavesQueriesToAnAttachedTerminal(t *testing.T) {
	sock := filepath.Join(sockDir(t), "s.sock")
	script := `stty raw -echo; head -c 1 >/dev/null; printf '\033[c'; b=$(head -c 1 | od -An -c | tr -d ' '); printf '\r\nfirst %s\r\n' "$b"; exec cat`
	startServe(t, sock, nil, "sh", "-c", script)
	defer run(t, "stop", "--sock", sock)
	conn, err := attach(t, sock, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	readStream(t, conn, "\x1b[c")
	time.Sleep(200 * time.Millisecond) // room for a wrong reply to arrive first
	if _, err := conn.Write([]byte("Z")); err != nil {
		t.Fatal(err)
	}
	readStream(t, conn, "first Z")
}

// readStream reads the attach stream until want appears.
func readStream(t *testing.T, conn net.Conn, want string) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var got []byte
	buf := make([]byte, 4096)
	for !strings.Contains(string(got), want) {
		n, err := conn.Read(buf)
		got = append(got, buf[:n]...)
		if err != nil {
			t.Fatalf("stream ended before %q (err %v, got %q)", want, err, got)
		}
	}
}

// Starting a second bridge on a live socket must not orphan the first agent.
func TestServeRefusesALiveSocket(t *testing.T) {
	sock := filepath.Join(sockDir(t), "s.sock")
	startServe(t, sock, nil, "sh", "-c", "exec cat")
	defer run(t, "stop", "--sock", sock)
	_, errb, code := run(t, "serve", "--sock", sock, "--", "sh", "-c", "exec cat")
	if code == 0 || !strings.Contains(errb, "another running bridge") {
		t.Fatalf("second serve exit %d, stderr %q; want a refusal", code, errb)
	}
	if snap := dumpSnapshot(t, sock); snap.Error != "" {
		t.Fatalf("first bridge no longer answers: %s", snap.Error)
	}
}

// A socket deleted under a running bridge, as when its runtime directory is
// cleaned, comes back, so callers do not take the session for dead.
func TestServeRecreatesARemovedSocket(t *testing.T) {
	dir := filepath.Join(sockDir(t), "run")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "s.sock")
	startServe(t, sock, nil, "sh", "-c", "exec cat")
	defer run(t, "stop", "--sock", sock)
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the socket to come back", func() bool {
		conn, err := net.DialTimeout("unix", sock, 100*time.Millisecond)
		if err != nil {
			return false
		}
		_ = conn.Close()
		return true
	})
	if snap := dumpSnapshot(t, sock); snap.Error != "" {
		t.Fatalf("snapshot error: %s", snap.Error)
	}
}

// A bridge whose socket was replaced by another one's leaves the replacement
// in place when it exits.
func TestServeLeavesAReplacedSocketOnExit(t *testing.T) {
	sock := filepath.Join(sockDir(t), "s.sock")
	cmd, wait := startServeCmd(t, sock, nil, "sh", "-c", "exec cat")
	if err := os.Remove(sock); err != nil {
		t.Fatal(err)
	}
	other, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	other.(*net.UnixListener).SetUnlinkOnClose(false)
	defer func() { _ = other.Close() }()
	time.Sleep(1500 * time.Millisecond) // past a socket check, which must not steal the path
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	wait()
	conn, err := net.DialTimeout("unix", sock, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("the replacement socket is gone: %v", err)
	}
	_ = conn.Close()
}

func TestServeRecordsTheAgentsExit(t *testing.T) {
	dir := sockDir(t)
	sock := filepath.Join(dir, "s.sock")
	exitFile := filepath.Join(dir, "exit.json")
	if err := os.WriteFile(exitFile, []byte(`{"code":99}`), 0o600); err != nil {
		t.Fatal(err)
	}
	wait := startServe(t, sock, []string{"--exit-file", exitFile},
		"sh", "-c", "printf 'error: no conversation found\\n'; head -c 1 >/dev/null; exit 3")
	if _, err := os.Stat(exitFile); !os.IsNotExist(err) {
		t.Fatalf("a previous run's exit file is still there while serving (stat: %v)", err)
	}
	waitFor(t, "agent output", func() bool {
		return strings.Contains(dumpSnapshot(t, sock).Screen, "no conversation found")
	})
	if _, errb, code := run(t, "send", "--sock", sock, "--text", "x", "--enter"); code != 0 {
		t.Fatalf("send exit %d: %s", code, errb)
	}
	if code := wait(); code != 3 {
		t.Fatalf("serve exit %d, want 3", code)
	}
	data, err := os.ReadFile(exitFile)
	if err != nil {
		t.Fatal(err)
	}
	var rec sbridge.Exit
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatalf("exit file %q: %v", data, err)
	}
	if rec.Code != 3 || !strings.Contains(rec.Screen, "error: no conversation found") || !rec.QuitOnItsOwn() {
		t.Errorf("exit record %+v, want code 3, the final screen and an agent that ended on its own", rec)
	}
}

// An agent the bridge ended, by a stop request or a signal it forwarded, is
// recorded as such, so the daemon brings it back rather than taking it as a quit.
func TestServeRecordsThatTheBridgeEndedTheAgent(t *testing.T) {
	for _, tc := range []struct {
		name string
		end  func(t *testing.T, serve *exec.Cmd, sock string)
	}{
		{"stop", func(t *testing.T, _ *exec.Cmd, sock string) {
			if _, errb, code := run(t, "stop", "--sock", sock); code != 0 {
				t.Fatalf("stop exit %d: %s", code, errb)
			}
		}},
		{"SIGTERM", func(t *testing.T, serve *exec.Cmd, _ string) {
			if err := serve.Process.Signal(syscall.SIGTERM); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := sockDir(t)
			sock := filepath.Join(dir, "s.sock")
			exitFile := filepath.Join(dir, "exit.json")
			serve, wait := startServeCmd(t, sock, []string{"--exit-file", exitFile}, "sh", "-c", "printf 'ready\\n'; exec cat")
			waitFor(t, "agent output", func() bool { return strings.Contains(dumpSnapshot(t, sock).Screen, "ready") })
			tc.end(t, serve, sock)
			wait()
			data, err := os.ReadFile(exitFile)
			if err != nil {
				t.Fatal(err)
			}
			var rec sbridge.Exit
			if err := json.Unmarshal(data, &rec); err != nil {
				t.Fatalf("exit file %q: %v", data, err)
			}
			if rec.EndedByBridge == nil || !*rec.EndedByBridge {
				t.Errorf("exit record %s, want ended_by_bridge", data)
			}
		})
	}
}

// The agent's background colour reaches a terminal that attaches after it was
// set, and is taken back when that terminal detaches.
func TestAttachReplaysAndResetsTheBackgroundColour(t *testing.T) {
	sock := filepath.Join(sockDir(t), "s.sock")
	startServe(t, sock, nil, "sh", "-c", `printf '\033]11;#102030\007ready\r\n'; exec cat`)
	waitFor(t, "agent output", func() bool { return strings.Contains(dumpSnapshot(t, sock).Screen, "ready") })

	cmd := exec.Command(binPath, "bridge", "attach", "--sock", sock)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var out []byte
	buf := make([]byte, 4096)
	for !strings.Contains(string(out), "ready") {
		n, err := stdout.Read(buf)
		out = append(out, buf[:n]...)
		if err != nil {
			t.Fatalf("attach output ended before the repaint: %q, %v", out, err)
		}
	}
	if _, errb, code := run(t, "stop", "--sock", sock); code != 0 {
		t.Fatalf("stop exit %d: %s", code, errb)
	}
	rest, _ := io.ReadAll(stdout)
	if err := cmd.Wait(); err != nil {
		t.Fatalf("attach: %v", err)
	}
	out = append(out, rest...)
	got := string(out)
	if !strings.HasPrefix(got, "\x1b]11;#102030\x07\x1b[2J") {
		t.Errorf("attach output %q does not start with the background colour", got)
	}
	if !strings.HasSuffix(got, "\x1b]111\x07") {
		t.Errorf("attach output %q does not end with a background reset", got)
	}
}
