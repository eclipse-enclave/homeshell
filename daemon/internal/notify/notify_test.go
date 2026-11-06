package notify

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
	"github.com/eclipsesource/homeshell/daemon/internal/settings"
	"github.com/eclipsesource/homeshell/daemon/internal/store"
)

// deliveryRecorder is a Delivery that collects the notifications it receives.
type deliveryRecorder struct {
	got []Notification
}

func (r *deliveryRecorder) Deliver(_ context.Context, notification Notification) {
	r.got = append(r.got, notification)
}

func (r *deliveryRecorder) kinds() []string {
	var kinds []string
	for _, notification := range r.got {
		kinds = append(kinds, notification.Kind)
	}
	return kinds
}

// fixedConfig returns a ConfigFunc that always reports the given style/sound.
func fixedConfig(style, sound string) ConfigFunc {
	return func() Config { return Config{Style: style, Sound: sound} }
}

// snapshot marshals a set of sessions (in one workstream of one project) into a
// state payload the Monitor parses, mirroring store.marshalLocked.
func snapshot(sessions ...domain.Session) []byte {
	projects := []domain.Project{{
		ID:   "proj-1",
		Name: "alpha",
		Workstreams: []domain.Workstream{{
			ID:       "ws-1",
			Name:     "feature/x",
			Sessions: sessions,
		}},
	}}
	data, err := json.Marshal(struct {
		Projects []domain.Project `json:"projects"`
	}{projects})
	if err != nil {
		panic(err)
	}
	return data
}

func session(id string, state domain.SessionState) domain.Session {
	return domain.Session{ID: id, Title: "Implement", Agent: "Claude Code", State: state}
}

// runTransitions seeds a baseline then feeds each snapshot through process,
// collecting notifications into the given in-app recorder.
func runTransitions(recorder *deliveryRecorder, style string, baseline []domain.Session, snapshots [][]domain.Session) {
	monitor := NewMonitor(nil, recorder, nil, fixedConfig(style, "chime"))
	previous := monitor.sessionStates(snapshot(baseline...))
	for _, snap := range snapshots {
		previous = monitor.process(context.Background(), snapshot(snap...), previous)
	}
}

