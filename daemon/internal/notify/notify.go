// Package notify raises user-facing notifications on notable agent activity —
// today, a session entering the waiting-for-input state or finishing (issue
// #66).
//
// The daemon owns the trigger detection: a single Monitor observes the
// store's state changes and decides when a notification is warranted, then
// delivers it through the channel(s) the user chose. The two channels map to
// the two kinds the issue calls for: OS-native (a platform desktop toast, shown
// by the daemon's DesktopNotifier) and in-app (relayed to the web UI over the
// SSE "notification" event and rendered as a toast). The Notifications settings
// category picks the style and sound; the Monitor reads them live, so a change
// takes effect without a restart. The style vocabulary is owned by
// daemon/internal/settings (its Validate is the authority); the Monitor routes
// against those same constants so the two cannot drift.
//
// Not every state change is activity worth interrupting a user for, and the
// states are *detected* from a terminal screen rather than reported, so the same
// change can be observed more than once. Three filters keep the alerts to real
// activity, all of them here rather than in a client:
//
//   - the transition must start from a live state (see notableTransition), so
//     startup, recovery and lifecycle changes stay silent. An `unknown` reading
//     in between is skipped over rather than treated as the start, so a capture
//     that blips does not swallow the alert that follows it;
//   - each kind can be turned off entirely, or kept but muted, in the settings;
//   - a session that returns to the state it just left within the configured
//     quiet period is a flapping detection, not new activity, so that return
//     stays silent. Real turns that follow each other quickly still alert,
//     because the gate measures how long the session was away, not how long ago
//     it last alerted.
package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
	"github.com/eclipsesource/homeshell/daemon/internal/settings"
	"github.com/eclipsesource/homeshell/daemon/internal/store"
)

// Kinds of notification. The first two are the notify-worthy session
// transitions; KindTest is the sample the user asks for from the settings page.
const (
	// KindTest marks the sample notification a client asks for when the user
	// picks a delivery style ("show me what that looks like"). Nothing
	// transitioned, so the Monitor never raises it — see Tester.
	KindTest = "test"
	// KindWaiting marks a session that has entered the waiting-for-input state.
	KindWaiting = "waiting"
	// KindFinished marks a session that has finished its work — a busy session
	// (running or waiting) that went idle. This is the daemon-observable form of
	// the UI's "done" overlay: the daemon reports idle (never done, which is
	// idle-and-unseen and inherently per-viewer), so "finished" is detected as
	// the busy→idle transition.
	KindFinished = "finished"
)

// notableKinds are the kinds a session transition raises, each with its own
// EventRule in ConfigFrom.
var notableKinds = []string{KindWaiting, KindFinished}

// DefaultSound is the sound name used when the configured sound is empty.
// Delivery channels map it to a concrete platform/browser sound.
const DefaultSound = "default"

// SilentSound is the sound name that means "no sound". Every delivery channel
// maps it to silence (the browser plays no tone, macOS gets no `sound name`
// clause, Linux hints the server to suppress its sound and runs no player),
// so muting one event is a matter of stamping this instead of the configured
// sound — the notification itself is still delivered.
const SilentSound = "none"

// EventRule is the per-kind half of the configuration: whether this kind of
// transition notifies at all, and whether that notification makes a sound.
type EventRule struct {
	Notify bool
	Sound  bool
}

// Config is the live notification configuration the Monitor consults for each
// notification: which style to deliver, which sound to play, which kinds are
// wanted (and audible), and how long the same kind stays quiet for the same
// session. Style is one of the settings.NotifyStyle* values.
type Config struct {
	Style string
	Sound string
	// Events gates delivery per notification kind. A kind with no entry is
	// delivered audibly, so a zero Config behaves like the pre-settings default
	// rather than silently dropping everything.
	Events map[string]EventRule
	// Quiet is the shortest time a session must spend away from a state for its
	// return to that state to count as new activity. A shorter absence is a
	// flapping detection and is not announced. Zero disables suppression.
	Quiet time.Duration
}

// rule returns the gating for a kind, defaulting to "notify, with sound" when
// the configuration says nothing about it.
func (c Config) rule(kind string) EventRule {
	if rule, ok := c.Events[kind]; ok {
		return rule
	}
	return EventRule{Notify: true, Sound: true}
}

// audible reports whether a notification of the given kind both alerts and
// plays a sound.
func (c Config) audible(kind string) bool {
	rule := c.rule(kind)
	return rule.Notify && rule.Sound
}

