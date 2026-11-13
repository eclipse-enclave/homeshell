package store

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
)

// ephemeralFixture is one project holding one workstream with no sessions.
func ephemeralFixture(t *testing.T) *Store {
	t.Helper()
	return NewTestStore(t, []domain.Project{{
		ID: "p1", Name: "p1", Path: t.TempDir(),
		Workstreams: []domain.Workstream{{
			ID: "ws-1", Name: "ws", Provider: "core", StrategyID: "in-place",
		}},
	}})
}

// persistedIDs reads the session IDs the saved snapshot actually holds for ws-1.
func persistedIDs(t *testing.T, s *Store) []string {
	t.Helper()
	ids := []string{}
	for _, project := range Saved(t, s).Projects {
		for _, workstream := range project.Workstreams {
			if workstream.ID != "ws-1" {
				continue
			}
			for _, session := range workstream.Sessions {
				ids = append(ids, session.ID)
			}
		}
	}
	return ids
}

// The whole point of the ephemeral variant: a placeholder must be visible to
// clients but absent from the database, so a daemon killed mid-build cannot
// restore a session no provider will ever refresh: an immortal row that no
// reconcile can remove.
func TestAddEphemeralSessionIsNotPersisted(t *testing.T) {
	s := ephemeralFixture(t)

	if err := s.AddEphemeralSession("p1", "ws-1", domain.Session{
		ID: "starting-abc", Title: "Claude session", Agent: "Claude",
		Provider: "enclave", State: domain.SessionStarting,
	}); err != nil {
		t.Fatalf("AddEphemeralSession() error = %v", err)
	}

	ws, _ := s.Workstream("p1", "ws-1")
	if len(ws.Sessions) != 1 || ws.Sessions[0].ID != "starting-abc" {
		t.Fatalf("in-memory sessions = %+v, want the placeholder", ws.Sessions)
	}
	if ids := persistedIDs(t, s); len(ids) != 0 {
		t.Errorf("persisted sessions = %v, want none", ids)
	}
}

// Promotion is the swap the UI must not see as two steps, and the point at which
// the session becomes real enough to write down.
func TestPromoteEphemeralSessionSwapsInPlaceAndPersists(t *testing.T) {
	s := ephemeralFixture(t)
	if err := s.AddEphemeralSession("p1", "ws-1", domain.Session{
		ID: "starting-abc", Agent: "Claude", State: domain.SessionStarting,
	}); err != nil {
		t.Fatalf("AddEphemeralSession() error = %v", err)
	}
	// A second, unrelated placeholder so position is observable. What keeps a
	// session out of the saved state is its starting prefix, not the method
	// that added it, so this one carries the prefix too.
	if err := s.AddEphemeralSession("p1", "ws-1", domain.Session{
		ID: "starting-other", Agent: "Other", State: domain.SessionIdle,
	}); err != nil {
		t.Fatalf("AddEphemeralSession() error = %v", err)
	}

	real := domain.Session{ID: "sess-1", Agent: "Claude", Provider: "enclave", State: domain.SessionRunning}
	if err := s.PromoteEphemeralSession("p1", "ws-1", "starting-abc", real, domain.LaunchConfig{}); err != nil {
		t.Fatalf("PromoteEphemeralSession() error = %v", err)
	}

	ws, _ := s.Workstream("p1", "ws-1")
	if len(ws.Sessions) != 2 {
		t.Fatalf("sessions = %+v, want the promoted one and the other", ws.Sessions)
	}
	// Same slot: the row must not jump to the end of the list.
	if ws.Sessions[0].ID != "sess-1" {
		t.Errorf("sessions[0] = %q, want the promoted session in the placeholder's slot", ws.Sessions[0].ID)
	}
	if ids := persistedIDs(t, s); len(ids) != 1 || ids[0] != "sess-1" {
		t.Errorf("persisted = %v, want only the real session", ids)
	}
}

// Promoting when the placeholder has already gone must still land the session:
// losing it would lose a container the provider really started.
func TestPromoteEphemeralSessionAddsWhenPlaceholderIsGone(t *testing.T) {
	s := ephemeralFixture(t)

	real := domain.Session{ID: "sess-1", State: domain.SessionRunning}
	if err := s.PromoteEphemeralSession("p1", "ws-1", "starting-missing", real, domain.LaunchConfig{}); err != nil {
		t.Fatalf("PromoteEphemeralSession() error = %v", err)
	}

	ws, _ := s.Workstream("p1", "ws-1")
	if len(ws.Sessions) != 1 || ws.Sessions[0].ID != "sess-1" {
		t.Errorf("sessions = %+v, want the real session appended", ws.Sessions)
	}
}

