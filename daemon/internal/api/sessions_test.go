package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
	"github.com/eclipsesource/homeshell/daemon/internal/notify"
	"github.com/eclipsesource/homeshell/daemon/internal/provider"
	"github.com/eclipsesource/homeshell/daemon/internal/provider/fake"
	"github.com/eclipsesource/homeshell/daemon/internal/store"
)

// newProviderHandlerCap builds a handler whose fake provider declares a
// concurrency cap over one empty project.
func newProviderHandlerCap(t *testing.T, cap int) (http.Handler, *store.Store) {
	t.Helper()
	st := store.NewTestStore(t, []domain.Project{{
		ID: "proj-test", Name: "test", Path: t.TempDir(), GitBacked: true,
		Workstreams: []domain.Workstream{},
	}})
	reg := provider.NewRegistry()
	registerFakeBothRoles(t, reg, fake.Config{MaxConcurrentSessions: cap})
	svc := provider.NewService(st, reg, 0)
	svc.Reconcile(context.Background())
	return handlerForTest(Deps{Version: "test", Store: st, Heartbeat: time.Minute, Providers: svc, Notifications: notify.NewHub()}), st
}

func createTestSession(t *testing.T, h http.Handler, st *store.Store, wsID string) domain.Session {
	t.Helper()
	rec := do(t, h, http.MethodPost, "/api/projects/proj-test/workstreams/"+wsID+"/sessions",
		`{"providerId":"fake","optionId":"claude","config":{"model":"opus"}}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("create session status = %d (%s), want 202", rec.Code, rec.Body.String())
	}
	// Wait for async creation to finish.
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for session creation")
		default:
		}
		time.Sleep(10 * time.Millisecond)
		ws, err := st.Workstream("proj-test", wsID)
		if err != nil {
			continue
		}
		for _, s := range ws.Sessions {
			if s.Agent == "Claude Code" {
				return s
			}
		}
	}
}

func TestSessionSchemasEndpoint(t *testing.T) {
	h, _ := newProviderHandler(t)
	ws := createTestWorkstream(t, h)
	rec := do(t, h, http.MethodGet, "/api/projects/proj-test/workstreams/"+ws.ID+"/session-schemas", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	var groups []provider.SessionProviderSchemas
	if err := json.Unmarshal(rec.Body.Bytes(), &groups); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(groups) != 1 || groups[0].ProviderID != "fake" {
		t.Errorf("groups = %+v, want one entry for fake", groups)
	}
	// The endpoint serves what the provider offers, in its order — asserted as a
	// prefix so adding an agent to the fake does not fail this.
	if len(groups[0].Options) < 2 || groups[0].Options[0].ID != "claude" || groups[0].Options[1].ID != "codex" {
		t.Errorf("options = %+v, want claude and codex first", groups[0].Options)
	}
}

func TestCreateSessionEndpoint(t *testing.T) {
	h, st := newProviderHandler(t)
	ws := createTestWorkstream(t, h)
	sess := createTestSession(t, h, st, ws.ID)
	if sess.Agent != "Claude Code" {
		t.Errorf("agent = %q, want Claude Code", sess.Agent)
	}
	sessions := st.Projects()[0].Workstreams[0].Sessions
	if len(sessions) != 1 || sessions[0].ID != sess.ID {
		t.Errorf("store sessions = %+v; want the created one", sessions)
	}
}

// The 202 carries the placeholder standing for the session, so a client can
// find the row it asked for before the real session replaces it.
func TestCreateSessionReturnsThePlaceholder(t *testing.T) {
	handler, _ := newProviderHandler(t)
	workstream := createTestWorkstream(t, handler)
	recorder := do(t, handler, http.MethodPost, "/api/projects/proj-test/workstreams/"+workstream.ID+"/sessions",
		`{"providerId":"fake","optionId":"claude"}`)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d (%s), want 202", recorder.Code, recorder.Body.String())
	}
	var placeholder domain.Session
	if err := json.Unmarshal(recorder.Body.Bytes(), &placeholder); err != nil {
		t.Fatalf("decode: %v (%s)", err, recorder.Body.String())
	}
	if !provider.IsStartingPlaceholder(placeholder) {
		t.Errorf("session = %+v, want the starting placeholder", placeholder)
	}
	if placeholder.State != domain.SessionStarting {
		t.Errorf("state = %q, want %q", placeholder.State, domain.SessionStarting)
	}
}

func TestCreateSessionValidationError(t *testing.T) {
	h, _ := newProviderHandler(t)
	ws := createTestWorkstream(t, h)
	rec := do(t, h, http.MethodPost, "/api/projects/proj-test/workstreams/"+ws.ID+"/sessions",
		`{"providerId":"fake","optionId":"claude","config":{"model":"nope"}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
	var body struct {
		Fields map[string]string `json:"fields"`
	}
	json.Unmarshal(rec.Body.Bytes(), &body)
	if len(body.Fields) == 0 {
		t.Errorf("no field-level detail in %s", rec.Body.String())
	}
}

func TestCreateSessionConcurrencyReturns409(t *testing.T) {
	h, st := newProviderHandlerCap(t, 1)
	ws := createTestWorkstream(t, h)
	createTestSession(t, h, st, ws.ID)
	rec := do(t, h, http.MethodPost, "/api/projects/proj-test/workstreams/"+ws.ID+"/sessions",
		`{"providerId":"fake","optionId":"claude"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("second create status = %d, want 409 (%s)", rec.Code, rec.Body.String())
	}
}

func TestSessionStopStartRemoveLifecycle(t *testing.T) {
	h, st := newProviderHandler(t)
	ws := createTestWorkstream(t, h)
	sess := createTestSession(t, h, st, ws.ID)
	base := "/api/projects/proj-test/workstreams/" + ws.ID + "/sessions/" + sess.ID

	// Start → running.
	if rec := do(t, h, http.MethodPost, base+"/start", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("start status = %d (%s)", rec.Code, rec.Body.String())
	}
	if got := st.Projects()[0].Workstreams[0].Sessions[0].State; got != domain.SessionRunning {
		t.Errorf("after start = %q, want running", got)
	}
	// Stop → stopped.
	if rec := do(t, h, http.MethodPost, base+"/stop", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("stop status = %d (%s)", rec.Code, rec.Body.String())
	}
	if got := st.Projects()[0].Workstreams[0].Sessions[0].State; got != domain.SessionStopped {
		t.Errorf("after stop = %q, want stopped", got)
	}
	// Remove.
	if rec := do(t, h, http.MethodDelete, base, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d (%s)", rec.Code, rec.Body.String())
	}
	if n := len(st.Projects()[0].Workstreams[0].Sessions); n != 0 {
		t.Errorf("sessions after remove = %d, want 0", n)
	}
}

func TestSessionPromptEndpoint(t *testing.T) {
	h, st := newProviderHandler(t)
	ws := createTestWorkstream(t, h)
	sess := createTestSession(t, h, st, ws.ID)
	base := "/api/projects/proj-test/workstreams/" + ws.ID + "/sessions/" + sess.ID + "/prompt"

	// Valid body → 204 (the fake, a Prompter, echoes it into the preview).
	if rec := do(t, h, http.MethodPost, base, `{"text":"run tests"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("prompt status = %d (%s), want 204", rec.Code, rec.Body.String())
	}
	// Whitespace-only text → 400.
	if rec := do(t, h, http.MethodPost, base, `{"text":"   "}`); rec.Code != http.StatusBadRequest {
		t.Errorf("whitespace prompt status = %d, want 400", rec.Code)
	}
	// Invalid JSON body → 400.
	if rec := do(t, h, http.MethodPost, base, `not json`); rec.Code != http.StatusBadRequest {
		t.Errorf("invalid JSON status = %d, want 400", rec.Code)
	}
}

func TestSessionPromptOversizedReturns413(t *testing.T) {
	h, st := newProviderHandler(t)
	ws := createTestWorkstream(t, h)
	sess := createTestSession(t, h, st, ws.ID)
	base := "/api/projects/proj-test/workstreams/" + ws.ID + "/sessions/" + sess.ID + "/prompt"

	// A body past the size cap is rejected as too-large, not as invalid JSON.
	body := `{"text":"` + strings.Repeat("x", 128*1024) + `"}`
	if rec := do(t, h, http.MethodPost, base, body); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized prompt status = %d, want 413 (%s)", rec.Code, rec.Body.String())
	}
}

func TestSessionPromptStoppedReturns409(t *testing.T) {
	h, st := newProviderHandler(t)
	ws := createTestWorkstream(t, h)
	sess := createTestSession(t, h, st, ws.ID)
	base := "/api/projects/proj-test/workstreams/" + ws.ID + "/sessions/" + sess.ID

	// Stop the session, then a prompt is a state conflict, not a 500.
	if rec := do(t, h, http.MethodPost, base+"/stop", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("stop status = %d (%s)", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, http.MethodPost, base+"/prompt", `{"text":"hi"}`); rec.Code != http.StatusConflict {
		t.Fatalf("prompt on stopped session = %d, want 409 (%s)", rec.Code, rec.Body.String())
	}
}

func TestSessionKeyEndpoint(t *testing.T) {
	h, st := newProviderHandler(t)
	ws := createTestWorkstream(t, h)
	sess := createTestSession(t, h, st, ws.ID)
	base := "/api/projects/proj-test/workstreams/" + ws.ID + "/sessions/" + sess.ID + "/key"

	// Valid key → 204 (the fake echoes the key into the preview).
	if rec := do(t, h, http.MethodPost, base, `{"key":"down"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("key status = %d (%s), want 204", rec.Code, rec.Body.String())
	}
	// Unknown key → 400.
	if rec := do(t, h, http.MethodPost, base, `{"key":"ctrl-c"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown key status = %d, want 400", rec.Code)
	}
	// Invalid JSON → 400.
	if rec := do(t, h, http.MethodPost, base, `nope`); rec.Code != http.StatusBadRequest {
		t.Errorf("invalid JSON status = %d, want 400", rec.Code)
	}
}

func TestSessionKeyStoppedReturns409(t *testing.T) {
	h, st := newProviderHandler(t)
	ws := createTestWorkstream(t, h)
	sess := createTestSession(t, h, st, ws.ID)
	base := "/api/projects/proj-test/workstreams/" + ws.ID + "/sessions/" + sess.ID

	if rec := do(t, h, http.MethodPost, base+"/stop", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("stop status = %d (%s)", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, http.MethodPost, base+"/key", `{"key":"enter"}`); rec.Code != http.StatusConflict {
		t.Fatalf("key on stopped session = %d, want 409 (%s)", rec.Code, rec.Body.String())
	}
}

func TestSessionEndpointsUnwiredReturn503(t *testing.T) {
	st := store.NewTestStore(t, []domain.Project{{ID: "p", Path: "/p", Workstreams: []domain.Workstream{}}})
	h := handlerForTest(Deps{Store: st, Heartbeat: time.Minute, Notifications: notify.NewHub()}) // no Providers
	for _, path := range []string{
		"/api/projects/p/workstreams/w/sessions",
		"/api/projects/p/workstreams/w/sessions/s/stop",
	} {
		if rec := do(t, h, http.MethodPost, path, `{}`); rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s status = %d, want 503", path, rec.Code)
		}
	}
}