func TestMonitorProcessTransitions(t *testing.T) {
	tests := []struct {
		name      string
		baseline  []domain.Session
		snapshots [][]domain.Session
		wantKinds []string
	}{
		{
			name:      "running to waiting fires waiting",
			baseline:  []domain.Session{session("s1", domain.SessionRunning)},
			snapshots: [][]domain.Session{{session("s1", domain.SessionWaiting)}},
			wantKinds: []string{KindWaiting},
		},
		{
			name:      "idle to waiting fires waiting",
			baseline:  []domain.Session{session("s1", domain.SessionIdle)},
			snapshots: [][]domain.Session{{session("s1", domain.SessionWaiting)}},
			wantKinds: []string{KindWaiting},
		},
		{
			name:      "running to idle fires finished",
			baseline:  []domain.Session{session("s1", domain.SessionRunning)},
			snapshots: [][]domain.Session{{session("s1", domain.SessionIdle)}},
			wantKinds: []string{KindFinished},
		},
		{
			name:      "waiting to idle fires finished",
			baseline:  []domain.Session{session("s1", domain.SessionWaiting)},
			snapshots: [][]domain.Session{{session("s1", domain.SessionIdle)}},
			wantKinds: []string{KindFinished},
		},
		{
			name:     "staying waiting does not repeat",
			baseline: []domain.Session{session("s1", domain.SessionRunning)},
			snapshots: [][]domain.Session{
				{session("s1", domain.SessionWaiting)},
				{session("s1", domain.SessionWaiting)},
			},
			wantKinds: []string{KindWaiting},
		},
		{
			name:     "a full run cycle fires waiting then finished then waiting",
			baseline: []domain.Session{session("s1", domain.SessionRunning)},
			snapshots: [][]domain.Session{
				{session("s1", domain.SessionWaiting)},
				{session("s1", domain.SessionIdle)},
				{session("s1", domain.SessionWaiting)},
			},
			wantKinds: []string{KindWaiting, KindFinished, KindWaiting},
		},
		{
			name:      "idle to running (startup) does not fire",
			baseline:  []domain.Session{session("s1", domain.SessionIdle)},
			snapshots: [][]domain.Session{{session("s1", domain.SessionRunning)}},
			wantKinds: nil,
		},
		{
			name:      "stopped to idle does not fire (only busy→idle counts as finished)",
			baseline:  []domain.Session{session("s1", domain.SessionStopped)},
			snapshots: [][]domain.Session{{session("s1", domain.SessionIdle)}},
			wantKinds: nil,
		},
		{
			name:      "session first seen as waiting is a baseline, not a transition",
			baseline:  []domain.Session{},
			snapshots: [][]domain.Session{{session("s1", domain.SessionWaiting)}},
			wantKinds: nil,
		},
		{
			name:      "starting to waiting does not fire (a session coming up, not activity)",
			baseline:  []domain.Session{session("s1", domain.SessionStarting)},
			snapshots: [][]domain.Session{{session("s1", domain.SessionWaiting)}},
			wantKinds: nil,
		},
		{
			name:      "unknown to waiting does not fire (signal regained, not activity)",
			baseline:  []domain.Session{session("s1", domain.SessionUnknown)},
			snapshots: [][]domain.Session{{session("s1", domain.SessionWaiting)}},
			wantKinds: nil,
		},
		{
			name:      "stopped to waiting does not fire",
			baseline:  []domain.Session{session("s1", domain.SessionStopped)},
			snapshots: [][]domain.Session{{session("s1", domain.SessionWaiting)}},
			wantKinds: nil,
		},
		{
			name:      "error to waiting does not fire",
			baseline:  []domain.Session{session("s1", domain.SessionError)},
			snapshots: [][]domain.Session{{session("s1", domain.SessionWaiting)}},
			wantKinds: nil,
		},
		{
			name:      "done to waiting does not fire",
			baseline:  []domain.Session{session("s1", domain.SessionDone)},
			snapshots: [][]domain.Session{{session("s1", domain.SessionWaiting)}},
			wantKinds: nil,
		},
		{
			name:     "a session recovering from unknown still notifies on its next real transition",
			baseline: []domain.Session{session("s1", domain.SessionUnknown)},
			snapshots: [][]domain.Session{
				{session("s1", domain.SessionRunning)},
				{session("s1", domain.SessionWaiting)},
			},
			wantKinds: []string{KindWaiting},
		},
		{
			name:     "an unknown blip between running and waiting still fires waiting",
			baseline: []domain.Session{session("s1", domain.SessionRunning)},
			snapshots: [][]domain.Session{
				{session("s1", domain.SessionUnknown)},
				{session("s1", domain.SessionWaiting)},
			},
			wantKinds: []string{KindWaiting},
		},
		{
			name:     "an unknown blip between running and idle still fires finished",
			baseline: []domain.Session{session("s1", domain.SessionRunning)},
			snapshots: [][]domain.Session{
				{session("s1", domain.SessionUnknown)},
				{session("s1", domain.SessionUnknown)},
				{session("s1", domain.SessionIdle)},
			},
			wantKinds: []string{KindFinished},
		},
		{
			name:     "an unknown blip that returns to the same state does not fire",
			baseline: []domain.Session{session("s1", domain.SessionRunning)},
			snapshots: [][]domain.Session{
				{session("s1", domain.SessionWaiting)},
				{session("s1", domain.SessionUnknown)},
				{session("s1", domain.SessionWaiting)},
			},
			wantKinds: []string{KindWaiting},
		},
		{
			name:     "a stop in between is a fresh start, not a transition",
			baseline: []domain.Session{session("s1", domain.SessionRunning)},
			snapshots: [][]domain.Session{
				{session("s1", domain.SessionStopped)},
				{session("s1", domain.SessionWaiting)},
			},
			wantKinds: nil,
		},
		{
			name:     "resume through starting and unknown stays silent",
			baseline: []domain.Session{session("s1", domain.SessionStopped)},
			snapshots: [][]domain.Session{
				{session("s1", domain.SessionStarting)},
				{session("s1", domain.SessionUnknown)},
				{session("s1", domain.SessionWaiting)},
			},
			wantKinds: nil,
		},
		{
			name:     "a second session becomes waiting independently",
			baseline: []domain.Session{session("s1", domain.SessionRunning)},
			snapshots: [][]domain.Session{
				{session("s1", domain.SessionRunning), session("s2", domain.SessionRunning)},
				{session("s1", domain.SessionRunning), session("s2", domain.SessionWaiting)},
			},
			wantKinds: []string{KindWaiting},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := &deliveryRecorder{}
			runTransitions(recorder, settings.NotifyStyleInApp, tt.baseline, tt.snapshots)
			if !slices.Equal(recorder.kinds(), tt.wantKinds) {
				t.Fatalf("delivered kinds %v, want %v", recorder.kinds(), tt.wantKinds)
			}
		})
	}
}