// The provider records a session before its create returns, so a projection
// can list the real session while still carrying the placeholder. Promoting
// then must leave it listed once, where the projection put it.
func TestPromoteEphemeralSessionAlreadyListedByAProjection(t *testing.T) {
	testStore := ephemeralFixture(t)
	if err := testStore.AddEphemeralSession("p1", "ws-1", domain.Session{
		ID: "starting-abc", Agent: "Claude", State: domain.SessionStarting,
	}); err != nil {
		t.Fatalf("AddEphemeralSession() error = %v", err)
	}
	projected := domain.Session{ID: "sess-1", Agent: "Claude", State: domain.SessionIdle}
	if err := testStore.ReplaceWorkstreams("p1", nil, []domain.Workstream{{
		ID: "ws-1", Name: "ws", Provider: "core", StrategyID: "in-place",
		Sessions: []domain.Session{projected},
	}}); err != nil {
		t.Fatalf("ReplaceWorkstreams() error = %v", err)
	}

	real := domain.Session{ID: "sess-1", Agent: "Claude", State: domain.SessionRunning}
	if err := testStore.PromoteEphemeralSession("p1", "ws-1", "starting-abc", real, domain.LaunchConfig{}); err != nil {
		t.Fatalf("PromoteEphemeralSession() error = %v", err)
	}

	workstream, _ := testStore.Workstream("p1", "ws-1")
	if len(workstream.Sessions) != 1 || workstream.Sessions[0].ID != "sess-1" {
		t.Fatalf("sessions = %+v, want the real session once", workstream.Sessions)
	}
	if workstream.Sessions[0].State != domain.SessionRunning {
		t.Errorf("state = %q, want the promoted session's %q", workstream.Sessions[0].State, domain.SessionRunning)
	}
	if ids := persistedIDs(t, testStore); !slices.Equal(ids, []string{"sess-1"}) {
		t.Errorf("persisted = %v, want the real session once", ids)
	}
}

// Unwinding a failed create should not need to care whether the placeholder is
// still there.
func TestRemoveEphemeralSessionToleratesAnAbsentSession(t *testing.T) {
	s := ephemeralFixture(t)

	if err := s.RemoveEphemeralSession("p1", "ws-1", "starting-nope"); err != nil {
		t.Errorf("RemoveEphemeralSession() error = %v, want nil for an absent session", err)
	}
	if err := s.RemoveEphemeralSession("nope", "ws-1", "starting-nope"); err == nil {
		t.Error("RemoveEphemeralSession() on an unknown project = nil, want an error")
	}
}

func TestSetSessionPreview(t *testing.T) {
	s := ephemeralFixture(t)
	if err := s.AddEphemeralSession("p1", "ws-1", domain.Session{
		ID: "starting-abc", State: domain.SessionStarting,
	}); err != nil {
		t.Fatalf("AddEphemeralSession() error = %v", err)
	}

	if err := s.SetSessionPreview("p1", "ws-1", "starting-abc", "=> [2/7] RUN apt-get"); err != nil {
		t.Fatalf("SetSessionPreview() error = %v", err)
	}
	ws, _ := s.Workstream("p1", "ws-1")
	if ws.Sessions[0].Preview != "=> [2/7] RUN apt-get" {
		t.Errorf("preview = %q, want the tail", ws.Sessions[0].Preview)
	}

	if err := s.SetSessionPreview("p1", "ws-1", "absent", "x"); err == nil {
		t.Error("SetSessionPreview() on an unknown session = nil, want an error")
	}
}

// Callers read what Projects() and Workstream() hand out without the lock, so a
// later mutation must not write into it.
func TestMutatorsDoNotWriteIntoAHandedOutSnapshot(t *testing.T) {
	s := ephemeralFixture(t)
	if err := s.AddEphemeralSession("p1", "ws-1", domain.Session{
		ID: "starting-abc", Agent: "Claude", State: domain.SessionStarting,
	}); err != nil {
		t.Fatalf("AddEphemeralSession() error = %v", err)
	}
	if err := s.AddEphemeralSession("p1", "ws-1", domain.Session{
		ID: "starting-def", Agent: "Claude", State: domain.SessionStarting,
	}); err != nil {
		t.Fatalf("AddEphemeralSession() error = %v", err)
	}
	projects := s.Projects()
	workstream, _ := s.Workstream("p1", "ws-1")
	// Encoded, so the expectation shares no memory with what it guards.
	encode := func(value any) string {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("json.Marshal() error = %v", err)
		}
		return string(data)
	}
	wantWorkstream, wantProjects := encode(workstream), encode(projects)

	mutations := []struct {
		name   string
		mutate func() error
	}{
		{"SetSessionState", func() error { return s.SetSessionState("p1", "ws-1", "starting-abc", domain.SessionRunning) }},
		{"SetSessionPreview", func() error { return s.SetSessionPreview("p1", "ws-1", "starting-abc", "new output") }},
		{"SetRuntimeState", func() error { return s.SetRuntimeState("p1", "ws-1", domain.RuntimeRunning) }},
		{"FailEphemeralSession", func() error { return s.FailEphemeralSession("p1", "ws-1", "starting-def", "boom") }},
		{"PromoteEphemeralSession", func() error {
			return s.PromoteEphemeralSession("p1", "ws-1", "starting-abc",
				domain.Session{ID: "sess-1", State: domain.SessionRunning}, domain.LaunchConfig{})
		}},
		{"RemoveEphemeralSession", func() error { return s.RemoveEphemeralSession("p1", "ws-1", "starting-def") }},
	}
	for _, mutation := range mutations {
		if err := mutation.mutate(); err != nil {
			t.Fatalf("%s() error = %v", mutation.name, err)
		}
		if encode(workstream) != wantWorkstream {
			t.Fatalf("after %s the Workstream() snapshot changed to %+v; the mutation wrote into shared memory",
				mutation.name, workstream)
		}
		if encode(projects) != wantProjects {
			t.Fatalf("after %s the Projects() snapshot changed to %+v; the mutation wrote into shared memory",
				mutation.name, projects)
		}
	}
}

