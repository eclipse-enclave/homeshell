package detect

import (
	"testing"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
)

func TestHoldIdle(t *testing.T) {
	start := time.Unix(100, 0)
	for _, tt := range []struct {
		name     string
		previous domain.SessionState
		result   Result
		pending  time.Time
		now      time.Time
		interval time.Duration
		want     domain.SessionState
		waiting  bool
	}{
		{"first plain idle", domain.SessionRunning, Result{State: domain.SessionIdle}, time.Time{}, start, 0, domain.SessionRunning, true},
		{"short pause", domain.SessionRunning, Result{State: domain.SessionIdle}, start, start.Add(299 * time.Millisecond), 0, domain.SessionRunning, true},
		{"confirmed", domain.SessionRunning, Result{State: domain.SessionIdle}, start, start.Add(300 * time.Millisecond), 0, domain.SessionIdle, false},
		{"visible idle", domain.SessionRunning, Result{State: domain.SessionIdle, VisibleIdle: true}, start, start, 0, domain.SessionIdle, false},
		{"waiting is immediate", domain.SessionRunning, Result{State: domain.SessionWaiting, VisibleBlocker: true}, start, start, 0, domain.SessionWaiting, false},
		{"working resets", domain.SessionRunning, Result{State: domain.SessionRunning}, start, start, 0, domain.SessionRunning, false},
		{"unknown resets", domain.SessionRunning, Result{State: domain.SessionUnknown}, start, start, 0, domain.SessionUnknown, false},
		{"stopped bypasses", domain.SessionRunning, Result{State: domain.SessionStopped}, start, start, 0, domain.SessionStopped, false},
		{"skip preserves and resets", domain.SessionRunning, Result{State: domain.SessionIdle, SkipStateUpdate: true}, start, start, 0, domain.SessionRunning, false},
		{"idle remains idle", domain.SessionIdle, Result{State: domain.SessionIdle}, time.Time{}, start, 0, domain.SessionIdle, false},
		{"held at the slowest fast interval", domain.SessionRunning, Result{State: domain.SessionIdle}, time.Time{}, start, IdleHoldMaxInterval, domain.SessionRunning, true},
		{"slow interval publishes at once", domain.SessionRunning, Result{State: domain.SessionIdle}, time.Time{}, start, 5 * time.Second, domain.SessionIdle, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			state, pending := holdIdle(tt.previous, tt.result, tt.pending, tt.now, tt.interval)
			if state != tt.want || !pending.IsZero() != tt.waiting {
				t.Fatalf("state=%s pending=%v", state, pending)
			}
		})
	}
}

func TestVisibleIdleComesFromWinningRule(t *testing.T) {
	for _, state := range []string{"idle", "working", "blocked"} {
		rules, err := parseRuleSet([]byte("id = 'synthetic'\n[[rules]]\nid = 'prompt'\nstate = '" + state + "'\nvisible_idle = true\ncontains = ['ready']\n"))
		if err != nil {
			t.Fatal(err)
		}
		result := rules.evaluate(Snapshot{Screen: "ready"})
		if result.VisibleIdle != (state == "idle") {
			t.Fatalf("state=%s result=%+v", state, result)
		}
	}
}

func TestHoldObserve(t *testing.T) {
	start := time.Unix(100, 0)
	working, idle := Result{State: domain.SessionRunning}, Result{State: domain.SessionIdle}
	var h Hold
	if got := h.Observe("codex", Result{SkipStateUpdate: true}, start, 0); got != domain.SessionUnknown {
		t.Fatalf("first skipped screen=%s; want unknown", got)
	}
	h.Observe("codex", working, start, 0)
	if got := h.Observe("codex", idle, start, 0); got != domain.SessionRunning {
		t.Fatalf("held idle=%s; want running", got)
	}
	if got := h.Observe("codex", idle, start.Add(IdleConfirmAfter), 0); got != domain.SessionIdle {
		t.Fatalf("confirmed idle=%s; want idle", got)
	}
	h.Observe("codex", working, start, 0)
	if got := h.Observe("claude", idle, start, 0); got != domain.SessionIdle {
		t.Fatalf("idle under a new rule set=%s; want idle without a hold", got)
	}
	restored := Hold{State: domain.SessionRunning}
	if got := restored.Observe("codex", idle, start, 0); got != domain.SessionRunning {
		t.Fatalf("restored hold without a rule set=%s; want it kept", got)
	}
}
