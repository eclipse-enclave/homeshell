// Inspired by Herdr v0.9.5 src/pane/agent_detection.rs (Apache-2.0).
// Source provenance: manifests/herdr/manifests.lock. Adapted to polling.
package detect

import (
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
)

// IdleConfirmAfter adapts Herdr's three 100 ms rechecks to polling: idle must
// still show this long after it was first seen. A visible idle prompt does not
// need this confirmation.
const IdleConfirmAfter = 300 * time.Millisecond

// IdleHoldMaxInterval is the slowest poll interval the hold applies at. Each
// confirmation costs one extra poll, so slower intervals publish idle at once.
const IdleHoldMaxInterval = time.Second

// holdIdle returns the published state and pending idle observation time.
// It is pure: providers own the clock and per-session temporal state.
// interval is the poll period, 0 when unknown.
func holdIdle(previous domain.SessionState, result Result, pending, now time.Time, interval time.Duration) (domain.SessionState, time.Time) {
	if result.SkipStateUpdate {
		return previous, time.Time{}
	}
	if previous != domain.SessionRunning || result.State != domain.SessionIdle || result.VisibleIdle || result.VisibleBlocker || interval > IdleHoldMaxInterval {
		return result.State, time.Time{}
	}
	if pending.IsZero() {
		return previous, now
	}
	if now.Sub(pending) < IdleConfirmAfter {
		return previous, pending
	}
	return result.State, time.Time{}
}

// Hold is one session's publication state across polls, shared by providers
// so a screen means the same thing whichever runtime it came from.
type Hold struct {
	State     domain.SessionState
	RuleSet   string
	IdleSince time.Time
}

// Observe publishes result, classified with ruleSet at a poll every interval.
// A changed rule set starts over, since the old state was judged by different rules.
func (h *Hold) Observe(ruleSet string, result Result, now time.Time, interval time.Duration) domain.SessionState {
	if h.State == "" || (h.RuleSet != "" && h.RuleSet != ruleSet) {
		h.State, h.IdleSince = domain.SessionUnknown, time.Time{}
	}
	h.RuleSet = ruleSet
	h.State, h.IdleSince = holdIdle(h.State, result, h.IdleSince, now, interval)
	return h.State
}

// Detail is the detection evidence published as a session's ObservationData.
type Detail struct {
	MatchedRule    string `json:"matchedRule,omitempty"`
	Confident      bool   `json:"confident"`
	VisibleBlocker bool   `json:"visibleBlocker,omitempty"`
}

// Detail returns the evidence behind the result's state.
func (r Result) Detail() Detail {
	return Detail{MatchedRule: r.MatchedRule, Confident: r.Confident, VisibleBlocker: r.VisibleBlocker}
}
