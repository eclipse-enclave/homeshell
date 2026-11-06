package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/daemonstate"
	"github.com/eclipsesource/homeshell/daemon/internal/persist"
)

// stubDiscover replaces discoverPhase with a function returning the phases in
// order (repeating the last one), and restores it via t.Cleanup.
func stubDiscover(t *testing.T, phases ...daemonPhase) *[]daemonstate.State {
	t.Helper()
	orig := discoverPhase
	t.Cleanup(func() { discoverPhase = orig })
	calls := 0
	returned := &[]daemonstate.State{}
	discoverPhase = func(context.Context) (daemonstate.State, daemonPhase, error) {
		p := phases[len(phases)-1]
		if calls < len(phases) {
			p = phases[calls]
		}
		calls++
		st := daemonstate.State{}
		if p == daemonRunning {
			st.Address = "127.0.0.1:8420"
		}
		*returned = append(*returned, st)
		return st, p, nil
	}
	return returned
}

// spawnCall records one spawnDaemon invocation, so tests can assert both that a
// spawn happened and which flags it would have forwarded.
type spawnCall struct {
	called  bool
	homeDir string
	addr    string
}

// stubSpawn replaces spawnDaemon with a recorder and returns the record; it
// restores the original via t.Cleanup. The nil *spawnedDaemon it hands back
// stands in for "no real child" — died() then blocks forever, so waitForRunning
// behaves as it does for a daemon that is still starting up.
func stubSpawn(t *testing.T) *spawnCall {
	t.Helper()
	orig := spawnDaemon
	t.Cleanup(func() { spawnDaemon = orig })
	call := &spawnCall{}
	spawnDaemon = func(homeDir, addr string) (*spawnedDaemon, error) {
		call.called, call.homeDir, call.addr = true, homeDir, addr
		return nil, nil
	}
	return call
}

// stubProcessAlive forces processAlive to report the given liveness, restoring
// the original via t.Cleanup.
func stubProcessAlive(t *testing.T, alive bool) {
	t.Helper()
	orig := processAlive
	t.Cleanup(func() { processAlive = orig })
	processAlive = func(int) bool { return alive }
}

func TestEnsureRunningAttachesWithoutSpawning(t *testing.T) {
	stubDiscover(t, daemonRunning)
	spawned := stubSpawn(t)

	st, didSpawn, err := ensureRunning(context.Background(), "", "")
	if err != nil {
		t.Fatalf("ensureRunning: %v", err)
	}
	if didSpawn || spawned.called {
		t.Fatalf("attached daemon should not spawn (didSpawn=%v spawned=%v)", didSpawn, spawned.called)
	}
	if got := daemonURL(st); got != "http://127.0.0.1:8420/" {
		t.Fatalf("daemonURL = %q, want http://127.0.0.1:8420/", got)
	}
}

func TestEnsureRunningSpawnsWhenAbsent(t *testing.T) {
	isolateStateFile(t) // isolate daemonstate.Remove()
	stubDiscover(t, daemonNone, daemonRunning)
	spawned := stubSpawn(t)

	st, didSpawn, err := ensureRunning(context.Background(), "", "")
	if err != nil {
		t.Fatalf("ensureRunning: %v", err)
	}
	if !spawned.called || !didSpawn {
		t.Fatalf("absent daemon should spawn (didSpawn=%v spawned=%v)", didSpawn, spawned.called)
	}
	if got := daemonURL(st); got != "http://127.0.0.1:8420/" {
		t.Fatalf("daemonURL = %q, want http://127.0.0.1:8420/", got)
	}
}

