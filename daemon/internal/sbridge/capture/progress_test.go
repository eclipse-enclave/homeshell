package capture

import "testing"

func feedString(s *ProgressScanner, in string) {
	s.Feed([]byte(in))
}

func TestProgressScannerSetAndClear(t *testing.T) {
	var s ProgressScanner
	feedString(&s, "building\x1b]9;4;1;42\x07more output")
	if got := s.Progress(); got == nil || *got != "1;42" {
		t.Fatalf("expected progress 1;42, got %v", got)
	}
	feedString(&s, "\x1b]9;4;0;0\x07")
	if got := s.Progress(); got != nil {
		t.Fatalf("expected progress cleared, got %q", *got)
	}
}

func TestProgressScannerSTTerminator(t *testing.T) {
	var s ProgressScanner
	feedString(&s, "\x1b]9;4;3\x1b\\")
	if got := s.Progress(); got == nil || *got != "3" {
		t.Fatalf("expected indeterminate progress 3, got %v", got)
	}
}

func TestProgressScannerSplitAcrossFeeds(t *testing.T) {
	var s ProgressScanner
	for _, chunk := range []string{"\x1b", "]9;", "4;1;", "97", "\x07"} {
		feedString(&s, chunk)
	}
	if got := s.Progress(); got == nil || *got != "1;97" {
		t.Fatalf("expected progress 1;97, got %v", got)
	}
}

func TestProgressScannerIgnoresOtherSequences(t *testing.T) {
	var s ProgressScanner
	feedString(&s, "\x1b]0;a title\x07\x1b[31mred\x1b[0m\x1b]8;;https://x\x07link\x1b]8;;\x07")
	if got := s.Progress(); got != nil {
		t.Fatalf("expected no progress from unrelated sequences, got %q", *got)
	}
}

func TestProgressScannerOverflowDiscarded(t *testing.T) {
	var s ProgressScanner
	long := make([]byte, 8192)
	for i := range long {
		long[i] = 'A'
	}
	feedString(&s, "\x1b]52;c;"+string(long)+"\x07after\x1b]9;4;1;5\x07")
	if got := s.Progress(); got == nil || *got != "1;5" {
		t.Fatalf("expected scanner to recover after oversized OSC, got %v", got)
	}
}

func TestProgressScannerAbortedOSCThenProgress(t *testing.T) {
	var s ProgressScanner
	// An ESC inside an OSC aborts it; the following bytes start a fresh
	// progress sequence that must still be recognized.
	feedString(&s, "\x1b]0;partial title\x1b]9;4;2;10\x07")
	if got := s.Progress(); got == nil || *got != "2;10" {
		t.Fatalf("expected progress from restarted OSC, got %v", got)
	}
}
