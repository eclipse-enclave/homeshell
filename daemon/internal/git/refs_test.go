package git

import (
	"context"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"
)

func TestListRefs(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	// commitAt adds an empty commit dated seconds after the epoch, so the
	// expected order does not hang on the clock.
	commitAt := func(t *testing.T, dir string, seconds int64) {
		date := time.Unix(seconds, 0).UTC().Format(time.RFC3339)
		t.Setenv("GIT_AUTHOR_DATE", date)
		t.Setenv("GIT_COMMITTER_DATE", date)
		runGit(t, dir, "commit", "-q", "--allow-empty", "-m", date)
	}

	tests := []struct {
		name  string
		setup func(t *testing.T) string
		want  []Ref
	}{
		{
			name: "branches, remote-tracking branches and tags, newest first",
			setup: func(t *testing.T) string {
				upstream := t.TempDir()
				initEmptyRepo(t, upstream)
				runGit(t, upstream, "checkout", "-q", "-b", "main")
				commitAt(t, upstream, 1000)
				dir := filepath.Join(t.TempDir(), "clone")
				runGit(t, t.TempDir(), "clone", "-q", upstream, dir)
				runGit(t, dir, "tag", "v1.0")
				runGit(t, dir, "checkout", "-q", "-b", "feature")
				commitAt(t, dir, 3000)
				runGit(t, dir, "checkout", "-q", "-b", "older", "main")
				commitAt(t, dir, 2000)
				return dir
			},
			want: []Ref{
				{Name: "feature", Branch: "feature"},
				{Name: "older", Branch: "older", CheckedOut: true},
				{Name: "main", Branch: "main"},
				{Name: "origin/main"},
				{Name: "v1.0"},
			},
		},
		{
			name: "a branch that shares its name with a tag",
			setup: func(t *testing.T) string {
				dir := t.TempDir()
				initRepo(t, dir)
				runGit(t, dir, "branch", "-M", "main")
				runGit(t, dir, "branch", "foo")
				runGit(t, dir, "tag", "foo")
				return dir
			},
			want: []Ref{
				{Name: "heads/foo", Branch: "foo"},
				{Name: "main", Branch: "main", CheckedOut: true},
				{Name: "tags/foo"},
			},
		},
		{
			name: "names git would read as an option",
			setup: func(t *testing.T) string {
				dir := t.TempDir()
				initRepo(t, dir)
				runGit(t, dir, "branch", "-M", "main")
				runGit(t, dir, "update-ref", "refs/heads/-evil", "HEAD")
				runGit(t, dir, "update-ref", "refs/tags/--no-checkout", "HEAD")
				return dir
			},
			want: []Ref{{Name: "main", Branch: "main", CheckedOut: true}},
		},
		{
			name: "a branch checked out in a linked worktree",
			setup: func(t *testing.T) string {
				dir := t.TempDir()
				initRepo(t, dir)
				runGit(t, dir, "branch", "-M", "main")
				runGit(t, dir, "checkout", "-q", "--detach")
				runGit(t, dir, "worktree", "add", "-q", "-b", "linked", filepath.Join(t.TempDir(), "wt"))
				return dir
			},
			want: []Ref{
				{Name: "linked", Branch: "linked", CheckedOut: true},
				{Name: "main", Branch: "main"},
			},
		},
		{
			name: "empty repo",
			setup: func(t *testing.T) string {
				dir := t.TempDir()
				initEmptyRepo(t, dir)
				return dir
			},
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ListRefs(context.Background(), tt.setup(t))
			if err != nil {
				t.Fatalf("ListRefs: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ListRefs = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// Tags do not crowd out local branches: each part has its own cap.
func TestListRefsCapsEachPart(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	initRepo(t, dir)
	for i := range maxRefs + 5 {
		runGit(t, dir, "branch", "b"+strconv.Itoa(i))
		runGit(t, dir, "tag", "t"+strconv.Itoa(i))
	}
	refs, err := ListRefs(context.Background(), dir)
	if err != nil {
		t.Fatalf("ListRefs: %v", err)
	}
	localBranches := 0
	for _, ref := range refs {
		if ref.Branch != "" {
			localBranches++
		}
	}
	if localBranches != maxRefs || len(refs) != 2*maxRefs {
		t.Errorf("ListRefs returned %d local branches of %d refs, want %d of %d", localBranches, len(refs), maxRefs, 2*maxRefs)
	}
}

func TestListRefsNotARepository(t *testing.T) {
	if _, err := ListRefs(context.Background(), t.TempDir()); err == nil {
		t.Error("ListRefs outside a repository: want an error")
	}
}
