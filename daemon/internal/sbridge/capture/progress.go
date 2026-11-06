package capture

import (
	"strings"
	"sync"
)

// ProgressScanner extracts OSC 9;4 progress reports ("\x1b]9;4;st;pr\x07",
// ConEmu convention) from a terminal output stream. The VT emulator that
// renders the screen ignores these sequences, so the bridge scans the raw
// byte stream itself. Feed and Progress may be called from different
// goroutines. Sequences split across Feed calls are handled.
type ProgressScanner struct {
	mu       sync.Mutex
	state    scanState
	payload  []byte
	overflow bool
	progress string
	set      bool
}

type scanState int

const (
	scanText   scanState = iota // outside any escape sequence
	scanEsc                     // saw ESC, deciding sequence type
	scanOSC                     // collecting an OSC payload
	scanOSCEsc                  // saw ESC inside OSC, expecting ST terminator
)

// oscPayloadCap bounds the collected payload. Progress payloads are tiny;
// anything longer (e.g. base64 OSC 52 clipboard writes) is discarded while
// still tracking the terminator.
const oscPayloadCap = 64

// Feed scans a chunk of terminal output for progress reports.
func (s *ProgressScanner) Feed(p []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, b := range p {
		switch s.state {
		case scanText:
			if b == 0x1b {
				s.state = scanEsc
			}
		case scanEsc:
			if b == ']' {
				s.state = scanOSC
				s.payload = s.payload[:0]
				s.overflow = false
			} else if b != 0x1b {
				s.state = scanText
			}
		case scanOSC:
			switch b {
			case 0x07: // BEL terminator
				s.finish()
			case 0x1b:
				s.state = scanOSCEsc
			default:
				if len(s.payload) < oscPayloadCap {
					s.payload = append(s.payload, b)
				} else {
					s.overflow = true
				}
			}
		case scanOSCEsc:
			if b == '\\' { // ESC \ = ST terminator
				s.finish()
			} else {
				// ESC inside an OSC string aborts it; re-dispatch the byte
				// as the start of a new escape sequence.
				s.state = scanEsc
				if b == ']' {
					s.state = scanOSC
					s.payload = s.payload[:0]
					s.overflow = false
				} else if b != 0x1b {
					s.state = scanText
				}
			}
		}
	}
}

// finish evaluates a terminated OSC payload and resets to plain scanning.
func (s *ProgressScanner) finish() {
	s.state = scanText
	if s.overflow {
		return
	}
	rest, ok := strings.CutPrefix(string(s.payload), "9;4")
	if !ok {
		return
	}
	rest = strings.TrimPrefix(rest, ";")
	// State 0 (and a bare "9;4") means "remove progress".
	if state, _, _ := strings.Cut(rest, ";"); state == "" || state == "0" {
		s.progress, s.set = "", false
		return
	}
	s.progress, s.set = rest, true
}

// Progress returns the last reported progress as "state[;percent]", or nil
// when none is set.
func (s *ProgressScanner) Progress() *string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.set {
		return nil
	}
	value := s.progress
	return &value
}
