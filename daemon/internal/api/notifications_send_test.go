//go:build dev

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/notify"
	"github.com/eclipsesource/homeshell/daemon/internal/settings"
	"github.com/eclipsesource/homeshell/daemon/internal/store"
)

// countingDelivery records the notifications one channel received.
type countingDelivery struct {
	got []notify.Notification
}

func (c *countingDelivery) Deliver(_ context.Context, notification notify.Notification) {
	c.got = append(c.got, notification)
}

// notifyTestHandler builds the endpoint over recording channels and a fixed
// configuration, returning the handler and both channels.
func notifyTestHandler(t *testing.T, style, sound string) (http.Handler, *countingDelivery, *countingDelivery) {
	t.Helper()
	inApp, native := &countingDelivery{}, &countingDelivery{}
	tester := notify.NewTester(inApp, native, func() notify.Config {
		return notify.Config{Style: style, Sound: sound}
	})
	handler := handlerForTest(Deps{
		Version:       "1.2.3",
		Store:         store.NewTestStore(t, store.Seed()),
		Heartbeat:     time.Minute,
		Notifications: notify.NewHub(),
		NotifyTester:  tester,
	})
	return handler, inApp, native
}

func postNotificationTest(t *testing.T, handler http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/notifications/test", strings.NewReader(body))
	handler.ServeHTTP(recorder, request)
	return recorder
}

// decodeTestResult reads the endpoint's {style, channels} body.
func decodeTestResult(t *testing.T, recorder *httptest.ResponseRecorder) notify.TestResult {
	t.Helper()
	var result notify.TestResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatalf("decoding %s: %v", recorder.Body.String(), err)
	}
	return result
}

func TestNotificationTestDeliversTheRequestedStyle(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantStyle  string
		wantInApp  int
		wantNative int
	}{
		{name: "in-app", body: `{"style":"in-app"}`, wantStyle: settings.NotifyStyleInApp, wantInApp: 1},
		{name: "native", body: `{"style":"native"}`, wantStyle: settings.NotifyStyleNative, wantNative: 1},
		{name: "both", body: `{"style":"both"}`, wantStyle: settings.NotifyStyleBoth, wantInApp: 1, wantNative: 1},
		{name: "none", body: `{"style":"none"}`, wantStyle: settings.NotifyStyleNone},
		// No style named, and no body at all: both mean "test what is configured".
		{name: "empty style", body: `{}`, wantStyle: settings.NotifyStyleInApp, wantInApp: 1},
		{name: "no body", body: ``, wantStyle: settings.NotifyStyleInApp, wantInApp: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler, inApp, native := notifyTestHandler(t, settings.NotifyStyleInApp, "chime")
			recorder := postNotificationTest(t, handler, tt.body)

			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d; body: %s", recorder.Code, http.StatusOK, recorder.Body.String())
			}
			if got := decodeTestResult(t, recorder).Style; got != tt.wantStyle {
				t.Errorf("style = %q, want %q", got, tt.wantStyle)
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

func TestNotificationTestCarriesTheSampleText(t *testing.T) {
	handler, inApp, _ := notifyTestHandler(t, settings.NotifyStyleNone, "glass")
	postNotificationTest(t, handler, `{"style":"in-app"}`)

	if len(inApp.got) != 1 {
		t.Fatalf("in-app deliveries = %d, want 1", len(inApp.got))
	}
	got := inApp.got[0]
	if got.Kind != notify.KindTest {
		t.Errorf("kind = %q, want %q", got.Kind, notify.KindTest)
	}
	if got.Title == "" {
		t.Error("the test notification has no title")
	}
	// The sound comes from the settings, not the request: the point is to show
	// what the configured notification will be like.
	if got.Sound != "glass" {
		t.Errorf("sound = %q, want the configured %q", got.Sound, "glass")
	}
}

func TestNotificationTestRejectsAnUnknownStyle(t *testing.T) {
	for _, body := range []string{`{"style":"telepathy"}`, `{"style":"IN-APP"}`, `not json`} {
		t.Run(body, func(t *testing.T) {
			handler, inApp, native := notifyTestHandler(t, settings.NotifyStyleInApp, "chime")
			recorder := postNotificationTest(t, handler, body)

			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d; body: %s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
			}
			if len(inApp.got)+len(native.got) != 0 {
				t.Error("a rejected request still delivered a notification")
			}
		})
	}
}

// The endpoint reports 503 rather than 500 when the daemon was built without a
// tester, matching the other optional-dependency endpoints.
func TestNotificationTestWithoutATesterIsUnavailable(t *testing.T) {
	handler := handlerForTest(Deps{
		Version:       "1.2.3",
		Store:         store.NewTestStore(t, store.Seed()),
		Heartbeat:     time.Minute,
		Notifications: notify.NewHub(),
	})
	recorder := postNotificationTest(t, handler, `{"style":"in-app"}`)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusServiceUnavailable)
	}
}

// The native channel spawns its platform command in the background, so the
// context it is delivered with must survive the request it arrived on.
func TestNotificationTestDeliversWithALivingContext(t *testing.T) {
	var delivered context.Context
	native := deliveryFunc(func(ctx context.Context, _ notify.Notification) { delivered = ctx })
	tester := notify.NewTester(nil, native, func() notify.Config {
		return notify.Config{Style: settings.NotifyStyleNative, Sound: "chime"}
	})
	handler := handlerForTest(Deps{
		Version:       "1.2.3",
		Store:         store.NewTestStore(t, store.Seed()),
		Heartbeat:     time.Minute,
		Notifications: notify.NewHub(),
		NotifyTester:  tester,
	})

	// httptest cancels a request's context when the handler returns, the way the
	// server does; the notification must not be cancelled with it.
	ctx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodPost, "/api/notifications/test", strings.NewReader(`{}`)).WithContext(ctx)
	handler.ServeHTTP(httptest.NewRecorder(), request)
	cancel()

	if delivered == nil {
		t.Fatal("no notification was delivered")
	}
	if err := delivered.Err(); err != nil {
		t.Errorf("delivery context is already done (%v); the platform command would be killed mid-notification", err)
	}
}

// deliveryFunc adapts a function to notify.Delivery.
type deliveryFunc func(context.Context, notify.Notification)

func (f deliveryFunc) Deliver(ctx context.Context, n notify.Notification) { f(ctx, n) }
