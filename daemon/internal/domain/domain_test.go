package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSessionPreviewMarshalsOmitEmpty(t *testing.T) {
	withPreview, _ := json.Marshal(Session{ID: "s1", Preview: "hello"})
	if !strings.Contains(string(withPreview), `"preview":"hello"`) {
		t.Fatalf("preview not marshaled: %s", withPreview)
	}
	without, _ := json.Marshal(Session{ID: "s1"})
	if strings.Contains(string(without), "preview") {
		t.Fatalf("empty preview should be omitted: %s", without)
	}
}

func TestAggregate(t *testing.T) {
	tests := []struct {
		name   string
		states []SessionState
		want   SessionState
	}{
		{"no children means idle", nil, SessionIdle},
		{"a single running child", []SessionState{SessionRunning}, SessionRunning},
		{"waiting outranks running", []SessionState{SessionRunning, SessionWaiting}, SessionWaiting},
		// A create that failed is the one thing here that will not resolve itself,
		// so it leads — as it does in the runtime precedence.
		{"error outranks waiting", []SessionState{SessionWaiting, SessionError}, SessionError},
		{"error outranks everything", []SessionState{SessionDone, SessionIdle, SessionRunning, SessionWaiting, SessionUnknown, SessionStopped, SessionError}, SessionError},
		// Error is terminal, but it must not fall through to "done" with the rest.
		{"an all-error parent is error, not done", []SessionState{SessionError, SessionError}, SessionError},
		{"error alongside done", []SessionState{SessionDone, SessionError}, SessionError},
		{"waiting outranks everything", []SessionState{SessionDone, SessionIdle, SessionRunning, SessionWaiting, SessionUnknown, SessionStopped}, SessionWaiting},
		{"unknown outranks running", []SessionState{SessionRunning, SessionUnknown, SessionIdle}, SessionUnknown},
		{"unknown outranks idle", []SessionState{SessionIdle, SessionUnknown}, SessionUnknown},
		{"unknown outranks stopped", []SessionState{SessionStopped, SessionUnknown}, SessionUnknown},
		{"running outranks idle", []SessionState{SessionIdle, SessionRunning, SessionDone}, SessionRunning},
		{"running outranks starting", []SessionState{SessionStarting, SessionRunning}, SessionRunning},
		{"starting outranks idle", []SessionState{SessionIdle, SessionStarting}, SessionStarting},
		{"starting outranks stopped and done", []SessionState{SessionDone, SessionStopped, SessionStarting}, SessionStarting},
		{"a lone starting child", []SessionState{SessionStarting}, SessionStarting},
		{"idle outranks stopped", []SessionState{SessionStopped, SessionIdle}, SessionIdle},
		{"idle outranks done", []SessionState{SessionDone, SessionIdle}, SessionIdle},
		{"stopped outranks done", []SessionState{SessionDone, SessionStopped}, SessionStopped},
		{"all stopped means stopped", []SessionState{SessionStopped, SessionStopped}, SessionStopped},
		{"done only when every child is done", []SessionState{SessionDone, SessionDone}, SessionDone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Aggregate(tt.states); got != tt.want {
				t.Errorf("Aggregate(%v) = %q, want %q", tt.states, got, tt.want)
			}
		})
	}
}

