package notify

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/settings"
)

// testerFor builds a Tester over two recorders and returns all three.
func testerFor(config ConfigFunc) (*Tester, *deliveryRecorder, *deliveryRecorder) {
	inApp, native := &deliveryRecorder{}, &deliveryRecorder{}
	return NewTester(inApp, native, config), inApp, native
}

func TestTesterSendRoutesByStyle(t *testing.T) {
	tests := []struct {
		name         string
		configStyle  string
		requested    string
		wantStyle    string
		wantChannels []string
		wantInApp    int
		wantNative   int
	}{
		{
			name:         "in-app reaches the browser only",
			configStyle:  settings.NotifyStyleNone,
			requested:    settings.NotifyStyleInApp,
			wantStyle:    settings.NotifyStyleInApp,
			wantChannels: []string{ChannelInApp},
			wantInApp:    1,
		},
		{
			name:         "native reaches the desktop only",
			configStyle:  settings.NotifyStyleNone,
			requested:    settings.NotifyStyleNative,
			wantStyle:    settings.NotifyStyleNative,
			wantChannels: []string{ChannelNative},
			wantNative:   1,
		},
		{
			name:         "both reaches both channels",
			configStyle:  settings.NotifyStyleNone,
			requested:    settings.NotifyStyleBoth,
			wantStyle:    settings.NotifyStyleBoth,
			wantChannels: []string{ChannelInApp, ChannelNative},
			wantInApp:    1,
			wantNative:   1,
		},
		{
			// "None" is a real answer to "what does this style look like?": it
			// looks like nothing, and the empty channel list says so.
			name:         "none delivers nothing",
			configStyle:  settings.NotifyStyleBoth,
			requested:    settings.NotifyStyleNone,
			wantStyle:    settings.NotifyStyleNone,
			wantChannels: []string{},
		},
		{
			name:         "an unnamed style tests the configured one",
			configStyle:  settings.NotifyStyleInApp,
			requested:    "",
			wantStyle:    settings.NotifyStyleInApp,
			wantChannels: []string{ChannelInApp},
			wantInApp:    1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tester, inApp, native := testerFor(fixedConfig(tt.configStyle, "chime"))
			result := tester.Send(context.Background(), tt.requested)
			if result.Style != tt.wantStyle {
				t.Errorf("style = %q, want %q", result.Style, tt.wantStyle)
			}
			if !slices.Equal(result.Channels, tt.wantChannels) {
				t.Errorf("channels = %v, want %v", result.Channels, tt.wantChannels)
			}
			if len(inApp.got) != tt.wantInApp {
				t.Errorf("in-app deliveries = %d, want %d", len(inApp.got), tt.wantInApp)
			}
			if len(native.got) != tt.wantNative {
				t.Errorf("native deliveries = %d, want %d", len(native.got), tt.wantNative)
			}
		})
	}
}

func TestTesterSendStampsTheConfiguredSound(t *testing.T) {
	tester, inApp, _ := testerFor(fixedConfig(settings.NotifyStyleInApp, "glass"))
	tester.Send(context.Background(), settings.NotifyStyleInApp)
	if len(inApp.got) != 1 {
		t.Fatalf("deliveries = %d, want 1", len(inApp.got))
	}
	got := inApp.got[0]
	if got.Sound != "glass" {
		t.Errorf("sound = %q, want the configured %q", got.Sound, "glass")
	}
	if got.Kind != KindTest {
		t.Errorf("kind = %q, want %q", got.Kind, KindTest)
	}
	if got.Title == "" || got.Body == "" {
		t.Errorf("the daemon authors the text; got title %q body %q", got.Title, got.Body)
	}
	// No session transitioned, so the sample names none rather than pointing at
	// one the user did not act on.
	if got.SessionID != "" || got.ProjectID != "" {
		t.Errorf("test notification carries session identifiers: %+v", got)
	}
}

// A missing sound falls back to the default rather than to silence: a test the
// user asked for that made no sound would read as a broken notification.
func TestTesterSendDefaultsAnEmptySound(t *testing.T) {
	tester, inApp, _ := testerFor(fixedConfig(settings.NotifyStyleInApp, ""))
	tester.Send(context.Background(), settings.NotifyStyleInApp)
	if got := inApp.got[0].Sound; got != DefaultSound {
		t.Errorf("sound = %q, want %q", got, DefaultSound)
	}
}

