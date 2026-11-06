package capture

import (
	"strings"
	"testing"
)

// Agents lay out text with wide runes two cells wide; the screen must agree or
// everything after one on its row is drawn a cell to the left.
func TestWideRunesTakeTwoCells(t *testing.T) {
	tests := []struct {
		name, input string
		cols        int
		want        []string // the screen's first rows
	}{
		{"emoji", "a✨b", 10, []string{"a✨b"}},
		{"CJK, then absolute column", "日本\x1b[1;6Hx", 10, []string{"日本 x"}},
		{"emoji, then relative move", "✨\x1b[2Cx", 10, []string{"✨  x"}},
		{"fills the row, then wraps", "abc✨x", 5, []string{"abc✨", "x"}},
		{"does not fit the last column", "abcd✨", 5, []string{"abcd", "✨"}},
		{"after a pending wrap", "abcde✨", 5, []string{"abcde", "✨"}},
		{"left half overwritten", "✨b\rx", 10, []string{"x b"}},
		{"right half overwritten", "✨b\x1b[1;2Hy", 10, []string{" yb"}},
		{"in a title it is text", "\x1b]0;✨ title\x07ok", 10, []string{"ok"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := New(tc.cols, 4)
			c.Feed([]byte(tc.input))
			rows := strings.Split(c.Snapshot().Screen, "\n")
			for i, want := range tc.want {
				if rows[i] != want {
					t.Errorf("row %d = %q, want %q (screen %q)", i, rows[i], want, rows)
				}
			}
		})
	}
}

func TestCursorReportCountsWideRunesTwice(t *testing.T) {
	c := New(80, 24)
	c.AnswerQueries()
	if got, want := string(c.Feed([]byte("✨日\x1b[6n"))), "\x1b[1;5R"; got != want {
		t.Errorf("cursor report %q, want %q", got, want)
	}
}

func TestRepaintDrawsWideRunesOnce(t *testing.T) {
	c := New(80, 24)
	c.Feed([]byte("a✨b"))
	if got := string(c.RepaintBytes()); !strings.Contains(got, "a✨b\x1b[1;5H") {
		t.Errorf("repaint %q, want the row once and the cursor after b", got)
	}
}
