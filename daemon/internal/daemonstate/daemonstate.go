// Package daemonstate is the daemon's discovery seam. On start the serving path
// writes a small JSON record (address, PID, version, started-at) to a
// well-known per-user location and removes it on clean shutdown; every client
// reads it to locate and classify the running daemon (docs/ARCHITECTURE.md,
// "Daemon lifecycle"). The state file lives in the same platform data directory as the
// rest of the persisted state (persist.DefaultDir), so both the serve path and the client verbs
// can share it.
package daemonstate

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/persist"
)

// State is the daemon's discovery record.
type State struct {
	Address   string    `json:"address"`
	PID       int       `json:"pid"`
	Version   string    `json:"version"`
	StartedAt time.Time `json:"startedAt"`
	StatePath string    `json:"statePath,omitempty"`
	// Token is the per-startup API secret the CLI and web UI present on /api
	// requests (Bearer header or hs_api_token cookie). It is a shared secret, so
	// the state file is written 0600 (see Write). Empty when auth is disabled.
	Token string `json:"token,omitempty"`
}

// Path returns the state file's location: daemon.json inside the data
// directory returned by persist.DefaultDir (which respects -home-dir).
func Path() (string, error) {
	dir, err := persist.DefaultDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "daemon.json"), nil
}

// Write publishes the state record, creating the parent directory as needed.
// It writes to a temporary file and renames it into place so a reader never
// observes a half-written record.
func Write(s State) error {
	path, err := Path()
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(dir, "daemon-*.json.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	// The record carries the API token, so restrict it to the owner before it is
	// renamed into place (CreateTemp already makes it 0600, but make the guarantee
	// explicit and independent of the umask).
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

// Read loads the state record. When no daemon has published one, the error
// wraps fs.ErrNotExist so callers can distinguish "no daemon" from a real fault.
func Read() (State, error) {
	path, err := Path()
	if err != nil {
		return State{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return State{}, err
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return State{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	return s, nil
}

// Remove deletes the state file. A missing file is not an error, so it is safe
// to call on shutdown regardless of whether the file was written.
func Remove() error {
	path, err := Path()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// ProcessAlive reports whether a process with the given PID currently exists.
// Signal 0 does the kernel's permission and existence checks without delivering
// a signal: a nil result means the process exists and we may signal it, and
// EPERM means it exists but is owned by another user.
func ProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid) // never fails on unix
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, syscall.EPERM)
}
