package local

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHasConversation(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	// The directory name is the working directory with its separators mangled; the
	// lookup globs for the file name, so any name will do here.
	project := filepath.Join(dir, "projects", "-tmp-proj")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	const known = "11111111-2222-3333-4444-555555555555"
	if err := os.WriteFile(filepath.Join(project, known+".jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		id   string
		want bool
	}{
		{"a session with a transcript", known, true},
		{"a session that never answered", "99999999-2222-3333-4444-555555555555", false},
		// The id reaches the lookup as a path fragment: anything that could escape
		// the projects directory or widen the glob is refused.
		{"empty", "", false},
		{"blank", "  ", false},
		{"a traversal", "../../etc/passwd", false},
		{"a glob", "*", false},
		{"a separator", "a/b", false},
		{"a backslash", `a\b`, false},
		{"a wildcard", "a?b", false},
		{"a class", "a[b]", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasConversation(tt.id); got != tt.want {
				t.Errorf("hasConversation(%q) = %v, want %v", tt.id, got, tt.want)
			}
		})
	}
}
