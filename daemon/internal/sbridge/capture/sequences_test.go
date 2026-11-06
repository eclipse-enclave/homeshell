package capture

import (
	"fmt"
	"strings"
	"testing"
)

func TestFeedAnswersTerminalQueries(t *testing.T) {
	tests := []struct {
		name, input, want string
	}{
		{"device attributes", "\x1b[c", replyDA1},
		{"device attributes with 0", "\x1b[0c", replyDA1},
		{"device status", "\x1b[5n", replyDSR},
		{"cursor position after text", "abc\r\nde\x1b[6n", "\x1b[2;3R"},
		{"cursor position mid chunk", "ab\x1b[6nlonger text after", "\x1b[1;3R"},
		{"foreground colour, BEL", "\x1b]10;?\x07", "\x1b]" + replyFGColor + "\x07"},
		{"background colour, ST", "\x1b]11;?\x1b\\", "\x1b]" + replyBGColor + "\x1b\\"},
		{"several, in order", "\x1b[?u\x1b[c\x1b[6n\x1b]10;?\x1b\\", replyDA1 + "\x1b[1;1R" + "\x1b]" + replyFGColor + "\x1b\\"},
		{"secondary attributes are not answered", "\x1b[>c", ""},
		{"kitty keyboard probe is not answered", "\x1b[?u", ""},
		{"version query is not answered", "\x1b[>q", ""},
		{"colour set is not a query", "\x1b]11;rgb:1/2/3\x07", ""},
		{"title is not a query", "\x1b]0;title\x07", ""},
		{"aborted sequence", "\x1b[6\x18n", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := New(80, 24)
			c.AnswerQueries()
			if got := string(c.Feed([]byte(tc.input))); got != tc.want {
				t.Errorf("replies %q, want %q", got, tc.want)
			}
		})
	}
}

// Queries split across PTY reads are answered once, and the screen renders
// the same as without answering.
func TestFeedAnswersQueriesAcrossChunks(t *testing.T) {
	input := []byte("● 日本語 ready\r\n\x1b[6n\x1b]11;?\x1b\\❯ prompt\x1b[c")
	want := "\x1b[2;1R" + "\x1b]" + replyBGColor + "\x1b\\" + replyDA1

	plain := New(80, 24)
	plain.Feed(input)
	ref := plain.Snapshot().Screen

	for chunk := 1; chunk <= len(input); chunk++ {
		c := New(80, 24)
		c.AnswerQueries()
		var got []byte
		for off := 0; off < len(input); off += chunk {
			got = append(got, c.Feed(input[off:min(off+chunk, len(input))])...)
		}
		if string(got) != want {
			t.Errorf("chunk=%d: replies %q, want %q", chunk, got, want)
		}
		if screen := c.Snapshot().Screen; screen != ref {
			t.Errorf("chunk=%d: screen differs from a capture that does not answer:\n%s\nwant:\n%s", chunk, screen, ref)
		}
	}
}

func TestFeedIsSilentUnlessAnswering(t *testing.T) {
	c := New(80, 24)
	if got := c.Feed([]byte("\x1b[c\x1b[6n\x1b]11;?\x07")); got != nil {
		t.Errorf("replies %q without AnswerQueries", got)
	}
}

// Sequences vt10x does not implement must leave the screen, cursor and
// attributes exactly as if they were not there, however the output is split.
func TestFeedCutsOutMisreadSequences(t *testing.T) {
	// Saved cursor at the top, bold text lower down, then a relative redraw:
	// the shape of Claude's trust dialog, whose kitty probe sent the redraw to
	// the top row.
	before := "\x1b7\x1b[1m\x1b[5;5Hab"
	after := "\x1b[1A\x1b[2Dcd\x1b[3;1Hdone"
	tests := []struct{ name, seq string }{
		{"kitty keyboard query", "\x1b[?u"},
		{"kitty keyboard push", "\x1b[>7u"},
		{"kitty keyboard pop", "\x1b[<u"},
		{"kitty keyboard pop with count", "\x1b[<1u"},
		{"kitty keyboard set", "\x1b[=1;1u"},
		{"modifyOtherKeys", "\x1b[>4;0m"},
		{"modifyOtherKeys reset", "\x1b[>4m"},
		{"private save", "\x1b[?1049s"},
		{"version query", "\x1b[>0q"},
		{"cursor style", "\x1b[0 q"},
		{"mode request", "\x1b[?2026$p"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			want := New(40, 10)
			want.Feed([]byte(before + after))
			input := []byte(before + tc.seq + after)
			for chunk := 1; chunk <= len(input); chunk++ {
				c := New(40, 10)
				for off := 0; off < len(input); off += chunk {
					c.Feed(input[off:min(off+chunk, len(input))])
				}
				if diff := renderDiff(c, want); diff != "" {
					t.Fatalf("chunk=%d: %s\n%s", chunk, diff, c.Snapshot().Screen)
				}
			}
		})
	}
}