func TestEnsureRunningRefusesWhenStaleDaemonAlive(t *testing.T) {
	origDiscover := discoverPhase
	t.Cleanup(func() { discoverPhase = origDiscover })
	discoverPhase = func(context.Context) (daemonstate.State, daemonPhase, error) {
		return daemonstate.State{Address: "127.0.0.1:8420", PID: 4242}, daemonStale, nil
	}
	stubProcessAlive(t, true)
	spawned := stubSpawn(t)

	if _, _, err := ensureRunning(context.Background(), "", ""); err == nil {
		t.Fatal("ensureRunning: expected an error when a stale daemon is still alive")
	}
	if spawned.called {
		t.Fatal("ensureRunning must not spawn while a stale daemon still holds the port")
	}
}

func TestEnsureRunningSpawnsWhenStaleDaemonDead(t *testing.T) {
	isolateStateFile(t) // isolate daemonstate.Remove()
	stubDiscover(t, daemonStale, daemonRunning)
	stubProcessAlive(t, false)
	spawned := stubSpawn(t)

	st, didSpawn, err := ensureRunning(context.Background(), "", "")
	if err != nil {
		t.Fatalf("ensureRunning: %v", err)
	}
	if !spawned.called || !didSpawn {
		t.Fatalf("dead stale daemon should spawn (didSpawn=%v spawned=%v)", didSpawn, spawned.called)
	}
	if got := daemonURL(st); got != "http://127.0.0.1:8420/" {
		t.Fatalf("daemonURL = %q, want http://127.0.0.1:8420/", got)
	}
}

func TestEnsureRunningTimesOutWhenNeverReady(t *testing.T) {
	isolateStateFile(t)
	stubDiscover(t, daemonNone)
	stubSpawn(t)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, _, err := ensureRunning(ctx, "", ""); err == nil {
		t.Fatal("ensureRunning: expected a timeout error when the daemon never becomes ready")
	}
}

// A daemon that dies on startup used to be indistinguishable from a slow one:
// the caller waited out the whole timeout and the child's reason was discarded.
func TestWaitForRunningFailsFastWhenTheChildDies(t *testing.T) {
	isolateStateFile(t)
	stubDiscover(t, daemonNone)

	logPath := filepath.Join(t.TempDir(), spawnLogName)
	const bindError = "listen tcp 127.0.0.1:8420: bind: address already in use"
	if err := os.WriteFile(logPath, []byte(bindError+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	child := &spawnedDaemon{exited: make(chan struct{}), logPath: logPath, waitErr: errors.New("exit status 1")}
	close(child.exited)

	// A timeout far longer than the test may take, so a pass proves the error
	// came from the dead child rather than from the deadline.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	start := time.Now()
	_, err := waitForRunning(ctx, child)
	if err == nil {
		t.Fatal("waitForRunning: expected an error when the spawned daemon dies")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("waitForRunning took %s; it must fail as soon as the child dies", elapsed)
	}
	for _, want := range []string{"exited before it was ready", "exit status 1", bindError, logPath} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to contain %q", err, want)
		}
	}
}

// The log must land beside the data of the daemon being spawned. Resolving it
// from the package-level default instead only agreed with the -home-dir by
// accident, via runEnsureRunning setting that default first — so any other
// caller of ensureRunning got the log and the daemon's data in different places.
func TestSpawnLogFollowsTheHomeDirItIsGiven(t *testing.T) {
	isolateStateFile(t) // sets the package default to a *different* temp dir
	homeDir := t.TempDir()

	log, err := openSpawnLog(homeDir)
	if err != nil {
		t.Fatalf("openSpawnLog: %v", err)
	}
	log.file.Close()
	if got := filepath.Dir(log.path); got != homeDir {
		t.Errorf("log directory = %q, want the home dir passed in (%q)", got, homeDir)
	}

	// Empty means "no -home-dir given", which is the platform default — the same
	// directory the spawned daemon will use.
	defaultLog, err := openSpawnLog("")
	if err != nil {
		t.Fatalf("openSpawnLog(\"\"): %v", err)
	}
	defaultLog.file.Close()
	if got := filepath.Dir(defaultLog.path); got != persist.HomeDir() {
		t.Errorf("log directory = %q, want the platform default (%q)", got, persist.HomeDir())
	}
}

