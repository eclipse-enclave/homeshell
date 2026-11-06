package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/notify"
	"github.com/eclipsesource/homeshell/daemon/internal/provider"
	"github.com/eclipsesource/homeshell/daemon/internal/store"
)

// handlerForTest builds the daemon HTTP surface the way this package's tests need it:
// with the security middlewares' zero-value seams acknowledged, so Handler does not
// refuse an empty APIToken (auth off) or ListenAddr (guard off). Most tests want both
// off — they drive httptest requests, whose default Host is "example.com" and which
// carry no credential — while a few set one or both fields to exercise that middleware.
//
// Every test goes through this rather than calling Handler directly, so Handler itself
// stays strict for production callers.
func handlerForTest(deps Deps) http.Handler {
	deps.unsecuredForTest = true
	return Handler(deps)
}

// testHandler builds the daemon HTTP surface backed by seeded state.
func testHandler(t *testing.T, heartbeat time.Duration) (http.Handler, *store.Store) {
	t.Helper()
	st := store.NewTestStore(t, store.Seed())
	return handlerForTest(Deps{Version: "1.2.3", Store: st, Heartbeat: heartbeat, Notifications: notify.NewHub()}), st
}

// TestHandlerRefusesToServeWithoutItsSecurityInputs pins that neither security control
// can be switched off by accident. Both middlewares pass everything through on a
// zero-value field — requireToken without APIToken, guardLoopback without ListenAddr —
// so Handler refuses to build instead, turning a forgotten field into a startup failure
// rather than an open port nobody notices. Opting out is possible only through
// handlerForTest in this file, and the field it sets is unexported, so no caller outside
// the package can construct an unauthenticated /api at all; that is what matters once
// #26 adds a second call site.
func TestHandlerRefusesToServeWithoutItsSecurityInputs(t *testing.T) {
	tests := []struct {
		name       string
		token      string
		listenAddr string
		wantPanic  string
	}{
		{"no token", "", "127.0.0.1:8420", "APIToken"},
		{"no listen address", "tok", "", "ListenAddr"},
		{"neither", "", "", "APIToken"},
		{"both wired, as production does", "tok", "127.0.0.1:8420", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deps := Deps{
				Version:       "t",
				Store:         store.NewTestStore(t, store.Seed()),
				Heartbeat:     time.Minute,
				Notifications: notify.NewHub(),
				ListenAddr:    tt.listenAddr,
				APIToken:      tt.token,
			}

			panicked := func() (message string) {
				defer func() {
					if recovered := recover(); recovered != nil {
						message = fmt.Sprint(recovered)
					}
				}()
				Handler(deps)
				return ""
			}()

			if tt.wantPanic == "" {
				if panicked != "" {
					t.Errorf("Handler panicked with %q on fully wired Deps", panicked)
				}
				return
			}
			if !strings.Contains(panicked, tt.wantPanic) {
				t.Errorf("panic = %q, want it to name %s", panicked, tt.wantPanic)
			}
		})
	}

	// The tests' own path must stay open, or every other test in this package breaks.
	t.Run("handlerForTest opts out", func(t *testing.T) {
		defer func() {
			if recovered := recover(); recovered != nil {
				t.Errorf("handlerForTest panicked: %v", recovered)
			}
		}()
		handlerForTest(Deps{
			Version:       "t",
			Store:         store.NewTestStore(t, store.Seed()),
			Heartbeat:     time.Minute,
			Notifications: notify.NewHub(),
		})
	})
}

func TestResolvePathRejectsRelative(t *testing.T) {
	_, err := resolvePath("some/relative/path")
	if err == nil {
		t.Error("expected error for relative path")
	}
}

func TestResolvePathAcceptsAbsolute(t *testing.T) {
	got, err := resolvePath("/tmp/test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "/tmp/test" {
		t.Errorf("resolvePath(/tmp/test) = %q, want /tmp/test", got)
	}
}

func TestResolvePathExpandsTilde(t *testing.T) {
	got, err := resolvePath("~/projects")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == "~/projects" || got == "" {
		t.Errorf("resolvePath(~/projects) was not expanded: %q", got)
	}
}

func TestUnknownAPIRouteDoesNotFallThroughToUI(t *testing.T) {
	handler, _ := testHandler(t, time.Minute)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/does-not-exist", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

// A session whose terminal is gone is a state conflict the client can act on
// (start it), so it must reach the client as a 409 — not as the 500 that an
// unclassified provider error becomes, which is what a raw runtime failure (a
// `bridge send` to a bridge that is not there) used to arrive as, with nothing in
// it for the user.
func TestProviderErrorStatuses(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{"gone session", provider.ErrSessionGone, http.StatusConflict},
		{"gone session, wrapped", fmt.Errorf("session %q: %w", "sess-1", provider.ErrSessionGone), http.StatusConflict},
		{"stopped session", provider.ErrSessionStopped, http.StatusConflict},
		{"session not found", provider.ErrSessionNotFound, http.StatusNotFound},
		{"anything else", errors.New("boom"), http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			writeProviderError(recorder, tc.err)
			if recorder.Code != tc.want {
				t.Errorf("status = %d, want %d", recorder.Code, tc.want)
			}
			var body struct{ Error string }
			if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
				t.Fatalf("body %q: %v", recorder.Body.String(), err)
			}
			if body.Error == "" {
				t.Error("a refusal must say why")
			}
		})
	}
}
