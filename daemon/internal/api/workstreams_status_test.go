package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
	"github.com/eclipsesource/homeshell/daemon/internal/notify"
	"github.com/eclipsesource/homeshell/daemon/internal/store"
)

// statusHandler builds a handler over one project holding one workstream that
// already carries a status message — the state a failed session creation leaves
// behind.
func statusHandler(t *testing.T, message string) (http.Handler, *store.Store) {
	t.Helper()
	st := store.NewTestStore(t, []domain.Project{{
		ID: "p1", Name: "p1", Path: t.TempDir(),
		Workstreams: []domain.Workstream{{
			ID: "ws-1", Name: "ws", Provider: "core", StrategyID: "in-place",
		}},
	}})
	if err := st.SetStatusMessage("p1", "ws-1", message); err != nil {
		t.Fatalf("seeding the status message: %v", err)
	}
	return handlerForTest(Deps{
		Version: "test", Store: st, Heartbeat: time.Minute, Notifications: notify.NewHub(),
	}), st
}

// statusMessageOf reads the message back out of the projection the clients see,
// rather than out of the store's Go value — this is what the UI renders from.
func statusMessageOf(t *testing.T, handler http.Handler) string {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/projects", nil))
	var payload struct {
		Projects []domain.Project `json:"projects"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decoding /api/projects: %v", err)
	}
	for _, project := range payload.Projects {
		for _, ws := range project.Workstreams {
			if ws.ID == "ws-1" {
				return ws.StatusMessage
			}
		}
	}
	t.Fatal("ws-1 missing from the projection")
	return ""
}

// A provider's failure stays on the workstream until something clears it; this is
// the something.
func TestClearWorkstreamStatusRemovesTheMessage(t *testing.T) {
	const message = "start enclave container: enclave run: exit status 1"
	handler, _ := statusHandler(t, message)

	if got := statusMessageOf(t, handler); got != message {
		t.Fatalf("statusMessage before = %q, want the seeded failure", got)
	}

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(
		http.MethodDelete, "/api/projects/p1/workstreams/ws-1/status", nil))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d; body: %s",
			recorder.Code, http.StatusNoContent, recorder.Body.String())
	}
	if got := statusMessageOf(t, handler); got != "" {
		t.Errorf("statusMessage after = %q, want it gone", got)
	}
}

// Clearing an already-clear message is not an error: two clients dismissing the
// same message must both succeed.
func TestClearWorkstreamStatusIsIdempotent(t *testing.T) {
	handler, _ := statusHandler(t, "something happened")

	for i := 0; i < 2; i++ {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(
			http.MethodDelete, "/api/projects/p1/workstreams/ws-1/status", nil))
		if recorder.Code != http.StatusNoContent {
			t.Fatalf("attempt %d: status = %d, want %d", i+1, recorder.Code, http.StatusNoContent)
		}
	}
}

// A failed creation's message is the only record of why the workstream is red, and
// nothing will ever write another one — so it is not the user's to dismiss. The
// disposal for a failed workstream is removing it, not blanking its explanation.
func TestClearWorkstreamStatusRefusesAnErroredWorkstream(t *testing.T) {
	const message = `branch "main" is checked out in another worktree`
	st := store.NewTestStore(t, []domain.Project{{
		ID: "p1", Name: "p1", Path: t.TempDir(),
		Workstreams: []domain.Workstream{{
			ID: "creating-1", Name: "dup-branch", Provider: "core",
			RuntimeState: domain.RuntimeError, StatusMessage: message,
		}},
	}})
	handler := handlerForTest(Deps{
		Version: "test", Store: st, Heartbeat: time.Minute, Notifications: notify.NewHub(),
	})

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(
		http.MethodDelete, "/api/projects/p1/workstreams/creating-1/status", nil))
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d; body: %s",
			recorder.Code, http.StatusConflict, recorder.Body.String())
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil || body.Error == "" {
		t.Errorf("body = %q, want a JSON {error}", recorder.Body.String())
	}

	// The refusal has to be a no-op, not a partial one: the message the user was
	// trying to be rid of is exactly what must survive.
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/projects", nil))
	var payload struct {
		Projects []domain.Project `json:"projects"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decoding /api/projects: %v", err)
	}
	got := payload.Projects[0].Workstreams[0].StatusMessage
	if got != message {
		t.Errorf("statusMessage after the refusal = %q, want it untouched (%q)", got, message)
	}
}

// An unknown workstream is a 404 in the daemon's JSON shape, not a silent success
// — a client clearing the wrong thing should hear about it.
func TestClearWorkstreamStatusUnknownTargets(t *testing.T) {
	handler, _ := statusHandler(t, "message")

	for _, path := range []string{
		"/api/projects/p1/workstreams/nope/status",
		"/api/projects/nope/workstreams/ws-1/status",
	} {
		t.Run(path, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodDelete, path, nil))
			if recorder.Code != http.StatusNotFound {
				t.Errorf("status = %d, want %d", recorder.Code, http.StatusNotFound)
			}
			var body struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil || body.Error == "" {
				t.Errorf("body = %q, want a JSON {error}", recorder.Body.String())
			}
		})
	}
}

// The clear has to reach connected clients, or one browser dismisses it and every
// other keeps showing it until the next unrelated push.
func TestClearWorkstreamStatusBroadcasts(t *testing.T) {
	handler, st := statusHandler(t, "message")

	events, cancel := st.Subscribe()
	defer cancel()

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(
		http.MethodDelete, "/api/projects/p1/workstreams/ws-1/status", nil))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNoContent)
	}

	select {
	case event := <-events:
		if event.Type != "state" {
			t.Errorf("event type = %q, want state", event.Type)
		}
	case <-time.After(2 * time.Second):
		t.Error("no state event after clearing the status message")
	}
}