func TestAggregateRuntime(t *testing.T) {
	tests := []struct {
		name   string
		states []RuntimeState
		want   RuntimeState
	}{
		{"no children means idle", nil, RuntimeIdle},
		{"single running", []RuntimeState{RuntimeRunning}, RuntimeRunning},
		{"error outranks everything", []RuntimeState{RuntimeRunning, RuntimeCreating, RuntimeError, RuntimeStopped}, RuntimeError},
		{"creating outranks starting", []RuntimeState{RuntimeStarting, RuntimeCreating}, RuntimeCreating},
		{"starting outranks stopping", []RuntimeState{RuntimeStopping, RuntimeStarting}, RuntimeStarting},
		{"stopping outranks running", []RuntimeState{RuntimeRunning, RuntimeStopping}, RuntimeStopping},
		{"running outranks stopped", []RuntimeState{RuntimeStopped, RuntimeRunning}, RuntimeRunning},
		{"stopped outranks unknown", []RuntimeState{RuntimeUnknown, RuntimeStopped}, RuntimeStopped},
		{"unknown outranks idle", []RuntimeState{RuntimeIdle, RuntimeUnknown}, RuntimeUnknown},
		{"all idle", []RuntimeState{RuntimeIdle, RuntimeIdle}, RuntimeIdle},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := AggregateRuntime(tt.states); got != tt.want {
				t.Errorf("AggregateRuntime(%v) = %q, want %q", tt.states, got, tt.want)
			}
		})
	}
}

func TestEffectiveState(t *testing.T) {
	tests := []struct {
		name    string
		runtime RuntimeState
		session SessionState
		want    string
	}{
		// Runtime problems always surface.
		{"error beats running sessions", RuntimeError, SessionRunning, "error"},
		{"error beats waiting sessions", RuntimeError, SessionWaiting, "error"},
		{"creating beats idle sessions", RuntimeCreating, SessionIdle, "creating"},
		{"starting beats running sessions", RuntimeStarting, SessionRunning, "starting"},
		{"stopping beats waiting sessions", RuntimeStopping, SessionWaiting, "stopping"},
		{"stopped beats running sessions", RuntimeStopped, SessionRunning, "stopped"},
		// Healthy/uninteresting runtimes defer to session state.
		{"running runtime defers to session", RuntimeRunning, SessionWaiting, "waiting"},
		{"idle runtime defers to session", RuntimeIdle, SessionRunning, "running"},
		{"unknown runtime defers to session", RuntimeUnknown, SessionDone, "done"},
		{"running runtime + idle sessions", RuntimeRunning, SessionIdle, "idle"},
		{"zero-value runtime defers to session", "", SessionIdle, "idle"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := EffectiveState(tt.runtime, tt.session); got != tt.want {
				t.Errorf("EffectiveState(%q, %q) = %q, want %q", tt.runtime, tt.session, got, tt.want)
			}
		})
	}
}

func TestRecompute(t *testing.T) {
	project := Project{
		Workstreams: []Workstream{
			{Sessions: []Session{{State: SessionRunning}, {State: SessionWaiting}}},
			{Sessions: []Session{{State: SessionDone}}},
			{Sessions: []Session{}},
		},
	}
	project.Recompute()

	for i, want := range []SessionState{SessionWaiting, SessionDone, SessionIdle} {
		if got := project.Workstreams[i].State; got != want {
			t.Errorf("workstream[%d].State = %q, want %q", i, got, want)
		}
	}
	if project.State != SessionWaiting {
		t.Errorf("project.State = %q, want %q", project.State, SessionWaiting)
	}

	empty := Project{Workstreams: []Workstream{}}
	empty.Recompute()
	if empty.State != SessionIdle {
		t.Errorf("empty project state = %q, want %q", empty.State, SessionIdle)
	}
}

