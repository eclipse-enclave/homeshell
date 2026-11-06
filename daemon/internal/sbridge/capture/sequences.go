package capture

import (
	"fmt"
	"strings"
)

// Replies a terminal would send. The colours describe a dark terminal, which
// is what an agent's "auto" theme then picks.
const (
	replyDA1     = "\x1b[?62;22c" // VT220 with ANSI colour
	replyDSR     = "\x1b[0n"
	replyFGColor = "10;rgb:e5e5/e5e5/e5e5"
	replyBGColor = "11;rgb:0000/0000/0000"
)

// seqKind is a sequence the scanner reports: a terminal query the bridge can
// answer in a terminal's place, a title change, or one the emulator must not
// see.
type seqKind int

const (
	queryDA1     seqKind = iota + 1 // CSI c, CSI 0 c
	queryDSR                        // CSI 5 n
	queryCPR                        // CSI 6 n
	queryFGColor                    // OSC 10 ; ?
	queryBGColor                    // OSC 11 ; ?

	// seqMisread is a CSI vt10x does not implement and would run as the plain
	// sequence with the same final byte: it parses only the DEC private "?"
	// prefix and ignores even that outside modes. The kitty keyboard query and
	// push/pop (CSI ? u, CSI > 7 u, CSI < u) would restore the cursor, and
	// CSI > 4 m (modifyOtherKeys) would reset the text attributes.
	seqMisread

	// vt10x ignores an empty title, which is how agents clear theirs on exit,
	// so the capture tracks whether the last title set was empty.
	seqTitle   // OSC 0 or 2 with a title
	seqNoTitle // OSC 0 or 2 with an empty title

	// The background colour is not part of the emulator's screen, so the
	// capture keeps the last one set to replay it to a terminal that attaches.
	seqSetBG   // OSC 11 with a colour
	seqResetBG // OSC 111

	seqWide // a rune two cells wide, in text
)

// sequence is a recognised sequence spanning bytes [start, end) of the scanned
// chunk. start is -1 when the sequence began in an earlier chunk.
type sequence struct {
	start, end int
	kind       seqKind
	bel        bool   // OSC terminated by BEL rather than ST; the reply mirrors it
	value      string // OSC body of a seqSetBG, e.g. "11;#1a2b3c"
}

// seqScanner finds queries, misread sequences and wide runes in an output stream.
// Sequences split across chunks are handled; everything it does not recognise
// is skipped.
type seqScanner struct {
	state    seqState
	start    int
	buf      []byte
	overflow bool
	inter    bool // CSI has intermediate bytes
	ctl      bool // CSI has embedded C0 controls, which the emulator executes
}

type seqState int

const (
	qText seqState = iota
	qEsc
	qCSI
	qOSC
	qOSCEsc
)

// seqBufCap bounds collected CSI parameters and OSC payloads; queries are
// short, so anything longer is skipped while its terminator is still tracked.
const seqBufCap = 32

func (s *seqScanner) scan(p []byte) []sequence {
	var found []sequence
	for i, b := range p {
		switch s.state {
		case qText:
			switch {
			case b == 0x1b:
				s.state, s.start = qEsc, i
			case b >= 0xc0: // a rune's start byte; Feed passes only whole runes
				if n := wideRuneAt(p[i:]); n > 0 {
					found = append(found, sequence{start: i, end: i + n, kind: seqWide})
				}
			}
		case qEsc:
			s.begin(b, i)
		case qCSI:
			switch {
			case b >= 0x40 && b <= 0x7e:
				if kind := s.csiKind(b); kind != 0 {
					found = append(found, sequence{start: s.start, end: i + 1, kind: kind})
				}
				s.state = qText
			case b == 0x1b:
				s.state, s.start = qEsc, i
			case b == 0x18 || b == 0x1a: // CAN, SUB abort the sequence
				s.state = qText
			case b >= 0x20 && b <= 0x2f:
				s.inter = true
				s.collect(b)
			case b < 0x20:
				s.ctl = true
			default:
				s.collect(b)
			}
		case qOSC:
			switch b {
			case 0x07:
				if kind := s.oscKind(); kind != 0 {
					found = append(found, sequence{start: s.start, end: i + 1, kind: kind, bel: true, value: string(s.buf)})
				}
				s.state = qText
			case 0x1b:
				s.state = qOSCEsc
			default:
				s.collect(b)
			}
		case qOSCEsc:
			if b == '\\' {
				if kind := s.oscKind(); kind != 0 {
					found = append(found, sequence{start: s.start, end: i + 1, kind: kind, value: string(s.buf)})
				}
				s.state = qText
			} else {
				s.start = i - 1
				s.begin(b, i)
			}
		}
	}
	s.start = -1
	return found
}