// TestMonitorStyleRouting verifies each style routes to the right channel(s).
func TestMonitorStyleRouting(t *testing.T) {
	tests := []struct {
		style      string
		wantInApp  int
		wantNative int
	}{
		{settings.NotifyStyleInApp, 1, 0},
		{settings.NotifyStyleNative, 0, 1},
		{settings.NotifyStyleBoth, 1, 1},
		{settings.NotifyStyleNone, 0, 0},
		{"", 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.style, func(t *testing.T) {
			inApp, native := &deliveryRecorder{}, &deliveryRecorder{}
			monitor := NewMonitor(nil, inApp, native, fixedConfig(tt.style, "default"))
			previous := monitor.sessionStates(snapshot(session("s1", domain.SessionRunning)))
			monitor.process(context.Background(), snapshot(session("s1", domain.SessionWaiting)), previous)
			if len(inApp.got) != tt.wantInApp {
				t.Errorf("in-app deliveries = %d, want %d", len(inApp.got), tt.wantInApp)
			}
			if len(native.got) != tt.wantNative {
				t.Errorf("native deliveries = %d, want %d", len(native.got), tt.wantNative)
			}
		})
	}
}

func TestMonitorWaitingContent(t *testing.T) {
	recorder := &deliveryRecorder{}
	monitor := NewMonitor(nil, recorder, nil, fixedConfig(settings.NotifyStyleInApp, "chime"))
	previous := monitor.sessionStates(snapshot(session("s1", domain.SessionRunning)))
	monitor.process(context.Background(), snapshot(session("s1", domain.SessionWaiting)), previous)

	if len(recorder.got) != 1 {
		t.Fatalf("want exactly one notification, got %d", len(recorder.got))
	}
	notification := recorder.got[0]
	if notification.Kind != KindWaiting {
		t.Errorf("kind = %q, want %q", notification.Kind, KindWaiting)
	}
	if notification.Title != "Implement is waiting for input" {
		t.Errorf("title = %q", notification.Title)
	}
	if notification.Body != "alpha · feature/x" {
		t.Errorf("body = %q", notification.Body)
	}
	if notification.Sound != "chime" {
		t.Errorf("sound = %q, want chime", notification.Sound)
	}
	if notification.ProjectID != "proj-1" || notification.WorkstreamID != "ws-1" || notification.SessionID != "s1" {
		t.Errorf("identifiers = %q/%q/%q", notification.ProjectID, notification.WorkstreamID, notification.SessionID)
	}
	if notification.ID == "" {
		t.Error("notification id is empty")
	}
}

