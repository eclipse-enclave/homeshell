package provider

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
	"github.com/eclipsesource/homeshell/daemon/internal/persist"
	"github.com/eclipsesource/homeshell/daemon/internal/store"
)

// ds is a small helper for building a discovered session.
func ds(dir, id string, state domain.SessionState) DiscoveredSession {
	return DiscoveredSession{WorkingDir: dir, Session: domain.Session{ID: id, State: state}}
}

func TestReconcileMatchesDiscoveredSessionsByWorkingDir(t *testing.T) {
	// A workstream the daemon already knows (in the store), and a session
	// provider that discovers a session running in that workstream's directory.
	// The reconciler must attach the session to the existing workstream, not
	// create a new one.
	st := store.NewTestStore(t, []domain.Project{{ID: "p1", Name: "p1", Path: "/p1", Workstreams: []domain.Workstream{
		{ID: "ws1", Name: "ws", Provider: "core", WorkingDir: "/p1/work", Sessions: []domain.Session{}},
	}}})
	reg := NewRegistry()
	reg.RegisterSessionProvider(&stubSessionProvider{id: "prov", available: true, discovered: map[string][]DiscoveredSession{
		"p1": {ds("/p1/work", "s1", domain.SessionRunning)},
	}})
	NewReconciler(st, reg, 0).Reconcile(context.Background())

	wss := st.Projects()[0].Workstreams
	if len(wss) != 1 {
		t.Fatalf("workstreams = %d, want 1 (no duplicate adopted)", len(wss))
	}
	w := wss[0]
	if w.ID != "ws1" {
		t.Errorf("workstream id = %q, want ws1 (matched, not adopted)", w.ID)
	}
	if len(w.Sessions) != 1 || w.Sessions[0].ID != "s1" {
		t.Fatalf("sessions = %+v, want [s1] attached", w.Sessions)
	}
	if w.Sessions[0].Provider != "prov" {
		t.Errorf("session provider = %q, want prov (stamped by reconciler)", w.Sessions[0].Provider)
	}
	if w.State != domain.SessionRunning {
		t.Errorf("workstream state = %q, want running (recomputed)", w.State)
	}
}

func TestReconcileDeduplicatesStoredAndDiscoveredSessionByID(t *testing.T) {
	// A session persisted in the store carries no Provider — the sessions table
	// does not store it, so it loads as "". When the owning provider rediscovers
	// the same session by ID, the reconciler must replace the stored copy with the
	// live one, not keep both. Otherwise the session appears twice after a daemon
	// restart (and ReplaceProjection hits a UNIQUE constraint on sessions.id).
	st := store.NewTestStore(t, []domain.Project{{ID: "p1", Path: "/p1", Workstreams: []domain.Workstream{
		{ID: "ws1", Provider: "core", WorkingDir: "/p1/w", Sessions: []domain.Session{
			// Provider intentionally omitted (== ""), reproducing a store-loaded session.
			{ID: "s1", Title: "stored copy", Agent: "Claude Code"},
		}},
	}}})
	reg := NewRegistry()
	live := DiscoveredSession{WorkingDir: "/p1/w", Session: domain.Session{ID: "s1", Title: "live copy", State: domain.SessionIdle}}
	reg.RegisterSessionProvider(&stubSessionProvider{id: "local", available: true, discovered: map[string][]DiscoveredSession{
		"p1": {live},
	}})
	NewReconciler(st, reg, 0).Reconcile(context.Background())

	sessions := st.Projects()[0].Workstreams[0].Sessions
	if len(sessions) != 1 {
		t.Fatalf("sessions = %d, want 1 (no duplicate of s1); got %+v", len(sessions), sessions)
	}
	// Title asserted too: the live copy replaces the stored one wholesale, so the
	// stored "stored copy" title must not survive.
	if sessions[0].ID != "s1" || sessions[0].Provider != "local" || sessions[0].Title != "live copy" {
		t.Errorf("session = %+v; want the live discovered copy (id s1, provider local, title %q)", sessions[0], "live copy")
	}
}