// A failed create keeps its row: the placeholder becomes the record of the
// attempt, in place, still unpersisted.
func TestFailEphemeralSessionMarksTheRowInPlace(t *testing.T) {
	s := ephemeralFixture(t)
	if err := s.AddEphemeralSession("p1", "ws-1", domain.Session{
		ID: "starting-abc", Title: "Claude session", Agent: "Claude",
		Provider: "enclave", State: domain.SessionStarting, Preview: "step 1\nstep 2",
	}); err != nil {
		t.Fatalf("AddEphemeralSession() error = %v", err)
	}
	if err := s.AddEphemeralSession("p1", "ws-1", domain.Session{
		ID: "other", Agent: "Other", State: domain.SessionIdle,
	}); err != nil {
		t.Fatalf("AddEphemeralSession() error = %v", err)
	}

	if err := s.FailEphemeralSession("p1", "ws-1", "starting-abc", "image build failed"); err != nil {
		t.Fatalf("FailEphemeralSession() error = %v", err)
	}

	ws, _ := s.Workstream("p1", "ws-1")
	if len(ws.Sessions) != 2 {
		t.Fatalf("sessions = %+v, want the failed row kept alongside the other", ws.Sessions)
	}
	failed := ws.Sessions[0]
	if failed.ID != "starting-abc" {
		t.Errorf("sessions[0] = %q, want the failed row in its own slot", failed.ID)
	}
	if failed.State != domain.SessionError {
		t.Errorf("state = %q, want %q", failed.State, domain.SessionError)
	}
	if failed.Error != "image build failed" {
		t.Errorf("error = %q, want the provider's message", failed.Error)
	}
	// The startup output is usually what explains the failure, so it stays.
	if failed.Preview != "step 1\nstep 2" {
		t.Errorf("preview = %q, want the startup output kept", failed.Preview)
	}
	// Still ephemeral: a restart forgets the attempt rather than restoring a row
	// no provider can ever refresh.
	if ids := persistedIDs(t, s); len(ids) != 0 {
		t.Errorf("persisted sessions = %v, want none", ids)
	}
	// The workstream's aggregate follows: error leads the precedence.
	if ws.State != domain.SessionError {
		t.Errorf("workstream state = %q, want %q", ws.State, domain.SessionError)
	}
}

// Unlike the remove, this one reports a missing placeholder: the caller is
// holding a failure it has to put somewhere, and needs to know it did not land.
func TestFailEphemeralSessionReportsAnAbsentSession(t *testing.T) {
	s := ephemeralFixture(t)

	if err := s.FailEphemeralSession("p1", "ws-1", "starting-gone", "boom"); err == nil {
		t.Error("FailEphemeralSession() on a missing session = nil, want an error")
	}
}

// A projection carries placeholders through every cycle (the reconciler retains
// them deliberately), so the persistence path has to be the thing that refuses
// them — otherwise the never-persisted contract holds only until the next
// reconcile, and a restart brings the placeholder back as an immortal row.
func TestReplaceWorkstreamsDoesNotPersistAPlaceholder(t *testing.T) {
	s := ephemeralFixture(t)
	placeholder := domain.Session{
		ID: StartingIDPrefix + "abc", Title: "Claude session",
		Provider: "enclave", State: domain.SessionStarting,
	}
	real := domain.Session{ID: "hs-real", Title: "Claude", Provider: "enclave", State: domain.SessionRunning}
	if err := s.AddEphemeralSession("p1", "ws-1", placeholder); err != nil {
		t.Fatalf("AddEphemeralSession: %v", err)
	}

	if err := s.ReplaceWorkstreams("p1", nil, []domain.Workstream{{
		ID: "ws-1", Name: "ws", Provider: "core", StrategyID: "in-place",
		Sessions: []domain.Session{real, placeholder},
	}}); err != nil {
		t.Fatalf("ReplaceWorkstreams: %v", err)
	}

	if got, want := persistedIDs(t, s), []string{"hs-real"}; !slices.Equal(got, want) {
		t.Errorf("persisted session IDs = %v, want %v — a placeholder must never reach the database", got, want)
	}
	// It is still on screen: not persisting it is not the same as dropping it.
	ws, err := s.Workstream("p1", "ws-1")
	if err != nil {
		t.Fatalf("Workstream: %v", err)
	}
	if len(ws.Sessions) != 2 {
		t.Errorf("projected sessions = %d, want both the real session and the placeholder", len(ws.Sessions))
	}
}

