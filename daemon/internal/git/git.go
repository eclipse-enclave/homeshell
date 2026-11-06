// Package git provides git-related utilities for the HomeShell daemon:
// repository detection and worktree management.
//
// Detection is a daemon-owned domain fact (docs/ARCHITECTURE.md, "Principles"): the
// daemon computes it once and clients render the plain boolean. It shells
// out to `git rev-parse` rather than probing for a `.git` entry so it is
// correct for worktrees (whose `.git` is a file, not a directory),
// submodules (a gitlink file), and projects nested below the work-tree
// root. When `git` is missing or errors, detection degrades gracefully to
// "not git-backed" rather than failing the operation that triggered
// it.
//
// Worktree functions (worktree.go) create and remove git worktrees for
// the workstream filesystem strategy described in issues #10 and #11.
package git

import (
	"context"
	"log/slog"
	"os/exec"
	"strings"
	"time"
)

// queryTimeout caps how long one git invocation may run, so a hung git never
// blocks the request or store mutation waiting on it. A caller's context can
// cut it shorter. A variable so tests can shorten it.
var queryTimeout = 5 * time.Second

// query runs a read-only git command in dir and returns its trimmed output.
func query(ctx context.Context, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

// Detector reports whether a path is inside a git work tree. It is an
// interface so the store can be exercised against a fake in tests without
// shelling out.
type Detector interface {
	Detect(path string) bool
}

// gitDetector is the default Detector; it invokes the git binary.
type gitDetector struct{}

// New returns the default git-backed Detector.
func New() Detector { return gitDetector{} }

// Detect reports whether path is inside a git work tree by running
// `git rev-parse --is-inside-work-tree` there. Any error (git absent, not a
// repository, timeout) yields false.
func (gitDetector) Detect(path string) bool {
	out, err := query(context.Background(), path, "rev-parse", "--is-inside-work-tree")
	if err != nil {
		// Not a repository, git not installed, or the path is unreadable —
		// all mean "not git-backed" as far as HomeShell is concerned.
		slog.Debug("git detection: not git-backed", "path", path, "error", err)
		return false
	}
	return out == "true"
}