func TestMonitorFinishedContent(t *testing.T) {
	recorder := &deliveryRecorder{}
	monitor := NewMonitor(nil, recorder, nil, fixedConfig(settings.NotifyStyleInApp, ""))
	previous := monitor.sessionStates(snapshot(session("s1", domain.SessionRunning)))
	monitor.process(context.Background(), snapshot(session("s1", domain.SessionIdle)), previous)

	if len(recorder.got) != 1 {
		t.Fatalf("want exactly one notification, got %d", len(recorder.got))
	}
	notification := recorder.got[0]
	if notification.Kind != KindFinished {
		t.Errorf("kind = %q, want %q", notification.Kind, KindFinished)
	}
	if notification.Title != "Implement finished" {
		t.Errorf("title = %q, want %q", notification.Title, "Implement finished")
	}
	if notification.SessionID != "s1" {
		t.Errorf("sessionID = %q, want s1", notification.SessionID)
	}
}

func TestMonitorTitleFallsBackToAgent(t *testing.T) {
	recorder := &deliveryRecorder{}
	monitor := NewMonitor(nil, recorder, nil, fixedConfig(settings.NotifyStyleInApp, ""))
	base := domain.Session{ID: "s1", Agent: "Codex", State: domain.SessionRunning}
	waiting := domain.Session{ID: "s1", Agent: "Codex", State: domain.SessionWaiting}
	previous := monitor.sessionStates(snapshot(base))
	monitor.process(context.Background(), snapshot(waiting), previous)

	if len(recorder.got) != 1 || recorder.got[0].Title != "Codex is waiting for input" {
		t.Fatalf("expected agent-based title, got %+v", recorder.got)
	}
}

func TestMonitorNilConfigDefaultsToNative(t *testing.T) {
	inApp, native := &deliveryRecorder{}, &deliveryRecorder{}
	monitor := NewMonitor(nil, inApp, native, nil)
	previous := monitor.sessionStates(snapshot(session("s1", domain.SessionRunning)))
	monitor.process(context.Background(), snapshot(session("s1", domain.SessionWaiting)), previous)
	if len(native.got) != 1 || len(inApp.got) != 0 {
		t.Fatalf("nil config should default to OS-native delivery, got native=%d in-app=%d", len(native.got), len(inApp.got))
	}
}

// TestMonitorEventGating covers the per-kind settings: a kind whose Notify flag
// is off is not delivered at all, and a kind whose Sound flag is off is still
// delivered but stamped silent — the "notify me, quietly" case.
func TestMonitorEventGating(t *testing.T) {
	tests := []struct {
		name      string
		events    map[string]EventRule
		wantKinds []string
		wantSound map[string]string
	}{
		{
			name:      "both kinds on and audible",
			events:    map[string]EventRule{KindWaiting: {true, true}, KindFinished: {true, true}},
			wantKinds: []string{KindWaiting, KindFinished},
			wantSound: map[string]string{KindWaiting: "chime", KindFinished: "chime"},
		},
		{
			name:      "finished off leaves only waiting",
			events:    map[string]EventRule{KindWaiting: {true, true}, KindFinished: {false, true}},
			wantKinds: []string{KindWaiting},
			wantSound: map[string]string{KindWaiting: "chime"},
		},
		{
			name:      "waiting off leaves only finished",
			events:    map[string]EventRule{KindWaiting: {false, true}, KindFinished: {true, true}},
			wantKinds: []string{KindFinished},
			wantSound: map[string]string{KindFinished: "chime"},
		},
		{
			name:      "both off delivers nothing",
			events:    map[string]EventRule{KindWaiting: {false, false}, KindFinished: {false, false}},
			wantKinds: nil,
		},
		{
			name:      "finished muted is still delivered, silently",
			events:    map[string]EventRule{KindWaiting: {true, true}, KindFinished: {true, false}},
			wantKinds: []string{KindWaiting, KindFinished},
			wantSound: map[string]string{KindWaiting: "chime", KindFinished: SilentSound},
		},
		{
			name:      "an unconfigured kind stays on and audible",
			events:    nil,
			wantKinds: []string{KindWaiting, KindFinished},
			wantSound: map[string]string{KindWaiting: "chime", KindFinished: "chime"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := &deliveryRecorder{}
			monitor := NewMonitor(nil, recorder, nil, func() Config {
				return Config{Style: settings.NotifyStyleInApp, Sound: "chime", Events: tt.events}
			})
			previous := monitor.sessionStates(snapshot(session("s1", domain.SessionRunning)))
			previous = monitor.process(context.Background(), snapshot(session("s1", domain.SessionWaiting)), previous)
			monitor.process(context.Background(), snapshot(session("s1", domain.SessionIdle)), previous)

			if !slices.Equal(recorder.kinds(), tt.wantKinds) {
				t.Fatalf("delivered kinds %v, want %v", recorder.kinds(), tt.wantKinds)
			}
			for _, got := range recorder.got {
				if want := tt.wantSound[got.Kind]; got.Sound != want {
					t.Errorf("%s sound = %q, want %q", got.Kind, got.Sound, want)
				}
			}
		})
	}
}

