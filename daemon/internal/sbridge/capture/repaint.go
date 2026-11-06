package capture

import (
	"fmt"
	"strings"
)

// RepaintBytes synthesizes a plain-text repaint of the current screen for a
// freshly attached (or overflow-recovered) client: clear, home, each row's
// text, then the cursor restored to the emulator's position. Colors and
// attributes are not reproduced (v1), except the background colour the agent
// set for the whole terminal; a TUI agent repaints itself in full
// fidelity on the SIGWINCH that follows attach anyway. A broken capture
// degrades to a bare clear.
func (c *Capture) RepaintBytes() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	clear := []byte("\x1b[2J\x1b[H")
	// Set before the clear, so the cleared screen already has the agent's
	// background (Enclave's session tint is emitted once, before any attach).
	if c.background != "" {
		clear = append([]byte("\x1b]"+c.background+"\x07"), clear...)
	}
	if c.broken != "" {
		return clear
	}
	defer c.recoverBroken()
	c.vt.Lock()
	defer c.vt.Unlock()

	var sb strings.Builder
	sb.Write(clear)
	rows := strings.Split(strings.TrimRight(screenText(c.vt), "\n"), "\n")
	// Trailing blank rows are dropped so a shorter client terminal does not
	// scroll the top of the screen away.
	for len(rows) > 0 && rows[len(rows)-1] == "" {
		rows = rows[:len(rows)-1]
	}
	for i, row := range rows {
		if i > 0 {
			sb.WriteString("\r\n")
		}
		sb.WriteString(row)
	}
	cur := c.vt.Cursor()
	fmt.Fprintf(&sb, "\x1b[%d;%dH", cur.Y+1, cur.X+1) // CUP is 1-based
	return []byte(sb.String())
}
