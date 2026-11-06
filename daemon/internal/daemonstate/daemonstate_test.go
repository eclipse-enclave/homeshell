package daemonstate

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/persist"
)

// isolateHomeDir points persist at a throwaway directory for the duration of
// a test so nothing touches the real per-user data directory.
func isolateHomeDir(t *testing.T) {
	t.Helper()
	old := persist.HomeDir()
	persist.SetHomeDir(t.TempDir())
	t.Cleanup(func() { persist.SetHomeDir(old) })
}

func TestPathUsesDataDir(t *testing.T) {
	old := persist.HomeDir()
	persist.SetHomeDir("")
	t.Cleanup(func() { persist.SetHomeDir(old) })
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir)
	got, err := Path()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join(dir, "homeshell", "daemon.json")
	if got != want {
		t.Errorf("Path() = %q, want %q", got, want)
	}
}

func TestPathLivesNextToDB(t *testing.T) {
	old := persist.HomeDir()
	persist.SetHomeDir("")
	t.Cleanup(func() { persist.SetHomeDir(old) })
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir)
	got, err := Path()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The daemon.json file must sit in the same directory as the DB.
	if !strings.HasPrefix(got, filepath.Join(dir, "homeshell")+string(filepath.Separator)) {
		t.Errorf("Path() = %q, want it under %s", got, filepath.Join(dir, "homeshell"))
	}
}

func TestPathHonorsHomeDir(t *testing.T) {
	dir := t.TempDir()
	old := persist.HomeDir()
	persist.SetHomeDir(dir)
	t.Cleanup(func() { persist.SetHomeDir(old) })
	got, err := Path()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join(dir, "daemon.json")
	if got != want {
		t.Errorf("Path() = %q, want %q", got, want)
	}
}

func TestWriteReadRoundTrip(t *testing.T) {
	isolateHomeDir(t)

	want := State{
		Address:   "127.0.0.1:8420",
		PID:       4321,
		Version:   "1.2.3",
		StartedAt: time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC),
		Token:     "an-api-token-secret",
	}
	if err := Write(want); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got, err := Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.Address != want.Address || got.PID != want.PID || got.Version != want.Version {
		t.Errorf("read %+v, want %+v", got, want)
	}
	if got.Token != want.Token {
		t.Errorf("Token = %q, want %q", got.Token, want.Token)
	}
	if !got.StartedAt.Equal(want.StartedAt) {
		t.Errorf("StartedAt = %v, want %v", got.StartedAt, want.StartedAt)
	}
}

// TestWriteRestrictsFileMode verifies the state file is written owner-only
// (0600), since it now carries the API token.
func TestWriteRestrictsFileMode(t *testing.T) {
	isolateHomeDir(t)

	if err := Write(State{Address: "127.0.0.1:8420", PID: os.Getpid(), Token: "secret"}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	path, err := Path()
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("state file mode = %o, want 0600", perm)
	}
}

func TestReadMissingWrapsNotExist(t *testing.T) {
	isolateHomeDir(t)
	_, err := Read()
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Read of a missing file = %v, want an fs.ErrNotExist", err)
	}
}

func TestRemoveIsIdempotent(t *testing.T) {
	isolateHomeDir(t)
	// Removing a file that was never written must not error.
	if err := Remove(); err != nil {
		t.Fatalf("Remove of a missing file: %v", err)
	}
	if err := Write(State{Address: "127.0.0.1:0", PID: os.Getpid()}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := Remove(); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := Read(); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("state file still present after Remove: %v", err)
	}
}

func TestProcessAlive(t *testing.T) {
	if !ProcessAlive(os.Getpid()) {
		t.Error("ProcessAlive(self) = false, want true")
	}
	if ProcessAlive(-1) {
		t.Error("ProcessAlive(-1) = true, want false")
	}
	// PID 0 is not a real, signalable user process.
	if ProcessAlive(0) {
		t.Error("ProcessAlive(0) = true, want false")
	}
	// A very high PID is almost certainly not in use on a test host.
	if ProcessAlive(4_000_000_000) {
		t.Error("ProcessAlive(4e9) = true, want false")
	}
}