// begin dispatches the byte after an ESC.
func (s *seqScanner) begin(b byte, i int) {
	s.buf, s.overflow, s.inter, s.ctl = s.buf[:0], false, false, false
	switch b {
	case '[':
		s.state = qCSI
	case ']':
		s.state = qOSC
	case 0x1b:
		s.state, s.start = qEsc, i
	default:
		s.state = qText
	}
}

func (s *seqScanner) collect(b byte) {
	if len(s.buf) < seqBufCap {
		s.buf = append(s.buf, b)
	} else {
		s.overflow = true
	}
}

func (s *seqScanner) csiKind(final byte) seqKind {
	if s.ctl {
		return 0
	}
	if s.inter {
		return seqMisread
	}
	if len(s.buf) > 0 {
		switch s.buf[0] {
		case '<', '=', '>':
			return seqMisread
		case '?':
			// Modes are parsed as private; selective erase erases as plain ED/EL would.
			if strings.IndexByte("hlJK", final) < 0 {
				return seqMisread
			}
			return 0
		}
	}
	if s.overflow {
		return 0
	}
	switch params := string(s.buf); {
	case final == 'c' && (params == "" || params == "0"):
		return queryDA1
	case final == 'n' && params == "5":
		return queryDSR
	case final == 'n' && params == "6":
		return queryCPR
	}
	return 0
}

func (s *seqScanner) oscKind() seqKind {
	payload := string(s.buf)
	if strings.HasPrefix(payload, "0;") || strings.HasPrefix(payload, "2;") {
		if len(payload) == 2 {
			return seqNoTitle
		}
		return seqTitle
	}
	if payload == "111" || strings.HasPrefix(payload, "111;") {
		return seqResetBG
	}
	if s.overflow {
		return 0 // a truncated colour must not be replayed
	}
	switch {
	case payload == "10;?":
		return queryFGColor
	case payload == "11;?":
		return queryBGColor
	case strings.HasPrefix(payload, "11;"):
		return seqSetBG
	}
	return 0
}

// reply renders the answer to a query; cursor is the 0-based cursor position
// at the moment the query was emitted.
func (s sequence) reply(cursorX, cursorY int) string {
	osc := func(body string) string {
		if s.bel {
			return "\x1b]" + body + "\x07"
		}
		return "\x1b]" + body + "\x1b\\"
	}
	switch s.kind {
	case queryDA1:
		return replyDA1
	case queryDSR:
		return replyDSR
	case queryCPR:
		return fmt.Sprintf("\x1b[%d;%dR", cursorY+1, cursorX+1)
	case queryFGColor:
		return osc(replyFGColor)
	case queryBGColor:
		return osc(replyBGColor)
	}
	return ""
}

// maxHeldSeq bounds how much of an unfinished CSI at the end of a chunk is
// held back; longer ones pass through and cannot be filtered.
const maxHeldSeq = 64

// incompleteCSIStart returns where p ends in an unfinished ESC or CSI, or
// len(p). Holding it back for the next chunk keeps every CSI whole within one
// Feed, so a misread one can be cut out before the emulator sees any of it.
func incompleteCSIStart(p []byte) int {
	for i := len(p) - 1; i >= 0 && len(p)-i <= maxHeldSeq; i-- {
		if p[i] != 0x1b {
			continue
		}
		rest := p[i+1:]
		if len(rest) == 0 {
			return i
		}
		if rest[0] != '[' {
			return len(p)
		}
		for _, b := range rest[1:] {
			if b < 0x20 || b > 0x3f {
				return len(p)
			}
		}
		return i
	}
	return len(p)
}
