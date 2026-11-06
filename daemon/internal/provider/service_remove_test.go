package provider_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
	"github.com/eclipsesource/homeshell/daemon/internal/provider"
	"github.com/eclipsesource/homeshell/daemon/internal/provider/fake"
	"github.com/eclipsesource/homeshell/daemon/internal/store"
)

// serviceWithSessions wires a service over one project holding a single "fake"
// workstream whose sessions are in the given states. The sessions are placed in
// the store directly: the guard under test reads session *state*, and the fake's
// own creation path only ever yields idle sessions, so states like waiting are
// not otherwise reachable in a unit test.
func serviceWithSessions(t *testing.T, states ...domain.SessionState) (*provider.Service, *store.Store) {
	t.Helper()
	sessions := make([]domain.Session, 0, len(states))
	for i, state := range states {
		sessions = append(sessions, domain.Session{
			ID:       string(rune('a'+i)) + "-sess",
			Title:    "Session " + string(rune('A'+i)),
			Agent:    "Claude Code",
			Provider: "fake",
			State:    state,
		})
	}
	st := store.NewTestStore(t, []domain.Project{{
		ID: "p1", Name: "p1", Path: t.TempDir(),
		Workstreams: []domain.Workstream{{
			ID: "ws-1", Name: "guarded", Provider: "fake",
			StrategyID: "in-place", WorkingDir: t.TempDir(), Sessions: sessions,
		}},
	}})
	reg := provider.NewRegistry()
	registerFake(t, reg, fake.Config{})
	return provider.NewService(st, reg, 0), st
}

// The guard is the daemon's, so it is expressed once over every state
// combination rather than trusted to a client. Busy = running or waiting
// (domain.SessionState.Busy); everything else is removable without ceremony.
func TestServiceRemoveGuardsBusySessions(t *testing.T) {
	ref := provider.WorkstreamRef{ProjectID: "p1", WorkstreamID: "ws-1"}
	tests := []struct {
		name     string
		states   []domain.SessionState
		force    bool
		wantBusy []string // titles named in the refusal; empty means "removal allowed"
	}{
		{name: "no sessions"},
		{name: "idle only", states: []domain.SessionState{domain.SessionIdle}},
		{name: "finished and stopped", states: []domain.SessionState{domain.SessionDone, domain.SessionStopped}},
		{
			name:     "one running",
			states:   []domain.SessionState{domain.SessionRunning},
			wantBusy: []string{"Session A"},
		},
		{
			name:     "one waiting for input",
			states:   []domain.SessionState{domain.SessionWaiting},
			wantBusy: []string{"Session A"},
		},
		{
			name:     "running beside idle names only the running one",
			states:   []domain.SessionState{domain.SessionIdle, domain.SessionRunning},
			wantBusy: []string{"Session B"},
		},
		{
			name:     "several busy are all named",
			states:   []domain.SessionState{domain.SessionRunning, domain.SessionWaiting},
			wantBusy: []string{"Session A", "Session B"},
		},
		{
			name:   "force removes despite a running session",
			states: []domain.SessionState{domain.SessionRunning},
			force:  true,
		},
		{
			name:   "force removes despite several busy sessions",
			states: []domain.SessionState{domain.SessionRunning, domain.SessionWaiting},
			force:  true,
		},
		// A session the daemon lost the signal for is not busy: "unknown" is not
		// evidence of live work, and refusing on it would make a workstream
		// unremovable whenever a capture fails.
		{name: "unknown is not busy", states: []domain.SessionState{domain.SessionUnknown}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, st := serviceWithSessions(t, tc.states...)
			err := svc.Remove(context.Background(), ref, provider.RemoveOptions{Force: tc.force})

			if len(tc.wantBusy) == 0 {
				if err != nil {
					t.Fatalf("Remove = %v; want the removal to go through", err)
				}
				if _, err := st.Workstream("p1", "ws-1"); err == nil {
					t.Error("workstream is still in the store after a successful removal")
				}
				return
			}

			if !errors.Is(err, provider.ErrWorkstreamBusy) {
				t.Fatalf("Remove = %v; want ErrWorkstreamBusy", err)
			}
			var gotTitles []string
			for _, session := range provider.BusySessions(err) {
				gotTitles = append(gotTitles, session.Title)
			}
			if !slices.Equal(gotTitles, tc.wantBusy) {
				t.Errorf("refusal names %v; want %v", gotTitles, tc.wantBusy)
			}
			// The message is what a direct API caller reads, so it names them too.
			for _, want := range tc.wantBusy {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("refusal message %q does not name %q", err.Error(), want)
				}
			}
			if _, stillThere := st.Workstream("p1", "ws-1"); stillThere != nil {
				t.Error("workstream disappeared even though the removal was refused")
			}
		})
	}
}