// The user's "Silent" choice is honored: the test is delivered, stamped silent,
// exactly as a muted real notification would be.
func TestTesterSendKeepsSilentSilent(t *testing.T) {
	tester, inApp, _ := testerFor(fixedConfig(settings.NotifyStyleInApp, SilentSound))
	tester.Send(context.Background(), settings.NotifyStyleInApp)
	if got := inApp.got[0].Sound; got != SilentSound {
		t.Errorf("sound = %q, want %q", got, SilentSound)
	}
}

// The per-event "Play a sound" switches govern the test too: a user who has
// muted every event they are alerted to must not hear the sample either.
func TestTesterSendFollowsThePerEventSoundSwitches(t *testing.T) {
	tests := []struct {
		name   string
		events map[string]EventRule
		want   string
	}{
		{
			name:   "every event muted plays nothing",
			events: map[string]EventRule{KindWaiting: {Notify: true}, KindFinished: {Notify: true}},
			want:   SilentSound,
		},
		{
			name:   "one audible event plays the configured sound",
			events: map[string]EventRule{KindWaiting: {Notify: true}, KindFinished: {Notify: true, Sound: true}},
			want:   "chime",
		},
		{
			// A switched-off event never alerts, so its leftover sound flag
			// cannot make the sample audible.
			name:   "a sound on an event that never alerts does not count",
			events: map[string]EventRule{KindWaiting: {Sound: true}, KindFinished: {Notify: true}},
			want:   SilentSound,
		},
		{
			name:   "no per-event configuration is audible",
			events: nil,
			want:   "chime",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := func() Config {
				return Config{Style: settings.NotifyStyleInApp, Sound: "chime", Events: tt.events}
			}
			tester, inApp, _ := testerFor(config)
			tester.Send(context.Background(), "")
			if len(inApp.got) != 1 {
				t.Fatalf("deliveries = %d, want 1 (muting sounds never drops the test)", len(inApp.got))
			}
			if got := inApp.got[0].Sound; got != tt.want {
				t.Errorf("sound = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTesterSendGivesEachTestItsOwnID(t *testing.T) {
	tester, inApp, _ := testerFor(fixedConfig(settings.NotifyStyleInApp, "chime"))
	tester.Send(context.Background(), settings.NotifyStyleInApp)
	tester.Send(context.Background(), settings.NotifyStyleInApp)
	if inApp.got[0].ID == inApp.got[1].ID {
		t.Errorf("both tests carry id %q; a repeated id would collapse the toasts", inApp.got[0].ID)
	}
}

// None of the Monitor's activity gates apply to a test: the user asked for it
// directly, so a muted kind and a quiet period that would suppress a real alert
// must not swallow it.
func TestTesterSendIgnoresTheEventGatesAndQuietPeriod(t *testing.T) {
	config := func() Config {
		return Config{
			Style:  settings.NotifyStyleInApp,
			Sound:  "chime",
			Events: map[string]EventRule{KindWaiting: {}, KindFinished: {}},
			Quiet:  time.Hour,
		}
	}
	tester, inApp, _ := testerFor(config)
	tester.Send(context.Background(), "")
	tester.Send(context.Background(), "")
	if len(inApp.got) != 2 {
		t.Errorf("deliveries = %d, want 2 (a test is never gated or rate-limited)", len(inApp.got))
	}
}

// A channel the daemon was not wired with is not reported as delivered.
func TestTesterSendReportsOnlyWiredChannels(t *testing.T) {
	inApp := &deliveryRecorder{}
	tester := NewTester(inApp, nil, fixedConfig(settings.NotifyStyleBoth, "chime"))
	result := tester.Send(context.Background(), settings.NotifyStyleBoth)
	if !slices.Equal(result.Channels, []string{ChannelInApp}) {
		t.Errorf("channels = %v, want just %v", result.Channels, []string{ChannelInApp})
	}
}
