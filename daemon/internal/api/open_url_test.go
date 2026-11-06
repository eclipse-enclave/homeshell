package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/notify"
	"github.com/eclipsesource/homeshell/daemon/internal/opener"
	"github.com/eclipsesource/homeshell/daemon/internal/store"
)

// recordingOpener records every URL it was asked to open, and can be made to
// fail, which the shared fakeOpener in workstreams_open_test.go cannot do.
type recordingOpener struct {
	opened []string
	err    error
}

func (f *recordingOpener) Open(_ context.Context, target string) error {
	f.opened = append(f.opened, target)
	return f.err
}

func openURLHandlerForTest(t *testing.T, urlOpener opener.Opener) http.Handler {
	t.Helper()
	return openURLHandlerBound(t, urlOpener, false)
}

func openURLHandlerBound(t *testing.T, urlOpener opener.Opener, publicBind bool) http.Handler {
	t.Helper()
	return handlerForTest(Deps{
		Version:       "1.2.3",
		Store:         store.NewTestStore(t, store.Seed()),
		Heartbeat:     time.Minute,
		Notifications: notify.NewHub(),
		Opener:        urlOpener,
		PublicBind:    publicBind,
	})
}

func postOpenURL(t *testing.T, handler http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/open-url", strings.NewReader(body)))
	return recorder
}

func TestOpenURLHandsHTTPURLsToTheDesktop(t *testing.T) {
	for _, url := range []string{
		"http://127.0.0.1:9999/?folder=/work",
		"https://example.com/ide",
	} {
		t.Run(url, func(t *testing.T) {
			fake := &recordingOpener{}
			recorder := postOpenURL(t, openURLHandlerForTest(t, fake), `{"url":`+jsonString(url)+`}`)

			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d; body: %s", recorder.Code, http.StatusOK, recorder.Body.String())
			}
			if len(fake.opened) != 1 || fake.opened[0] != url {
				t.Errorf("opened = %v, want [%q]", fake.opened, url)
			}
		})
	}
}

// The scheme allow-list is the endpoint's entire security control: the platform
// helpers behind internal/opener will happily open a file manager or launch
// whatever application has registered a scheme, so anything but http/https must
// be refused before it reaches them.
func TestOpenURLRefusesAnythingButHTTP(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"a local file", `{"url":"file:///etc/passwd"}`},
		{"a script URL", `{"url":"javascript:alert(1)"}`},
		{"a registered application scheme", `{"url":"vscode://file/etc/passwd"}`},
		{"a mail client", `{"url":"mailto:someone@example.com"}`},
		{"a bare path", `{"url":"/etc/passwd"}`},
		{"no scheme", `{"url":"example.com"}`},
		{"no host", `{"url":"http://"}`},
		{"empty", `{"url":""}`},
		{"missing field", `{}`},
		{"malformed JSON", `not json`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &recordingOpener{}
			recorder := postOpenURL(t, openURLHandlerForTest(t, fake), tt.body)

			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d; body: %s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
			}
			if len(fake.opened) != 0 {
				t.Errorf("opened %v, want nothing", fake.opened)
			}
		})
	}
}

func TestOpenURLReportsAFailedHandOff(t *testing.T) {
	fake := &recordingOpener{err: errors.New("no desktop opener (xdg-open) on this host")}
	recorder := postOpenURL(t, openURLHandlerForTest(t, fake), `{"url":"https://example.com"}`)

	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusConflict)
	}
	if !strings.Contains(recorder.Body.String(), "xdg-open") {
		t.Errorf("body %q does not carry the reason", recorder.Body.String())
	}
}

func TestOpenURLWithoutAnOpenerIsUnavailable(t *testing.T) {
	recorder := postOpenURL(t, openURLHandlerForTest(t, nil), `{"url":"https://example.com"}`)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusServiceUnavailable)
	}
}

// The browser window opens on the daemon's screen, exactly like the native
// folder chooser. On a public bind that screen belongs to somebody else, so a
// remote client must not be able to raise a window on it.
func TestOpenURLRefusesOnAPublicBind(t *testing.T) {
	urlOpener := &recordingOpener{}
	recorder := postOpenURL(t,
		openURLHandlerBound(t, urlOpener, true),
		`{"url":"http://127.0.0.1:3000"}`)

	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusConflict)
	}
	if len(urlOpener.opened) != 0 {
		t.Errorf("opened %v; want nothing — no window may appear on the daemon's host", urlOpener.opened)
	}
}
