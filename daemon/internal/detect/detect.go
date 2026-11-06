// Derived from herdr src/detect/manifest.rs at v0.9.5 (Apache-2.0); modified.
// Source provenance is recorded in manifests/herdr/manifests.lock.

// Package detect classifies an agent session's state from a rendered snapshot
// of its terminal. It observes; it does not define or aggregate state. The
// normalized state vocabulary and its aggregation live in package domain
// (docs/ARCHITECTURE.md, "State model"); this package maps its observations onto that
// vocabulary and returns a domain.SessionState.
package detect

import "github.com/eclipsesource/homeshell/daemon/internal/domain"

// TailLines is how many bottom content lines of the rendered screen are
// surfaced as Result.Tail for the UI's session preview. It is set to the
// conventional 24-row terminal height so a whole visible screen is available;
// a capture taller than that is truncated to the last 24 lines, so this is an
// upper bound, not a fixed size. The client chooses how many of these to
// display (the previewLines setting, 1..TailLines); bounding the stream here
// keeps the SSE payload small even though the tail changes on nearly every
// capture while an agent works.
const TailLines = 24

// Snapshot is the detection input. It is the "detection inputs" subset of the
// ingest contract: the envelope fields (session id, timestamp) belong to the
// caller and are not part of classification.
type Snapshot struct {
	Agent       string // which agent's rule set to apply, e.g. "claude"
	Screen      string // rendered text of the bottom rows of the terminal
	OSCTitle    string // the terminal window/tab title (may be empty)
	OSCProgress string // the terminal progress string (may be empty)
}

// Result is the outcome of classifying a Snapshot.
type Result struct {
	State           domain.SessionState // mapped normalized state
	Confident       bool                // false when no rule set or no rule matched
	MatchedRule     string              // id of the winning rule ("" when fallback)
	VisibleBlocker  bool                // the winning rule shows live needs-input UI
	VisibleIdle     bool                // the winning rule shows an idle prompt
	SkipStateUpdate bool                // screen is a history viewer; keep prior state
	// Title is the observed terminal title (the input Snapshot's OSCTitle),
	// surfaced verbatim so a caller can use it as a live session title. It is an
	// observation, not a classification; it may be empty.
	Title string
	// Tail is a plain-text snapshot of the bottom rows of the rendered screen
	// (up to TailLines non-empty lines), surfaced for the UI's session preview.
	// It is an observation, not a classification, and may be empty.
	Tail string
}

// mapState maps a rule's detection state string onto the normalized vocabulary.
// The bool reports whether the classification is confident (a real state) vs a
// fallback.
//
// NOTE: this intentionally never returns domain.SessionDone. Screen detection
// cannot distinguish "finished the task" from "idle, waiting for the first
// prompt" — both show an empty prompt box. "done" is a UI-attention concept: it
// requires the UI to report that the user actually saw the finished session
// (cf. herdr's `done = idle && !seen`). That signal does not exist yet, so a
// finished session reads as idle for now. When the UI reports "seen", promote
// idle -> done in the store/attention layer, NOT here.
func mapState(s string) (domain.SessionState, bool) {
	switch s {
	case "working":
		return domain.SessionRunning, true
	case "blocked":
		return domain.SessionWaiting, true
	case "idle":
		return domain.SessionIdle, true
	default: // "unknown" or unset
		return domain.SessionIdle, false
	}
}

// Detect classifies a session snapshot. Screen detection is optional: an agent
// without a rule set produces an unconfident unknown result, while still
// surfacing the generic title and terminal preview observations.
func Detect(s Snapshot) Result {
	registry, _ := enabledRuleSets()
	rs, ok := registry[s.Agent]
	if !ok {
		return Result{
			State: domain.SessionUnknown,
			Title: s.OSCTitle,
			Tail:  bottomContentLines(s.Screen, TailLines, nil),
		}
	}
	return rs.evaluate(s)
}

// evaluate runs every rule against its region and returns the result of the
// highest-priority matching rule. Ties keep the earlier rule (rules are in
// file order). No match yields an idle fallback (Confident=false).
func (rs ruleSet) evaluate(s Snapshot) Result {
	var best *compiledRule
	for i := range rs.rules {
		r := &rs.rules[i]
		text, _ := region(s, r.region)
		if !r.gate.matches(text, toLower(text)) {
			continue
		}
		if best == nil || r.priority > best.priority {
			best = r
		}
	}
	if best == nil {
		return Result{State: domain.SessionIdle, Confident: false, Title: s.OSCTitle, Tail: rs.contentTail(s)}
	}
	state, confident := mapState(best.state)
	return Result{
		State:           state,
		Confident:       confident,
		MatchedRule:     best.id,
		VisibleBlocker:  best.visibleBlocker && state == domain.SessionWaiting,
		VisibleIdle:     best.visibleIdle && state == domain.SessionIdle,
		SkipStateUpdate: best.skipStateUpdate,
		Title:           s.OSCTitle,
		Tail:            rs.contentTail(s),
	}
}

// contentTail extracts a short tail of meaningful content for the UI's session
// preview: recent output/question lines, with the agent's persistent input box
// and status bar removed. For Claude the status bar (elapsed time, cost, context
// %) changes every second, so including it would bury the signal AND make the
// preview churn on every capture (defeating the watcher's "broadcast only on
// real change"); excluding it keeps the preview stable while idle. The chrome
// shapes are agent-specific, so the Claude/Codex handling is keyed per agent;
// agents without a known input box (e.g. gemini) fall back to the raw bottom
// non-empty lines.
func (rs ruleSet) contentTail(s Snapshot) string {
	content := s.Screen
	lines := splitLines(content)
	switch rs.id {
	case "claude":
		// The input box is delimited by horizontal rules; the status bar and
		// footer sit below its bottom border (the last rule). Drop them, then
		// filter Claude's box/prompt/spinner chrome from what remains.
		if idx, ok := lastHorizontalRuleIndex(lines); ok {
			content = content[:lineStartOffset(lines, idx)]
		}
		return bottomContentLines(content, TailLines, isClaudeChrome)
	case "codex":
		// Content sits above the last '›' prompt marker; the footer is below it.
		if idx, ok := lastCodexPromptIndex(lines); ok {
			content = content[:lineStartOffset(lines, idx)]
		}
		return bottomContentLines(content, TailLines, nil)
	default:
		return bottomContentLines(content, TailLines, nil)
	}
}

// ScreenTail is the preview of a screen no rule set applies to, such as the
// one an agent left behind when it exited: its bottom count non-empty lines.
func ScreenTail(screen string, count int) string {
	return bottomContentLines(screen, count, nil)
}