// The reconciler computes its projection from a snapshot taken before it asks
// the providers, and discovery is slow (a subprocess each). A create started
// inside that window is missing from the projection through no fault of its own,
// and replacing the state wholesale would drop the row the user is watching —
// for the rest of a multi-minute build, with nothing to bring it back, because
// no provider reports a placeholder.
func TestReplaceWorkstreamsKeepsAPlaceholderAddedDuringTheCycle(t *testing.T) {
	s := ephemeralFixture(t)

	// What the reconciler saw when it started: one workstream, no sessions.
	snapshot := []domain.Workstream{{
		ID: "ws-1", Name: "ws", Provider: "core", StrategyID: "in-place",
		Sessions: []domain.Session{},
	}}

	// A create lands while the providers are being asked.
	placeholder := domain.Session{
		ID: StartingIDPrefix + "abc", Title: "Claude session",
		Provider: "enclave", State: domain.SessionStarting,
	}
	if err := s.AddEphemeralSession("p1", "ws-1", placeholder); err != nil {
		t.Fatalf("AddEphemeralSession: %v", err)
	}

	// The projection, built from the stale snapshot, finally lands.
	if err := s.ReplaceWorkstreams("p1", nil, snapshot); err != nil {
		t.Fatalf("ReplaceWorkstreams: %v", err)
	}

	ws, err := s.Workstream("p1", "ws-1")
	if err != nil {
		t.Fatalf("Workstream: %v", err)
	}
	if len(ws.Sessions) != 1 || ws.Sessions[0].ID != placeholder.ID {
		t.Fatalf("sessions after the stale projection = %+v, want the placeholder to survive", ws.Sessions)
	}
	if ws.Sessions[0].State != domain.SessionStarting {
		t.Errorf("placeholder state = %q, want it carried over untouched", ws.Sessions[0].State)
	}
}

// The carry-over must not double a placeholder the projection already has — the
// common case, since the reconciler retains the ones it saw.
func TestReplaceWorkstreamsDoesNotDuplicateARetainedPlaceholder(t *testing.T) {
	s := ephemeralFixture(t)
	placeholder := domain.Session{
		ID: StartingIDPrefix + "abc", Provider: "enclave", State: domain.SessionStarting,
	}
	if err := s.AddEphemeralSession("p1", "ws-1", placeholder); err != nil {
		t.Fatalf("AddEphemeralSession: %v", err)
	}
	if err := s.ReplaceWorkstreams("p1", nil, []domain.Workstream{{
		ID: "ws-1", Name: "ws", Provider: "core", StrategyID: "in-place",
		Sessions: []domain.Session{placeholder},
	}}); err != nil {
		t.Fatalf("ReplaceWorkstreams: %v", err)
	}
	ws, err := s.Workstream("p1", "ws-1")
	if err != nil {
		t.Fatalf("Workstream: %v", err)
	}
	if len(ws.Sessions) != 1 {
		t.Errorf("sessions = %d, want 1 — the retained placeholder must not be carried twice", len(ws.Sessions))
	}
}

// A promoted session replaces its placeholder before the reconcile that follows,
// so the carry-over must not resurrect the placeholder it just retired.
func TestReplaceWorkstreamsDoesNotResurrectAPromotedPlaceholder(t *testing.T) {
	s := ephemeralFixture(t)
	placeholder := domain.Session{
		ID: StartingIDPrefix + "abc", Provider: "enclave", State: domain.SessionStarting,
	}
	if err := s.AddEphemeralSession("p1", "ws-1", placeholder); err != nil {
		t.Fatalf("AddEphemeralSession: %v", err)
	}
	real := domain.Session{ID: "hs-real", Provider: "enclave", State: domain.SessionRunning}
	if err := s.PromoteEphemeralSession("p1", "ws-1", placeholder.ID, real, domain.LaunchConfig{}); err != nil {
		t.Fatalf("PromoteEphemeralSession: %v", err)
	}
	if err := s.ReplaceWorkstreams("p1", nil, []domain.Workstream{{
		ID: "ws-1", Name: "ws", Provider: "core", StrategyID: "in-place",
		Sessions: []domain.Session{real},
	}}); err != nil {
		t.Fatalf("ReplaceWorkstreams: %v", err)
	}
	ws, err := s.Workstream("p1", "ws-1")
	if err != nil {
		t.Fatalf("Workstream: %v", err)
	}
	if len(ws.Sessions) != 1 || ws.Sessions[0].ID != "hs-real" {
		t.Errorf("sessions = %+v, want only the promoted session", ws.Sessions)
	}
}

