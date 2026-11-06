package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
	"github.com/eclipsesource/homeshell/daemon/internal/provider"
)

// sessionPath is the collection path a session's own endpoints hang off.
func sessionPath(wsID, sessionID string) string {
	return "/api/projects/proj-test/workstreams/" + wsID + "/sessions/" + sessionID
}

// servedTitle reads one session's title out of the state clients actually see.
func servedTitle(t *testing.T, h http.Handler, wsID, sessionID string) string {
	t.Helper()
	rec := do(t, h, http.MethodGet, "/api/projects", "")
	var body struct {
		Projects []domain.Project `json:"projects"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding /api/projects: %v", err)
	}
	for _, project := range body.Projects {
		for _, ws := range project.Workstreams {
			if ws.ID != wsID {
				continue
			}
			for _, sess := range ws.Sessions {
				if sess.ID == sessionID {
					return sess.Title
				}
			}
		}
	}
	t.Fatalf("session %q is not in the served state", sessionID)
	return ""
}

func TestRenameSessionEndpoint(t *testing.T) {
	h, st := newProviderHandlerCap(t, 0)
	ws := createTestWorkstream(t, h)
	sess := createTestSession(t, h, st, ws.ID)

	rec := do(t, h, http.MethodPatch, sessionPath(ws.ID, sess.ID), `{"title":"Port the CLI"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("PATCH status = %d (%s), want 204", rec.Code, rec.Body.String())
	}
	if got := servedTitle(t, h, ws.ID, sess.ID); got != "Port the CLI" {
		t.Errorf("title = %q; want %q", got, "Port the CLI")
	}
}

func TestRenameSessionEndpointRejectsEmptyTitle(t *testing.T) {
	h, st := newProviderHandlerCap(t, 0)
	ws := createTestWorkstream(t, h)
	sess := createTestSession(t, h, st, ws.ID)
	before := servedTitle(t, h, ws.ID, sess.ID)

	// An empty title is a 400, not a reset: DELETE …/title is how a client asks
	// for the provider's own name back, and a client that submitted an empty form
	// should hear about it.
	rec := do(t, h, http.MethodPatch, sessionPath(ws.ID, sess.ID), `{"title":"   "}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("PATCH with a blank title = %d (%s), want 400", rec.Code, rec.Body.String())
	}
	if got := servedTitle(t, h, ws.ID, sess.ID); got != before {
		t.Errorf("title = %q; want the original %q untouched", got, before)
	}
}

func TestRenameSessionEndpointRejectsMalformedBody(t *testing.T) {
	h, st := newProviderHandlerCap(t, 0)
	ws := createTestWorkstream(t, h)
	sess := createTestSession(t, h, st, ws.ID)

	rec := do(t, h, http.MethodPatch, sessionPath(ws.ID, sess.ID), `{"title":`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("PATCH with malformed JSON = %d (%s), want 400", rec.Code, rec.Body.String())
	}
}

// An oversized body is too long, not malformed. Reading exactly the limit cuts
// the JSON mid-string, so the client would be told "invalid JSON" about a title
// whose only problem was its length — the trap promptHandler reads limit+1 to
// avoid.
func TestRenameSessionEndpointRejectsAnOversizedTitle(t *testing.T) {
	h, st := newProviderHandlerCap(t, 0)
	ws := createTestWorkstream(t, h)
	sess := createTestSession(t, h, st, ws.ID)
	before := servedTitle(t, h, ws.ID, sess.ID)

	body, err := json.Marshal(map[string]string{"title": strings.Repeat("a", maxTitleBytes)})
	if err != nil {
		t.Fatalf("building the oversized body: %v", err)
	}
	rec := do(t, h, http.MethodPatch, sessionPath(ws.ID, sess.ID), string(body))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("PATCH with an oversized title = %d (%s), want 413", rec.Code, rec.Body.String())
	}
	if got := servedTitle(t, h, ws.ID, sess.ID); got != before {
		t.Errorf("title = %q; want the original %q untouched", got, before)
	}
}

// A placeholder has no session behind it to name, which the service refuses with
// ErrSessionStarting. That is a conflict with the row's current state, not a
// server fault: unmapped it would surface as a 500 and an ERROR log line for a
// refusal the daemon makes on purpose.
func TestRenameSessionEndpointReportsAStartingPlaceholderAsConflict(t *testing.T) {
	h, st := newProviderHandlerCap(t, 0)
	ws := createTestWorkstream(t, h)
	placeholder := domain.Session{
		ID:    provider.StartingIDPrefix + "abc",
		Title: "Claude Code session",
		Agent: "Claude Code",
		State: domain.SessionStarting,
	}
	if err := st.AddEphemeralSession("proj-test", ws.ID, placeholder); err != nil {
		t.Fatalf("seeding the placeholder: %v", err)
	}

	rec := do(t, h, http.MethodPatch, sessionPath(ws.ID, placeholder.ID), `{"title":"Anything"}`)
	if rec.Code != http.StatusConflict {
		t.Errorf("PATCH of a starting placeholder = %d (%s), want 409", rec.Code, rec.Body.String())
	}
}

func TestRenameSessionEndpointUnknownTargets(t *testing.T) {
	h, st := newProviderHandlerCap(t, 0)
	ws := createTestWorkstream(t, h)
	sess := createTestSession(t, h, st, ws.ID)

	tests := []struct {
		name string
		path string
	}{
		{"unknown session", sessionPath(ws.ID, "ghost")},
		{"unknown workstream", "/api/projects/proj-test/workstreams/ghost/sessions/" + sess.ID},
		{"unknown project", "/api/projects/ghost/workstreams/" + ws.ID + "/sessions/" + sess.ID},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rec := do(t, h, http.MethodPatch, test.path, `{"title":"x"}`)
			if rec.Code != http.StatusNotFound {
				t.Errorf("PATCH status = %d (%s), want 404", rec.Code, rec.Body.String())
			}
			rec = do(t, h, http.MethodDelete, test.path+"/title", "")
			if rec.Code != http.StatusNotFound {
				t.Errorf("DELETE title status = %d (%s), want 404", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestResetSessionTitleEndpoint(t *testing.T) {
	h, st := newProviderHandlerCap(t, 0)
	ws := createTestWorkstream(t, h)
	sess := createTestSession(t, h, st, ws.ID)

	if rec := do(t, h, http.MethodPatch, sessionPath(ws.ID, sess.ID), `{"title":"mine"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("PATCH status = %d, want 204", rec.Code)
	}
	rec := do(t, h, http.MethodDelete, sessionPath(ws.ID, sess.ID)+"/title", "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE title status = %d (%s), want 204", rec.Code, rec.Body.String())
	}
	// Idempotent: two clients clearing the same name must both succeed.
	rec = do(t, h, http.MethodDelete, sessionPath(ws.ID, sess.ID)+"/title", "")
	if rec.Code != http.StatusNoContent {
		t.Errorf("second DELETE title status = %d (%s), want 204", rec.Code, rec.Body.String())
	}
}

// The rename endpoint must not shadow the removal one: they share a path and
// differ only by method.
func TestRemoveSessionStillWorksAlongsideRename(t *testing.T) {
	h, st := newProviderHandlerCap(t, 0)
	ws := createTestWorkstream(t, h)
	sess := createTestSession(t, h, st, ws.ID)

	if rec := do(t, h, http.MethodPatch, sessionPath(ws.ID, sess.ID), `{"title":"doomed"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("PATCH status = %d, want 204", rec.Code)
	}
	if rec := do(t, h, http.MethodDelete, sessionPath(ws.ID, sess.ID), ""); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE session status = %d (%s), want 204", rec.Code, rec.Body.String())
	}
	stored, err := st.Workstream("proj-test", ws.ID)
	if err != nil {
		t.Fatalf("Workstream: %v", err)
	}
	for _, s := range stored.Sessions {
		if s.ID == sess.ID {
			t.Error("the session survived its DELETE")
		}
	}
}
