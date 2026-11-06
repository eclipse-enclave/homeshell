// Package capture maintains the bridge's snapshot state: a VT emulator
// rendering the agent's PTY output plus an OSC 9;4 progress scanner. It is
// the only package in this module that depends on the terminal emulator.
package capture

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/eclipsesource/homeshell/daemon/internal/sbridge"

	"github.com/hinshun/vt10x"
)

// Capture serializes all emulator access through mu; a panic inside the
// emulator marks the capture broken instead of crashing the session — the
// emulator is never touched again (its internal lock may be poisoned), and
// snapshots degrade to an error payload.
type Capture struct {
	mu       sync.Mutex
	vt       vt10x.Terminal
	progress ProgressScanner
	broken   string
	tail     []byte // incomplete UTF-8 or unfinished CSI held for the next feed

	// answer makes Feed return replies to the terminal queries it sees.
	answer bool
	seqs   seqScanner

	noTitle    bool   // the last title set was empty
	background string // OSC body of the last background colour set, "" when reset
}

// New returns a Capture rendering at the given initial size.
func New(width, height int) *Capture {
	return &Capture{vt: vt10x.New(vt10x.WithSize(width, height))}
}

// AnswerQueries makes Feed return what a terminal would reply to the queries
// in the output (device attributes and status, cursor position, colours).
func (c *Capture) AnswerQueries() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.answer = true
}

// Feed passes chunk to the progress scanner and the emulator, and returns the
// replies to any terminal queries in it when AnswerQueries is on. Sequences
// vt10x would misread are cut out, and wide runes get their second cell. PTY reads can split a multi-byte rune
// across chunks; vt10x drops the leading bytes of an incomplete trailing
// sequence instead of reporting them back, so the incomplete tail, like an
// unfinished CSI, is held here and prepended to the next chunk.
func (c *Capture) Feed(chunk []byte) (replies []byte) {
	c.progress.Feed(chunk)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.broken != "" {
		return nil
	}
	defer c.recoverBroken()
	data := chunk
	if len(c.tail) > 0 {
		data = append(c.tail, chunk...)
		c.tail = nil
	}
	n := incompleteCSIStart(data[:completePrefixLen(data)])
	if n < len(data) {
		// data[n:] may alias the caller's read buffer; copy it.
		c.tail = append([]byte(nil), data[n:]...)
	}
	if n == 0 {
		return nil
	}
	// Write up to each query, so a cursor position report sees the cursor
	// where the query was emitted, and up to each wide rune. Both end a
	// sequence or are text, so every split leaves the emulator's parser at rest.
	pos := 0
	for _, s := range c.seqs.scan(data[:n]) {
		switch {
		case s.kind == seqMisread:
			if s.start < pos {
				continue // began in an earlier chunk, already written
			}
			_, _ = c.vt.Write(data[pos:s.start])
			pos = s.end
		case s.kind == seqTitle, s.kind == seqNoTitle:
			c.noTitle = s.kind == seqNoTitle
		case s.kind == seqSetBG:
			c.background = s.value
		case s.kind == seqResetBG:
			c.background = ""
		case s.kind == seqWide:
			_, _ = c.vt.Write(data[pos:s.start])
			c.writeWide(data[s.start:s.end])
			pos = s.end
		case c.answer:
			_, _ = c.vt.Write(data[pos:s.end])
			pos = s.end
			c.vt.Lock()
			cur := c.vt.Cursor()
			c.vt.Unlock()
			replies = append(replies, s.reply(cur.X, cur.Y)...)
		}
	}
	_, _ = c.vt.Write(data[pos:n])
	return replies
}

// completePrefixLen returns the length of the longest prefix of p that does
// not end in an incomplete (but so far valid) UTF-8 sequence. Invalid bytes
// are never held back — only a truncated encoding that the next chunk could
// complete.
func completePrefixLen(p []byte) int {
	for i := len(p) - 1; i >= 0 && len(p)-i < utf8.UTFMax; i-- {
		b := p[i]
		if b < utf8.RuneSelf {
			break // ASCII: sequence before it is complete
		}
		if utf8.RuneStart(b) {
			if !utf8.FullRune(p[i:]) {
				return i
			}
			break
		}
		// continuation byte: keep scanning backwards for the start byte
	}
	return len(p)
}

// Resize adjusts the emulator to a new PTY size.
func (c *Capture) Resize(width, height int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.broken != "" {
		return
	}
	defer c.recoverBroken()
	c.vt.Resize(width, height)
}

// recoverBroken converts an emulator panic into a persistent capture error.
func (c *Capture) recoverBroken() {
	if r := recover(); r != nil {
		c.broken = fmt.Sprintf("terminal emulation failed: %v", r)
	}
}

// SnapshotJSON marshals the current snapshot, newline-terminated — the dump
// payload served per socket connection.
func (c *Capture) SnapshotJSON() []byte {
	snap := c.Snapshot()
	data, err := json.Marshal(snap)
	if err != nil {
		data, _ = json.Marshal(sbridge.Snapshot{Error: err.Error()})
	}
	return append(data, '\n')
}

// Snapshot returns the rendered screen, OSC title, and OSC 9;4 progress.
func (c *Capture) Snapshot() (snap sbridge.Snapshot) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.broken != "" {
		return sbridge.Snapshot{Error: c.broken}
	}
	defer func() {
		c.recoverBroken()
		if c.broken != "" {
			snap = sbridge.Snapshot{Error: c.broken}
		}
	}()
	c.vt.Lock()
	defer c.vt.Unlock()
	title := c.vt.Title()
	if c.noTitle {
		title = ""
	}
	return sbridge.Snapshot{
		Screen:      screenText(c.vt),
		OSCTitle:    title,
		OSCProgress: c.progress.Progress(),
	}
}

// screenText renders the emulator's visible screen as plain text, one line
// per row, trailing whitespace stripped — the same shape `tmux
// capture-pane -p` produced. The caller holds the emulator lock.
func screenText(vt vt10x.Terminal) string {
	cols, rows := vt.Size()
	var sb strings.Builder
	for y := 0; y < rows; y++ {
		var line strings.Builder
		for x := 0; x < cols; x++ {
			if r, ok := cellText(vt, x, y, cols); ok {
				line.WriteRune(r)
			}
		}
		sb.WriteString(strings.TrimRight(line.String(), " "))
		sb.WriteByte('\n')
	}
	return sb.String()
}