// The workstream-level half of the same race: a create that starts while a
// reconcile is in flight must not have its placeholder card wiped by the
// projection that raced it.
func TestReplaceWorkstreamsKeepsAWorkstreamPlaceholderAddedDuringTheCycle(t *testing.T) {
	s := ephemeralFixture(t)
	snapshot := []domain.Workstream{{
		ID: "ws-1", Name: "ws", Provider: "core", StrategyID: "in-place",
		Sessions: []domain.Session{},
	}}
	placeholder := domain.Workstream{
		ID: PlaceholderIDPrefix + "abc", Name: "feature/x", Provider: "core",
		RuntimeState: domain.RuntimeCreating, StatusMessage: "Setting up workstream…",
	}
	if err := s.AddWorkstream("p1", placeholder); err != nil {
		t.Fatalf("AddWorkstream: %v", err)
	}
	if err := s.ReplaceWorkstreams("p1", nil, snapshot); err != nil {
		t.Fatalf("ReplaceWorkstreams: %v", err)
	}
	if _, err := s.Workstream("p1", placeholder.ID); err != nil {
		t.Errorf("the creation placeholder is gone after a stale projection: %v", err)
	}
}

// A workstream create that lands while a projection is being computed replaces
// a placeholder the projection still holds: the projection must carry the real
// workstream rather than restore the placeholder and drop what replaced it.
func TestReplaceWorkstreamsKeepsAWorkstreamPromotedDuringTheCycle(t *testing.T) {
	s := ephemeralFixture(t)
	placeholder := domain.Workstream{
		ID: PlaceholderIDPrefix + "abc", Name: "feature/x", Provider: "core",
		RuntimeState: domain.RuntimeCreating, Sessions: []domain.Session{},
	}
	if err := s.AddWorkstream("p1", placeholder); err != nil {
		t.Fatalf("AddWorkstream: %v", err)
	}
	snapshot := []domain.Workstream{
		{ID: "ws-1", Name: "ws", Provider: "core", StrategyID: "in-place", Sessions: []domain.Session{}},
		placeholder,
	}
	real := domain.Workstream{ID: "ws-2", Name: "feature/x", Provider: "core", WorkingDir: "/tmp/x", Sessions: []domain.Session{}}
	if err := s.PromoteWorkstream("p1", placeholder.ID, real); err != nil {
		t.Fatalf("PromoteWorkstream: %v", err)
	}
	if err := s.ReplaceWorkstreams("p1", nil, snapshot); err != nil {
		t.Fatalf("ReplaceWorkstreams: %v", err)
	}

	var ids []string
	for _, project := range s.Projects() {
		if project.ID == "p1" {
			for _, workstream := range project.Workstreams {
				ids = append(ids, workstream.ID)
			}
		}
	}
	if want := []string{"ws-1", "ws-2"}; !slices.Equal(ids, want) {
		t.Errorf("workstreams = %v, want %v", ids, want)
	}
}

// A placeholder removed while a projection was being computed — its create
// failed or was cancelled — must stay gone: the snapshot's copy is stale.
func TestReplaceWorkstreamsDoesNotResurrectARemovedWorkstreamPlaceholder(t *testing.T) {
	s := ephemeralFixture(t)
	placeholder := domain.Workstream{
		ID: PlaceholderIDPrefix + "abc", Name: "feature/x", Provider: "core",
		RuntimeState: domain.RuntimeCreating, Sessions: []domain.Session{},
	}
	if err := s.AddWorkstream("p1", placeholder); err != nil {
		t.Fatalf("AddWorkstream: %v", err)
	}
	snapshot := []domain.Workstream{
		{ID: "ws-1", Name: "ws", Provider: "core", StrategyID: "in-place", Sessions: []domain.Session{}},
		placeholder,
	}
	if err := s.RemoveWorkstream("p1", placeholder.ID); err != nil {
		t.Fatalf("RemoveWorkstream: %v", err)
	}
	if err := s.ReplaceWorkstreams("p1", nil, snapshot); err != nil {
		t.Fatalf("ReplaceWorkstreams: %v", err)
	}
	if ids, want := workstreamIDs(s, "p1"), []string{"ws-1"}; !slices.Equal(ids, want) {
		t.Errorf("workstreams = %v, want %v", ids, want)
	}
}

