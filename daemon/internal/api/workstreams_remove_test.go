package api

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
	"github.com/eclipsesource/homeshell/daemon/internal/notify"
	"github.com/eclipsesource/homeshell/daemon/internal/provider"
	"github.com/eclipsesource/homeshell/daemon/internal/store"
)

// newRemoveHandler builds a handler over one project holding a single workstream
// whose sessions are in the given states. Sessions are seeded into the store
// because the guard reads session *state*, which the API cannot set directly.
func newRemoveHandler(t *testing.T, states ...domain.SessionState) (http.Handler, *store.Store) {
	t.Helper()
	sessions := make([]domain.Session, 0, len(states))
	for i, state := range states {
		sessions = append(sessions, domain.Session{
			ID:       "sess-" + string(rune('a'+i)),
			Title:    "Session " + string(rune('A'+i)),
			Agent:    "Claude Code",
			Provider: provider.CoreProviderID,
			State:    state,
		})
	}
	st := store.NewTestStore(t, []domain.Project{{
		ID: "proj-test", Name: "test", Path: t.TempDir(),
		Workstreams: []domain.Workstream{{
			ID: "ws-1", Name: "guarded", Provider: provider.CoreProviderID,
			StrategyID: "in-place", WorkingDir: t.TempDir(), Sessions: sessions,
		}},
	}})
	// Deliberately *not* reconciled: a projection pass drops sessions whose
	// owning session provider is not registered, which would take the seeded
	// sessions with it and leave the guard nothing to guard.
	svc := provider.NewService(st, provider.NewRegistry(), 0)
	h := handlerForTest(Deps{
		Version: "test", Store: st, Heartbeat: time.Minute,
		Providers: svc, Notifications: notify.NewHub(),
	})
	return h, st
}

// busyBody is the refusal a client is expected to act on: a machine-readable
// reason plus the sessions themselves, not just prose.
type busyBody struct {
	Error    string `json:"error"`
	Reason   string `json:"reason"`
	Sessions []struct {
		ID    string `json:"id"`
		Title string `json:"title"`
		State string `json:"state"`
	} `json:"sessions"`
}

func TestRemoveWorkstreamWithRunningSessionIsRefused(t *testing.T) {
	h, st := newRemoveHandler(t, domain.SessionIdle, domain.SessionRunning)

	rec := do(t, h, http.MethodDelete, "/api/projects/proj-test/workstreams/ws-1", "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("delete status = %d (%s), want 409", rec.Code, rec.Body.String())
	}
	var body busyBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode refusal: %v", err)
	}
	if body.Reason != ReasonSessionsRunning {
		t.Errorf("reason = %q, want %q", body.Reason, ReasonSessionsRunning)
	}
	if len(body.Sessions) != 1 {
		t.Fatalf("refusal lists %d sessions, want only the busy one", len(body.Sessions))
	}
	if body.Sessions[0].ID != "sess-b" || body.Sessions[0].State != "running" {
		t.Errorf("refusal lists %+v, want the running sess-b", body.Sessions[0])
	}
	if body.Error == "" {
		t.Error("refusal carries no human-readable message")
	}

	// And nothing was torn down.
	if _, err := st.Workstream("proj-test", "ws-1"); err != nil {
		t.Errorf("workstream is gone after a refused delete: %v", err)
	}
}

// The guard is the daemon's, so it holds for a caller that never sees the UI —
// and force is the only way past it.
func TestRemoveWorkstreamForce(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		body       string
		wantStatus int
	}{
		{
			name:       "query parameter",
			path:       "/api/projects/proj-test/workstreams/ws-1?force=true",
			wantStatus: http.StatusNoContent,
		},
		{
			name:       "body field",
			path:       "/api/projects/proj-test/workstreams/ws-1",
			body:       `{"force":true}`,
			wantStatus: http.StatusNoContent,
		},
		{
			name:       "body field beside keepFiles",
			path:       "/api/projects/proj-test/workstreams/ws-1",
			body:       `{"keepFiles":true,"force":true}`,
			wantStatus: http.StatusNoContent,
		},
		{
			name:       "force=false is still guarded",
			path:       "/api/projects/proj-test/workstreams/ws-1?force=false",
			wantStatus: http.StatusConflict,
		},
		{
			name:       "a non-boolean force value does not disarm the guard",
			path:       "/api/projects/proj-test/workstreams/ws-1?force=yes",
			wantStatus: http.StatusConflict,
		},
		{
			name:       "an empty body is still guarded",
			path:       "/api/projects/proj-test/workstreams/ws-1",
			body:       `{}`,
			wantStatus: http.StatusConflict,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, st := newRemoveHandler(t, domain.SessionRunning)
			rec := do(t, h, http.MethodDelete, tc.path, tc.body)
			if rec.Code != tc.wantStatus {
				t.Fatalf("delete status = %d (%s), want %d", rec.Code, rec.Body.String(), tc.wantStatus)
			}
			_, err := st.Workstream("proj-test", "ws-1")
			if tc.wantStatus == http.StatusNoContent && err == nil {
				t.Error("workstream survived a forced delete")
			}
			if tc.wantStatus == http.StatusConflict && err != nil {
				t.Errorf("workstream is gone after a refused delete: %v", err)
			}
		})
	}
}

// A workstream with nothing busy needs no force: the guard must not turn every
// removal into a two-step.
func TestRemoveQuiescentWorkstreamNeedsNoForce(t *testing.T) {
	for _, state := range []domain.SessionState{domain.SessionIdle, domain.SessionDone, domain.SessionStopped} {
		t.Run(string(state), func(t *testing.T) {
			h, st := newRemoveHandler(t, state)
			rec := do(t, h, http.MethodDelete, "/api/projects/proj-test/workstreams/ws-1", "")
			if rec.Code != http.StatusNoContent {
				t.Fatalf("delete status = %d (%s), want 204", rec.Code, rec.Body.String())
			}
			if _, err := st.Workstream("proj-test", "ws-1"); err == nil {
				t.Error("workstream survived its removal")
			}
		})
	}
}