// soundFor returns the sound a notification of the given kind plays: the
// configured one, or SilentSound if that kind does not alert or is muted.
func (c Config) soundFor(kind string) string {
	if c.audible(kind) {
		return c.Sound
	}
	return SilentSound
}

// ConfigFrom builds the Monitor's configuration from the user's Notifications
// settings. It is the single place the settings vocabulary is mapped onto the
// notification kinds, so the two cannot drift. Its Events keys are notableKinds.
func ConfigFrom(n settings.NotificationsSettings) Config {
	return Config{
		Style: n.Style,
		Sound: n.Sound,
		Events: map[string]EventRule{
			KindWaiting:  {Notify: n.Waiting.Notify, Sound: n.Waiting.Sound},
			KindFinished: {Notify: n.Finished.Notify, Sound: n.Finished.Sound},
		},
		Quiet: n.QuietPeriod(),
	}
}

// ConfigFunc returns the current notification configuration. The Monitor calls
// it per notification so a settings change takes effect immediately.
type ConfigFunc func() Config

// Notification is one user-facing alert. It is authored by the daemon and
// carries both display text and the identifiers a client needs to locate the
// source session. It is the JSON payload of the SSE "notification" event.
type Notification struct {
	ID           string `json:"id"`
	Kind         string `json:"kind"`
	Title        string `json:"title"`
	Body         string `json:"body"`
	Sound        string `json:"sound,omitempty"`
	ProjectID    string `json:"projectId"`
	ProjectName  string `json:"projectName"`
	WorkstreamID string `json:"workstreamId"`
	SessionID    string `json:"sessionId"`
	SessionTitle string `json:"sessionTitle"`
	Agent        string `json:"agent"`
}

// Delivery shows a Notification on one channel (the browser Hub, the native
// DesktopNotifier). It must not block: the Monitor calls it from its single
// event loop, so a slow channel would back the store subscription up.
type Delivery interface {
	Deliver(ctx context.Context, notification Notification)
}

// Channel names: the concrete delivery channels a style routes onto. A style is
// what the user picks ("both"); a channel is where a notification actually goes.
const (
	ChannelInApp  = "in-app"
	ChannelNative = "native"
)

// channelsFor returns the channels a style delivers on, in delivery order.
// Anything that is not a delivering style — "none", the empty string, or a
// value that never passed validation — selects nothing. Both the Monitor and
// the Tester route through this, so a test notification always lands exactly
// where a real one would.
func channelsFor(style string) []string {
	switch style {
	case settings.NotifyStyleInApp:
		return []string{ChannelInApp}
	case settings.NotifyStyleNative:
		return []string{ChannelNative}
	case settings.NotifyStyleBoth:
		return []string{ChannelInApp, ChannelNative}
	default:
		return nil
	}
}

// channels holds the concrete delivery channels a component was wired with, so
// channelsFor's names can be resolved to them. Either may be nil, which
// disables that channel.
type channels struct {
	inApp  Delivery
	native Delivery
}

// pick resolves a channel name to the channel that delivers it, or nil.
func (c channels) pick(name string) Delivery {
	if name == ChannelNative {
		return c.native
	}
	return c.inApp
}

// Monitor watches the store for session state transitions and delivers
// notifications through the channel(s) the configured style selects. It is
// production infrastructure and runs in every mode; the fake demo provider's
// cycler exercises it end-to-end.
type Monitor struct {
	store stateSource
	channels
	config ConfigFunc
	seq    atomic.Uint64
	// now is the clock the quiet period is measured on; tests replace it.
	now func() time.Time
}

// sessionTrack is what the Monitor remembers about one session between passes.
// It lives in the per-pass map process returns, so a session that leaves the
// snapshot takes its history with it and nothing accumulates.
type sessionTrack struct {
	// live is the session's last live state (running, waiting, idle), the state
	// a transition is judged from. An `unknown` reading leaves it as it was; a
	// lifecycle state (starting, stopped, done, error) clears it, so whatever
	// follows is a fresh start rather than activity.
	live domain.SessionState
	// leftAt records when the session last left each live state, so a quick
	// return to it can be recognised as a flap.
	leftAt map[domain.SessionState]time.Time
}

// stateSource is the slice of the store the Monitor needs: subscribe to change
// events and read the current snapshot to seed a baseline.
type stateSource interface {
	Subscribe() (<-chan store.Event, func())
	Snapshot() store.Event
}

