package capture

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestFeedChunkedUTF8 verifies that multi-byte runes split across Feed calls
// render identically to a single write. vt10x drops the leading bytes of an
// incomplete trailing sequence, so Feed must hold the tail back (PTY reads
// split at arbitrary byte offsets).
func TestFeedChunkedUTF8(t *testing.T) {
	input := []byte("\x1b]0;⠹ Refactoring the parser\a● 日本語テスト 😀 café ─────\r\n❯ ready\r\n")

	want := New(80, 24)
	want.Feed(input)
	ref := want.Snapshot()
	if ref.Error != "" {
		t.Fatalf("reference snapshot error: %s", ref.Error)
	}
	if !strings.Contains(ref.Screen, "日本語テスト") {
		t.Fatalf("reference screen missing multibyte text:\n%s", ref.Screen)
	}

	for chunk := 1; chunk <= utf8.UTFMax+3; chunk++ {
		c := New(80, 24)
		for off := 0; off < len(input); off += chunk {
			end := off + chunk
			if end > len(input) {
				end = len(input)
			}
			c.Feed(input[off:end])
		}
		got := c.Snapshot()
		if got.Screen != ref.Screen || got.OSCTitle != ref.OSCTitle {
			t.Errorf("chunk=%d diverges from single write\ntitle: %q want %q\nscreen:\n%s\nwant:\n%s",
				chunk, got.OSCTitle, ref.OSCTitle, got.Screen, ref.Screen)
		}
	}
}

// TestFeedDoesNotHoldInvalidBytes ensures garbage bytes pass straight through
// instead of accumulating in the tail buffer.
func TestFeedDoesNotHoldInvalidBytes(t *testing.T) {
	c := New(80, 24)
	c.Feed([]byte{0xff, 0xfe})
	if len(c.tail) != 0 {
		t.Fatalf("invalid bytes held in tail: %q", c.tail)
	}
	c.Feed([]byte("ok\r\n"))
	if got := c.Snapshot(); !strings.Contains(got.Screen, "ok") {
		t.Fatalf("screen missing content after invalid bytes:\n%s", got.Screen)
	}
}

func TestCompletePrefixLen(t *testing.T) {
	emoji := "😀" // 4 bytes
	tests := []struct {
		name string
		in   []byte
		want int
	}{
		{"empty", nil, 0},
		{"ascii", []byte("abc"), 3},
		{"complete rune", []byte("a✳"), 4},
		{"split 3-byte after 1", []byte("a" + "✳"[:1]), 1},
		{"split 3-byte after 2", []byte("a" + "✳"[:2]), 1},
		{"split 4-byte after 3", []byte(emoji[:3]), 0},
		{"lone continuation bytes", []byte{0x80, 0x80}, 2},
		{"invalid start byte", []byte{0xff}, 1},
	}
	for _, tt := range tests {
		if got := completePrefixLen(tt.in); got != tt.want {
			t.Errorf("%s: completePrefixLen(%q) = %d, want %d", tt.name, tt.in, got, tt.want)
		}
	}
}

// TestRepaintBytes checks the synthesized repaint carries the screen content
// and a cursor-position sequence, and starts with a clear.
func TestRepaintBytes(t *testing.T) {
	c := New(40, 10)
	c.Feed([]byte("hello\r\nworld\r\n"))
	got := string(c.RepaintBytes())
	if !strings.HasPrefix(got, "\x1b[2J\x1b[H") {
		t.Fatalf("repaint missing clear prefix: %q", got)
	}
	if !strings.Contains(got, "hello\r\nworld") {
		t.Fatalf("repaint missing screen rows: %q", got)
	}
	if !strings.Contains(got, "\x1b[3;1H") {
		t.Fatalf("repaint missing cursor restore (want row 3 col 1): %q", got)
	}
}
