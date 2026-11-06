package capture

import (
	"unicode/utf8"

	"github.com/hinshun/vt10x"
	"golang.org/x/text/width"
)

// vt10x gives every rune one cell, while agents and terminals give East Asian
// wide runes (CJK, most emoji) two. Feed writes wideTail after each wide rune
// so it fills the second cell, and screenText leaves it out again.
const wideTail = '\U0010FFFF' // a noncharacter, so agents do not print it

var wideTailBytes = []byte(string(wideTail))

// vtWrapNext is vt10x's unexported cursorWrapNext: the cursor sits past the
// last column and the next rune wraps first.
const vtWrapNext = 1 << 1

func isWide(r rune) bool {
	switch width.LookupRune(r).Kind() {
	case width.EastAsianWide, width.EastAsianFullwidth:
		return true
	}
	return false
}

// wideRuneAt returns the length of the wide rune starting p, or 0.
func wideRuneAt(p []byte) int {
	r, n := utf8.DecodeRune(p)
	if r == utf8.RuneError || !isWide(r) {
		return 0
	}
	return n
}

// writeWide writes a wide rune and its tail cell. A wide rune does not fit in
// the last column, so with autowrap it moves to the next line, as in a real
// terminal; the blank keeps the column empty and leaves the wrap to vt10x.
// The caller holds c.mu.
func (c *Capture) writeWide(r []byte) {
	c.vt.Lock()
	cols, _ := c.vt.Size()
	cur := c.vt.Cursor()
	wrap := c.vt.Mode()&vt10x.ModeWrap != 0
	c.vt.Unlock()
	if wrap && cols > 1 && cur.X == cols-1 && cur.State&vtWrapNext == 0 {
		_, _ = c.vt.Write([]byte(" "))
	}
	_, _ = c.vt.Write(r)
	_, _ = c.vt.Write(wideTailBytes)
}

// cellText is what cell x of row y contributes to the screen text: a wide
// rune stands for both its cells, and a half that was overwritten is blank.
// The caller holds the emulator lock.
func cellText(vt vt10x.Terminal, x, y, cols int) (r rune, ok bool) {
	g := vt.Cell(x, y)
	switch {
	case g.Char == wideTail:
		if x > 0 && isWide(vt.Cell(x-1, y).Char) {
			return 0, false
		}
		return ' ', true
	case g.Char == 0:
		return ' ', true
	case isWide(g.Char) && (x+1 >= cols || vt.Cell(x+1, y).Char != wideTail):
		return ' ', true
	}
	return g.Char, true
}