// Sequences vt10x implements still reach it.
func TestFeedPassesImplementedSequences(t *testing.T) {
	tests := []struct{ name, input, want string }{
		{"private mode", "abc\x1b[?1049hxy", "   xy"},
		{"selective erase", "abc\x1b[?2Jxy", "   xy"},
		{"plain restore", "\x1b[2;2Hab\x1b[s\x1b[Hx\x1b[uy", "x\n aby"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := New(20, 3)
			c.Feed([]byte(tc.input))
			if got := strings.TrimRight(c.Snapshot().Screen, "\n"); got != tc.want {
				t.Errorf("screen %q, want %q", got, tc.want)
			}
		})
	}
}

func TestIncompleteCSIStart(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"text", 4},
		{"ab\x1b", 2},
		{"ab\x1b[", 2},
		{"ab\x1b[>4;", 2},
		{"ab\x1b[0 ", 2},
		{"ab\x1b[1m", 6},
		{"ab\x1b]0;title", 11},
		{"ab\x1b[1\r", 6},
		{"ab\x1b[" + strings.Repeat("1", maxHeldSeq), 4 + maxHeldSeq},
	}
	for _, tc := range tests {
		if got := incompleteCSIStart([]byte(tc.input)); got != tc.want {
			t.Errorf("incompleteCSIStart(%q) = %d, want %d", tc.input, got, tc.want)
		}
	}
}

// renderDiff compares every cell and the cursor of two captures.
func renderDiff(got, want *Capture) string {
	got.vt.Lock()
	defer got.vt.Unlock()
	want.vt.Lock()
	defer want.vt.Unlock()
	if g, w := got.vt.Cursor(), want.vt.Cursor(); g.X != w.X || g.Y != w.Y || g.Attr != w.Attr {
		return fmt.Sprintf("cursor %+v, want %+v", g, w)
	}
	cols, rows := want.vt.Size()
	for y := 0; y < rows; y++ {
		for x := 0; x < cols; x++ {
			if g, w := got.vt.Cell(x, y), want.vt.Cell(x, y); g != w {
				return fmt.Sprintf("cell %d,%d is %+v, want %+v", x, y, g, w)
			}
		}
	}
	return ""
}

func TestSnapshotTitle(t *testing.T) {
	tests := []struct{ name, input, want string }{
		{"set", "\x1b]0;✳ Claude Code\x07", "✳ Claude Code"},
		{"window title, ST", "\x1b]2;codex\x1b\\", "codex"},
		{"cleared on exit", "\x1b]0;✳ Claude Code\x07\x1b]0;\x07", ""},
		{"cleared with ST", "\x1b]2;codex\x1b\\\x1b]2;\x1b\\", ""},
		{"set again after clearing", "\x1b]0;a\x07\x1b]0;\x07\x1b]0;a\x07", "a"},
		{"long title after clearing", "\x1b]0;\x07\x1b]0;" + strings.Repeat("x", 100) + "\x07", strings.Repeat("x", 100)},
		{"other OSC keeps the title", "\x1b]0;a\x07\x1b]11;?\x07\x1b]9;4;0\x07", "a"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			input := []byte(tc.input)
			for chunk := 1; chunk <= len(input); chunk++ {
				c := New(80, 24)
				for off := 0; off < len(input); off += chunk {
					c.Feed(input[off:min(off+chunk, len(input))])
				}
				if got := c.Snapshot().OSCTitle; got != tc.want {
					t.Fatalf("chunk=%d: title %q, want %q", chunk, got, tc.want)
				}
			}
		})
	}
}

// A terminal that attaches later never saw the agent's background colour, so
// the repaint sets it before clearing.
func TestRepaintReplaysTheBackgroundColour(t *testing.T) {
	tests := []struct {
		name, input, want string
	}{
		{"none set", "hi", ""},
		{"set, BEL", "\x1b]11;#102030\x07hi", "\x1b]11;#102030\x07"},
		{"set, ST", "\x1b]11;rgb:10/20/30\x1b\\hi", "\x1b]11;rgb:10/20/30\x07"},
		{"last one wins", "\x1b]11;#102030\x07\x1b]11;#405060\x07hi", "\x1b]11;#405060\x07"},
		{"reset", "\x1b]11;#102030\x07\x1b]111\x07hi", ""},
		{"a query sets nothing", "\x1b]11;?\x07hi", ""},
		{"a truncated colour is not replayed", "\x1b]11;" + strings.Repeat("x", seqBufCap) + "\x07hi", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := New(80, 24)
			c.Feed([]byte(tc.input))
			got := string(c.RepaintBytes())
			if want := tc.want + "\x1b[2J\x1b[H"; !strings.HasPrefix(got, want) {
				t.Errorf("repaint %q, want it to start with %q", got, want)
			}
		})
	}
}
