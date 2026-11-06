// Package pathutil provides path-resolution utilities shared across the
// daemon: tilde expansion, relative paths (rejected, or resolved against a
// base), and cleaning. Every user-supplied filesystem path should pass through
// Resolve before use.
package pathutil

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Resolve expands ~ to the user's home directory, rejects relative paths
// (which would resolve against the daemon's CWD — never what the user
// means), and returns a cleaned absolute path.
func Resolve(raw string) (string, error) {
	if raw == "~" || strings.HasPrefix(raw, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("cannot expand ~: %w", err)
		}
		raw = filepath.Join(home, raw[1:])
	}
	if !filepath.IsAbs(raw) {
		return "", fmt.Errorf("path must be absolute (got %q)", raw)
	}
	return filepath.Clean(raw), nil
}

// ResolveFrom is Resolve with a relative path taken relative to base instead
// of rejected.
func ResolveFrom(raw, base string) (string, error) {
	if !IsAnchored(raw) {
		raw = filepath.Join(base, raw)
	}
	return Resolve(raw)
}

// IsAnchored reports whether raw names a place on its own, starting with ~ or
// being absolute, rather than relative to some base.
func IsAnchored(raw string) bool {
	return strings.HasPrefix(raw, "~") || filepath.IsAbs(raw)
}

// Within reports whether path is dir or lies under it. Both should be clean
// absolute paths.
func Within(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && filepath.IsLocal(rel)
}
