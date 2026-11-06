package workstream

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runGit runs a git command in dir, hermetic against the developer's config.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test",
		"GIT_COMMITTER_EMAIL=test@example.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// initRepo creates a git repository with one commit at dir.
func initRepo(t *testing.T, dir string) {
	t.Helper()
	runGit(t, dir, "init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-q", "-m", "initial")
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed; skipping")
	}
}

func TestPrepareInPlace(t *testing.T) {
	proj := t.TempDir()
	dir, err := Prepare(context.Background(), InPlace, proj, "", "", "/ignored", false)
	if err != nil {
		t.Fatal(err)
	}
	if dir != proj {
		t.Errorf("in-place dir = %q, want project dir %q", dir, proj)
	}
}

func TestPrepareCopy(t *testing.T) {
	proj := t.TempDir()
	if err := os.WriteFile(filepath.Join(proj, "file.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "copy")
	dir, err := Prepare(context.Background(), Copy, proj, "", "", dest, false)
	if err != nil {
		t.Fatal(err)
	}
	if dir != dest {
		t.Errorf("dir = %q, want %q", dir, dest)
	}
	if _, err := os.Stat(filepath.Join(dest, "file.txt")); err != nil {
		t.Errorf("copied file missing: %v", err)
	}
	if err := Discard(Copy, proj, dest); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Errorf("copy dir should be gone after Discard, stat err = %v", err)
	}
}

func TestPrepareCopyRequiresDestination(t *testing.T) {
	if _, err := Prepare(context.Background(), Copy, t.TempDir(), "", "", "", false); err == nil {
		t.Fatal("copy without a destination must fail")
	}
}

func TestPrepareWorktree(t *testing.T) {
	requireGit(t)
	proj := t.TempDir()
	initRepo(t, proj)
	dest := filepath.Join(t.TempDir(), "wt")

	dir, err := Prepare(context.Background(), Worktree, proj, "feature/x", "", dest, true)
	if err != nil {
		t.Fatalf("Prepare worktree: %v", err)
	}
	if dir != dest {
		t.Errorf("dir = %q, want %q", dir, dest)
	}
	if _, err := os.Stat(filepath.Join(dest, "README.md")); err != nil {
		t.Errorf("worktree should contain the repo files: %v", err)
	}
	if err := Discard(Worktree, proj, dest); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Errorf("worktree dir should be gone after Discard, stat err = %v", err)
	}
}

func TestPrepareWorktreeValidation(t *testing.T) {
	requireGit(t)
	repo := t.TempDir()
	initRepo(t, repo)

	empty := t.TempDir()
	runGit(t, empty, "init", "-q") // git repo, no commits

	tests := []struct {
		name       string
		projectDir string
		branch     string
		destDir    string
		gitBacked  bool
	}{
		{"not git-backed", repo, "feature/x", filepath.Join(t.TempDir(), "d"), false},
		{"no branch", repo, "", filepath.Join(t.TempDir(), "d"), true},
		{"invalid branch name", repo, "bad branch~name", filepath.Join(t.TempDir(), "d"), true},
		{"no commits", empty, "feature/x", filepath.Join(t.TempDir(), "d"), true},
		{"branch checked out in worktree", repo, defaultBranch(t, repo), filepath.Join(t.TempDir(), "d"), true},
		{"empty destination", repo, "feature/y", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Prepare(context.Background(), Worktree, tt.projectDir, tt.branch, "", tt.destDir, tt.gitBacked); err == nil {
				t.Errorf("expected an error for %q", tt.name)
			}
		})
	}
}

func TestPrepareWorktreeExistingBranch(t *testing.T) {
	requireGit(t)
	proj := t.TempDir()
	initRepo(t, proj)

	// Create a branch but don't check it out — detach HEAD.
	runGit(t, proj, "branch", "reuse-me")
	runGit(t, proj, "checkout", "--detach", "HEAD")

	dest := filepath.Join(t.TempDir(), "wt-reuse")
	dir, err := Prepare(context.Background(), Worktree, proj, "reuse-me", "", dest, true)
	if err != nil {
		t.Fatalf("Prepare with existing unchecked-out branch: %v", err)
	}
	if dir != dest {
		t.Errorf("dir = %q, want %q", dir, dest)
	}
	if _, err := os.Stat(filepath.Join(dest, "README.md")); err != nil {
		t.Errorf("worktree should contain the repo files: %v", err)
	}
	if err := Discard(Worktree, proj, dest); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareWorktreeCheckedOutBranch(t *testing.T) {
	requireGit(t)
	proj := t.TempDir()
	initRepo(t, proj)

	branch := defaultBranch(t, proj)
	dest := filepath.Join(t.TempDir(), "wt-fail")
	_, err := Prepare(context.Background(), Worktree, proj, branch, "", dest, true)
	if err == nil {
		t.Fatal("expected error when branch is checked out in main worktree")
	}
	if got := err.Error(); !strings.Contains(got, "checked out in another worktree") {
		t.Errorf("unexpected error message: %s", got)
	}
}

func TestPrepareUnknownStrategy(t *testing.T) {
	if _, err := Prepare(context.Background(), Strategy("teleport"), t.TempDir(), "", "", "", false); err == nil {
		t.Fatal("unknown strategy must fail")
	}
}

func TestDiscardInPlaceIsNoop(t *testing.T) {
	proj := t.TempDir()
	if err := Discard(InPlace, proj, proj); err != nil {
		t.Errorf("in-place discard should be a no-op, got %v", err)
	}
	if _, err := os.Stat(proj); err != nil {
		t.Errorf("in-place discard must not remove the project dir: %v", err)
	}
}

// defaultBranch returns the repo's current branch name.
func defaultBranch(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("git", "branch", "--show-current")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	branch := string(out)
	return branch[:len(branch)-1] // trim trailing newline
}
