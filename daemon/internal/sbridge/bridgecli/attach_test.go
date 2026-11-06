package bridgecli

import (
	"io"
	"testing"
)

func TestBGWatcherNotesAColourSet(t *testing.T) {
	tests := []struct {
		name, stream string
		want         bool
	}{
		{"plain output", "hello\r\n", false},
		{"colour set", "a\x1b]11;#102030\x07b", true},
		{"query", "a\x1b]11;?\x07b", false},
		{"query, then set", "\x1b]11;?\x07\x1b]11;rgb:1/2/3\x1b\\", true},
		{"foreground only", "\x1b]10;#ffffff\x07", false},
		{"prefix at the very end", "a\x1b]11;", false},
	}
	for _, tc := range tests {
		// Every split point, since PTY output arrives in arbitrary chunks.
		for cut := 0; cut <= len(tc.stream); cut++ {
			w := &bgWatcher{w: io.Discard}
			_, _ = w.Write([]byte(tc.stream[:cut]))
			_, _ = w.Write([]byte(tc.stream[cut:]))
			if w.set != tc.want {
				t.Errorf("%s, cut at %d: set = %v, want %v", tc.name, cut, w.set, tc.want)
			}
		}
	}
}