// A session has ended when it is done or stopped; a failed start has not, and
// one that runs again is live once more.
func TestRecomputeMarksEndedSessions(t *testing.T) {
	tests := []struct {
		state SessionState
		want  bool
	}{
		{SessionDone, true},
		{SessionStopped, true},
		{SessionError, false},
		{SessionRunning, false},
		{SessionWaiting, false},
		{SessionIdle, false},
		{SessionStarting, false},
		{SessionUnknown, false},
	}
	for _, tt := range tests {
		t.Run(string(tt.state), func(t *testing.T) {
			// Ended starts out wrong both ways, so Recompute is what sets it.
			project := Project{Workstreams: []Workstream{{Sessions: []Session{{State: tt.state, Ended: !tt.want}}}}}
			project.Recompute()
			if got := project.Workstreams[0].Sessions[0].Ended; got != tt.want {
				t.Errorf("Ended = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRecomputeWithRuntimeStates(t *testing.T) {
	project := Project{
		Workstreams: []Workstream{
			{
				RuntimeState: RuntimeStopped,
				Sessions:     []Session{{State: SessionRunning}},
			},
			{
				RuntimeState: RuntimeRunning,
				Sessions:     []Session{{State: SessionWaiting}},
			},
		},
	}
	project.Recompute()

	// Stopped runtime overrides session state.
	if got := project.Workstreams[0].Effective; got != "stopped" {
		t.Errorf("workstream[0].Effective = %q, want %q", got, "stopped")
	}
	// Running runtime defers to session state.
	if got := project.Workstreams[1].Effective; got != "waiting" {
		t.Errorf("workstream[1].Effective = %q, want %q", got, "waiting")
	}
	// Project runtime: running > stopped in precedence.
	if got := project.RuntimeState; got != RuntimeRunning {
		t.Errorf("project.RuntimeState = %q, want %q", got, RuntimeRunning)
	}
	// Project effective: running runtime defers to session aggregate (waiting).
	if got := project.Effective; got != "waiting" {
		t.Errorf("project.Effective = %q, want %q", got, "waiting")
	}
}

func TestRecomputeWithAllStoppedRuntime(t *testing.T) {
	project := Project{
		Workstreams: []Workstream{
			{
				RuntimeState: RuntimeStopped,
				Sessions:     []Session{{State: SessionDone}},
			},
		},
	}
	project.Recompute()

	if got := project.RuntimeState; got != RuntimeStopped {
		t.Errorf("project.RuntimeState = %q, want %q", got, RuntimeStopped)
	}
	if got := project.Effective; got != "stopped" {
		t.Errorf("project.Effective = %q, want %q", got, "stopped")
	}
}

func TestRecomputeEffectiveWithHealthyRuntime(t *testing.T) {
	project := Project{
		Workstreams: []Workstream{
			{
				RuntimeState: RuntimeRunning,
				Sessions:     []Session{{State: SessionRunning}},
			},
		},
	}
	project.Recompute()

	if got := project.Workstreams[0].Effective; got != "running" {
		t.Errorf("workstream[0].Effective = %q, want %q", got, "running")
	}
	if got := project.Effective; got != "running" {
		t.Errorf("project.Effective = %q, want %q", got, "running")
	}
}

func TestRecomputeEffectiveDefaultsToSessionState(t *testing.T) {
	// Zero-value RuntimeState (empty string) should defer to session state.
	project := Project{
		Workstreams: []Workstream{
			{Sessions: []Session{{State: SessionWaiting}}},
		},
	}
	project.Recompute()

	if got := project.Workstreams[0].Effective; got != "waiting" {
		t.Errorf("workstream[0].Effective = %q, want %q", got, "waiting")
	}
	if got := project.Effective; got != "waiting" {
		t.Errorf("project.Effective = %q, want %q", got, "waiting")
	}
}

func TestRecomputeInProjectFolder(t *testing.T) {
	tests := []struct {
		name       string
		strategyID string
		workingDir string
		want       bool
	}{
		{"the project folder", "", "/work/app", true},
		{"the project folder, spelt differently", "", "/work/app/", true},
		{"a separate folder", "worktree", "/work/app-feature", false},
		{"an in-place create still being made", "in-place", "", true},
		{"another create still being made", "worktree", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			project := Project{Path: "/work/app", Workstreams: []Workstream{{StrategyID: tt.strategyID, WorkingDir: tt.workingDir}}}
			project.Recompute()
			if got := project.Workstreams[0].InProjectFolder; got != tt.want {
				t.Errorf("InProjectFolder = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSessionStateValid(t *testing.T) {
	for _, s := range []SessionState{SessionIdle, SessionRunning, SessionWaiting, SessionStarting, SessionUnknown, SessionStopped, SessionDone, SessionError} {
		if !s.Valid() {
			t.Errorf("%q should be valid", s)
		}
	}
	for _, s := range []SessionState{"", "bogus", "Running"} {
		if s.Valid() {
			t.Errorf("%q should be invalid", s)
		}
	}
}

func TestSessionStateBusy(t *testing.T) {
	// Starting is busy: a build in flight is work a workstream removal would
	// interrupt, and the container it is about to produce would outlive it.
	busy := []SessionState{SessionRunning, SessionWaiting, SessionStarting}
	for _, s := range busy {
		if !s.Busy() {
			t.Errorf("%q should be busy", s)
		}
	}
	// Error is not busy: a create that failed left nothing running to interrupt,
	// so the row it leaves behind must not make its workstream unremovable.
	notBusy := []SessionState{SessionIdle, SessionUnknown, SessionStopped, SessionDone, SessionError}
	for _, s := range notBusy {
		if s.Busy() {
			t.Errorf("%q should not be busy", s)
		}
	}
}

func TestSessionStateTerminal(t *testing.T) {
	// Error is terminal: the session never started, so it holds no slot.
	terminal := []SessionState{SessionDone, SessionStopped, SessionError}
	for _, s := range terminal {
		if !s.Terminal() {
			t.Errorf("%q should be terminal", s)
		}
	}
	// Starting is non-terminal, so it keeps occupying a concurrency slot while
	// the provider works — the slot must not free up mid-build.
	notTerminal := []SessionState{SessionIdle, SessionRunning, SessionWaiting, SessionStarting, SessionUnknown}
	for _, s := range notTerminal {
		if s.Terminal() {
			t.Errorf("%q should not be terminal", s)
		}
	}
}

func TestRuntimeStateValid(t *testing.T) {
	for _, r := range []RuntimeState{
		RuntimeIdle, RuntimeUnknown, RuntimeStopped, RuntimeRunning,
		RuntimeStopping, RuntimeStarting, RuntimeCreating, RuntimeError,
	} {
		if !r.ValidRuntime() {
			t.Errorf("%q should be valid", r)
		}
	}
	for _, r := range []RuntimeState{"", "paused", "Running"} {
		if r.ValidRuntime() {
			t.Errorf("%q should be invalid", r)
		}
	}
}

func TestRecomputeCountsLiveSessionsOnUntrackedWorktrees(t *testing.T) {
	tests := []struct {
		name   string
		states []SessionState
		want   int
	}{
		{"none", nil, 0},
		{"only ended", []SessionState{SessionDone, SessionStopped}, 0},
		{"a failed one, which has not ended", []SessionState{SessionError}, 1},
		{"live and ended", []SessionState{
			SessionRunning, SessionWaiting, SessionDone, SessionStopped,
		}, 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var sessions []Session
			for _, state := range test.states {
				sessions = append(sessions, Session{ID: "s", State: state})
			}
			worktree := UntrackedWorktree{Name: "wt", Path: "/wt", Sessions: sessions}
			project := Project{UntrackedWorktrees: []UntrackedWorktree{worktree}}
			project.Recompute()
			if got := project.UntrackedWorktrees[0].SessionCount; got != test.want {
				t.Errorf("SessionCount = %d, want %d", got, test.want)
			}
		})
	}
}

// A suggestion's sessions stay hidden: clients see only how many are live.
func TestUntrackedWorktreeJSONLeavesOutItsSessions(t *testing.T) {
	worktree := UntrackedWorktree{Name: "wt", Path: "/wt", Sessions: []Session{{ID: "s", State: SessionRunning}}}
	data, err := json.Marshal(worktree)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"sessions"`) {
		t.Errorf("suggestion JSON lists its sessions: %s", data)
	}
}

func TestRecomputeMakesNoUntrackedWorktreesAnEmptyList(t *testing.T) {
	project := Project{}
	project.Recompute()
	data, err := json.Marshal(project)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"untrackedWorktrees":[]`) {
		t.Errorf("want an empty array, got %s", data)
	}
}