func TestReconcileDeduplicatesSessionDiscoveredUnderADifferentDir(t *testing.T) {
	// Same duplication trap as above, except the provider reports the session
	// under a directory that does not match its stored workstream's WorkingDir
	// (e.g. a symlinked or since-moved working dir). Pass 1 must still drop the
	// stored copy — otherwise it is retained here *and* adopted into a `disc-…`
	// workstream by Pass 2, yielding two s1 rows and the UNIQUE constraint again.
	st := store.NewTestStore(t, []domain.Project{{ID: "p1", Path: "/p1", Workstreams: []domain.Workstream{
		{ID: "ws1", Provider: "core", WorkingDir: "/p1/w", Sessions: []domain.Session{
			{ID: "s1", Title: "stored copy", Agent: "Claude Code"},
		}},
	}}})
	reg := NewRegistry()
	reg.RegisterSessionProvider(&stubSessionProvider{id: "local", available: true, discovered: map[string][]DiscoveredSession{
		"p1": {ds("/p1/elsewhere", "s1", domain.SessionIdle)},
	}})
	NewReconciler(st, reg, 0).Reconcile(context.Background())

	var total int
	for _, ws := range st.Projects()[0].Workstreams {
		for _, sess := range ws.Sessions {
			if sess.ID == "s1" {
				total++
				if sess.Provider != "local" {
					t.Errorf("s1 in %q has provider %q, want local (the live copy)", ws.ID, sess.Provider)
				}
			}
		}
	}
	if total != 1 {
		t.Errorf("s1 appears %d times across workstreams, want 1 (only the adopted live copy)", total)
	}
}

func TestReconcileAdoptsUnmatchedDiscoveredSessions(t *testing.T) {
	// A session discovered in a directory no known workstream owns is adopted
	// into a fresh workstream keyed by that directory.
	st := store.NewTestStore(t, []domain.Project{{ID: "p1", Path: "/p1", Workstreams: []domain.Workstream{}}})
	reg := NewRegistry()
	reg.RegisterSessionProvider(&stubSessionProvider{id: "prov", available: true, discovered: map[string][]DiscoveredSession{
		"p1": {ds("/elsewhere", "s1", domain.SessionRunning)},
	}})
	NewReconciler(st, reg, 0).Reconcile(context.Background())

	wss := st.Projects()[0].Workstreams
	if len(wss) != 1 {
		t.Fatalf("workstreams = %d, want 1 adopted", len(wss))
	}
	if wss[0].WorkingDir != "/elsewhere" || wss[0].Provider != "prov" {
		t.Errorf("adopted workstream = %+v; want WorkingDir=/elsewhere provider=prov", wss[0])
	}
	if len(wss[0].Sessions) != 1 {
		t.Errorf("adopted workstream sessions = %d, want 1", len(wss[0].Sessions))
	}
}

func TestReconcileErroringProviderDoesNotSinkOthers(t *testing.T) {
	st := store.NewTestStore(t, []domain.Project{{ID: "p1", Path: "/p1", Workstreams: []domain.Workstream{}}})
	reg := NewRegistry()
	reg.RegisterSessionProvider(&stubSessionProvider{id: "bad", available: true, discoverErr: errors.New("boom")})
	reg.RegisterSessionProvider(&stubSessionProvider{id: "good", available: true, discovered: map[string][]DiscoveredSession{
		"p1": {ds("/p1/a", "s1", domain.SessionRunning)},
	}})
	NewReconciler(st, reg, 0).Reconcile(context.Background())
	if n := len(st.Projects()[0].Workstreams); n != 1 {
		t.Fatalf("workstreams = %d, want 1 (good provider survives the bad one)", n)
	}
}