// NewMonitor builds a Monitor over the given store. inApp delivers the web-UI
// toast channel (the SSE hub) and native delivers the OS-native channel (the
// desktop notifier); either may be nil to disable that channel. config supplies
// the live style and sound (defaulting to OS-native when nil, matching the
// product default).
func NewMonitor(source stateSource, inApp, native Delivery, config ConfigFunc) *Monitor {
	if config == nil {
		config = func() Config { return Config{Style: settings.NotifyStyleNative, Sound: DefaultSound} }
	}
	return &Monitor{
		store:    source,
		channels: channels{inApp: inApp, native: native},
		config:   config,
		now:      time.Now,
	}
}

// Run observes state changes until ctx is cancelled. It seeds a baseline from
// the current snapshot without notifying, so only genuine transitions after
// start fire, and resubscribes (reseeding) if the store drops it as a slow
// subscriber.
func (m *Monitor) Run(ctx context.Context) {
	for m.runOnce(ctx) {
		slog.Debug("notify monitor resubscribing after a dropped subscription")
	}
}

// runOnce subscribes, seeds a baseline, and processes events until ctx is
// cancelled (returns false) or the subscription is dropped (returns true, so
// Run resubscribes).
func (m *Monitor) runOnce(ctx context.Context) bool {
	events, cancel := m.store.Subscribe()
	defer cancel()

	// Seed the baseline from the current snapshot: every session's state is
	// recorded without notifying, so a session already waiting at start (e.g. a
	// persisted or seeded one) does not fire until it transitions afresh.
	previous := m.sessionStates(m.store.Snapshot().Data)

	for {
		select {
		case <-ctx.Done():
			return false
		case event, ok := <-events:
			if !ok {
				return true
			}
			previous = m.process(ctx, event.Data, previous)
		}
	}
}

// process compares the sessions in the new snapshot against previous, delivering
// a notification for each session that made a notify-worthy transition, and
// returns the updated tracks. A first observation (no prior track) is a
// baseline, not a transition, so startup and reconnects do not spam.
func (m *Monitor) process(ctx context.Context, data []byte, previous map[string]sessionTrack) map[string]sessionTrack {
	next := make(map[string]sessionTrack, len(previous))
	for _, session := range parseSessions(data) {
		key := session.key()
		track, existed := previous[key]
		if !existed {
			next[key] = baselineTrack(session.state)
			continue
		}
		next[key] = m.observe(ctx, session, track)
	}
	return next
}

// baselineTrack is the track for a session seen for the first time.
func baselineTrack(state domain.SessionState) sessionTrack {
	track := sessionTrack{leftAt: map[domain.SessionState]time.Time{}}
	if liveState(state) {
		track.live = state
	}
	return track
}

// observe folds one newly observed state into a session's track, notifying if
// it completes a notable transition from the session's last live state.
func (m *Monitor) observe(ctx context.Context, session sessionInfo, track sessionTrack) sessionTrack {
	state := session.state
	switch {
	case state == domain.SessionUnknown, state == track.live:
		// No signal says nothing about what the session is doing, so the last
		// live state stands and the next real reading is judged against it.
		return track
	case !liveState(state):
		if track.live != "" {
			track.leftAt[track.live] = m.now()
		}
		track.live = ""
		return track
	}
	from := track.live
	if from != "" {
		track.leftAt[from] = m.now()
	}
	track.live = state
	if kind := notableTransition(from, state); kind != "" {
		m.dispatch(ctx, session, kind, track.leftAt[state])
	}
	return track
}

// dispatch delivers a notification for the given transition through the
// channel(s) the currently-configured style selects, stamping the configured
// sound. Three gates sit in front of delivery, all read live from the settings:
// the style (none delivers nothing), the per-kind Notify flag (this kind is not
// wanted), and the quiet period (the session is back in a state it left only
// moments ago, at leftAt). A kind whose Sound flag is off is still delivered,
// stamped silent.
func (m *Monitor) dispatch(ctx context.Context, session sessionInfo, kind string, leftAt time.Time) {
	config := m.config()
	if config.Style == settings.NotifyStyleNone || config.Style == "" {
		return
	}
	if !config.rule(kind).Notify {
		return
	}
	if m.flapped(leftAt, config.Quiet) {
		slog.Debug("notify: suppressed a return to a state the session just left",
			"kind", kind, "session", session.sessionID, "quiet", config.Quiet)
		return
	}
	notification := m.build(session, kind, config.soundFor(kind))
	for _, channel := range channelsFor(config.Style) {
		deliver(ctx, m.pick(channel), notification)
	}
}

func deliver(ctx context.Context, channel Delivery, notification Notification) {
	if channel != nil {
		channel.Deliver(ctx, notification)
	}
}

