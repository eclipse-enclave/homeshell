package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestValidateBranchName(t *testing.T) {
	tests := []struct {
		name    string
		branch  string
		wantErr bool
	}{
		{"simple name", "main", false},
		{"with slash", "feature/foo", false},
		{"with dots", "release-1.2.3", false},
		{"with underscore", "my_branch", false},
		{"with dash", "my-branch", false},
		{"empty", "", true},
		{"space", "my branch", true},
		{"tilde", "my~branch", true},
		{"caret", "my^branch", true},
		{"colon", "my:branch", true},
		{"question mark", "my?branch", true},
		{"asterisk", "my*branch", true},
		{"backslash", `my\branch`, true},
		{"open bracket", "my[branch", true},
		{"double dot", "my..branch", true},
		{"double slash", "my//branch", true},
		{"at-brace", "my@{branch", true},
		{"dot-lock suffix", "my.lock", true},
		{"starts with dot", ".hidden", true},
		{"starts with dash", "-flag", true},
		{"ends with dot", "branch.", true},
		{"ends with slash", "branch/", true},
		{"single char", "x", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateBranchName(tt.branch)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateBranchName(%q) error = %v, wantErr %v", tt.branch, err, tt.wantErr)
			}
		})
	}
}

// TestBranchNameFrom checks that every branch it derives, bar "", is one
// ValidateBranchName accepts.
func TestBranchNameFrom(t *testing.T) {
	tests := []struct {
		text   string
		branch string
	}{
		{"Fix login bug", "fix-login-bug"},
		{"  Fix   login  ", "fix-login"},
		{"Äpfel & Birnen", "apfel-birnen"},
		{"Crème brûlée", "creme-brulee"},
		{"feature/Login page", "feature/login-page"},
		{"a / b", "a/b"},
		{"a//b", "a/b"},
		{"release 1.2.3", "release-1.2.3"},
		{"my_branch", "my_branch"},
		{"a..b", "a-b"},
		{"-flag", "flag"},
		{".hidden", "hidden"},
		{"branch/", "branch"},
		{"branch.", "branch"},
		{"my.lock", "my-lock"},
		{"my.locker", "my-locker"},
		{"my@{branch}", "my-branch"},
		{`what? * [x] ~^:\`, "what-x"},
		{"日本語", ""},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.text, func(t *testing.T) {
			got := BranchNameFrom(tt.text)
			if got != tt.branch {
				t.Errorf("BranchNameFrom(%q) = %q, want %q", tt.text, got, tt.branch)
			}
			if got == "" {
				return
			}
			if err := ValidateBranchName(got); err != nil {
				t.Errorf("ValidateBranchName(%q) = %v, want nil", got, err)
			}
		})
	}
}

func TestDefaultWorktreePath(t *testing.T) {
	tests := []struct {
		name     string
		repoDir  string
		branch   string
		wantBase string
	}{
		{
			name:     "simple branch",
			repoDir:  "/home/dev/myrepo",
			branch:   "main",
			wantBase: "myrepo-main",
		},
		{
			name:     "branch with slash",
			repoDir:  "/home/dev/myrepo",
			branch:   "feature/foo",
			wantBase: "myrepo-feature-foo",
		},
		{
			name:     "nested slashes",
			repoDir:  "/home/dev/myrepo",
			branch:   "bugfix/issue/123",
			wantBase: "myrepo-bugfix-issue-123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DefaultWorktreePath(tt.repoDir, tt.branch)
			if filepath.Base(got) != tt.wantBase {
				t.Errorf("DefaultWorktreePath(%q, %q) base = %q, want %q", tt.repoDir, tt.branch, filepath.Base(got), tt.wantBase)
			}
			if filepath.Dir(got) != filepath.Dir(tt.repoDir) {
				t.Errorf("DefaultWorktreePath(%q, %q) parent = %q, want %q", tt.repoDir, tt.branch, filepath.Dir(got), filepath.Dir(tt.repoDir))
			}
		})
	}
}

// --- integration tests requiring git ---
// These reuse initRepo and runGit from git_test.go.

// initEmptyRepo creates a temporary git repository with no commits.
func initEmptyRepo(t *testing.T, dir string) {
	t.Helper()
	runGit(t, dir, "init", "-q")
}

// defaultBranch returns the current branch name of the repo at dir.
func defaultBranch(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("git", "branch", "--show-current")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git branch --show-current: %v", err)
	}
	return string(out[:len(out)-1])
}

func TestHasCommits(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	t.Run("repo with commits", func(t *testing.T) {
		dir := t.TempDir()
		initRepo(t, dir)
		got, err := HasCommits(context.Background(), dir)
		if err != nil {
			t.Fatalf("HasCommits: %v", err)
		}
		if !got {
			t.Error("HasCommits = false, want true")
		}
	})

	t.Run("empty repo (no commits)", func(t *testing.T) {
		dir := t.TempDir()
		initEmptyRepo(t, dir)
		got, err := HasCommits(context.Background(), dir)
		if err != nil {
			t.Fatalf("HasCommits: %v", err)
		}
		if got {
			t.Error("HasCommits = true, want false")
		}
	})

	// A git killed for hanging is an error, not "no commits", even when it is
	// the query's own timeout that kills it.
	t.Run("a hung git is an error", func(t *testing.T) {
		dir := t.TempDir()
		initRepo(t, dir)
		hangingGit(t)
		defer func(previous time.Duration) { queryTimeout = previous }(queryTimeout)
		queryTimeout = 50 * time.Millisecond
		if _, err := HasCommits(context.Background(), dir); err == nil {
			t.Error("HasCommits(hung) err = nil, want an error")
		}
	})
}

func TestBranchExists(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	dir := t.TempDir()
	initRepo(t, dir)
	branch := defaultBranch(t, dir)

	t.Run("default branch exists", func(t *testing.T) {
		got, err := BranchExists(context.Background(), dir, branch)
		if err != nil {
			t.Fatalf("BranchExists: %v", err)
		}
		if !got {
			t.Errorf("BranchExists(%q) = false, want true", branch)
		}
	})

	t.Run("nonexistent branch", func(t *testing.T) {
		got, err := BranchExists(context.Background(), dir, "does-not-exist")
		if err != nil {
			t.Fatalf("BranchExists: %v", err)
		}
		if got {
			t.Error("BranchExists(does-not-exist) = true, want false")
		}
	})

	// A git killed for hanging is an error, not "no such branch".
	t.Run("a hung git is an error", func(t *testing.T) {
		hangingGit(t)
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		if _, err := BranchExists(ctx, dir, branch); err == nil {
			t.Error("BranchExists(hung) err = nil, want an error")
		}
		if _, err := BranchWorktree(ctx, dir, branch); err == nil {
			t.Error("BranchWorktree(hung) err = nil, want an error")
		}
	})
}

// hangingGit puts a git first on PATH that never answers.
func hangingGit(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\nexec sleep 10\n"), 0o755); err != nil {
		t.Fatalf("writing the hanging git: %v", err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestWorktreeAddAndRemove(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	dir := t.TempDir()
	initRepo(t, dir)
	wtPath := filepath.Join(t.TempDir(), "worktree")

	if err := WorktreeAdd(dir, wtPath, "feature/test", "", false); err != nil {
		t.Fatalf("WorktreeAdd: %v", err)
	}

	if _, err := os.Stat(filepath.Join(wtPath, "README.md")); err != nil {
		t.Errorf("worktree missing README.md: %v", err)
	}

	exists, err := BranchExists(context.Background(), dir, "feature/test")
	if err != nil {
		t.Fatalf("BranchExists: %v", err)
	}
	if !exists {
		t.Error("branch feature/test should exist after worktree add")
	}

	if err := WorktreeRemove(dir, wtPath); err != nil {
		t.Fatalf("WorktreeRemove: %v", err)
	}

	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Error("worktree directory still exists after removal")
	}

	// Branch is deliberately kept on removal.
	exists, err = BranchExists(context.Background(), dir, "feature/test")
	if err != nil {
		t.Fatalf("BranchExists: %v", err)
	}
	if !exists {
		t.Error("branch feature/test should still exist after worktree removal")
	}
}

func TestWorktreeAddBranchCollision(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	dir := t.TempDir()
	initRepo(t, dir)
	branch := defaultBranch(t, dir)

	// The default branch is checked out in the main worktree, so creating a
	// new worktree for it should fail (git won't allow it). The caller is
	// expected to check BranchCheckedOutInWorktree first, but git also refuses.
	checkedOut, err := BranchCheckedOutInWorktree(context.Background(), dir, branch)
	if err != nil {
		t.Fatalf("BranchCheckedOutInWorktree: %v", err)
	}
	if !checkedOut {
		t.Errorf("expected default branch %q to be checked out in a worktree", branch)
	}
}

func TestWorktreeAddNonGitDir(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	wtPath := filepath.Join(t.TempDir(), "wt")
	if err := WorktreeAdd(t.TempDir(), wtPath, "test-branch", "", false); err == nil {
		t.Error("expected error when creating worktree in non-git directory")
	}
}

// A ref that looks like an option must reach git as a ref, which it then
// refuses, rather than change what git does.
func TestWorktreeAddTakesOptionLikeRefsAsRefs(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	tests := []struct {
		name           string
		branch         string
		baseRef        string
		existingBranch bool
	}{
		{"base ref", "new-branch", "--no-checkout", false},
		{"existing branch", "--no-checkout", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			initRepo(t, dir)
			wtPath := filepath.Join(t.TempDir(), "wt")
			if err := WorktreeAdd(dir, wtPath, tt.branch, tt.baseRef, tt.existingBranch); err == nil {
				t.Errorf("WorktreeAdd(%q, %q) = nil, want git to refuse the ref", tt.branch, tt.baseRef)
			}
			if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
				t.Errorf("worktree %s exists, want none", wtPath)
			}
		})
	}
}

func TestBranchCheckedOutInWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	dir := t.TempDir()
	initRepo(t, dir)
	branch := defaultBranch(t, dir)

	t.Run("default branch is checked out in main worktree", func(t *testing.T) {
		got, err := BranchCheckedOutInWorktree(context.Background(), dir, branch)
		if err != nil {
			t.Fatalf("BranchCheckedOutInWorktree: %v", err)
		}
		if !got {
			t.Errorf("expected %q to be checked out", branch)
		}
	})

	t.Run("non-checked-out branch returns false", func(t *testing.T) {
		runGit(t, dir, "branch", "other-branch")
		got, err := BranchCheckedOutInWorktree(context.Background(), dir, "other-branch")
		if err != nil {
			t.Fatalf("BranchCheckedOutInWorktree: %v", err)
		}
		if got {
			t.Error("expected other-branch to NOT be checked out")
		}
	})

	t.Run("branch checked out in a worktree returns true", func(t *testing.T) {
		wtPath := filepath.Join(t.TempDir(), "wt")
		if err := WorktreeAdd(dir, wtPath, "wt-branch", "", false); err != nil {
			t.Fatalf("WorktreeAdd: %v", err)
		}
		got, err := BranchCheckedOutInWorktree(context.Background(), dir, "wt-branch")
		if err != nil {
			t.Fatalf("BranchCheckedOutInWorktree: %v", err)
		}
		if !got {
			t.Error("expected wt-branch to be checked out in worktree")
		}
		// Clean up worktree.
		if err := WorktreeRemove(dir, wtPath); err != nil {
			t.Fatalf("WorktreeRemove: %v", err)
		}
	})
}

// The main worktree and a linked one are told apart by their paths.
func TestBranchWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	initRepo(t, dir)
	branch := defaultBranch(t, dir)
	linked := filepath.Join(t.TempDir(), "wt")
	if err := WorktreeAdd(dir, linked, "wt-branch", "", false); err != nil {
		t.Fatalf("WorktreeAdd: %v", err)
	}
	gone := filepath.Join(t.TempDir(), "gone")
	if err := WorktreeAdd(dir, gone, "gone-branch", "", false); err != nil {
		t.Fatalf("WorktreeAdd: %v", err)
	}
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "branch", "idle")
	realPath := func(path string) string {
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			t.Fatalf("EvalSymlinks(%q): %v", path, err)
		}
		return resolved
	}

	tests := []struct {
		branch string
		want   string
	}{
		{branch: branch, want: realPath(dir)},
		{branch: "wt-branch", want: realPath(linked)},
		{branch: "idle", want: ""},
		{branch: "gone-branch", want: ""},
		{branch: "missing", want: ""},
	}
	for _, tt := range tests {
		got, err := BranchWorktree(context.Background(), dir, tt.branch)
		if err != nil {
			t.Fatalf("BranchWorktree(%q): %v", tt.branch, err)
		}
		if got != "" {
			got = realPath(got)
		}
		if got != tt.want {
			t.Errorf("BranchWorktree(%q) = %q, want %q", tt.branch, got, tt.want)
		}
	}
}

func TestWorktreeAddExistingBranch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	dir := t.TempDir()
	initRepo(t, dir)

	// Create a branch, then detach HEAD so it's not checked out anywhere.
	runGit(t, dir, "branch", "existing-branch")
	runGit(t, dir, "checkout", "--detach", "HEAD")

	wtPath := filepath.Join(t.TempDir(), "wt-existing")
	if err := WorktreeAdd(dir, wtPath, "existing-branch", "", true); err != nil {
		t.Fatalf("WorktreeAdd with existing branch: %v", err)
	}

	// Verify the worktree was created and has the repo files.
	if _, err := os.Stat(filepath.Join(wtPath, "README.md")); err != nil {
		t.Errorf("worktree missing README.md: %v", err)
	}

	// Verify the branch is now checked out in the worktree.
	checkedOut, err := BranchCheckedOutInWorktree(context.Background(), dir, "existing-branch")
	if err != nil {
		t.Fatalf("BranchCheckedOutInWorktree: %v", err)
	}
	if !checkedOut {
		t.Error("expected existing-branch to be checked out in worktree")
	}

	if err := WorktreeRemove(dir, wtPath); err != nil {
		t.Fatalf("WorktreeRemove: %v", err)
	}
}

func TestWorktreeAddWithBaseRef(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	dir := t.TempDir()
	initRepo(t, dir)

	// Make a second commit so we can branch from the first.
	runGit(t, dir, "commit", "--allow-empty", "-m", "second")

	// Get the first commit hash.
	cmd := exec.Command("git", "rev-list", "--max-parents=0", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("rev-list: %v", err)
	}
	firstCommit := strings.TrimSpace(string(out))

	wtPath := filepath.Join(t.TempDir(), "wt-baseref")
	if err := WorktreeAdd(dir, wtPath, "from-first", firstCommit, false); err != nil {
		t.Fatalf("WorktreeAdd with baseRef: %v", err)
	}

	// Verify the worktree HEAD points to the first commit.
	wtCmd := exec.Command("git", "rev-parse", "HEAD")
	wtCmd.Dir = wtPath
	wtOut, err := wtCmd.Output()
	if err != nil {
		t.Fatalf("rev-parse in worktree: %v", err)
	}
	if strings.TrimSpace(string(wtOut)) != firstCommit {
		t.Errorf("worktree HEAD = %s, want %s", strings.TrimSpace(string(wtOut)), firstCommit)
	}

	if err := WorktreeRemove(dir, wtPath); err != nil {
		t.Fatalf("WorktreeRemove: %v", err)
	}
}

func TestDefaultBaseRef(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	// DefaultBaseRef reads init.defaultBranch; keep the developer's out of it.
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")

	// clone returns a clone of a one-commit upstream whose branches are named
	// by branches, the first being the one checked out.
	clone := func(t *testing.T, branches ...string) string {
		upstream := t.TempDir()
		initRepo(t, upstream)
		runGit(t, upstream, "branch", "-M", branches[0])
		for _, branch := range branches[1:] {
			runGit(t, upstream, "branch", branch)
		}
		dir := filepath.Join(t.TempDir(), "clone")
		runGit(t, t.TempDir(), "clone", "-q", upstream, dir)
		return dir
	}
	// local returns a one-commit repository with the given branches, checked
	// out on the last one.
	local := func(t *testing.T, branches ...string) string {
		dir := t.TempDir()
		initRepo(t, dir)
		runGit(t, dir, "branch", "-M", branches[0])
		for _, branch := range branches[1:] {
			runGit(t, dir, "checkout", "-q", "-b", branch)
		}
		return dir
	}

	tests := []struct {
		name  string
		setup func(t *testing.T) string
		want  string
	}{
		{
			name:  "origin/HEAD names the remote's default branch",
			setup: func(t *testing.T) string { return clone(t, "develop", "main") },
			want:  "origin/develop",
		},
		{
			name: "origin/main without origin/HEAD",
			setup: func(t *testing.T) string {
				dir := clone(t, "main")
				runGit(t, dir, "remote", "set-head", "origin", "--delete")
				return dir
			},
			want: "origin/main",
		},
		{
			name: "origin/master without origin/HEAD",
			setup: func(t *testing.T) string {
				dir := clone(t, "master")
				runGit(t, dir, "remote", "set-head", "origin", "--delete")
				return dir
			},
			want: "origin/master",
		},
		{
			name: "origin/HEAD when a local branch is called origin/main too",
			setup: func(t *testing.T) string {
				dir := clone(t, "main")
				runGit(t, dir, "branch", "origin/main")
				return dir
			},
			want: "remotes/origin/main",
		},
		{
			name: "local main when a tag is called main too",
			setup: func(t *testing.T) string {
				dir := local(t, "main", "feature")
				runGit(t, dir, "tag", "main")
				return dir
			},
			want: "heads/main",
		},
		{
			name:  "local main while on a feature branch",
			setup: func(t *testing.T) string { return local(t, "main", "feature") },
			want:  "main",
		},
		{
			name:  "local master while on a feature branch",
			setup: func(t *testing.T) string { return local(t, "master", "feature") },
			want:  "master",
		},
		{
			name: "init.defaultBranch when that branch exists",
			setup: func(t *testing.T) string {
				dir := local(t, "trunk", "feature")
				runGit(t, dir, "config", "init.defaultBranch", "trunk")
				return dir
			},
			want: "trunk",
		},
		{
			name:  "the current branch when nothing else fits",
			setup: func(t *testing.T) string { return local(t, "trunk", "feature") },
			want:  "feature",
		},
		{
			name: "empty repo",
			setup: func(t *testing.T) string {
				dir := t.TempDir()
				initEmptyRepo(t, dir)
				return dir
			},
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DefaultBaseRef(context.Background(), tt.setup(t)); got != tt.want {
				t.Errorf("DefaultBaseRef = %q, want %q", got, tt.want)
			}
		})
	}
}

// A default base must start a worktree even where its short name would be
// ambiguous, which git refuses.
func TestWorktreeAddFromTheDefaultBaseRef(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	initRepo(t, dir)
	runGit(t, dir, "branch", "-M", "main")
	runGit(t, dir, "tag", "main")
	baseRef := DefaultBaseRef(context.Background(), dir)
	if err := WorktreeAdd(dir, filepath.Join(t.TempDir(), "wt"), "new-branch", baseRef, false); err != nil {
		t.Errorf("WorktreeAdd from %q: %v", baseRef, err)
	}
}

// A worktree whose folder was deleted by hand, leaving git's registration
// behind, does not block adding a new one at the same path.
func TestWorktreeAddOverAMissingRegisteredWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	initRepo(t, dir)
	worktreePath := filepath.Join(t.TempDir(), "wt")
	if err := WorktreeAdd(dir, worktreePath, "first", "", false); err != nil {
		t.Fatalf("first WorktreeAdd: %v", err)
	}
	if err := os.RemoveAll(worktreePath); err != nil {
		t.Fatal(err)
	}

	if err := WorktreeAdd(dir, worktreePath, "first", "", true); err != nil {
		t.Fatalf("WorktreeAdd over a missing registered worktree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(worktreePath, "README.md")); err != nil {
		t.Errorf("worktree missing README.md: %v", err)
	}
}

// Adding or removing a worktree leaves the registration of another whose
// folder is missing, say on a drive that is not mounted.
func TestWorktreeAddAndRemoveKeepAnotherMissingWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	initRepo(t, dir)
	drive := filepath.Join(t.TempDir(), "drive")
	if err := WorktreeAdd(dir, filepath.Join(drive, "unmounted"), "kept", "", false); err != nil {
		t.Fatalf("WorktreeAdd: %v", err)
	}
	if err := os.RemoveAll(drive); err != nil {
		t.Fatal(err)
	}
	stillRegistered := func(step string) {
		t.Helper()
		out, err := query(context.Background(), dir, "worktree", "list", "--porcelain")
		if err != nil {
			t.Fatalf("git worktree list: %v", err)
		}
		if !strings.Contains(out, "branch refs/heads/kept\n") {
			t.Errorf("after %s, the missing worktree is no longer registered:\n%s", step, out)
		}
	}

	worktreePath := filepath.Join(t.TempDir(), "wt")
	if err := WorktreeAdd(dir, worktreePath, "other", "", false); err != nil {
		t.Fatalf("WorktreeAdd: %v", err)
	}
	stillRegistered("an add")
	if err := WorktreeRemove(dir, worktreePath); err != nil {
		t.Fatalf("WorktreeRemove: %v", err)
	}
	stillRegistered("a remove")
}

// A worktree whose folder was removed by hand and whose registration was then
// pruned can still be removed.
func TestWorktreeRemoveWhatIsAlreadyGone(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	initRepo(t, dir)
	worktreePath := filepath.Join(t.TempDir(), "wt")
	if err := WorktreeAdd(dir, worktreePath, "gone", "", false); err != nil {
		t.Fatalf("WorktreeAdd: %v", err)
	}
	if err := os.RemoveAll(worktreePath); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "worktree", "prune")

	if err := WorktreeRemove(dir, worktreePath); err != nil {
		t.Errorf("WorktreeRemove: %v", err)
	}
}

// A folder that is no worktree is not taken for one already gone.
func TestWorktreeRemoveRefusesAFolderThatIsNoWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	initRepo(t, dir)
	if err := WorktreeRemove(dir, t.TempDir()); err == nil {
		t.Error("WorktreeRemove of a plain folder succeeded")
	}
}

// A worktree whose folder was removed by hand keeps its branch registered, but
// the branch counts as checked out nowhere, and a worktree elsewhere can take it.
func TestWorktreeAddOverAMissingWorktreeOfItsBranchElsewhere(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	initRepo(t, dir)
	stale := filepath.Join(t.TempDir(), "stale")
	if err := WorktreeAdd(dir, stale, "feat", "", false); err != nil {
		t.Fatalf("WorktreeAdd: %v", err)
	}
	if err := os.RemoveAll(stale); err != nil {
		t.Fatal(err)
	}
	if worktree, err := BranchWorktree(context.Background(), dir, "feat"); err != nil || worktree != "" {
		t.Fatalf("BranchWorktree(feat) = %q, %v; want none", worktree, err)
	}

	worktreePath := filepath.Join(t.TempDir(), "wt")
	if err := WorktreeAdd(dir, worktreePath, "feat", "", true); err != nil {
		t.Fatalf("WorktreeAdd of the branch elsewhere: %v", err)
	}
	if _, err := os.Stat(filepath.Join(worktreePath, "README.md")); err != nil {
		t.Errorf("worktree missing README.md: %v", err)
	}
}

// A worktree whose folder is missing along with the folder it sat in may be on
// a drive not mounted: its branch still counts as checked out, and adding a
// worktree of that branch elsewhere is refused rather than dropping it.
func TestWorktreeAddKeepsAWorktreeOfItsBranchOutOfReach(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	initRepo(t, dir)
	drive := filepath.Join(t.TempDir(), "drive")
	if err := WorktreeAdd(dir, filepath.Join(drive, "wt"), "feat", "", false); err != nil {
		t.Fatalf("WorktreeAdd: %v", err)
	}
	if err := os.RemoveAll(drive); err != nil {
		t.Fatal(err)
	}
	if worktree, err := BranchWorktree(context.Background(), dir, "feat"); err != nil || worktree == "" {
		t.Errorf("BranchWorktree(feat) = %q, %v; want the worktree out of reach", worktree, err)
	}

	if err := WorktreeAdd(dir, filepath.Join(t.TempDir(), "wt"), "feat", "", true); err == nil {
		t.Error("WorktreeAdd of the branch elsewhere succeeded")
	}
	entries, err := worktrees(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(entries, func(entry worktree) bool { return entry.branch == "feat" }) {
		t.Errorf("the worktree out of reach is no longer registered: %+v", entries)
	}
}

// A worktree path with a newline in it is listed whole.
func TestWorktreesListsAPathWithANewline(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	initRepo(t, dir)
	worktreePath := filepath.Join(t.TempDir(), "two\nlines")
	if err := WorktreeAdd(dir, worktreePath, "newline", "", false); err != nil {
		t.Skipf("git cannot add a worktree at a path with a newline: %v", err)
	}
	worktree, err := BranchWorktree(context.Background(), dir, "newline")
	if err != nil {
		t.Fatalf("BranchWorktree: %v", err)
	}
	if !SamePath(worktree, worktreePath) {
		t.Errorf("BranchWorktree(newline) = %q, want %q", worktree, worktreePath)
	}
}

// Paths through a symlink name the same file as the paths it points to, even
// where neither they nor their parent exist.
func TestSamePathThroughASymlink(t *testing.T) {
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("cannot create a symlink: %v", err)
	}
	for _, rest := range []string{"", "missing", filepath.Join("missing", "deeper")} {
		if !SamePath(filepath.Join(link, rest), filepath.Join(target, rest)) {
			t.Errorf("SamePath(%q, %q) = false, want true", filepath.Join(link, rest), filepath.Join(target, rest))
		}
	}
	if SamePath(filepath.Join(link, "missing", "one"), filepath.Join(target, "missing", "other")) {
		t.Error("SamePath of two missing paths apart = true, want false")
	}
}

// recordingGit puts a git first on PATH that runs the real one and records the
// arguments of each run to the returned file.
func recordingGit(t *testing.T) string {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not installed")
	}
	bin := t.TempDir()
	record := filepath.Join(bin, "record")
	script := fmt.Sprintf("#!/bin/sh\necho \"$*\" >> %q\nexec %q \"$@\"\n", record, realGit)
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatalf("writing the recording git: %v", err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return record
}

func recordedRuns(t *testing.T, record string) string {
	t.Helper()
	runs, err := os.ReadFile(record)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return string(runs)
}

// A git before 2.36, which has no `worktree list -z`, still lists worktrees:
// one whose folder was removed by hand is found, replaced and removed.
func TestWorktreesWithAGitThatHasNoNULOutput(t *testing.T) {
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not installed")
	}
	bin := t.TempDir()
	script := fmt.Sprintf("#!/bin/sh\nfor arg in \"$@\"; do\n  if [ \"$arg\" = -z ]; then echo \"error: unknown switch \\`z'\" >&2; exit 129; fi\ndone\nexec %q \"$@\"\n", realGit)
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatalf("writing the old git: %v", err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	dir := t.TempDir()
	initRepo(t, dir)
	worktreePath := filepath.Join(t.TempDir(), "wt")
	if err := WorktreeAdd(dir, worktreePath, "old", "", false); err != nil {
		t.Fatalf("WorktreeAdd: %v", err)
	}
	if worktree, err := BranchWorktree(context.Background(), dir, "old"); err != nil || !SamePath(worktree, worktreePath) {
		t.Fatalf("BranchWorktree(old) = %q, %v; want %q", worktree, err, worktreePath)
	}
	if err := os.RemoveAll(worktreePath); err != nil {
		t.Fatal(err)
	}

	if err := WorktreeAdd(dir, worktreePath, "old", "", true); err != nil {
		t.Fatalf("WorktreeAdd over the missing worktree: %v", err)
	}
	if err := WorktreeRemove(dir, worktreePath); err != nil {
		t.Errorf("WorktreeRemove: %v", err)
	}
}

// A git before 2.31 marks no worktree prunable or locked: one whose folder was
// removed by hand is told by the folder being gone, and its branch can be taken
// elsewhere, while one out of reach still keeps its branch.
func TestWorktreesWithAGitThatMarksNothingPrunable(t *testing.T) {
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not installed")
	}
	bin := t.TempDir()
	script := fmt.Sprintf("#!/bin/sh\nfor arg in \"$@\"; do\n  if [ \"$arg\" = -z ]; then echo \"error: unknown switch \\`z'\" >&2; exit 129; fi\ndone\nif [ \"$1 $2\" = \"worktree list\" ]; then\n  %q \"$@\" | grep -v -e '^prunable' -e '^locked'\n  exit 0\nfi\nexec %q \"$@\"\n", realGit, realGit)
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatalf("writing the old git: %v", err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	dir := t.TempDir()
	initRepo(t, dir)
	stale := filepath.Join(t.TempDir(), "stale")
	if err := WorktreeAdd(dir, stale, "feat", "", false); err != nil {
		t.Fatalf("WorktreeAdd: %v", err)
	}
	drive := filepath.Join(t.TempDir(), "drive")
	if err := WorktreeAdd(dir, filepath.Join(drive, "wt"), "kept", "", false); err != nil {
		t.Fatalf("WorktreeAdd: %v", err)
	}
	for _, folder := range []string{stale, drive} {
		if err := os.RemoveAll(folder); err != nil {
			t.Fatal(err)
		}
	}

	if worktree, err := BranchWorktree(context.Background(), dir, "feat"); err != nil || worktree != "" {
		t.Errorf("BranchWorktree(feat) = %q, %v; want none", worktree, err)
	}
	if worktree, err := BranchWorktree(context.Background(), dir, "kept"); err != nil || worktree == "" {
		t.Errorf("BranchWorktree(kept) = %q, %v; want the worktree out of reach", worktree, err)
	}
	if err := WorktreeAdd(dir, filepath.Join(t.TempDir(), "wt"), "feat", "", true); err != nil {
		t.Errorf("WorktreeAdd of the branch elsewhere: %v", err)
	}
	if err := WorktreeAdd(dir, stale, "again", "", false); err != nil {
		t.Errorf("WorktreeAdd at the missing worktree's folder: %v", err)
	}
}

// Adding a worktree at a fresh path removes no stale registration.
func TestWorktreeAddAtAFreshPathRemovesNothing(t *testing.T) {
	record := recordingGit(t)
	dir := t.TempDir()
	initRepo(t, dir)

	if err := WorktreeAdd(dir, filepath.Join(t.TempDir(), "wt"), "fresh", "", false); err != nil {
		t.Fatalf("WorktreeAdd: %v", err)
	}
	if runs := recordedRuns(t, record); strings.Contains(runs, "worktree remove") {
		t.Errorf("WorktreeAdd removed a worktree:\n%s", runs)
	}
}

// A worktree already gone from disk and from git counts as removed by what git
// lists, not by what git says, which may be translated.
func TestWorktreeRemoveOfWhatIsGoneAsksGitNothingElse(t *testing.T) {
	record := recordingGit(t)
	dir := t.TempDir()
	initRepo(t, dir)
	if err := WorktreeRemove(dir, filepath.Join(t.TempDir(), "never-added")); err != nil {
		t.Fatalf("WorktreeRemove: %v", err)
	}
	if runs := recordedRuns(t, record); strings.Contains(runs, "worktree remove") {
		t.Errorf("WorktreeRemove ran git worktree remove:\n%s", runs)
	}
}

// A worktree whose folder was removed by hand but is still registered is
// removed from git's list.
func TestWorktreeRemoveOfAMissingRegisteredWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	initRepo(t, dir)
	worktreePath := filepath.Join(t.TempDir(), "wt")
	if err := WorktreeAdd(dir, worktreePath, "missing", "", false); err != nil {
		t.Fatalf("WorktreeAdd: %v", err)
	}
	if err := os.RemoveAll(worktreePath); err != nil {
		t.Fatal(err)
	}

	if err := WorktreeRemove(dir, worktreePath); err != nil {
		t.Fatalf("WorktreeRemove: %v", err)
	}
	entries, err := worktrees(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.branch == "missing" {
			t.Errorf("worktree %s is still registered", entry.path)
		}
	}
}

// A failed add says what git said went wrong, without its progress lines,
// "fatal:" prefix or exit status.
func TestWorktreeAddSaysWhatWentWrong(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	initRepo(t, dir)

	err := WorktreeAdd(dir, filepath.Join(t.TempDir(), "wt"), "topic", "no-such-ref", false)
	if err == nil {
		t.Fatal("WorktreeAdd from an unknown ref succeeded")
	}
	if got, want := err.Error(), "invalid reference: no-such-ref"; got != want {
		t.Errorf("error = %q, want %q", got, want)
	}
}

func TestFailure(t *testing.T) {
	exitErr := errors.New("exit status 128")
	tests := []struct {
		name   string
		stderr string
		want   string
	}{
		{name: "a fatal line after progress", stderr: "Preparing worktree (new branch 'x')\nfatal: cannot lock ref 'refs/heads/x'\n", want: "cannot lock ref 'refs/heads/x'"},
		{name: "error and fatal lines", stderr: "error: one\nfatal: two\n", want: "one; two"},
		{name: "no prefixed line", stderr: "something odd\n", want: "something odd"},
		{name: "nothing written", stderr: "", want: "exit status 128"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := failure(tt.stderr, exitErr).Error(); got != tt.want {
				t.Errorf("failure() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCurrentBranch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	t.Run("the checked-out branch", func(t *testing.T) {
		dir := t.TempDir()
		initRepo(t, dir)
		runGit(t, dir, "checkout", "-q", "-b", "feature/x")
		got, err := CurrentBranch(context.Background(), dir)
		if err != nil || got != "feature/x" {
			t.Errorf("CurrentBranch = %q, %v; want feature/x", got, err)
		}
	})

	// An unborn branch is still the one a first commit lands on.
	t.Run("a repository with no commits", func(t *testing.T) {
		dir := t.TempDir()
		initEmptyRepo(t, dir)
		runGit(t, dir, "symbolic-ref", "HEAD", "refs/heads/trunk")
		got, err := CurrentBranch(context.Background(), dir)
		if err != nil || got != "trunk" {
			t.Errorf("CurrentBranch = %q, %v; want trunk", got, err)
		}
	})

	t.Run("a detached HEAD has none", func(t *testing.T) {
		dir := t.TempDir()
		initRepo(t, dir)
		runGit(t, dir, "checkout", "-q", "--detach")
		got, err := CurrentBranch(context.Background(), dir)
		if err != nil || got != "" {
			t.Errorf("CurrentBranch = %q, %v; want no branch and no error", got, err)
		}
	})

	t.Run("not a repository is an error", func(t *testing.T) {
		if _, err := CurrentBranch(context.Background(), t.TempDir()); err == nil {
			t.Error("CurrentBranch(not a repo) err = nil, want an error")
		}
	})
}

func TestHeadBranch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := t.TempDir()
	initRepo(t, repo)
	runGit(t, repo, "checkout", "-q", "-b", "feature/x")
	worktree := filepath.Join(t.TempDir(), "wt")
	runGit(t, repo, "worktree", "add", "-q", "-b", "topic", worktree)
	detached := filepath.Join(t.TempDir(), "detached")
	runGit(t, repo, "worktree", "add", "-q", "--detach", detached)
	// A folder inside a repository that is no repository itself, as a copy
	// made without .git is.
	inside := filepath.Join(repo, "copy")
	if err := os.Mkdir(inside, 0o755); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		dir     string
		want    string
		wantErr bool
	}{
		{"a repository's checked-out branch", repo, "feature/x", false},
		{"a linked worktree's own branch", worktree, "topic", false},
		{"a detached HEAD has none", detached, "", false},
		{"a folder with no .git of its own is an error", inside, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := HeadBranch(context.Background(), tt.dir)
			if got != tt.want || (err != nil) != tt.wantErr {
				t.Errorf("HeadBranch = %q, %v; want %q, error %v", got, err, tt.want, tt.wantErr)
			}
		})
	}
}

func TestEnclosingHeadBranch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := t.TempDir()
	initRepo(t, repo)
	runGit(t, repo, "checkout", "-q", "-b", "feature/x")
	below := filepath.Join(repo, "sub", "dir")
	if err := os.MkdirAll(below, 0o755); err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(t.TempDir(), "wt")
	runGit(t, repo, "worktree", "add", "-q", "-b", "topic", worktree)
	worktreeBelow := filepath.Join(worktree, "sub")
	if err := os.Mkdir(worktreeBelow, 0o755); err != nil {
		t.Fatal(err)
	}
	detached := filepath.Join(t.TempDir(), "detached")
	runGit(t, repo, "worktree", "add", "-q", "--detach", detached)

	tests := []struct {
		name    string
		dir     string
		want    string
		wantErr bool
	}{
		{"a repository's checked-out branch", repo, "feature/x", false},
		{"a folder below the repository's root", below, "feature/x", false},
		{"a folder below a linked worktree's root", worktreeBelow, "topic", false},
		{"a detached HEAD has none", detached, "", false},
		{"no repository around it is an error", t.TempDir(), "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := EnclosingHeadBranch(context.Background(), tt.dir)
			if got != tt.want || (err != nil) != tt.wantErr {
				t.Errorf("EnclosingHeadBranch = %q, %v; want %q, error %v", got, err, tt.want, tt.wantErr)
			}
		})
	}
}

func TestLinkedWorktreesSkipsTheMainWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	initRepo(t, dir)
	parent := t.TempDir()
	withBranch, detached := filepath.Join(parent, "a"), filepath.Join(parent, "b")
	runGit(t, dir, "worktree", "add", "-b", "wt-a", withBranch)
	runGit(t, dir, "worktree", "add", "--detach", detached)

	got, err := LinkedWorktrees(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []LinkedWorktree{{Path: RealPath(withBranch), Branch: "wt-a"}, {Path: RealPath(detached)}}
	slices.SortFunc(got, func(a, b LinkedWorktree) int { return strings.Compare(a.Path, b.Path) })
	if !slices.Equal(got, want) {
		t.Errorf("LinkedWorktrees = %+v; want %+v", got, want)
	}
}

func TestLinkedWorktreesFromALinkedWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	initRepo(t, dir)
	linked := filepath.Join(t.TempDir(), "wt")
	runGit(t, dir, "worktree", "add", "-b", "wt-a", linked)

	got, err := LinkedWorktrees(context.Background(), linked)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !SamePath(got[0].Path, linked) {
		t.Errorf("LinkedWorktrees from a linked worktree = %+v; want only %s", got, linked)
	}
}

func TestLinkedWorktreesMissingAndDeletedFlags(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	keep := func(_, _ string) string { return "" }
	tests := []struct {
		name        string
		lock        bool
		remove      func(parent, worktree string) string
		wantMissing bool
		wantDeleted bool
	}{
		{"folder there", false, keep, false, false},
		{"folder deleted", false, func(_, worktree string) string { return worktree }, true, true},
		{"folder out of reach", false, func(parent, _ string) string { return parent }, true, false},
		{"locked, folder out of reach", true, func(parent, _ string) string { return parent }, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			initRepo(t, dir)
			parent := filepath.Join(t.TempDir(), "drive")
			linked := filepath.Join(parent, "wt")
			runGit(t, dir, "worktree", "add", "-b", "wt-a", linked)
			if tt.lock {
				runGit(t, dir, "worktree", "lock", linked)
			}
			if removed := tt.remove(parent, linked); removed != "" {
				if err := os.RemoveAll(removed); err != nil {
					t.Fatal(err)
				}
			}

			got, err := LinkedWorktrees(context.Background(), dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 || got[0].Missing != tt.wantMissing || got[0].Deleted != tt.wantDeleted {
				t.Errorf("LinkedWorktrees = %+v; want one with Missing=%v, Deleted=%v",
					got, tt.wantMissing, tt.wantDeleted)
			}
		})
	}
}

func TestLinkedWorktreesOutsideARepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	if _, err := LinkedWorktrees(context.Background(), t.TempDir()); err == nil {
		t.Error("LinkedWorktrees outside a repository succeeded")
	}
}

func TestLinkedWorktreesOfEntries(t *testing.T) {
	tests := []struct {
		name    string
		entries []worktree
		want    []LinkedWorktree
		wantErr bool
	}{
		{name: "no entry, not even the main worktree", wantErr: true},
		{name: "the main worktree only", entries: []worktree{{path: "/repo"}}},
		{name: "a linked worktree", entries: []worktree{{path: "/repo"}, {path: "/wt", branch: "wt-a"}},
			want: []LinkedWorktree{{Path: "/wt", Branch: "wt-a"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := linkedWorktrees(tt.entries)
			if (err != nil) != tt.wantErr || !slices.Equal(got, tt.want) {
				t.Errorf("linkedWorktrees = %+v, %v; want %+v, error %v", got, err, tt.want, tt.wantErr)
			}
		})
	}
}