// The daemon inherits this handle for its whole lifetime, so the parent cannot
// truncate between spawns without corrupting a log the previous daemon may still
// be writing into. Appending is the correct behavior; rotation is what bounds it.
func TestSpawnLogAppendsAndRotatesWhenOversized(t *testing.T) {
	homeDir := t.TempDir()

	first, err := openSpawnLog(homeDir)
	if err != nil {
		t.Fatalf("openSpawnLog: %v", err)
	}
	path := first.path
	if first.offset != 0 {
		t.Errorf("first spawn offset = %d, want 0 for a fresh log", first.offset)
	}
	if _, err := first.file.WriteString("first daemon\n"); err != nil {
		t.Fatal(err)
	}
	first.file.Close()

	second, err := openSpawnLog(homeDir)
	if err != nil {
		t.Fatalf("openSpawnLog (second): %v", err)
	}
	// The offset is what keeps a failure report about this spawn only.
	if second.offset != int64(len("first daemon\n")) {
		t.Errorf("second spawn offset = %d, want it past the first daemon's output", second.offset)
	}
	if _, err := second.file.WriteString("second daemon\n"); err != nil {
		t.Fatal(err)
	}
	second.file.Close()

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"first daemon", "second daemon"} {
		if !strings.Contains(string(content), want) {
			t.Errorf("log = %q, want it to still contain %q", content, want)
		}
	}

	// Past the rotation threshold the previous generation is moved aside, which
	// is what keeps the file bounded across spawns.
	if err := os.WriteFile(path, make([]byte, spawnLogRotateAt+1), 0o600); err != nil {
		t.Fatal(err)
	}
	rotated, err := openSpawnLog(homeDir)
	if err != nil {
		t.Fatalf("openSpawnLog (rotating): %v", err)
	}
	rotated.file.Close()
	if rotated.offset != 0 {
		t.Errorf("offset after rotation = %d, want 0", rotated.offset)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 0 {
		t.Errorf("log size = %d, want a fresh file after rotation", info.Size())
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Errorf("previous generation not kept: %v", err)
	}
}

// Appending across runs turned the failure report into a dump of every previous
// daemon's output, with this spawn's real error at the very end — and worse, a
// bind failure from an *earlier* run stayed in the text, so the address-in-use
// guidance got attached to failures that had nothing to do with a taken port.
func TestLogHintReportsOnlyThisSpawnsOutput(t *testing.T) {
	homeDir := t.TempDir()
	logPath := filepath.Join(homeDir, spawnLogName)

	// A previous daemon's log, including a bind error of its own.
	previous := strings.Repeat("previous daemon chatter\n", 500) +
		"error=\"listen tcp 127.0.0.1:8420: bind: address already in use\"\n"
	if err := os.WriteFile(logPath, []byte(previous), 0o600); err != nil {
		t.Fatal(err)
	}

	log, err := openSpawnLog(homeDir)
	if err != nil {
		t.Fatalf("openSpawnLog: %v", err)
	}
	if _, err := log.file.WriteString("error=\"opening database: permission denied\"\n"); err != nil {
		t.Fatal(err)
	}
	log.file.Close()

	child := &spawnedDaemon{exited: make(chan struct{}), logPath: log.path, logOffset: log.offset}
	hint := child.logHint()

	if !strings.Contains(hint, "permission denied") {
		t.Errorf("hint = %q, want this spawn's own error", hint)
	}
	if strings.Contains(hint, "previous daemon chatter") {
		t.Error("hint quotes the previous daemon's output")
	}
	// The stale bind line belongs to the earlier run, so the port guidance must
	// not fire for what is actually a permissions failure.
	if strings.Contains(hint, "homeshell stop -home-dir") {
		t.Error("hint attached the address-in-use guidance using an earlier run's bind error")
	}
}