func TestReconcileTransientErrorPreservesSessions(t *testing.T) {
	st := store.NewTestStore(t, []domain.Project{{ID: "p1", Path: "/p1", Workstreams: []domain.Workstream{
		{ID: "ws1", Provider: "core", WorkingDir: "/p1/w", Sessions: []domain.Session{}},
	}}})
	reg := NewRegistry()
	stub := &stubSessionProvider{id: "prov", available: true, discovered: map[string][]DiscoveredSession{
		"p1": {ds("/p1/w", "s1", domain.SessionRunning)},
	}}
	reg.RegisterSessionProvider(stub)
	rec := NewReconciler(st, reg, 0)
	rec.Reconcile(context.Background()) // attaches s1

	// A transient discover error must not drop the already-attached session.
	stub.discoverErr = errors.New("boom")
	rec.Reconcile(context.Background())
	sessions := st.Projects()[0].Workstreams[0].Sessions
	if len(sessions) != 1 || sessions[0].ID != "s1" {
		t.Errorf("after transient discover error, sessions = %+v; want s1 preserved", sessions)
	}

	// A transient availability error must also preserve it.
	stub.discoverErr = nil
	stub.availErr = errors.New("hiccup")
	rec.Reconcile(context.Background())
	sessions = st.Projects()[0].Workstreams[0].Sessions
	if len(sessions) != 1 || sessions[0].ID != "s1" {
		t.Errorf("after transient availability error, sessions = %+v; want s1 preserved", sessions)
	}
}

func TestReconcileUnavailableProviderDropsItsSessions(t *testing.T) {
	// A provider that is registered but deliberately unavailable clears its
	// sessions (it refreshed with nothing).
	st := store.NewTestStore(t, []domain.Project{{ID: "p1", Path: "/p1", Workstreams: []domain.Workstream{
		{ID: "ws1", Provider: "core", WorkingDir: "/p1/w", Sessions: []domain.Session{{ID: "stale", Provider: "down"}}},
	}}})
	reg := NewRegistry()
	reg.RegisterSessionProvider(&stubSessionProvider{id: "down", available: false})
	NewReconciler(st, reg, 0).Reconcile(context.Background())
	if n := len(st.Projects()[0].Workstreams[0].Sessions); n != 0 {
		t.Errorf("sessions = %d, want 0 (unavailable provider contributes nothing)", n)
	}
}

func TestReconcilePreservesWorkstreamsWithoutSessions(t *testing.T) {
	// A workstream in the store survives reconciliation even when no provider
	// discovers a session for it — the store is the source of truth for
	// workstream identity.
	st := store.NewTestStore(t, []domain.Project{{ID: "p1", Path: "/p1", Workstreams: []domain.Workstream{
		{ID: "ws1", Name: "kept", Provider: "core", WorkingDir: "/p1/w", Sessions: []domain.Session{}},
	}}})
	reg := NewRegistry()
	reg.RegisterSessionProvider(&stubSessionProvider{id: "prov", available: true})
	NewReconciler(st, reg, 0).Reconcile(context.Background())

	wss := st.Projects()[0].Workstreams
	if len(wss) != 1 || wss[0].ID != "ws1" || wss[0].Name != "kept" {
		t.Errorf("workstreams = %+v; want ws1 kept", wss)
	}
}

func TestReconcilePreservesCreationPlaceholders(t *testing.T) {
	st := store.NewTestStore(t, []domain.Project{{ID: "p1", Path: "/p1", Workstreams: []domain.Workstream{
		{ID: "creating-abc123", Name: "New workstream", Provider: "core", RuntimeState: domain.RuntimeCreating},
		{ID: "creating-err456", Name: "Failed", Provider: "core", RuntimeState: domain.RuntimeError, StatusMessage: "boom"},
	}}})
	reg := NewRegistry()
	reg.RegisterSessionProvider(&stubSessionProvider{id: "prov", available: true})
	NewReconciler(st, reg, 0).Reconcile(context.Background())

	ids := map[string]bool{}
	for _, w := range st.Projects()[0].Workstreams {
		ids[w.ID] = true
	}
	if !ids["creating-abc123"] || !ids["creating-err456"] {
		t.Errorf("placeholders = %v; want both in-progress and error placeholders retained", ids)
	}
}