// A workstream deleted after its placeholder was promoted must not bring the
// placeholder back as a card stuck in "creating" forever.
func TestReplaceWorkstreamsDoesNotResurrectThePlaceholderOfARemovedWorkstream(t *testing.T) {
	s := ephemeralFixture(t)
	placeholder := domain.Workstream{
		ID: PlaceholderIDPrefix + "abc", Name: "feature/x", Provider: "core",
		RuntimeState: domain.RuntimeCreating, Sessions: []domain.Session{},
	}
	if err := s.AddWorkstream("p1", placeholder); err != nil {
		t.Fatalf("AddWorkstream: %v", err)
	}
	snapshot := []domain.Workstream{
		{ID: "ws-1", Name: "ws", Provider: "core", StrategyID: "in-place", Sessions: []domain.Session{}},
		placeholder,
	}
	real := domain.Workstream{ID: "ws-2", Name: "feature/x", Provider: "core", WorkingDir: "/tmp/x", Sessions: []domain.Session{}}
	if err := s.PromoteWorkstream("p1", placeholder.ID, real); err != nil {
		t.Fatalf("PromoteWorkstream: %v", err)
	}
	if err := s.RemoveWorkstream("p1", "ws-2"); err != nil {
		t.Fatalf("RemoveWorkstream: %v", err)
	}
	if err := s.ReplaceWorkstreams("p1", nil, snapshot); err != nil {
		t.Fatalf("ReplaceWorkstreams: %v", err)
	}
	if ids, want := workstreamIDs(s, "p1"), []string{"ws-1"}; !slices.Equal(ids, want) {
		t.Errorf("workstreams = %v, want %v", ids, want)
	}
}

// A placeholder still in flight keeps the live copy, not the snapshot's: its
// status message has moved on since the snapshot was taken.
func TestReplaceWorkstreamsKeepsTheLiveCopyOfAWorkstreamPlaceholder(t *testing.T) {
	s := ephemeralFixture(t)
	placeholder := domain.Workstream{
		ID: PlaceholderIDPrefix + "abc", Name: "feature/x", Provider: "core",
		RuntimeState: domain.RuntimeCreating, StatusMessage: "Setting up workstream…",
		Sessions: []domain.Session{},
	}
	if err := s.AddWorkstream("p1", placeholder); err != nil {
		t.Fatalf("AddWorkstream: %v", err)
	}
	snapshot := []domain.Workstream{placeholder}
	progressed := placeholder
	progressed.StatusMessage = "Installing dependencies…"
	if err := s.UpdateWorkstream("p1", progressed); err != nil {
		t.Fatalf("UpdateWorkstream: %v", err)
	}
	if err := s.ReplaceWorkstreams("p1", nil, snapshot); err != nil {
		t.Fatalf("ReplaceWorkstreams: %v", err)
	}
	got, err := s.Workstream("p1", placeholder.ID)
	if err != nil {
		t.Fatalf("Workstream: %v", err)
	}
	if got.StatusMessage != progressed.StatusMessage {
		t.Errorf("status message = %q, want %q", got.StatusMessage, progressed.StatusMessage)
	}
}

func workstreamIDs(s *Store, projectID string) []string {
	var ids []string
	for _, project := range s.Projects() {
		if project.ID == projectID {
			for _, workstream := range project.Workstreams {
				ids = append(ids, workstream.ID)
			}
		}
	}
	return ids
}

// The workstream half of the never-persisted rule, and the sharper case: the
// table has no runtime_state or status_message column, so a persisted creation
// placeholder would come back from a restart as an ordinary workstream with no
// working directory and nothing saying what it was — a card nothing can finish,
// refresh or explain. AddWorkstream does not write one; the projection must not
// write it either, and it carries placeholders through every cycle.
func TestReplaceWorkstreamsDoesNotPersistAWorkstreamPlaceholder(t *testing.T) {
	s := ephemeralFixture(t)
	placeholder := domain.Workstream{
		ID: PlaceholderIDPrefix + "abc", Name: "feature/x", Provider: "core",
		RuntimeState: domain.RuntimeCreating, StatusMessage: "Setting up workstream…",
		Sessions: []domain.Session{},
	}
	if err := s.AddWorkstream("p1", placeholder); err != nil {
		t.Fatalf("AddWorkstream: %v", err)
	}
	if err := s.ReplaceWorkstreams("p1", nil, []domain.Workstream{
		{ID: "ws-1", Name: "ws", Provider: "core", StrategyID: "in-place", Sessions: []domain.Session{}},
		placeholder,
	}); err != nil {
		t.Fatalf("ReplaceWorkstreams: %v", err)
	}

	ids := []string{}
	for _, project := range Saved(t, s).Projects {
		if project.ID != "p1" {
			continue
		}
		for _, workstream := range project.Workstreams {
			ids = append(ids, workstream.ID)
		}
	}
	if !slices.Equal(ids, []string{"ws-1"}) {
		t.Errorf("persisted workstream IDs = %v, want only the real one", ids)
	}
	// Still projected, though: not persisting it is not the same as dropping it.
	if _, err := s.Workstream("p1", placeholder.ID); err != nil {
		t.Errorf("the placeholder left the projection: %v", err)
	}
}