// TestMonitorQuietPeriod covers the anti-flap suppression. A detection flap is
// an intermediate state that lasts a single poll, so the gate measures how long
// the session was away from the state it returns to, not how long ago it last
// alerted: a flap stays silent however often it repeats, while real turns that
// follow each other quickly are all announced.
func TestMonitorQuietPeriod(t *testing.T) {
	type step struct {
		after time.Duration
		state domain.SessionState
	}
	tests := []struct {
		name      string
		baseline  domain.SessionState
		steps     []step
		wantKinds []string
	}{
		{
			name:     "a running blip between two idles is one finished",
			baseline: domain.SessionRunning,
			steps: []step{
				{0, domain.SessionIdle},
				{5 * time.Second, domain.SessionRunning},
				{5 * time.Second, domain.SessionIdle},
				{5 * time.Second, domain.SessionRunning},
				{5 * time.Second, domain.SessionIdle},
			},
			wantKinds: []string{KindFinished},
		},
		{
			name:     "a running blip between two prompts is one waiting",
			baseline: domain.SessionRunning,
			steps: []step{
				{0, domain.SessionWaiting},
				{time.Minute, domain.SessionRunning},
				{5 * time.Second, domain.SessionWaiting},
			},
			wantKinds: []string{KindWaiting},
		},
		{
			name:     "prompts arriving less than a quiet period apart all alert",
			baseline: domain.SessionRunning,
			steps: []step{
				{0, domain.SessionWaiting},
				{2 * time.Second, domain.SessionRunning}, // approved
				{10 * time.Second, domain.SessionWaiting},
				{2 * time.Second, domain.SessionRunning}, // approved
				{10 * time.Second, domain.SessionWaiting},
			},
			wantKinds: []string{KindWaiting, KindWaiting, KindWaiting},
		},
		{
			name:     "short turns that finish in quick succession all alert",
			baseline: domain.SessionIdle,
			steps: []step{
				{0, domain.SessionRunning},
				{10 * time.Second, domain.SessionIdle},
				{3 * time.Second, domain.SessionRunning},
				{10 * time.Second, domain.SessionIdle},
			},
			wantKinds: []string{KindFinished, KindFinished},
		},
		{
			name:     "a flap of one kind does not hold back the other",
			baseline: domain.SessionRunning,
			steps: []step{
				{0, domain.SessionIdle},
				{5 * time.Second, domain.SessionWaiting},
			},
			wantKinds: []string{KindFinished, KindWaiting},
		},
		{
			name:     "an unknown blip counts as time away only once the state changes",
			baseline: domain.SessionRunning,
			steps: []step{
				{0, domain.SessionIdle},
				{time.Minute, domain.SessionUnknown},
				{time.Minute, domain.SessionRunning},
				{5 * time.Second, domain.SessionIdle},
			},
			wantKinds: []string{KindFinished},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clock := time.Unix(1_700_000_000, 0)
			recorder := &deliveryRecorder{}
			monitor := NewMonitor(nil, recorder, nil, func() Config {
				return Config{Style: settings.NotifyStyleInApp, Sound: "chime", Quiet: 8 * time.Second}
			})
			monitor.now = func() time.Time { return clock }
			previous := monitor.sessionStates(snapshot(session("s1", tt.baseline)))
			for _, step := range tt.steps {
				clock = clock.Add(step.after)
				previous = monitor.process(context.Background(), snapshot(session("s1", step.state)), previous)
			}
			if !slices.Equal(recorder.kinds(), tt.wantKinds) {
				t.Fatalf("delivered %v, want %v", recorder.kinds(), tt.wantKinds)
			}
		})
	}
}