// A taken port usually means another daemon under a different -home-dir, which
// no verb here can discover — so `homeshell stop` reports "no daemon is running"
// while that daemon keeps serving. The bare bind error sends the user there and
// leaves them stuck, so the guidance has to name the -home-dir scoping.
func TestAddressInUseHint(t *testing.T) {
	t.Run("explains the home-dir scoping and every way out", func(t *testing.T) {
		hint := addressInUseHint(`level=ERROR msg="daemon exited" ` +
			`error="listen tcp 127.0.0.1:8420: bind: address already in use"`)
		for _, want := range []string{"-home-dir", "homeshell stop -home-dir", "-addr"} {
			if !strings.Contains(hint, want) {
				t.Errorf("hint = %q, want it to mention %q", hint, want)
			}
		}
	})

	t.Run("stays quiet for an unrelated failure", func(t *testing.T) {
		if hint := addressInUseHint(`error="opening database: permission denied"`); hint != "" {
			t.Errorf("hint = %q, want nothing for a failure that is not a bind conflict", hint)
		}
	})

	t.Run("recognizes each platform's wording", func(t *testing.T) {
		tests := []struct {
			name   string
			output string
			want   bool
		}{
			{name: "linux and macos", output: "bind: address already in use", want: true},
			{name: "mixed case", output: "BIND: Address Already In Use", want: true},
			{name: "unrelated", output: "connection refused", want: false},
			{name: "empty", output: "", want: false},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				if got := mentionsAddressInUse(tt.output); got != tt.want {
					t.Errorf("mentionsAddressInUse(%q) = %v, want %v", tt.output, got, tt.want)
				}
			})
		}
	})
}