// A promoted session names the placeholder it replaced, so the client that
// asked for it can tell its row from others, and keeps doing so through the
// projections that follow, which report it without the link.
func TestPromotedSessionNamesItsPlaceholder(t *testing.T) {
	testStore := ephemeralFixture(t)
	if err := testStore.AddEphemeralSession("p1", "ws-1", domain.Session{
		ID: "starting-abc", State: domain.SessionStarting,
	}); err != nil {
		t.Fatalf("AddEphemeralSession() error = %v", err)
	}
	real := domain.Session{ID: "sess-1", Provider: "enclave", State: domain.SessionRunning}
	if err := testStore.PromoteEphemeralSession("p1", "ws-1", "starting-abc", real, domain.LaunchConfig{}); err != nil {
		t.Fatalf("PromoteEphemeralSession() error = %v", err)
	}
	assertLink := func(when string) {
		t.Helper()
		workstream, _ := testStore.Workstream("p1", "ws-1")
		if len(workstream.Sessions) != 1 || workstream.Sessions[0].PlaceholderID != "starting-abc" {
			t.Errorf("%s: sessions = %+v, want sess-1 naming starting-abc", when, workstream.Sessions)
		}
	}
	assertLink("promoted")

	if err := testStore.ReplaceWorkstreams("p1", nil, []domain.Workstream{{
		ID: "ws-1", Name: "ws", Provider: "core", StrategyID: "in-place",
		Sessions: []domain.Session{real},
	}}); err != nil {
		t.Fatalf("ReplaceWorkstreams() error = %v", err)
	}
	assertLink("reprojected")
}

func TestPromotedWorkstreamNamesItsPlaceholder(t *testing.T) {
	testStore := ephemeralFixture(t)
	placeholderID := PlaceholderIDPrefix + "abc"
	if err := testStore.AddWorkstream("p1", domain.Workstream{
		ID: placeholderID, Name: "feature", Provider: "core", RuntimeState: domain.RuntimeCreating,
	}); err != nil {
		t.Fatalf("AddWorkstream() error = %v", err)
	}
	if err := testStore.PromoteWorkstream("p1", placeholderID, domain.Workstream{
		ID: "ws-2", Name: "feature", Provider: "core", StrategyID: "in-place", Sessions: []domain.Session{},
	}); err != nil {
		t.Fatalf("PromoteWorkstream() error = %v", err)
	}
	workstream, err := testStore.Workstream("p1", "ws-2")
	if err != nil {
		t.Fatalf("Workstream() error = %v", err)
	}
	if workstream.PlaceholderID != placeholderID {
		t.Errorf("PlaceholderID = %q, want %q", workstream.PlaceholderID, placeholderID)
	}
}

// Promotion records what the session was started with in the same mutation, so
// the session is never listed without it — not even when the placeholder is gone.
func TestPromoteEphemeralSessionRecordsItsLaunch(t *testing.T) {
	s := ephemeralFixture(t)
	if err := s.AddEphemeralSession("p1", "ws-1", domain.Session{
		ID: "starting-abc", Agent: "Claude", State: domain.SessionStarting,
	}); err != nil {
		t.Fatalf("AddEphemeralSession() error = %v", err)
	}
	launch := domain.LaunchConfig{ProviderID: "enclave", OptionID: "claude", Config: []byte(`{"model":"opus"}`)}
	for _, placeholderID := range []string{"starting-abc", "starting-missing"} {
		sessionID := "sess-" + placeholderID
		real := domain.Session{ID: sessionID, Agent: "Claude", Provider: "enclave", State: domain.SessionRunning}
		if err := s.PromoteEphemeralSession("p1", "ws-1", placeholderID, real, launch); err != nil {
			t.Fatalf("PromoteEphemeralSession(%s) error = %v", placeholderID, err)
		}
		got, ok := s.LaunchConfig(sessionID)
		if !ok || got.ProviderID != "enclave" || got.OptionID != "claude" || string(got.Config) != `{"model":"opus"}` {
			t.Errorf("LaunchConfig(%s) = %+v, %v; want the launch it was promoted with", sessionID, got, ok)
		}
	}

	if err := s.PromoteEphemeralSession("p1", "ws-1", "starting-none",
		domain.Session{ID: "sess-unrecorded", State: domain.SessionRunning}, domain.LaunchConfig{}); err != nil {
		t.Fatalf("PromoteEphemeralSession() error = %v", err)
	}
	if _, ok := s.LaunchConfig("sess-unrecorded"); ok {
		t.Error("LaunchConfig of a session promoted without a launch: want none")
	}
}

