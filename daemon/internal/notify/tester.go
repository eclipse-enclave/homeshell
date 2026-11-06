package notify

import (
	"context"
	"fmt"
	"sync/atomic"
)

// Tester delivers the sample notification a client asks for when the user picks
// a delivery style on the settings page — "show me what that looks like".
//
// It is deliberately not the Monitor. The Monitor's gates all exist to decide
// whether an *observed* transition is worth interrupting someone for: the
// per-kind switches, the quiet period, and the live-state rule. A test has no
// transition behind it and the user asked for it directly, so it is always
// delivered — the only thing a test must share with a real notification is where
// it goes, what it looks like and what it sounds like. That is exactly what it
// borrows: channelsFor routes the style, and testSound reads the sound from the
// same live settings, so what the user sees and hears here is what they will see
// and hear later.
type Tester struct {
	channels
	config ConfigFunc
	seq    atomic.Uint64
}

// NewTester builds a Tester over the same channels the Monitor delivers on.
// Either channel may be nil to disable it; config supplies the live sound (and
// the style, for a test that does not name one).
func NewTester(inApp, native Delivery, config ConfigFunc) *Tester {
	if config == nil {
		config = func() Config { return Config{Sound: DefaultSound} }
	}
	return &Tester{channels: channels{inApp: inApp, native: native}, config: config}
}

// TestResult reports what a test notification did: the style it was delivered
// as (the requested one, or the configured one when the request named none) and
// the channels it actually reached. A style of "none" reaches nothing, which is
// the honest demonstration of what "none" does.
type TestResult struct {
	Style    string   `json:"style"`
	Channels []string `json:"channels"`
}

// Send delivers a test notification through the channels the given style
// selects and reports what it did. An empty style means "use the configured
// one", so a caller that only wants to demonstrate the current setting need not
// read it first.
//
// The context must outlive the caller's request: the native channel spawns a
// platform command asynchronously, so a context cancelled as the HTTP handler
// returns would kill the notification before the desktop ever showed it.
func (t *Tester) Send(ctx context.Context, style string) TestResult {
	config := t.config()
	if style == "" {
		style = config.Style
	}
	result := TestResult{Style: style, Channels: []string{}}
	notification := t.build(testSound(config))
	for _, channel := range channelsFor(style) {
		delivery := t.pick(channel)
		if delivery == nil {
			continue
		}
		delivery.Deliver(ctx, notification)
		result.Channels = append(result.Channels, channel)
	}
	return result
}

// testSound is the sound a test notification plays: the configured one if any
// kind that alerts plays a sound, silence otherwise.
func testSound(config Config) string {
	for _, kind := range notableKinds {
		if config.audible(kind) {
			return config.Sound
		}
	}
	return SilentSound
}

// build authors the sample notification (the daemon writes the text, the
// client renders it). It carries no session identifiers because no session is
// involved — the fields stay empty rather than naming a session the user did
// not act on.
func (t *Tester) build(sound string) Notification {
	if sound == "" {
		sound = DefaultSound
	}
	return Notification{
		ID:    fmt.Sprintf("ntf-test-%d", t.seq.Add(1)),
		Kind:  KindTest,
		Title: "Test notification",
		Body:  "This is how HomeShell will let you know.",
		Sound: sound,
	}
}