// The guidance is only useful if it reaches the error the user actually sees.
func TestWaitForRunningCarriesTheAddressInUseGuidance(t *testing.T) {
	isolateStateFile(t)
	stubDiscover(t, daemonNone)

	logPath := filepath.Join(t.TempDir(), spawnLogName)
	if err := os.WriteFile(logPath, []byte("bind: address already in use\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	child := &spawnedDaemon{exited: make(chan struct{}), logPath: logPath, waitErr: errors.New("exit status 1")}
	close(child.exited)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := waitForRunning(ctx, child)
	if err == nil {
		t.Fatal("waitForRunning: expected an error when the spawned daemon dies")
	}
	if !strings.Contains(err.Error(), "homeshell stop -home-dir") {
		t.Errorf("error = %q, want it to carry the -home-dir guidance", err)
	}
}

func TestSpawnedDaemonHintsAreSafeWithoutAChild(t *testing.T) {
	// The stubbed spawn seam yields a nil *spawnedDaemon, and so does any future
	// spawn implementation that cannot watch its child; the accessors must not
	// panic and died() must block rather than fire immediately.
	var none *spawnedDaemon
	if none.logHint() != "" || none.exitReason() != "" {
		t.Errorf("nil spawnedDaemon should describe nothing, got %q / %q", none.logHint(), none.exitReason())
	}
	select {
	case <-none.died():
		t.Fatal("nil spawnedDaemon reported an exit; it has no child to exit")
	default:
	}
}

func TestEnsureRunningVerb(t *testing.T) {
	t.Run("prints a bare URL for an already-running daemon", func(t *testing.T) {
		isolateStateFile(t)
		srv := liveDaemon(t)
		spawned := stubSpawn(t)

		code, stdout, stderr := runVerb(runEnsureRunning, nil)
		if code != 0 {
			t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
		}
		want := strings.TrimPrefix(srv.URL, "http://")
		if strings.TrimSpace(stdout) != "http://"+want+"/" {
			t.Errorf("stdout = %q, want the daemon URL http://%s/", stdout, want)
		}
		if spawned.called {
			t.Error("the verb spawned a daemon while one was already running")
		}
	})

	t.Run("prints the JSON contract the desktop shell parses", func(t *testing.T) {
		isolateStateFile(t)
		srv := liveDaemon(t)
		stubSpawn(t)

		code, stdout, stderr := runVerb(runEnsureRunning, []string{"--json"})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
		}
		var got ensureResult
		if err := json.Unmarshal([]byte(stdout), &got); err != nil {
			t.Fatalf("unmarshalling %q: %v", stdout, err)
		}
		if want := "http://" + strings.TrimPrefix(srv.URL, "http://") + "/"; got.URL != want {
			t.Errorf("url = %q, want %q", got.URL, want)
		}
		if got.Spawned {
			t.Error("spawned = true for a daemon that was already running")
		}
	})

	t.Run("reports spawned:true when it started the daemon", func(t *testing.T) {
		isolateStateFile(t)
		stubDiscover(t, daemonNone, daemonRunning)
		stubSpawn(t)

		code, stdout, stderr := runVerb(runEnsureRunning, []string{"--json"})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
		}
		var got ensureResult
		if err := json.Unmarshal([]byte(stdout), &got); err != nil {
			t.Fatalf("unmarshalling %q: %v", stdout, err)
		}
		if !got.Spawned {
			t.Error("spawned = false for a daemon this call started")
		}
		if got.URL == "" {
			t.Error("url is empty; the shell has nothing to navigate to")
		}
	})

	t.Run("forwards -addr and -home-dir to the spawned daemon", func(t *testing.T) {
		isolateStateFile(t)
		homeDir := persist.HomeDir()
		stubDiscover(t, daemonNone, daemonRunning)
		spawned := stubSpawn(t)

		code, _, stderr := runVerb(runEnsureRunning, []string{"-addr", "127.0.0.1:9931", "-home-dir", homeDir})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
		}
		if spawned.addr != "127.0.0.1:9931" {
			t.Errorf("spawned with addr %q, want 127.0.0.1:9931", spawned.addr)
		}
		if spawned.homeDir != homeDir {
			t.Errorf("spawned with home dir %q, want %q", spawned.homeDir, homeDir)
		}
	})

	t.Run("fails with the stop advice when a stale daemon still holds the port", func(t *testing.T) {
		isolateStateFile(t)
		origDiscover := discoverPhase
		t.Cleanup(func() { discoverPhase = origDiscover })
		discoverPhase = func(context.Context) (daemonstate.State, daemonPhase, error) {
			return daemonstate.State{Address: "127.0.0.1:8420", PID: 4242}, daemonStale, nil
		}
		stubProcessAlive(t, true)
		stubSpawn(t)

		code, stdout, stderr := runVerb(runEnsureRunning, nil)
		if code != 1 {
			t.Fatalf("exit code = %d, want 1", code)
		}
		if stdout != "" {
			t.Errorf("stdout = %q, want nothing printed when there is no URL to report", stdout)
		}
		// `homeshell stop` must be able to act on this, or the advice is a dead end.
		if !strings.Contains(stderr, "homeshell stop") {
			t.Errorf("stderr = %q, want it to point at `homeshell stop`", stderr)
		}
	})

	t.Run("rejects an unknown flag without spawning anything", func(t *testing.T) {
		isolateStateFile(t)
		spawned := stubSpawn(t)

		code, _, stderr := runVerb(runEnsureRunning, []string{"-nope"})
		if code != 2 {
			t.Errorf("exit code = %d, want 2", code)
		}
		if !strings.Contains(stderr, "nope") {
			t.Errorf("stderr = %q, want it to name the offending flag", stderr)
		}
		if spawned.called {
			t.Error("a rejected flag must not spawn a daemon")
		}
	})

	t.Run("-h prints usage and succeeds", func(t *testing.T) {
		isolateStateFile(t)
		stubSpawn(t)

		code, _, stderr := runVerb(runEnsureRunning, []string{"-h"})
		if code != 0 {
			t.Errorf("exit code = %d, want 0", code)
		}
		for _, want := range []string{"-json", "-addr", "-timeout", "-home-dir"} {
			if !strings.Contains(stderr, want) {
				t.Errorf("usage = %q, want it to document %q", stderr, want)
			}
		}
	})
}
