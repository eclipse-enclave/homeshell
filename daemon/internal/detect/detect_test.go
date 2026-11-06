package detect

import (
	"strings"
	"testing"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
)

func TestMapState(t *testing.T) {
	tests := []struct {
		name      string
		in        string
		want      domain.SessionState
		confident bool
	}{
		{"working maps to running", "working", domain.SessionRunning, true},
		{"blocked maps to waiting", "blocked", domain.SessionWaiting, true},
		{"idle maps to idle", "idle", domain.SessionIdle, true},
		{"unknown falls back to idle, not confident", "unknown", domain.SessionIdle, false},
		{"empty falls back to idle, not confident", "", domain.SessionIdle, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, confident := mapState(tt.in)
			if got != tt.want || confident != tt.confident {
				t.Errorf("mapState(%q) = (%v, %v), want (%v, %v)", tt.in, got, confident, tt.want, tt.confident)
			}
		})
	}
}

func TestEvaluate(t *testing.T) {
	rs, err := parseRuleSet([]byte(`id = "t"
[[rules]]
id = "working"
state = "working"
priority = 100
region = "osc_title"
regex = ["^spin "]
[[rules]]
id = "blocked"
state = "blocked"
priority = 90
region = "whole_recent"
visible_blocker = true
contains = ["do you want to proceed?"]
[[rules]]
id = "viewer"
state = "unknown"
priority = 80
skip_state_update = true
contains = ["history viewer"]
[[rules]]
id = "first_tie"
state = "blocked"
priority = 70
contains = ["equal priority"]
[[rules]]
id = "second_tie"
state = "working"
priority = 70
contains = ["equal priority"]
[[rules]]
id = "idle"
state = "idle"
priority = 50
region = "whole_recent"
line_regex = ['^\s*❯']
`))
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name           string
		snap           Snapshot
		wantRule       string
		wantState      domain.SessionState
		wantConfident  bool
		visibleBlocker bool
		skipUpdate     bool
	}{
		{"highest priority wins", Snapshot{OSCTitle: "spin x", Screen: "  ❯\n"}, "working", domain.SessionRunning, true, false, false},
		{"blocked surfaces visible blocker", Snapshot{Screen: "Do you want to proceed?\n  ❯\n"}, "blocked", domain.SessionWaiting, true, true, false},
		{"unknown preserves state", Snapshot{Screen: "history viewer\n"}, "viewer", domain.SessionIdle, false, false, true},
		{"equal priority keeps first rule", Snapshot{Screen: "equal priority\n"}, "first_tie", domain.SessionWaiting, true, false, false},
		{"idle when only prompt", Snapshot{Screen: "  ❯\n"}, "idle", domain.SessionIdle, true, false, false},
		{"fallback when nothing matches", Snapshot{Screen: "just output\n"}, "", domain.SessionIdle, false, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := rs.evaluate(tt.snap)
			if res.MatchedRule != tt.wantRule || res.State != tt.wantState ||
				res.Confident != tt.wantConfident || res.VisibleBlocker != tt.visibleBlocker ||
				res.SkipStateUpdate != tt.skipUpdate {
				t.Errorf("evaluate = %+v, want rule=%q state=%v confident=%v visibleBlocker=%v skip=%v",
					res, tt.wantRule, tt.wantState, tt.wantConfident, tt.visibleBlocker, tt.skipUpdate)
			}
		})
	}
}

func TestEvaluateSurfacesTitle(t *testing.T) {
	rs, err := parseRuleSet([]byte(`id = "t"
[[rules]]
id = "idle"
state = "idle"
priority = 50
region = "whole_recent"
line_regex = ['^\s*❯']
`))
	if err != nil {
		t.Fatal(err)
	}
	// A matched screen carries the observed title through verbatim.
	if got := rs.evaluate(Snapshot{OSCTitle: "Fix the parser", Screen: "  ❯\n"}); got.Title != "Fix the parser" {
		t.Errorf("matched result title = %q, want %q", got.Title, "Fix the parser")
	}
	// So does the no-match fallback.
	if got := rs.evaluate(Snapshot{OSCTitle: "Fix the parser", Screen: "output\n"}); got.Title != "Fix the parser" {
		t.Errorf("fallback result title = %q, want %q", got.Title, "Fix the parser")
	}
}

func TestEvaluateReturnsTail(t *testing.T) {
	// A claude screen with several non-empty lines; Tail should be the last
	// TailLines non-empty lines, trailing blank lines dropped.
	screen := "line one\nline two\nline three\n\n\n"
	res := Detect(Snapshot{Agent: "claude", Screen: screen})
	if res.Tail == "" {
		t.Fatal("Tail is empty; want the bottom non-empty lines")
	}
	if !strings.Contains(res.Tail, "line three") || strings.HasSuffix(res.Tail, "\n\n") {
		t.Fatalf("Tail = %q; want it to end at the last non-empty line", res.Tail)
	}
}

func TestContentTailExcludesInputBoxChrome(t *testing.T) {
	// A realistic Claude screen: content, then the input box (borders + empty
	// prompt) and the status bar/footer. The tail must carry the content and drop
	// the box chrome and the volatile status bar (the churning clock/cost/%).
	screen := strings.Join([]string{
		"● Done. Task complete.",
		"",
		"  Commits",
		"  - repo main → 1a2b3c4 — feat: add the thing",
		"",
		"✻ Brewed for 1m 49s",
		"",
		"────────────────────────────────────────",
		"❯",
		"────────────────────────────────────────",
		"  [Opus 4.8] │ $0.42 │ 3m 12s │ ███░░░ 28% │ user@host",
		"  ⏵⏵ accept edits on (shift+tab to cycle) · ← for agents",
	}, "\n")
	res := Detect(Snapshot{Agent: "claude", Screen: screen})
	for _, chrome := range []string{"for agents", "$0.42", "28%", "accept edits", "❯", "────", "✻", "Brewed for"} {
		if strings.Contains(res.Tail, chrome) {
			t.Fatalf("tail should exclude chrome %q; got %q", chrome, res.Tail)
		}
	}
	if !strings.Contains(res.Tail, "Done. Task complete") {
		t.Fatalf("tail should include real content; got %q", res.Tail)
	}
	// The inline teaser is the tail's last line: it must be real content, not
	// the empty prompt, status bar, or the ✻ spinner/summary line.
	lines := strings.Split(res.Tail, "\n")
	if last := lines[len(lines)-1]; !strings.Contains(last, "feat: add the thing") {
		t.Fatalf("inline teaser line = %q; want the last content line", last)
	}
}

func TestContentTailFallsBackWithoutInputBox(t *testing.T) {
	// Gemini has no input-box anchor: the tail falls back to the bottom content
	// lines of the whole screen (blank lines dropped).
	screen := "first\n\nsecond\nthird\n"
	res := Detect(Snapshot{Agent: "gemini", Screen: screen})
	if !strings.Contains(res.Tail, "third") || !strings.Contains(res.Tail, "first") {
		t.Fatalf("fallback tail should carry the recent content; got %q", res.Tail)
	}
}

func TestDetectUnknownAgent(t *testing.T) {
	res := Detect(Snapshot{Agent: "nope", Screen: "output\n", OSCTitle: "Unknown agent"})
	if res.State != domain.SessionUnknown || res.Confident {
		t.Errorf("Detect = %+v, want an unconfident unknown result", res)
	}
	if res.Title != "Unknown agent" || res.Tail != "output" {
		t.Errorf("Detect observations = title %q, tail %q", res.Title, res.Tail)
	}
}
