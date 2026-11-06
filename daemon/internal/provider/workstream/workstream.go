// Package workstream materializes and tears down a workstream's working
// directory for a filesystem strategy (in-place / git worktree / copy). It is
// shared by the providers that offer these strategies (local, enclave): it owns
// the validation and the filesystem operations, while the caller owns the
// destination-path policy (a managed directory, a sibling worktree path, or a
// user-chosen path) and passes the destination in.
package workstream

import (
	"context"
	"fmt"

	"github.com/eclipsesource/homeshell/daemon/internal/fsutil"
	"github.com/eclipsesource/homeshell/daemon/internal/git"
)

// Strategy is how a workstream's working directory is set up.
type Strategy string

const (
	InPlace  Strategy = "in-place" // run in the project directory itself
	Worktree Strategy = "worktree" // an isolated git worktree on a new branch
	Copy     Strategy = "copy"     // a copy of the project directory
)

// Prepare sets up the working directory for strategy and returns the directory
// the workstream's sessions run in.
//
//   - in-place: returns projectDir (destDir is ignored); nothing is created.
//   - worktree: requires a git-backed project (gitBacked) with at least one
//     commit. If the branch already exists but is not checked out in another
//     worktree, it is reused; if it is checked out elsewhere, Prepare returns
//     an error. A new branch starts from baseRef (which may be empty to use
//     git's default).
//   - copy: duplicates projectDir into destDir (excluding heavy/ephemeral dirs).
//
// It performs no cleanup on failure; a caller that created a parent directory
// discards a partial result with Discard. The caller must supply destDir for
// the worktree and copy strategies.
func Prepare(ctx context.Context, strategy Strategy, projectDir, branch, baseRef, destDir string, gitBacked bool) (string, error) {
	switch strategy {
	case InPlace:
		return projectDir, nil
	case Worktree:
		if !gitBacked {
			return "", fmt.Errorf("worktree strategy requires a git-backed project")
		}
		if branch == "" {
			return "", fmt.Errorf("worktree strategy requires a branch name")
		}
		if err := git.ValidateBranchName(branch); err != nil {
			return "", err
		}
		has, err := git.HasCommits(ctx, projectDir)
		if err != nil {
			return "", fmt.Errorf("checking repository: %w", err)
		}
		if !has {
			return "", fmt.Errorf("worktree strategy requires at least one commit")
		}
		exists, err := git.BranchExists(ctx, projectDir, branch)
		if err != nil {
			return "", fmt.Errorf("checking branch: %w", err)
		}
		if destDir == "" {
			return "", fmt.Errorf("worktree strategy requires a destination directory")
		}
		if exists {
			checkedOut, err := git.BranchCheckedOutInWorktree(ctx, projectDir, branch)
			if err != nil {
				return "", fmt.Errorf("checking worktree: %w", err)
			}
			if checkedOut {
				return "", fmt.Errorf("branch %q is checked out in another worktree", branch)
			}
			if err := git.WorktreeAdd(projectDir, destDir, branch, "", true); err != nil {
				return "", fmt.Errorf("could not create the worktree: %w", err)
			}
		} else {
			if baseRef == "" {
				baseRef = git.DefaultBaseRef(ctx, projectDir)
			}
			if err := git.WorktreeAdd(projectDir, destDir, branch, baseRef, false); err != nil {
				return "", fmt.Errorf("could not create the worktree: %w", err)
			}
		}
		return destDir, nil
	case Copy:
		if destDir == "" {
			return "", fmt.Errorf("copy strategy requires a destination directory")
		}
		if err := fsutil.CopyDir(projectDir, destDir, fsutil.DefaultCopyExcludes); err != nil {
			return "", fmt.Errorf("copy project: %w", err)
		}
		return destDir, nil
	default:
		return "", fmt.Errorf("unknown filesystem strategy %q", strategy)
	}
}

// Discard tears down a directory prepared by Prepare. In-place is a no-op (the
// project directory is not ours to remove); worktree removes the git worktree;
// copy deletes the copied directory.
func Discard(strategy Strategy, projectDir, dir string) error {
	switch strategy {
	case InPlace:
		return nil
	case Worktree:
		return git.WorktreeRemove(projectDir, dir)
	case Copy:
		return fsutil.RemoveCopy(dir)
	default:
		return nil
	}
}
