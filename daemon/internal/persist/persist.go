// Package persist handles reading and writing HomeShell's local state.
//
// State lives in JSON files under the platform-specific data directory, each
// a typed Go struct with a schema version, written temp-then-rename so a crash
// mid-write cannot corrupt it: state.json (projects, workstreams, last-known
// sessions, custom session titles, session launch configs; see Snapshot),
// agents.json (internal/agentcatalog), settings.json (internal/settings) and
// local-provider.json (internal/provider/local). This package provides the
// data directory, the atomic write, and the state file.
//
// The state file stores project/workstream/session metadata (the structural
// facts the user created) but not ephemeral runtime state (session states,
// derived workstream/project states). Those come from providers at runtime;
// persisting them would produce stale lies after a restart (docs/ARCHITECTURE.md,
// "Persistence").
//
// The data directory lives under $XDG_DATA_HOME/homeshell/ (or
// ~/.local/share/homeshell/ on Linux / ~/Library/Application Support/homeshell/
// on macOS when $XDG_DATA_HOME is unset).
package persist

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// homeDir is the explicit override for the data directory. When non-empty,
// DefaultDir returns it directly, bypassing all platform-specific logic.
var homeDir string

// HomeDir returns the current override, or empty if the platform default is
// in effect. Tests use it to save and restore the override.
func HomeDir() string { return homeDir }

// SetHomeDir sets an explicit data directory that overrides the platform
// default. All persisted state (state.json, agents.json, settings.json,
// daemon.json, provider data) will
// live under this directory. Pass an empty string to revert to the platform
// default.
func SetHomeDir(dir string) { homeDir = dir }

// DefaultDir returns the data directory. If SetHomeDir was called with a
// non-empty path, that path is returned. Otherwise it falls back to
// $XDG_DATA_HOME/homeshell, ~/Library/Application Support/homeshell (macOS),
// or ~/.local/share/homeshell (Linux).
func DefaultDir() (string, error) {
	if homeDir != "" {
		return homeDir, nil
	}
	if dir := os.Getenv("XDG_DATA_HOME"); dir != "" {
		return filepath.Join(dir, "homeshell"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("determining home directory: %w", err)
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "homeshell"), nil
	default:
		return filepath.Join(home, ".local", "share", "homeshell"), nil
	}
}