func TestReconcileDropsSessionsWithNoLiveProvider(t *testing.T) {
	// Retention keys on the registry, not on the string: a stored session whose
	// provider no registered provider answers for is a ghost. Nothing will
	// confirm it and sessionProvider cannot route to it, so delete/open would
	// 404 forever.
	tests := []struct {
		name     string
		provider string
	}{
		// A row written before migration 004 loads with no provider at all.
		{name: "no provider", provider: ""},
		// A row naming a provider this build no longer registers.
		{name: "unregistered provider", provider: "retired"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st := store.NewTestStore(t, []domain.Project{{ID: "p1", Path: "/p1", Workstreams: []domain.Workstream{
				{ID: "ws1", Provider: "core", WorkingDir: "/p1/w", Sessions: []domain.Session{{ID: "ghost", Provider: tc.provider}}},
			}}})
			reg := NewRegistry()
			// The registered provider is available and refreshes, but discovers
			// nothing here — so nothing claims the stored session.
			reg.RegisterSessionProvider(&stubSessionProvider{id: "prov", available: true})
			NewReconciler(st, reg, 0).Reconcile(context.Background())

			if n := len(st.Projects()[0].Workstreams[0].Sessions); n != 0 {
				t.Errorf("sessions = %d, want 0 (a session no registered provider owns is a ghost, not retained)", n)
			}
		})
	}
}

func TestReconcileDoesNotDuplicateRestoredSession(t *testing.T) {
	// A session restored from persistence with an empty Provider that is then
	// re-discovered by its owning provider must appear exactly once. Before the
	// provider was persisted, the blank-provider copy was retained AND the
	// freshly-discovered copy appended, producing a duplicate ID in the result
	// set (UNIQUE constraint failed: sessions.id on persist).
	st := store.NewTestStore(t, []domain.Project{{ID: "p1", Path: "/p1", Workstreams: []domain.Workstream{
		{ID: "ws1", Provider: "core", WorkingDir: "/p1/w", Sessions: []domain.Session{{ID: "s1", Provider: ""}}},
	}}})
	reg := NewRegistry()
	reg.RegisterSessionProvider(&stubSessionProvider{id: "prov", available: true, discovered: map[string][]DiscoveredSession{
		"p1": {ds("/p1/w", "s1", domain.SessionRunning)},
	}})
	NewReconciler(st, reg, 0).Reconcile(context.Background())

	sessions := st.Projects()[0].Workstreams[0].Sessions
	if len(sessions) != 1 || sessions[0].ID != "s1" {
		t.Fatalf("sessions = %+v, want exactly [s1] (no duplicate)", sessions)
	}
	if sessions[0].Provider != "prov" {
		t.Errorf("session provider = %q, want prov (re-stamped by discovery)", sessions[0].Provider)
	}
}

func TestReconcileProjectUnknownID(t *testing.T) {
	rec := NewReconciler(store.NewTestStore(t, []domain.Project{}), NewRegistry(), 0)
	if err := rec.ReconcileProject(context.Background(), "nope"); err == nil {
		t.Errorf("ReconcileProject(nope) = nil; want not found")
	}
}

func TestChangedIsNonBlocking(t *testing.T) {
	rec := NewReconciler(store.NewTestStore(t, []domain.Project{}), NewRegistry(), 0)
	rec.Changed()
	rec.Changed() // coalesces into the pending one; must not block
}

