package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// runGit runs a git command in dir, failing the test on error. It is only
// used to build fixtures, not to test detection.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	// Keep fixture creation hermetic and non-interactive regardless of the
	// developer's global git config.
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

func TestDetect(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed; skipping detection tests")
	}

	detector := New()

	t.Run("plain directory is not git-backed", func(t *testing.T) {
		dir := t.TempDir()
		if detector.Detect(dir) {
			t.Error("plain directory reported as git-backed")
		}
	})

	t.Run("git repository is git-backed", func(t *testing.T) {
		dir := t.TempDir()
		initRepo(t, dir)
		if !detector.Detect(dir) {
			t.Error("git repository not reported as git-backed")
		}
	})

	t.Run("subdirectory of a repository is git-backed", func(t *testing.T) {
		dir := t.TempDir()
		initRepo(t, dir)
		subDir := filepath.Join(dir, "nested", "deep")
		if err := os.MkdirAll(subDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if !detector.Detect(subDir) {
			t.Error("subdirectory of a repository not reported as git-backed")
		}
	})

	t.Run("git worktree is git-backed", func(t *testing.T) {
		mainDir := t.TempDir()
		initRepo(t, mainDir)
		// A linked worktree's .git is a file, not a directory — the case a
		// naive filesystem probe gets wrong.
		worktreeDir := filepath.Join(t.TempDir(), "wt")
		runGit(t, mainDir, "worktree", "add", "-q", worktreeDir, "-b", "feature")
		if !detector.Detect(worktreeDir) {
			t.Error("git worktree not reported as git-backed")
		}
	})

	t.Run("submodule is git-backed", func(t *testing.T) {
		subRepo := t.TempDir()
		initRepo(t, subRepo)

		super := t.TempDir()
		initRepo(t, super)
		// Local file:// submodules require this since git 2.38.
		runGit(t, super, "-c", "protocol.file.allow=always",
			"submodule", "add", "file://"+subRepo, "sub")

		subPath := filepath.Join(super, "sub")
		if !detector.Detect(subPath) {
			t.Error("submodule not reported as git-backed")
		}
	})

	t.Run("nonexistent path is not git-backed", func(t *testing.T) {
		if detector.Detect(filepath.Join(t.TempDir(), "does-not-exist")) {
			t.Error("nonexistent path reported as git-backed")
		}
	})
}