// TestMonitorQuietPeriodIsPerSession verifies one session's flap never silences
// another's alert: the time away is tracked per session.
func TestMonitorQuietPeriodIsPerSession(t *testing.T) {
	clock := time.Unix(1_700_000_000, 0)
	recorder := &deliveryRecorder{}
	monitor := NewMonitor(nil, recorder, nil, func() Config {
		return Config{Style: settings.NotifyStyleInApp, Quiet: time.Hour}
	})
	monitor.now = func() time.Time { return clock }

	previous := monitor.sessionStates(snapshot(
		session("s1", domain.SessionWaiting),
		session("s2", domain.SessionRunning),
	))
	// s1 leaves waiting, which starts its absence; s2 has never been away.
	previous = monitor.process(context.Background(), snapshot(
		session("s1", domain.SessionRunning),
		session("s2", domain.SessionRunning),
	), previous)
	clock = clock.Add(time.Second)
	monitor.process(context.Background(), snapshot(
		session("s1", domain.SessionWaiting),
		session("s2", domain.SessionWaiting),
	), previous)

	if len(recorder.got) != 1 || recorder.got[0].SessionID != "s2" {
		t.Fatalf("want only s2 announced, got %+v", recorder.got)
	}
}

// TestMonitorQuietPeriodDisabled verifies 0 means "no suppression" — every
// transition is announced, the pre-quiet-period behavior.
func TestMonitorQuietPeriodDisabled(t *testing.T) {
	recorder := &deliveryRecorder{}
	monitor := NewMonitor(nil, recorder, nil, func() Config {
		return Config{Style: settings.NotifyStyleInApp, Quiet: 0}
	})
	previous := monitor.sessionStates(snapshot(session("s1", domain.SessionRunning)))
	for i := 0; i < 3; i++ {
		previous = monitor.process(context.Background(), snapshot(session("s1", domain.SessionIdle)), previous)
		previous = monitor.process(context.Background(), snapshot(session("s1", domain.SessionRunning)), previous)
	}
	if got := len(recorder.got); got != 3 {
		t.Fatalf("delivered %d notifications, want 3 (no suppression)", got)
	}
}

// TestMonitorForgetsRemovedSessions verifies the per-session bookkeeping does
// not grow without bound: a session that leaves the snapshot takes its track
// with it, and one that comes back is a fresh baseline.
func TestMonitorForgetsRemovedSessions(t *testing.T) {
	recorder := &deliveryRecorder{}
	monitor := NewMonitor(nil, recorder, nil, func() Config {
		return Config{Style: settings.NotifyStyleInApp, Quiet: time.Hour}
	})
	previous := monitor.sessionStates(snapshot(session("s1", domain.SessionRunning)))
	previous = monitor.process(context.Background(), snapshot(session("s1", domain.SessionWaiting)), previous)
	previous = monitor.process(context.Background(), snapshot(), previous)
	if len(previous) != 0 {
		t.Fatalf("want the removed session forgotten, got %d tracks", len(previous))
	}
	previous = monitor.process(context.Background(), snapshot(session("s1", domain.SessionRunning)), previous)
	monitor.process(context.Background(), snapshot(session("s1", domain.SessionWaiting)), previous)
	if len(recorder.got) != 2 {
		t.Fatalf("want the returning session to alert as new, got %v", recorder.kinds())
	}
}

