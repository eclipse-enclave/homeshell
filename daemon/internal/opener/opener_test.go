package opener

import (
	"context"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// The per-platform invocation is the whole of this package's knowledge, so it is
// the thing worth pinning. The path goes through untouched as the only argument:
// exec already makes quoting unnecessary, and our own would corrupt a path
// containing the character being escaped.
func TestOpenCommandPerPlatform(t *testing.T) {
	for _, tc := range []struct {
		goos, name string
	}{
		{"darwin", "open"},
		{"linux", "xdg-open"},
		{"freebsd", "xdg-open"},
	} {
		for _, path := range []string{
			"/work/plain.md",
			"/work/with space/notes.md",
			`/work/it's "quoted".md`,
			"/work/x&calc&/NOTES.md",
		} {
			name, args := openCommand(tc.goos, path)
			if name != tc.name || !slices.Equal(args, []string{path}) {
				t.Errorf("openCommand(%q, %q) = %q %v, want %s with the path as its only argument",
					tc.goos, path, name, args, tc.name)
			}
		}
	}
}

// An empty path is refused before anything is launched: the platform helpers
// treat a missing argument as "open the current directory" or as a usage error,
// neither of which is what a caller with nothing to open meant.
func TestOpenRefusesAnEmptyPath(t *testing.T) {
	err := OSOpener{}.Open(context.Background(), "")
	if err == nil {
		t.Fatal("Open(\"\") = nil, want an error rather than a launch")
	}
	if !strings.Contains(err.Error(), "no path") {
		t.Errorf("error = %q, want it to say there was no path", err)
	}
}

// A host without the helper on PATH is a real configuration (a headless server,
// a minimal container), and the error has to name what was missing — "could not
// open" alone leaves the user nothing to install.
func TestOpenReportsAMissingHelperByName(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	err := OSOpener{}.Open(context.Background(), "/work/notes.md")
	if err == nil {
		t.Fatal("Open() = nil on a host with no opener, want an error")
	}
	name, _ := openCommand(runtime.GOOS, "/work/notes.md")
	if !strings.Contains(err.Error(), name) {
		t.Errorf("error = %q, want it to name the missing %q helper", err, name)
	}
}