// A starting placeholder is not reported by any provider — it stands for a
// session that does not exist yet — so the ordinary retention rule (keep only
// what a *non-refreshing* provider owns) would project it away on the first
// cycle after a create. That is mid-build, exactly when it is the only thing the
// user has to look at.
func TestReconcileRetainsStartingPlaceholders(t *testing.T) {
	placeholder := domain.Session{
		ID:       StartingIDPrefix + "abc",
		Title:    "Claude session",
		Agent:    "Claude",
		Provider: "prov",
		State:    domain.SessionStarting,
		Preview:  "=> [2/7] RUN apt-get",
	}
	st := store.NewTestStore(t, []domain.Project{{ID: "p1", Name: "p1", Path: "/p1", Workstreams: []domain.Workstream{
		{ID: "ws1", Name: "ws", Provider: "core", WorkingDir: "/p1/work",
			Sessions: []domain.Session{placeholder}},
	}}})
	reg := NewRegistry()
	// The provider refreshes normally and reports nothing for this directory,
	// which is what would otherwise drop the placeholder.
	reg.RegisterSessionProvider(&stubSessionProvider{id: "prov", available: true})

	NewReconciler(st, reg, 0).Reconcile(context.Background())

	ws, err := st.Workstream("p1", "ws1")
	if err != nil {
		t.Fatalf("reading the workstream: %v", err)
	}
	if len(ws.Sessions) != 1 {
		t.Fatalf("sessions = %+v, want the placeholder retained", ws.Sessions)
	}
	if ws.Sessions[0].ID != placeholder.ID {
		t.Errorf("session = %q, want the placeholder %q", ws.Sessions[0].ID, placeholder.ID)
	}
	// Its output has to survive too, or the box the user is watching goes blank
	// every reconcile tick.
	if ws.Sessions[0].Preview != placeholder.Preview {
		t.Errorf("preview = %q, want it retained", ws.Sessions[0].Preview)
	}
}

// Once the real session is reported, the placeholder must not linger beside it —
// Service.finishCreateSession promotes it, and a placeholder that survived would
// double the row.
func TestReconcileKeepsAPlaceholderAndADiscoveredSessionSeparate(t *testing.T) {
	st := store.NewTestStore(t, []domain.Project{{ID: "p1", Name: "p1", Path: "/p1", Workstreams: []domain.Workstream{
		{ID: "ws1", Name: "ws", Provider: "core", WorkingDir: "/p1/work", Sessions: []domain.Session{
			{ID: StartingIDPrefix + "abc", Provider: "prov", State: domain.SessionStarting},
		}},
	}}})
	reg := NewRegistry()
	reg.RegisterSessionProvider(&stubSessionProvider{id: "prov", available: true, discovered: map[string][]DiscoveredSession{
		"p1": {ds("/p1/work", "s1", domain.SessionRunning)},
	}})

	NewReconciler(st, reg, 0).Reconcile(context.Background())

	ws, _ := st.Workstream("p1", "ws1")
	if len(ws.Sessions) != 2 {
		t.Fatalf("sessions = %+v, want the placeholder and the discovered session", ws.Sessions)
	}
	// The placeholder's own lifecycle is the service's business; the reconciler
	// only has to not lose either one.
	var sawPlaceholder, sawReal bool
	for _, sess := range ws.Sessions {
		if IsStartingPlaceholder(sess) {
			sawPlaceholder = true
		}
		if sess.ID == "s1" {
			sawReal = true
		}
	}
	if !sawPlaceholder || !sawReal {
		t.Errorf("sessions = %+v, want both present", ws.Sessions)
	}
}

// A reconcile tick retries a failed write even when there is no project left
// to project, which is the case after removing the last one.
func TestReconcileRetriesAFailedWrite(t *testing.T) {
	saver := &persist.MemorySaver{}
	st := store.New([]domain.Project{{ID: "p1", Name: "p1", Path: "/p1", Workstreams: []domain.Workstream{}}}, saver)
	saver.Err = os.ErrPermission
	if _, err := st.RemoveProject("p1"); err != nil {
		t.Fatalf("RemoveProject: %v", err)
	}
	saver.Err = nil

	NewReconciler(st, NewRegistry(), 0).Reconcile(context.Background())

	if saver.Saves != 1 || len(saver.Last.Projects) != 0 {
		t.Errorf("after a tick: %d saves, projects %+v; want the removal written", saver.Saves, saver.Last.Projects)
	}
}