// flapped reports whether a session that left its current state at leftAt came
// back too quickly for the return to be new activity. A detection flap is an
// intermediate state that lasts a single poll; a real turn, however short the
// gap since the previous alert, keeps the session away for longer than that.
func (m *Monitor) flapped(leftAt time.Time, window time.Duration) bool {
	if window <= 0 || leftAt.IsZero() {
		return false
	}
	return m.now().Sub(leftAt) < window
}

// notableTransition returns the notification kind for a state change worth
// alerting on, or "" for one that is not. Attention-worthy changes: a session
// becoming ready for input (→ waiting) and a session finishing its work (a busy
// session → idle).
//
// The transition must *start* from a live state (running, waiting, idle) — one
// where the session was actually observed working. Coming out of starting,
// stopped, error, done or unknown is the daemon (re)establishing what a session
// is doing, not the session doing something: a container that starts up into a
// prompt or a session resumed from disk would otherwise each announce
// themselves as needing input the moment they were first classified. `unknown`
// never reaches here as `from`: the Monitor judges a transition against the
// session's last live state and skips over readings with no signal.
func notableTransition(from, to domain.SessionState) string {
	if !liveState(from) {
		return ""
	}
	switch {
	case to == domain.SessionWaiting:
		return KindWaiting
	case to == domain.SessionIdle && (from == domain.SessionRunning || from == domain.SessionWaiting):
		return KindFinished
	default:
		return ""
	}
}

// liveState reports whether a state is one a session is observed *working* in,
// as opposed to the lifecycle and no-signal states (starting, stopped, done,
// error, unknown) a transition out of says nothing about agent activity.
func liveState(state domain.SessionState) bool {
	switch state {
	case domain.SessionRunning, domain.SessionWaiting, domain.SessionIdle:
		return true
	default:
		return false
	}
}

// build turns an observed transition into a Notification with daemon-authored
// display text, stamped with the given sound.
func (m *Monitor) build(session sessionInfo, kind, sound string) Notification {
	subject := firstNonEmpty(session.sessionTitle, session.agent, "A session")
	title := subject + " is waiting for input"
	if kind == KindFinished {
		title = subject + " finished"
	}
	return Notification{
		ID:           fmt.Sprintf("ntf-%d", m.seq.Add(1)),
		Kind:         kind,
		Title:        title,
		Body:         locationText(session.projectName, session.workstreamName),
		Sound:        sound,
		ProjectID:    session.projectID,
		ProjectName:  session.projectName,
		WorkstreamID: session.workstreamID,
		SessionID:    session.sessionID,
		SessionTitle: session.sessionTitle,
		Agent:        session.agent,
	}
}

// sessionStates parses a state snapshot into per-session baseline tracks (used
// to seed the baseline).
func (m *Monitor) sessionStates(data []byte) map[string]sessionTrack {
	sessions := parseSessions(data)
	tracks := make(map[string]sessionTrack, len(sessions))
	for _, session := range sessions {
		tracks[session.key()] = baselineTrack(session.state)
	}
	return tracks
}

// sessionInfo is the flattened view of one session the Monitor reasons about.
type sessionInfo struct {
	projectID      string
	projectName    string
	workstreamID   string
	workstreamName string
	sessionID      string
	sessionTitle   string
	agent          string
	state          domain.SessionState
}

// key uniquely identifies a session across the project tree. The NUL separator
// cannot appear in an id, so distinct sessions never collide.
func (s sessionInfo) key() string {
	return s.projectID + "\x00" + s.workstreamID + "\x00" + s.sessionID
}

// parseSessions flattens the store's marshaled state snapshot into sessions. A
// malformed payload (never expected from our own store) yields no sessions.
func parseSessions(data []byte) []sessionInfo {
	var payload struct {
		Projects []domain.Project `json:"projects"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		slog.Warn("notify: parsing state snapshot", "error", err)
		return nil
	}
	var sessions []sessionInfo
	for _, project := range payload.Projects {
		for _, workstream := range project.Workstreams {
			for _, session := range workstream.Sessions {
				sessions = append(sessions, sessionInfo{
					projectID:      project.ID,
					projectName:    project.Name,
					workstreamID:   workstream.ID,
					workstreamName: workstream.Name,
					sessionID:      session.ID,
					sessionTitle:   session.Title,
					agent:          session.Agent,
					state:          session.State,
				})
			}
		}
	}
	return sessions
}

func locationText(project, workstream string) string {
	switch {
	case project != "" && workstream != "":
		return project + " · " + workstream
	case project != "":
		return project
	case workstream != "":
		return workstream
	default:
		return "HomeShell"
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