// A just-promoted session the projection lists under another workstream is not
// retained in its old one as well: it would be listed twice, and written twice.
func TestPromotedSessionListedElsewhereIsNotRetainedTwice(t *testing.T) {
	s := ephemeralFixture(t)
	if err := s.AddEphemeralSession("p1", "ws-1", domain.Session{ID: "starting-abc", State: domain.SessionStarting}); err != nil {
		t.Fatalf("AddEphemeralSession: %v", err)
	}
	promoted := domain.Session{ID: "sess-1", Agent: "claude", Provider: "enclave", State: domain.SessionRunning}
	if err := s.PromoteEphemeralSession("p1", "ws-1", "starting-abc", promoted, domain.LaunchConfig{}); err != nil {
		t.Fatalf("PromoteEphemeralSession: %v", err)
	}

	if err := s.ReplaceWorkstreams("p1", nil, []domain.Workstream{
		{ID: "ws-1", Name: "ws", Provider: "core", StrategyID: "in-place"},
		{ID: "ws-2", Name: "other", Provider: "core", StrategyID: "in-place", Sessions: []domain.Session{promoted}},
	}); err != nil {
		t.Fatalf("ReplaceWorkstreams: %v", err)
	}
	listings := 0
	for _, workstream := range s.Projects()[0].Workstreams {
		for _, listed := range workstream.Sessions {
			if listed.ID == promoted.ID {
				listings++
			}
		}
	}
	if listings != 1 {
		t.Errorf("the promoted session is listed %d times; want once", listings)
	}
}

// A projection whose snapshot still held a placeholder, promoted while the
// providers were being asked, must not bring the placeholder back beside the
// session that replaced it: nothing would ever finish or remove it.
func TestReplaceWorkstreamsDropsAPlaceholderPromotedDuringTheCycle(t *testing.T) {
	s := ephemeralFixture(t)
	placeholder := domain.Session{
		ID: StartingIDPrefix + "abc", Provider: "enclave", State: domain.SessionStarting,
	}
	if err := s.AddEphemeralSession("p1", "ws-1", placeholder); err != nil {
		t.Fatalf("AddEphemeralSession: %v", err)
	}
	snapshot := []domain.Workstream{{
		ID: "ws-1", Name: "ws", Provider: "core", StrategyID: "in-place",
		Sessions: []domain.Session{placeholder},
	}}
	real := domain.Session{ID: "hs-real", Provider: "enclave", State: domain.SessionRunning}
	if err := s.PromoteEphemeralSession("p1", "ws-1", placeholder.ID, real, domain.LaunchConfig{}); err != nil {
		t.Fatalf("PromoteEphemeralSession: %v", err)
	}
	if err := s.ReplaceWorkstreams("p1", nil, snapshot); err != nil {
		t.Fatalf("ReplaceWorkstreams: %v", err)
	}
	ws, err := s.Workstream("p1", "ws-1")
	if err != nil {
		t.Fatalf("Workstream: %v", err)
	}
	if len(ws.Sessions) != 1 || ws.Sessions[0].ID != "hs-real" {
		t.Errorf("sessions = %+v, want only the promoted session", ws.Sessions)
	}
}

// A workstream created from start to finish while a projection was being
// computed is in neither the projection nor, as a placeholder, the live state:
// it must still survive the swap, or it is lost and its name freed for another.
func TestReplaceWorkstreamsKeepsAWorkstreamCreatedDuringTheCycle(t *testing.T) {
	s := ephemeralFixture(t)
	snapshot := []domain.Workstream{
		{ID: "ws-1", Name: "ws", Provider: "core", StrategyID: "in-place", Sessions: []domain.Session{}},
	}
	placeholder := domain.Workstream{
		ID: PlaceholderIDPrefix + "abc", Name: "feature/x", Provider: "core",
		RuntimeState: domain.RuntimeCreating, Sessions: []domain.Session{},
	}
	if err := s.AddWorkstream("p1", placeholder); err != nil {
		t.Fatalf("AddWorkstream: %v", err)
	}
	real := domain.Workstream{ID: "ws-2", Name: "feature/x", Provider: "core", WorkingDir: "/tmp/x", Sessions: []domain.Session{}}
	if err := s.PromoteWorkstream("p1", placeholder.ID, real); err != nil {
		t.Fatalf("PromoteWorkstream: %v", err)
	}
	if err := s.ReplaceWorkstreams("p1", nil, snapshot); err != nil {
		t.Fatalf("ReplaceWorkstreams: %v", err)
	}
	if ids, want := workstreamIDs(s, "p1"), []string{"ws-1", "ws-2"}; !slices.Equal(ids, want) {
		t.Errorf("workstreams = %v, want %v", ids, want)
	}
}
