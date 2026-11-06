package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"golang.org/x/text/unicode/norm"
)

// branchNameRe matches a legal git branch-name component: no spaces, no
// control characters, no colon, no tilde, no caret, no backslash, no
// question mark, no asterisk, no open bracket. The full ref-name rules are
// complex; this is a practical subset that rejects the common mistakes.
var branchNameRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9._/-]*[A-Za-z0-9])?$`)

// branchNameBadPatterns rejects patterns that git forbids even when the
// character set is legal.
var branchNameBadPatterns = []string{"..", "//", "@{", ".lock"}

// ValidateBranchName checks whether name is a usable git branch name.
func ValidateBranchName(name string) error {
	if name == "" {
		return fmt.Errorf("branch name must not be empty")
	}
	if !branchNameRe.MatchString(name) {
		return fmt.Errorf("branch name %q contains illegal characters", name)
	}
	for _, bad := range branchNameBadPatterns {
		if strings.Contains(name, bad) {
			return fmt.Errorf("branch name %q contains forbidden pattern %q", name, bad)
		}
	}
	return nil
}

// BranchNameFrom turns free text, such as a workstream name, into a branch
// name ValidateBranchName accepts: lowercase ASCII letters and digits, with
// each run of anything else collapsed to one separator ("/" if the run has one,
// a lone "." or "_" kept, else "-") and none at either end. Accents are dropped
// rather than the letters they sit on. It returns "" when nothing in the text
// survives.
func BranchNameFrom(text string) string {
	var unaccented strings.Builder
	for _, char := range norm.NFKD.String(text) {
		if char < '\u0300' || char > '\u036f' {
			unaccented.WriteRune(char)
		}
	}
	name := nonAlphanumericRun.ReplaceAllStringFunc(strings.ToLower(unaccented.String()), func(run string) string {
		switch {
		case strings.Contains(run, "/"):
			return "/"
		case run == "." || run == "_":
			return run
		default:
			return "-"
		}
	})
	name = strings.Trim(name, "-./_")
	return strings.ReplaceAll(name, ".lock", "-lock")
}

var nonAlphanumericRun = regexp.MustCompile(`[^a-z0-9]+`)

// HasCommits reports whether the git repository at dir has at least one
// commit. A repo with no commits cannot create worktrees. A git that does not
// answer in time is an error.
func HasCommits(ctx context.Context, dir string) (bool, error) {
	_, err := query(ctx, dir, "rev-parse", "HEAD")
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 128 {
		// exit 128: no HEAD to resolve; a killed git exits with -1.
		return false, nil
	}
	return false, fmt.Errorf("checking for commits: %w", err)
}

// BranchExists reports whether a branch with the given name already exists
// in the repository at dir. A git that fails or is killed is an error, not a
// missing branch: what that means is the caller's to decide.
func BranchExists(ctx context.Context, dir, branch string) (bool, error) {
	_, err := query(ctx, dir, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		// exit 1 means the ref does not exist; a killed git exits with -1.
		return false, nil
	}
	return false, fmt.Errorf("checking branch %q: %w", branch, err)
}

// BranchCheckedOutInWorktree reports whether branch is checked out in any
// existing worktree of the repository at dir.
func BranchCheckedOutInWorktree(ctx context.Context, dir, branch string) (bool, error) {
	worktree, err := BranchWorktree(ctx, dir, branch)
	return worktree != "", err
}

// BranchWorktree returns the path of the worktree of the repository at dir that
// has branch checked out, or "" if none has. One whose folder was deleted has
// none: WorktreeAdd drops its registration. One whose folder is out of reach,
// on a drive not mounted say, still has it.
func BranchWorktree(ctx context.Context, dir, branch string) (string, error) {
	entries, err := worktrees(ctx, dir)
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if entry.branch == branch && !entry.deleted() {
			return entry.path, nil
		}
	}
	return "", nil
}

// worktree is a worktree as `git worktree list --porcelain` reports it.
type worktree struct {
	path     string
	branch   string
	prunable bool
	locked   bool
}

// deleted reports whether the worktree's folder was deleted: git marks it
// prunable, and the folder it sat in is still there. Where that is gone too,
// the folder may only be out of reach, on a drive not mounted say, and its
// registration is kept so it can be repaired once back. A worktree right at a
// mount point that stays while its drive is unmounted looks deleted.
func (w worktree) deleted() bool {
	if !w.prunable {
		return false
	}
	info, err := os.Stat(filepath.Dir(w.path))
	return err == nil && info.IsDir()
}

// worktrees lists the worktrees of the repository at dir. A git before 2.36
// has no -z and lists them a line per field, where a path with a newline in it
// is misread. One before 2.31 marks none prunable or locked: there one counts
// as prunable when its folder is gone, which is what a later git marks prunable
// too unless it is locked.
func worktrees(ctx context.Context, dir string) ([]worktree, error) {
	separator := "\x00"
	out, err := query(ctx, dir, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		separator = "\n"
		out, err = query(ctx, dir, "worktree", "list", "--porcelain")
	}
	if err != nil {
		return nil, fmt.Errorf("git worktree list: %w", err)
	}
	var entries []worktree
	for _, field := range strings.Split(out, separator) {
		if path, ok := strings.CutPrefix(field, "worktree "); ok {
			entries = append(entries, worktree{path: path})
			continue
		}
		if len(entries) == 0 {
			continue
		}
		current := &entries[len(entries)-1]
		if branch, ok := strings.CutPrefix(field, "branch refs/heads/"); ok {
			current.branch = branch
		} else if field == "prunable" || strings.HasPrefix(field, "prunable ") {
			current.prunable = true
		} else if field == "locked" || strings.HasPrefix(field, "locked ") {
			current.locked = true
		}
	}
	for i := range entries {
		if entry := &entries[i]; !entry.prunable && !entry.locked {
			_, err := os.Lstat(entry.path)
			entry.prunable = errors.Is(err, fs.ErrNotExist)
		}
	}
	return entries, nil
}

// SamePath reports whether two paths name the same file, symlinks resolved:
// git reports a worktree by its real path.
func SamePath(first, second string) bool {
	return realPath(first) == realPath(second)
}

// realPath is path absolute with the symlinks of its nearest existing ancestor
// resolved.
func realPath(path string) string {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	remainder := ""
	for ancestor := absolute; ; ancestor = filepath.Dir(ancestor) {
		if resolved, err := filepath.EvalSymlinks(ancestor); err == nil {
			return filepath.Join(resolved, remainder)
		}
		if filepath.Dir(ancestor) == ancestor {
			return absolute
		}
		remainder = filepath.Join(filepath.Base(ancestor), remainder)
	}
}

// WorktreeAdd creates a git worktree at worktreePath.
//
// When existingBranch is true the worktree checks out an already-existing
// branch: `git worktree add -- <path> <branch>` (no -b flag).
//
// When existingBranch is false a new branch is created:
//   - if baseRef is non-empty: `git worktree add -b <branch> -- <path> <baseRef>`
//   - if baseRef is empty: `git worktree add -b <branch> -- <path>`
//
// Callers should validate preconditions first:
//   - repoDir is a git repo (Detector.Detect)
//   - the repo has commits (HasCommits)
//   - the branch name is legal (ValidateBranchName)
//   - for new branches, the branch does not exist (BranchExists)
//   - for existing branches, the branch is not checked out elsewhere
//     (BranchCheckedOutInWorktree)
func WorktreeAdd(repoDir, worktreePath, branch, baseRef string, existingBranch bool) error {
	absPath, err := filepath.Abs(worktreePath)
	if err != nil {
		return fmt.Errorf("resolving worktree path: %w", err)
	}

	// "--" ends the options, so a path or ref that starts with "-" (a tag
	// fetched from someone else's remote, say) is never read as one.
	var args []string
	if existingBranch {
		args = []string{"worktree", "add", "--", absPath, branch}
	} else if baseRef != "" {
		args = []string{"worktree", "add", "-b", branch, "--", absPath, baseRef}
	} else {
		args = []string{"worktree", "add", "-b", branch, "--", absPath}
	}

	// A worktree whose folder is missing stays registered, and git refuses to
	// add one at its path or with its branch. A registration at the path is
	// dropped; one with the branch only where its folder was deleted rather than
	// out of reach. If that fails, the add below says why.
	if entries, err := worktrees(context.Background(), repoDir); err == nil {
		for _, entry := range entries {
			if (entry.prunable && SamePath(entry.path, absPath)) || (entry.branch == branch && entry.deleted()) {
				_, _ = query(context.Background(), repoDir, "worktree", "remove", "--force", "--", entry.path)
			}
		}
	}

	cmd := exec.Command("git", args...)
	cmd.Dir = repoDir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return failure(stderr.String(), err)
	}
	return nil
}

// failure words a failed git command by what git said went wrong: its fatal and
// error lines without their prefix, else all it wrote, else how it exited — not
// its progress chatter or exit status, which say nothing to a person.
func failure(stderr string, err error) error {
	var reasons []string
	for _, line := range strings.Split(stderr, "\n") {
		for _, prefix := range []string{"fatal: ", "error: "} {
			if reason, ok := strings.CutPrefix(strings.TrimSpace(line), prefix); ok {
				reasons = append(reasons, reason)
			}
		}
	}
	if len(reasons) > 0 {
		return errors.New(strings.Join(reasons, "; "))
	}
	if message := strings.TrimSpace(stderr); message != "" {
		return errors.New(message)
	}
	return err
}

// DefaultBaseRef returns the best starting point for a new branch in the
// repository at dir: the remote's default branch (origin/HEAD), else
// origin/main or origin/master, else a local main or master, else the branch
// init.defaultBranch names, else the current branch. Only refs that resolve to
// a commit count, so an empty repository yields "". The name is one git reads
// as that ref alone ("heads/main" when a tag is called main too).
func DefaultBaseRef(ctx context.Context, dir string) string {
	var candidates []string
	if remoteHead, err := query(ctx, dir, "symbolic-ref", "--quiet", "refs/remotes/origin/HEAD"); err == nil {
		candidates = append(candidates, remoteHead)
	}
	candidates = append(candidates, "refs/remotes/origin/main", "refs/remotes/origin/master", "refs/heads/main", "refs/heads/master")
	if configured, err := query(ctx, dir, "config", "init.defaultBranch"); err == nil {
		candidates = append(candidates, "refs/heads/"+configured)
	}
	if current, err := query(ctx, dir, "symbolic-ref", "--quiet", "HEAD"); err == nil {
		candidates = append(candidates, current)
	}
	for _, ref := range candidates {
		if !Resolves(ctx, dir, ref) {
			continue
		}
		if name, err := query(ctx, dir, "rev-parse", "--abbrev-ref", ref); err == nil {
			return name
		}
	}
	return ""
}

// Resolves reports whether ref names a commit in the repository at dir.
func Resolves(ctx context.Context, dir, ref string) bool {
	_, err := query(ctx, dir, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	return err == nil
}

// WorktreeRemove removes a git worktree, folder and registration. One already
// gone from both, say pruned after its folder was removed by hand, counts as
// removed. Other worktrees are left alone, even ones whose folder is missing (on a drive
// not mounted, say). The branch is deliberately kept — deleting user work
// requires an explicit separate decision.
func WorktreeRemove(repoDir, worktreePath string) error {
	absPath, err := filepath.Abs(worktreePath)
	if err != nil {
		return fmt.Errorf("resolving worktree path: %w", err)
	}

	entries, err := worktrees(context.Background(), repoDir)
	if err != nil {
		return err
	}
	registered := slices.ContainsFunc(entries, func(entry worktree) bool { return SamePath(entry.path, absPath) })
	if _, statErr := os.Lstat(absPath); !registered && errors.Is(statErr, fs.ErrNotExist) {
		return nil
	}

	rmCmd := exec.Command("git", "worktree", "remove", "--force", "--", absPath)
	rmCmd.Dir = repoDir
	var stderr bytes.Buffer
	rmCmd.Stderr = &stderr
	if err := rmCmd.Run(); err != nil {
		return fmt.Errorf("git worktree remove: %s: %w", strings.TrimSpace(stderr.String()), err)
	}
	return nil
}

// DefaultWorktreePath returns a sensible default location for a worktree
// given the repo directory and branch name. The convention is
// <parent-of-repo>/<repo-name>-<branch-with-slashes-replaced>.
func DefaultWorktreePath(repoDir, branch string) string {
	parent := filepath.Dir(repoDir)
	base := filepath.Base(repoDir)
	safeBranch := strings.ReplaceAll(branch, "/", "-")
	return filepath.Join(parent, base+"-"+safeBranch)
}
