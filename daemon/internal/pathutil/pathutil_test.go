package pathutil

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveFrom(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	tests := []struct {
		raw  string
		want string
	}{
		{raw: "/abs/dir", want: "/abs/dir"},
		{raw: "~/worktrees", want: filepath.Join(home, "worktrees")},
		{raw: "../worktrees", want: "/projects/worktrees"},
		{raw: "sub/dir/", want: "/projects/repo/sub/dir"},
	}
	for _, tt := range tests {
		got, err := ResolveFrom(tt.raw, "/projects/repo")
		if err != nil || got != tt.want {
			t.Errorf("ResolveFrom(%q) = %q, %v; want %q", tt.raw, got, err, tt.want)
		}
	}
}

func TestResolveFromRejectsAnotherUsersHome(t *testing.T) {
	if _, err := ResolveFrom("~other/worktrees", "/projects/repo"); err == nil {
		t.Error("ResolveFrom accepted ~other/worktrees")
	}
}

func TestIsAnchored(t *testing.T) {
	tests := []struct {
		raw  string
		want bool
	}{
		{raw: "/abs/dir", want: true},
		{raw: "~", want: true},
		{raw: "~/worktrees", want: true},
		{raw: "~other/worktrees", want: true},
		{raw: "../worktrees", want: false},
		{raw: "copies", want: false},
		{raw: "", want: false},
	}
	for _, tt := range tests {
		if got := IsAnchored(tt.raw); got != tt.want {
			t.Errorf("IsAnchored(%q) = %v, want %v", tt.raw, got, tt.want)
		}
	}
}

func TestWithin(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{path: "/projects/repo", want: true},
		{path: "/projects/repo/copies/a", want: true},
		{path: "/projects/repo/..data", want: true},
		{path: "/projects/worktrees", want: false},
		{path: "/projects/repo-2", want: false},
		{path: "/", want: false},
	}
	for _, tt := range tests {
		if got := Within(tt.path, "/projects/repo"); got != tt.want {
			t.Errorf("Within(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}