// A refusal must change nothing: the workstream and every one of its sessions
// are still there afterwards, so a rejected click cannot half-tear-down a
// workstream.
func TestServiceRemoveRefusalLeavesEverythingIntact(t *testing.T) {
	svc, st := serviceWithSessions(t, domain.SessionRunning, domain.SessionIdle)
	ref := provider.WorkstreamRef{ProjectID: "p1", WorkstreamID: "ws-1"}

	before, err := st.Workstream("p1", "ws-1")
	if err != nil {
		t.Fatalf("workstream before: %v", err)
	}
	if err := svc.Remove(context.Background(), ref, provider.RemoveOptions{}); !errors.Is(err, provider.ErrWorkstreamBusy) {
		t.Fatalf("Remove = %v; want ErrWorkstreamBusy", err)
	}

	after, err := st.Workstream("p1", "ws-1")
	if err != nil {
		t.Fatalf("workstream after a refused removal: %v", err)
	}
	if len(after.Sessions) != len(before.Sessions) {
		t.Errorf("sessions after refusal = %d, want %d", len(after.Sessions), len(before.Sessions))
	}
	for i := range after.Sessions {
		if after.Sessions[i].State != before.Sessions[i].State {
			t.Errorf("session %q state = %q, want %q (a refusal must not touch sessions)",
				after.Sessions[i].ID, after.Sessions[i].State, before.Sessions[i].State)
		}
	}
}

// serviceWithFake is newService plus a handle on the fake provider, so a test
// can ask the *provider* what it still owns rather than only reading the store.
func serviceWithFake(t *testing.T) (*provider.Service, *store.Store, *fake.Provider) {
	t.Helper()
	st := store.NewTestStore(t, []domain.Project{{
		ID: "p1", Name: "p1", Path: t.TempDir(), Workstreams: []domain.Workstream{},
	}})
	reg := provider.NewRegistry()
	fp := fake.New(fake.Config{})
	if err := reg.RegisterWorkstreamProvider(fp); err != nil {
		t.Fatalf("register fake workstream provider: %v", err)
	}
	if err := reg.RegisterSessionProvider(fp); err != nil {
		t.Fatalf("register fake session provider: %v", err)
	}
	svc := provider.NewService(st, reg, 0)
	svc.Reconcile(context.Background())
	return svc, st, fp
}

// The force path must really tear the sessions down through their owning
// provider, not just drop the store record — so this drives the whole thing
// through the fake's own create/start path rather than a hand-built store, and
// asks the provider afterwards.
func TestServiceRemoveForceTearsDownRunningSessions(t *testing.T) {
	svc, st, fp := serviceWithFake(t)
	wref := createWS(t, svc, st)
	if _, err := svc.CreateSession(context.Background(), provider.CreateSessionInput{
		ProjectID: "p1", WorkstreamID: wref.WorkstreamID, ProviderID: "fake", OptionID: "claude",
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	sess := awaitSession(t, st, "p1", wref.WorkstreamID, "Claude Code")
	sref := provider.SessionRef{ProjectID: "p1", WorkstreamID: wref.WorkstreamID, SessionID: sess.ID}

	// The fake creates sessions idle; stop→start lands it in running, the state
	// the guard cares about.
	if err := svc.StopSession(context.Background(), sref); err != nil {
		t.Fatalf("StopSession: %v", err)
	}
	if err := svc.StartSession(context.Background(), sref); err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	if got := st.Projects()[0].Workstreams[0].Sessions[0].State; got != domain.SessionRunning {
		t.Fatalf("session state = %q; want running before the removal is attempted", got)
	}

	if err := svc.Remove(context.Background(), wref, provider.RemoveOptions{}); !errors.Is(err, provider.ErrWorkstreamBusy) {
		t.Fatalf("unforced Remove = %v; want ErrWorkstreamBusy", err)
	}
	if err := svc.Remove(context.Background(), wref, provider.RemoveOptions{Force: true}); err != nil {
		t.Fatalf("forced Remove: %v", err)
	}
	if _, err := st.Workstream("p1", wref.WorkstreamID); err == nil {
		t.Error("workstream survived a forced removal")
	}
	// The session went down with it via its owning provider: the fake forgets a
	// session it removed, so asking it for the state now fails.
	if _, err := fp.SessionState(context.Background(), sref); err == nil {
		t.Error("the provider still knows the session after a forced removal")
	}
}

// A creation placeholder has no sessions to guard, and its removal is the user's
// way to clear a stuck create — so it must not be caught by the guard.
func TestServiceRemovePlaceholderIsNotGuarded(t *testing.T) {
	svc, st := newService(t)
	ws, err := svc.Create(context.Background(), provider.CreateInput{
		ProjectID: "p1", ProviderID: "fake", Name: "pending", Config: []byte(`{"branch":"main"}`),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	ref := provider.WorkstreamRef{ProjectID: "p1", WorkstreamID: ws.ID}
	if err := svc.Remove(context.Background(), ref, provider.RemoveOptions{}); err != nil {
		t.Fatalf("Remove placeholder: %v", err)
	}
	if _, err := st.Workstream("p1", ws.ID); err == nil {
		t.Error("placeholder survived removal")
	}
}