// TestConfigFrom verifies the settings document maps onto the Monitor's live
// configuration — the one place the two vocabularies meet.
func TestConfigFrom(t *testing.T) {
	got := ConfigFrom(settings.NotificationsSettings{
		Style:        settings.NotifyStyleBoth,
		Sound:        "glass",
		Waiting:      settings.NotificationEvent{Notify: true, Sound: true},
		Finished:     settings.NotificationEvent{Notify: true, Sound: false},
		QuietSeconds: 20,
	})
	if got.Style != settings.NotifyStyleBoth || got.Sound != "glass" {
		t.Errorf("style/sound = %q/%q", got.Style, got.Sound)
	}
	if want := (EventRule{Notify: true, Sound: true}); got.rule(KindWaiting) != want {
		t.Errorf("waiting rule = %+v, want %+v", got.rule(KindWaiting), want)
	}
	if want := (EventRule{Notify: true, Sound: false}); got.rule(KindFinished) != want {
		t.Errorf("finished rule = %+v, want %+v", got.rule(KindFinished), want)
	}
	if got.Quiet != 20*time.Second {
		t.Errorf("quiet = %s, want 20s", got.Quiet)
	}
	// The Tester walks notableKinds to pick its sound, so a kind ConfigFrom
	// maps but notableKinds misses would be ignored there.
	keys := slices.Sorted(maps.Keys(got.Events))
	if want := slices.Sorted(slices.Values(notableKinds)); !slices.Equal(keys, want) {
		t.Errorf("events keys = %v, want notableKinds %v", keys, want)
	}
}

// TestConfigFromDefaults verifies the shipped defaults notify on both kinds,
// audibly — the settings a fresh daemon runs with.
func TestConfigFromDefaults(t *testing.T) {
	got := ConfigFrom(settings.Defaults().Notifications)
	for _, kind := range []string{KindWaiting, KindFinished} {
		if rule := got.rule(kind); !rule.Notify || !rule.Sound {
			t.Errorf("default %s rule = %+v, want notify and sound", kind, rule)
		}
	}
	if got.Quiet <= 0 {
		t.Errorf("default quiet period = %s, want a non-zero window", got.Quiet)
	}
}

// TestMonitorRunSeedsBaselineWithoutFiring verifies the baseline seed against a
// real store: a session already waiting at start does not notify, but a fresh
// transition into waiting does.
func TestMonitorRunSeedsBaselineWithoutFiring(t *testing.T) {
	projectStore := store.New([]domain.Project{{
		ID:   "proj-1",
		Name: "alpha",
		Workstreams: []domain.Workstream{{
			ID:   "ws-1",
			Name: "feature/x",
			Sessions: []domain.Session{
				session("s1", domain.SessionWaiting),
				session("s2", domain.SessionRunning),
			},
		}},
	}}, nil)

	recorder := &deliveryRecorder{}
	monitor := NewMonitor(projectStore, recorder, nil, fixedConfig(settings.NotifyStyleInApp, ""))

	previous := monitor.sessionStates(projectStore.Snapshot().Data)
	if len(recorder.got) != 0 {
		t.Fatalf("baseline should not notify, got %+v", recorder.got)
	}
	monitor.process(context.Background(), snapshot(
		session("s1", domain.SessionWaiting),
		session("s2", domain.SessionWaiting),
	), previous)
	if len(recorder.got) != 1 || recorder.got[0].SessionID != "s2" {
		t.Fatalf("want one notification for s2, got %+v", recorder.got)
	}
}
