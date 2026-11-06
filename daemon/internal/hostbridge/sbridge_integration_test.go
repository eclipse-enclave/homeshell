package hostbridge

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/sbridge/sbridgetest"
)

// TestBridgeRuntimeIntegration exercises the whole bridge chain at once with
// the real bridge, this test binary run as `bridge serve` (TestMain): Start spawns a Setsid-detached serve and returns
// once the socket answers, State reads a dump rendered by the real VT
// emulator and exposes its terminal preview, attach clients
// stream the live screen (a second attach takes the session over — the
// behavior Open relies on to move a session into a fresh terminal window),
// and Stop tears the session down. The scripted agent draws stable terminal
// content, so the test covers PTY → emulator → snapshot → preview without
// depending on any particular agent rule. Detection-rule behavior is covered
// independently with synthetic manifests.
func TestBridgeRuntimeIntegration(t *testing.T) {
	ctx := context.Background()
	rt := Runtime{runDir: t.TempDir()}
	target := "hs-bridge-it"
	dir := t.TempDir()

	script := `printf 'content-above-box\n────────\n❯\n────────\nfooter\n'; exec sleep 60`
	if err := rt.Start(ctx, target, dir, []string{"sh", "-c", script}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = rt.Stop(ctx, target) }()

	if err := rt.Probe(ctx, target); err != nil {
		t.Fatalf("Probe right after Start returned: %v", err)
	}

	// The screen needs a moment to travel PTY → emulator; poll State until the
	// generic preview carries the content line.
	deadline := time.Now().Add(5 * time.Second)
	for {
		res, err := rt.State(ctx, "custom-agent", target)
		if err == nil && strings.Contains(res.Tail, "content-above-box") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("State never captured a terminal preview: res=%+v err=%v", res, err)
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Drive the attach command for real (headless: stdin is /dev/null, so
	// only the output stream is exercised). The initial repaint delivers the
	// current screen.
	argv := rt.AttachCommand(target)
	var outA syncBuffer
	attachA := exec.Command(argv[0], argv[1:]...)
	attachA.Stdout = &outA
	if err := attachA.Start(); err != nil {
		t.Fatalf("first attach: %v", err)
	}
	waitForOutput(t, &outA, "content-above-box", "first attach repaint")

	// A second attach takes the session over (last attach wins): the first
	// client gets the farewell line and exits 0, the new one the repaint.
	// Open depends on this to move a session into a fresh terminal window.
	var outB syncBuffer
	attachB := exec.Command(argv[0], argv[1:]...)
	attachB.Stdout = &outB
	if err := attachB.Start(); err != nil {
		t.Fatalf("second attach: %v", err)
	}
	doneA := make(chan error, 1)
	go func() { doneA <- attachA.Wait() }()
	select {
	case err := <-doneA:
		if err != nil {
			t.Fatalf("replaced attach client exited with error: %v", err)
		}
	case <-time.After(5 * time.Second):
		_ = attachA.Process.Kill()
		t.Fatal("first attach client was not detached by the takeover")
	}
	if !strings.Contains(outA.String(), "taken over by a new client") {
		t.Errorf("replaced client output %q, want the takeover farewell", outA.String())
	}
	waitForOutput(t, &outB, "content-above-box", "takeover repaint")

	// SendInput injects text over the socket; the PTY's echo puts it on the
	// screen (below the box, i.e. in the region the preview strips), so
	// delivery is asserted on the raw dump snapshot.
	if err := rt.SendInput(ctx, target, "typed-by-sendinput"); err != nil {
		t.Fatalf("SendInput: %v", err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for {
		snap, err := rt.Snapshot(ctx, target)
		if err == nil && strings.Contains(snap.Screen, "typed-by-sendinput") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("sent input never reached the screen: %q, %v", snap.Screen, err)
		}
		time.Sleep(50 * time.Millisecond)
	}

	if err := rt.Stop(ctx, target); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for rt.Probe(ctx, target) == nil {
		if time.Now().After(deadline) {
			t.Fatal("still alive after Stop")
		}
		time.Sleep(50 * time.Millisecond)
	}
	// The surviving attach client ends with the bridge.
	doneB := make(chan error, 1)
	go func() { doneB <- attachB.Wait() }()
	select {
	case <-doneB:
	case <-time.After(5 * time.Second):
		_ = attachB.Process.Kill()
		t.Fatal("attached client did not end with the bridge")
	}
	// Stopping an already-dead target must stay a no-op (tmux parity).
	if err := rt.Stop(ctx, target); err != nil {
		t.Fatalf("Stop on dead target: %v", err)
	}
}

// An agent that exits on its own explained itself on its PTY; LastExit reads
// that from the real bridge's exit record.
func TestBridgeRuntimeIntegrationRecordsTheAgentsExit(t *testing.T) {
	rt := Runtime{runDir: sbridgetest.TempDir(t)}
	// Start fails or succeeds depending on whether the probe beat the exit.
	_ = rt.Start(context.Background(), "hs-exit-it", t.TempDir(), []string{"sh", "-c", "echo 'error: no conversation found'; exit 3"})
	deadline := time.Now().Add(5 * time.Second)
	rec, ok := rt.LastExit("hs-exit-it", "")
	for !ok && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		rec, ok = rt.LastExit("hs-exit-it", "")
	}
	if !ok || rec.Code != 3 || !strings.Contains(rec.Screen, "error: no conversation found") {
		t.Errorf("LastExit = %+v, %v; want status 3 and the final screen", rec, ok)
	}
}

// syncBuffer is a goroutine-safe stdout sink for the attach clients: exec.Cmd
// writes from its copier goroutine while the test polls String.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// waitForOutput polls buf until want appears.
func waitForOutput(t *testing.T, buf *syncBuffer, want, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(buf.String(), want) {
		if time.Now().After(deadline) {
			t.Fatalf("%s: %q never appeared in %q", what, want, buf.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
}
