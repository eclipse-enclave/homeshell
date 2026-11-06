package hostbridge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
	"github.com/eclipsesource/homeshell/daemon/internal/sbridge"
	"github.com/eclipsesource/homeshell/daemon/internal/sbridge/sbridgetest"
)

// fakeBridge stands in for the executable Runtime runs as the bridge: a
// shell script that logs its argv and runs script, so the exec contract is
// testable without a real bridge.
func fakeBridge(t *testing.T, r *Runtime, script string) (logFile string) {
	t.Helper()
	dir := t.TempDir()
	logFile = filepath.Join(dir, "calls.log")
	full := "#!/bin/sh\necho \"$@\" >> " + logFile + "\n" + script
	r.exe = filepath.Join(dir, "homeshell")
	if err := os.WriteFile(r.exe, []byte(full), 0o755); err != nil {
		t.Fatal(err)
	}
	return logFile
}

// callWithPrefix waits for the logged call starting with prefix and returns it.
// The detached serve process and the dump readiness probe append to the same
// log concurrently, so the order they land in is not fixed — indexing the log
// positionally is flaky under load. Polling also covers the window where Start
// has already returned (the probe answered) but the serve child has not
// reached its own echo yet.
func callWithPrefix(t *testing.T, logFile, prefix string) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var logged []string
	for {
		data, err := os.ReadFile(logFile)
		if err == nil {
			logged = strings.Split(strings.TrimSpace(string(data)), "\n")
		}
		for _, call := range logged {
			if strings.HasPrefix(call, prefix) {
				return call
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no %q call logged; got %q", prefix, logged)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func testBridgeRuntime(t *testing.T) *Runtime {
	t.Helper()
	return &Runtime{runDir: sbridgetest.TempDir(t)}
}

// On Linux the bridge runs the daemon's loaded image, not whatever file an
// upgrade has since put at its path.
func TestBridgeRuntimeServesFromTheRunningImage(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	wantPath := self
	if runtime.GOOS == "linux" {
		wantPath = "/proc/self/exe"
	}
	tests := []struct {
		name     string
		exe      string
		wantPath string
		wantArg0 string
	}{
		{"own executable", "", wantPath, self},
		{"configured executable", "/opt/fake-bridge", "/opt/fake-bridge", "/opt/fake-bridge"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd, err := Runtime{exe: tt.exe}.serveCommand("--sock", "s")
			if err != nil {
				t.Fatal(err)
			}
			if cmd.Path != tt.wantPath || cmd.Args[0] != tt.wantArg0 {
				t.Errorf("serve command runs %q as %q, want %q as %q", cmd.Path, cmd.Args[0], tt.wantPath, tt.wantArg0)
			}
			if got := strings.Join(cmd.Args[1:], " "); got != "bridge serve --sock s" {
				t.Errorf("serve args = %q", got)
			}
		})
	}
}

func TestBridgeRuntimeStart(t *testing.T) {
	// serve is long-running (foreground daemon); dump answers, which is the
	// readiness probe Start polls.
	r := testBridgeRuntime(t)
	logFile := fakeBridge(t, r, `case "$2" in serve) sleep 2;; esac; exit 0`)
	go func() {
		callWithPrefix(t, logFile, "bridge serve ")
		sbridgetest.Serve(t, r.sock("hs-abc"), func() string { return `{}` })
	}()
	if err := r.Start(context.Background(), "hs-abc", "/work/dir", []string{"claude", "--model", "sonnet"}); err != nil {
		t.Fatal(err)
	}
	got := callWithPrefix(t, logFile, "bridge serve ")
	want := "bridge serve --sock " + r.sock("hs-abc") + " --exit-file " + filepath.Join(r.runDir, "hs-abc.exit.json") + " --dir /work/dir -- claude --model sonnet"
	if got != want {
		t.Errorf("argv\n got %q\nwant %q", got, want)
	}
}

// A start on a target a live bridge still serves must not unlink its socket:
// serve refuses to take it over, and an unlinked socket leaves that agent
// unreachable.
// The live bridge also answers the readiness probe, so Start has to refuse up
// front rather than report the launch serve refused as a success.
func TestBridgeRuntimeStartLeavesALiveSocket(t *testing.T) {
	r := testBridgeRuntime(t)
	logFile := fakeBridge(t, r, `case "$2" in serve) sleep 2;; esac; exit 0`)
	dumps := sbridgetest.Serve(t, r.sock("hs-abc"), func() string { return `{}` })
	err := r.Start(context.Background(), "hs-abc", "/work", []string{"claude"})
	if err == nil || !strings.Contains(err.Error(), "served by another running bridge") {
		t.Errorf("Start on a live socket = %v, want a refusal", err)
	}
	if logged, readErr := os.ReadFile(logFile); readErr == nil {
		t.Errorf("serve launched beside a live bridge: %s", logged)
	}
	if r.Probe(context.Background(), "hs-abc") != nil || dumps.Load() == 0 {
		t.Error("the live bridge's socket no longer answers after Start")
	}
}

func TestBridgeRuntimeStartSurfacesChildError(t *testing.T) {
	r := testBridgeRuntime(t)
	fakeBridge(t, r, `echo "sbridge: serve: start agent: exec: claude: not found" >&2; exit 1`)
	err := r.Start(context.Background(), "hs-abc", "/work", []string{"claude"})
	if err == nil || !strings.Contains(err.Error(), "claude: not found") {
		t.Fatalf("child error not surfaced: %v", err)
	}
}

// An agent that exits at once explains itself on its PTY, not on serve's
// stderr, so the start error quotes the exit record.
func TestBridgeRuntimeStartQuotesTheAgentsExit(t *testing.T) {
	r := testBridgeRuntime(t)
	fakeBridge(t, r, `printf '{"code":2,"screen":"Usage: claude [options]\\n\\nerror: unknown option --bogus\\n\\n"}' > "$6"; exit 2`)
	err := r.Start(context.Background(), "hs-abc", "/work", []string{"claude", "--bogus"})
	if err == nil || !strings.Contains(err.Error(), "agent exited with status 2: Usage: claude [options] / error: unknown option --bogus") {
		t.Fatalf("agent's exit not quoted: %v", err)
	}
}

// A deliberate stop leaves no exit record to report the session by.
func TestBridgeRuntimeStopClearsTheExitRecord(t *testing.T) {
	r := testBridgeRuntime(t)
	record := filepath.Join(r.runDir, "hs-abc.exit.json")
	if err := os.WriteFile(record, []byte(`{"code":0}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := r.Stop(context.Background(), "hs-abc"); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.LastExit("hs-abc", ""); ok {
		t.Error("exit record still reported after Stop")
	}
}

// A caller's long deadline does not stretch the agent's grace past StopTimeout.
func TestBridgeRuntimeStopCapsTheGrace(t *testing.T) {
	r := testBridgeRuntime(t)
	bridge := sbridgetest.Bridge(t, r.sock("hs-abc"), func() string { return `{}` })
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := r.Stop(ctx, "hs-abc"); err != nil {
		t.Fatal(err)
	}
	got := bridge.Requests()
	if len(got) != 1 {
		t.Fatalf("requests = %+v, want one stop", got)
	}
	var grace int
	if _, err := fmt.Sscanf(got[0].Line, "sbridge-1 stop %d", &grace); err != nil || time.Duration(grace)*time.Millisecond > sbridge.StopTimeout {
		t.Errorf("stop line %q, want a grace within %v", got[0].Line, sbridge.StopTimeout)
	}
}

// A session started under another run dir has its exit record there.
func TestBridgeRuntimeLastExitReadsTheRecordedSocket(t *testing.T) {
	r := testBridgeRuntime(t)
	recorded := filepath.Join(t.TempDir(), "hs-abc.sock")
	if err := os.WriteFile(exitFile(recorded), []byte(`{"code":4}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exitFile(r.sock("hs-abc")), []byte(`{"code":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if rec, ok := r.LastExit("hs-abc", recorded); !ok || rec.Code != 4 {
		t.Errorf("LastExit at the recorded socket = %+v, %v; want status 4", rec, ok)
	}
	if rec, ok := r.LastExit("hs-abc", ""); !ok || rec.Code != 1 {
		t.Errorf("LastExit without a recorded socket = %+v, %v; want status 1", rec, ok)
	}
}

func TestBridgeRuntimeStartTimesOutWhenNeverReady(t *testing.T) {
	// serve stays up but the socket never answers a dump probe.
	r := testBridgeRuntime(t)
	fakeBridge(t, r, `case "$2" in serve) sleep 2; exit 0;; esac; exit 1`)
	r.readyTimeout = 150 * time.Millisecond
	err := r.Start(context.Background(), "hs-abc", "/work", []string{"claude"})
	if err == nil || !strings.Contains(err.Error(), "not ready") {
		t.Fatalf("readiness timeout not surfaced: %v", err)
	}
}

// TestBridgeRuntimeStartTimeoutIgnoresSurvivingGrandchild pins the bound on the
// timeout path. `serve` inherits its stderr pipe to whatever it spawns, and
// os/exec's Wait only returns once that pipe hits EOF — so killing the serve
// leader is not enough if a descendant still holds the write end. Unbounded,
// Start would sit here for the descendant's whole lifetime while holding the
// provider lock, deadlocking every session call. The fake's serve leaves a
// long-lived grandchild behind and exits, exactly that shape.
func TestBridgeRuntimeStartTimeoutIgnoresSurvivingGrandchild(t *testing.T) {
	r := testBridgeRuntime(t)
	fakeBridge(t, r, `case "$2" in serve) sleep 30 & exit 0;; esac; exit 1`)
	r.readyTimeout = 150 * time.Millisecond

	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- r.Start(context.Background(), "hs-abc", "/work", []string{"claude"}) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Start succeeded; want a readiness failure")
		}
		// Generous vs. readyTimeout + sbridgeReapDelay, but far below the
		// grandchild's 30s: unbounded is what this guards against.
		if elapsed := time.Since(start); elapsed > 10*time.Second {
			t.Errorf("Start took %v; it waited out the surviving grandchild", elapsed)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Start never returned; it is waiting out the surviving grandchild")
	}
}

// TestSocketNamespaceFollowsHomeDir pins that -home-dir moves the socket
// namespace the way it already moves the session store. Two daemons with
// different home directories keep separate stores, so sharing one socket
// directory would let them collide on a target name and answer each other's
// liveness probes.
func TestSocketNamespaceFollowsHomeDir(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")

	one := New("/home/dev/.local/share/homeshell")
	two := New("/home/dev/other-home")
	if one.runDir == two.runDir {
		t.Errorf("different home dirs share the socket namespace %q", one.runDir)
	}
	if again := New("/home/dev/other-home"); again.runDir != two.runDir {
		t.Errorf("namespace is not stable: %q then %q", two.runDir, again.runDir)
	}
	// sbridge binds directly only below its own conservative 100-byte bound
	// (proto.maxSunPath); past that it silently falls back to chdir-ing to the
	// socket's directory, so exceeding it is a behavior change, not just a
	// theoretical brush with the 108-byte kernel limit.
	if got := len(one.sock("hs-11111111-2222-3333-4444-555555555555")); got > 100 {
		t.Errorf("socket path is %d bytes, past sbridge's direct-bind bound", got)
	}
}

// TestLocateElsewhereBranches drives the real runtime, not the provider's fake.
// Every recovery path now rests on this one method — a false negative resumes a
// live agent into a directory that already has one, and a false positive stops
// Stop, Remove and Open from ever reaching a genuinely dead session.
func TestLocateElsewhereBranches(t *testing.T) {
	runtimeDir := sbridgetest.TempDir(t)
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	r := New("/home/dev/.local/share/homeshell")
	live := filepath.Join(runtimeDir, "somewhere-else", "hs-abc.sock")

	// Both the recorded socket and the *current* one answer. The current one
	// matters for the third case: it is what makes dropping the recorded ==
	// current skip observable, rather than the probe failing for its own reasons.
	sbridgetest.Serve(t, live, func() string { return `{}` })
	sbridgetest.Serve(t, r.sock("hs-abc"), func() string { return `{}` })

	t.Run("recorded socket still answers", func(t *testing.T) {
		// The location itself is the result, not just the fact of one: Stop,
		// Remove and Open address whatever comes back here.
		if got := r.LocateElsewhere(context.Background(), "hs-abc", live); got != live {
			t.Errorf("located %q, want the recorded socket %q", got, live)
		}
	})
	t.Run("recorded socket is dead too", func(t *testing.T) {
		dead := filepath.Join(runtimeDir, "somewhere-else", "hs-gone.sock")
		if r.LocateElsewhere(context.Background(), "hs-gone", dead) != "" {
			t.Error("nothing answers anywhere; the session must be resumable")
		}
	})
	t.Run("recorded equals current", func(t *testing.T) {
		// The probe that already failed is the one this would repeat, so it must
		// not be treated as evidence of life — otherwise a plainly dead session
		// is never resumed, stopped or removed.
		current := r.sock("hs-abc")
		if r.LocateElsewhere(context.Background(), "hs-abc", current) != "" {
			t.Errorf("recorded == current (%q) reported alive elsewhere", current)
		}
	})
}

// TestLocateElsewhereChecksLegacyPathForUnrecordedSessions covers the upgrade
// itself: a session started before locations were recorded has no socket in its
// store record, and its bridge is still serving under the old un-namespaced
// directory. Without this lookup the first start after the upgrade would call
// every such session dead and resume a second agent for each.
func TestLocateElsewhereChecksLegacyPathForUnrecordedSessions(t *testing.T) {
	runtimeDir := sbridgetest.TempDir(t)
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	legacy := filepath.Join(runtimeDir, "homeshell", "hs-abc.sock")

	// A fake that answers a dump only for the legacy socket path.
	sbridgetest.Serve(t, legacy, func() string { return `{}` })
	r := New("/home/dev/.local/share/homeshell")

	// The legacy path is returned, not merely detected — an unrecorded session
	// is exactly the population Stop, Remove and Open used to mis-address.
	if got := r.LocateElsewhere(context.Background(), "hs-abc", ""); got != legacy {
		t.Errorf("located %q, want the pre-namespacing path %q", got, legacy)
	}
	if r.LocateElsewhere(context.Background(), "hs-other", "") != "" {
		t.Error("a target with no socket anywhere must not be reported alive")
	}
}

func TestBridgeRuntimeStateCapturesUnclassifiedAgent(t *testing.T) {
	payload := `{"screen":"captured output\n","osc_title":"Custom session\n","osc_progress":null}`
	r := testBridgeRuntime(t)
	sbridgetest.Serve(t, r.sock("hs-abc"), func() string { return payload })
	res, err := r.State(context.Background(), "custom-agent", "hs-abc")
	if err != nil {
		t.Fatal(err)
	}
	if res.State != domain.SessionUnknown || res.Confident || res.Title != "Custom session" || res.Tail != "captured output" {
		t.Fatalf("unexpected unclassified capture result: %+v", res)
	}
}

func TestDetectionSnapshotCarriesEverySignal(t *testing.T) {
	progress := "4;0"
	got := detectionSnapshot("agent", sbridge.Snapshot{
		Screen: "screen", OSCTitle: "title\n", OSCProgress: &progress,
	})
	if got.Agent != "agent" || got.Screen != "screen" || got.OSCTitle != "title" || got.OSCProgress != progress {
		t.Errorf("detection snapshot = %+v", got)
	}
}

func TestBridgeRuntimeStateBrokenCapture(t *testing.T) {
	r := testBridgeRuntime(t)
	sbridgetest.Serve(t, r.sock("hs-abc"), func() string { return `{"error":"terminal emulation failed"}` })
	if _, err := r.State(context.Background(), "claude", "hs-abc"); err == nil {
		t.Fatal("broken capture not reported")
	}
}

// Only a missing bridge may be resumed; one that accepts but never answers is unknown.
func TestBridgeRuntimeProbeTellsSilentFromAbsent(t *testing.T) {
	r := testBridgeRuntime(t)
	if err := r.Probe(context.Background(), "hs-absent"); !errors.Is(err, sbridge.ErrNotServing) {
		t.Fatalf("absent bridge: %v, want ErrNotServing", err)
	}
	sbridgetest.Listen(t, r.sock("hs-silent"), func(conn net.Conn) { _, _ = io.Copy(io.Discard, conn) })
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := r.Probe(ctx, "hs-silent"); err == nil || errors.Is(err, sbridge.ErrNotServing) {
		t.Fatalf("silent bridge: %v, want an unknown error", err)
	}
}

func TestBridgeRuntimeProbeAndStop(t *testing.T) {
	r := testBridgeRuntime(t)
	bridge := sbridgetest.Bridge(t, r.sock("hs-abc"), func() string { return `{}` })
	if err := r.Probe(context.Background(), "hs-abc"); err != nil {
		t.Fatalf("probe should use the socket snapshot: %v", err)
	}
	if err := r.Stop(context.Background(), "hs-abc"); err != nil {
		t.Fatal(err)
	}
	if got := bridge.Requests(); len(got) != 1 || !strings.HasPrefix(got[0].Line, "sbridge-1 stop ") {
		t.Fatalf("requests = %+v, want one stop", got)
	}
}

func TestBridgeRuntimeSendInput(t *testing.T) {
	r := testBridgeRuntime(t)
	bridge := sbridgetest.Bridge(t, r.sock("hs-abc"), func() string { return `{}` })
	if err := r.SendInput(context.Background(), "hs-abc", "fix the tests"); err != nil {
		t.Fatal(err)
	}
	if err := r.SendKey(context.Background(), "hs-abc", "up"); err != nil {
		t.Fatal(err)
	}
	got := bridge.Requests()
	if len(got) != 2 || got[0].Input != "fix the tests\r" || got[1].Input != "\x1b[A" {
		t.Errorf("requests = %+v, want the text with Enter, then the up key", got)
	}
}

func TestBridgeRuntimeSendInputToADeadBridge(t *testing.T) {
	r := testBridgeRuntime(t)
	if err := r.SendInput(context.Background(), "hs-abc", "hello"); !errors.Is(err, sbridge.ErrNotServing) {
		t.Fatalf("send to a dead bridge: %v, want ErrNotServing", err)
	}
}

func TestBridgeRuntimeAttachCommand(t *testing.T) {
	r := testBridgeRuntime(t)
	argv := r.AttachCommand("hs-abc")
	// The terminal emulator has its own PATH, so attach names this executable
	// by path.
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if argv[0] != exe {
		t.Errorf("attach runs %q, want this executable %q", argv[0], exe)
	}
	if got, want := strings.Join(argv[1:], " "), "bridge attach --sock "+r.sock("hs-abc"); got != want {
		t.Errorf("attach args %q, want %q", got, want)
	}
}

// The byte spelling of each key now lives in the provider package, shared with
// the Enclave provider; provider.TestSessionKeyBytesCoverSessionKeys holds it to
// the normalized set.

// Polling, input and stop talk to the socket; only Start runs a process.
func TestOnlyStartRunsTheBridgeExecutable(t *testing.T) {
	r := testBridgeRuntime(t)
	logFile := fakeBridge(t, r, "exit 0")
	ctx := context.Background()
	sbridgetest.Bridge(t, r.sock("hs-test"), func() string { return `{"screen":"live","osc_title":"hello","osc_progress":null}` })
	if _, err := r.State(ctx, "custom", "hs-test"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Snapshot(ctx, "hs-test"); err != nil {
		t.Fatal(err)
	}
	if err := r.SendInput(ctx, "hs-test", "hello"); err != nil {
		t.Fatal(err)
	}
	if err := r.SendKey(ctx, "hs-test", "enter"); err != nil {
		t.Fatal(err)
	}
	if err := r.Stop(ctx, "hs-test"); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(logFile); err == nil {
		t.Errorf("the bridge executable ran: %q", data)
	}
}

func TestAvailable(t *testing.T) {
	r := testBridgeRuntime(t)
	err := r.Available(context.Background(), "sh")
	if err != nil {
		t.Errorf("Available(sh) = %v", err)
	}
	if err := r.Available(context.Background(), "no-such-agent-binary"); err == nil || !strings.Contains(err.Error(), "not found on PATH") {
		t.Errorf("Available(missing agent) = %v", err)
	}
}
