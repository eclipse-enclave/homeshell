//go:build dev

package store

import (
	"errors"
	"testing"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
	"github.com/eclipsesource/homeshell/daemon/internal/persist"
)

// titledStore is one project holding one workstream with one session, the shape
// every test here renames within.
func titledStore(t *testing.T) *Store {
	t.Helper()
	return NewTestStore(t, []domain.Project{{
		ID: "p1", Name: "proj", Path: "/tmp/p1",
		Workstreams: []domain.Workstream{{
			ID: "ws-1", Name: "ws", Provider: "core", StrategyID: "in-place",
			WorkingDir: "/tmp/p1",
			Sessions: []domain.Session{{
				ID: "sess-1", Title: "claude session", Agent: "claude", Provider: "enclave",
				State: domain.SessionRunning,
			}},
		}},
	}})
}

// sessionTitle reads one session's title out of the store's projection.
func sessionTitle(t *testing.T, s *Store, sessionID string) string {
	t.Helper()
	for _, project := range s.Projects() {
		for _, ws := range project.Workstreams {
			for _, sess := range ws.Sessions {
				if sess.ID == sessionID {
					return sess.Title
				}
			}
		}
	}
	t.Fatalf("session %q is not in the store", sessionID)
	return ""
}

func TestRenameSession(t *testing.T) {
	s := titledStore(t)
	if err := s.RenameSession("p1", "ws-1", "sess-1", "Port the CLI"); err != nil {
		t.Fatalf("RenameSession: %v", err)
	}
	if got := sessionTitle(t, s, "sess-1"); got != "Port the CLI" {
		t.Errorf("title = %q; want %q", got, "Port the CLI")
	}
}

func TestRenameSessionBroadcasts(t *testing.T) {
	s := titledStore(t)
	events, cancel := s.Subscribe()
	defer cancel()
	if err := s.RenameSession("p1", "ws-1", "sess-1", "named"); err != nil {
		t.Fatalf("RenameSession: %v", err)
	}
	select {
	case event := <-events:
		if event.Type != "state" {
			t.Errorf("event type = %q; want state", event.Type)
		}
	default:
		t.Error("a rename did not broadcast; clients would not see the new name")
	}
}

func TestRenameSessionRejectsEmptyTitle(t *testing.T) {
	s := titledStore(t)
	if err := s.RenameSession("p1", "ws-1", "sess-1", ""); err == nil {
		t.Error("RenameSession(\"\") = nil; want an error — ResetSessionTitle is how the default comes back")
	}
	if got := sessionTitle(t, s, "sess-1"); got != "claude session" {
		t.Errorf("title = %q; want the original to be untouched", got)
	}
}

func TestRenameSessionUnknownTargets(t *testing.T) {
	tests := []struct {
		name                             string
		project, workstream, sessionName string
	}{
		{"unknown project", "ghost", "ws-1", "sess-1"},
		{"unknown workstream", "p1", "ghost", "sess-1"},
		{"unknown session", "p1", "ws-1", "ghost"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s := titledStore(t)
			if err := s.RenameSession(test.project, test.workstream, test.sessionName, "x"); err == nil {
				t.Error("RenameSession = nil; want a not-found error")
			}
		})
	}
}

// The point of the feature: a provider re-reporting its own title on the next
// reconcile cycle must not undo the rename.
func TestRenameSurvivesReprojection(t *testing.T) {
	s := titledStore(t)
	if err := s.RenameSession("p1", "ws-1", "sess-1", "Port the CLI"); err != nil {
		t.Fatalf("RenameSession: %v", err)
	}

	// What the reconciler does every cycle: hand over what the providers just
	// reported, under the providers' own titles.
	reprojected := []domain.Workstream{{
		ID: "ws-1", Name: "ws", Provider: "core", StrategyID: "in-place", WorkingDir: "/tmp/p1",
		Sessions: []domain.Session{{
			ID: "sess-1", Title: "claude session", Agent: "claude", Provider: "enclave",
			State: domain.SessionRunning,
		}},
	}}
	if err := s.ReplaceProjection("p1", nil, reprojected, nil); err != nil {
		t.Fatalf("ReplaceProjection: %v", err)
	}

	if got := sessionTitle(t, s, "sess-1"); got != "Port the CLI" {
		t.Errorf("title after re-projection = %q; want the rename to hold", got)
	}
}

// The renamed title is what other sessions do *not* get: a rename is per session,
// not per workstream.
func TestRenameLeavesOtherSessionsAlone(t *testing.T) {
	s := NewTestStore(t, []domain.Project{{
		ID: "p1", Name: "proj", Path: "/tmp/p1",
		Workstreams: []domain.Workstream{{
			ID: "ws-1", Name: "ws", Provider: "core", StrategyID: "in-place", WorkingDir: "/tmp/p1",
			Sessions: []domain.Session{
				{ID: "sess-1", Title: "claude session", Agent: "claude", Provider: "enclave"},
				{ID: "sess-2", Title: "claude session", Agent: "claude", Provider: "enclave"},
			},
		}},
	}})
	if err := s.RenameSession("p1", "ws-1", "sess-1", "the first one"); err != nil {
		t.Fatalf("RenameSession: %v", err)
	}
	if got := sessionTitle(t, s, "sess-2"); got != "claude session" {
		t.Errorf("sibling title = %q; want it untouched", got)
	}
}

func TestResetSessionTitle(t *testing.T) {
	s := titledStore(t)
	if err := s.RenameSession("p1", "ws-1", "sess-1", "named"); err != nil {
		t.Fatalf("RenameSession: %v", err)
	}
	if err := s.ResetSessionTitle("p1", "ws-1", "sess-1"); err != nil {
		t.Fatalf("ResetSessionTitle: %v", err)
	}
	// The provider's title returns with the next cycle — that is when the provider
	// next says what it is.
	if err := s.ReplaceProjection("p1", nil, []domain.Workstream{{
		ID: "ws-1", Name: "ws", Provider: "core", StrategyID: "in-place", WorkingDir: "/tmp/p1",
		Sessions: []domain.Session{{ID: "sess-1", Title: "claude session", Agent: "claude", Provider: "enclave"}},
	}}, nil); err != nil {
		t.Fatalf("ReplaceProjection: %v", err)
	}
	if got := sessionTitle(t, s, "sess-1"); got != "claude session" {
		t.Errorf("title after a reset = %q; want the provider's own title back", got)
	}
}

func TestResetSessionTitleIsIdempotent(t *testing.T) {
	s := titledStore(t)
	if err := s.ResetSessionTitle("p1", "ws-1", "sess-1"); err != nil {
		t.Errorf("resetting a never-renamed session = %v; want nil", err)
	}
}

func TestResetSessionTitleRejectsUnknownSession(t *testing.T) {
	s := titledStore(t)
	if err := s.ResetSessionTitle("p1", "ws-1", "ghost"); err == nil {
		t.Error("ResetSessionTitle(ghost) = nil; want a not-found error")
	}
}

// A session the providers have stopped reporting is gone from the daemon, so its
// name goes too — otherwise the row waits forever for an ID that never returns.
func TestReprojectionForgetsTitlesOfVanishedSessions(t *testing.T) {
	s := titledStore(t)
	if err := s.RenameSession("p1", "ws-1", "sess-1", "named"); err != nil {
		t.Fatalf("RenameSession: %v", err)
	}
	// The container is torn down: the provider reports nothing for this directory.
	if err := s.ReplaceProjection("p1", nil, []domain.Workstream{{
		ID: "ws-1", Name: "ws", Provider: "core", StrategyID: "in-place", WorkingDir: "/tmp/p1",
		Sessions: []domain.Session{},
	}}, nil); err != nil {
		t.Fatalf("ReplaceProjection: %v", err)
	}
	if len(s.sessionTitles) != 0 {
		t.Errorf("sessionTitles = %+v; want the vanished session's name forgotten", s.sessionTitles)
	}

	// A session that later reappears under the same ID (a provider that had simply
	// gone quiet is a different case — the reconciler retains those) is a new
	// session as far as naming goes.
	if err := s.ReplaceProjection("p1", nil, []domain.Workstream{{
		ID: "ws-1", Name: "ws", Provider: "core", StrategyID: "in-place", WorkingDir: "/tmp/p1",
		Sessions: []domain.Session{{ID: "sess-1", Title: "claude session", Agent: "claude", Provider: "enclave"}},
	}}, nil); err != nil {
		t.Fatalf("ReplaceProjection: %v", err)
	}
	if got := sessionTitle(t, s, "sess-1"); got != "claude session" {
		t.Errorf("title = %q; want the provider's title for a session that came back", got)
	}
}

// A reprojection of one project must not forget another project's names.
func TestReprojectionKeepsOtherProjectsTitles(t *testing.T) {
	s := NewTestStore(t, []domain.Project{
		{
			ID: "p1", Name: "one", Path: "/tmp/p1",
			Workstreams: []domain.Workstream{{
				ID: "ws-1", Name: "ws", Provider: "core", StrategyID: "in-place", WorkingDir: "/tmp/p1",
				Sessions: []domain.Session{{ID: "sess-1", Title: "claude session", Agent: "claude", Provider: "enclave"}},
			}},
		},
		{
			ID: "p2", Name: "two", Path: "/tmp/p2",
			Workstreams: []domain.Workstream{{
				ID: "ws-2", Name: "ws", Provider: "core", StrategyID: "in-place", WorkingDir: "/tmp/p2",
				Sessions: []domain.Session{{ID: "sess-2", Title: "claude session", Agent: "claude", Provider: "enclave"}},
			}},
		},
	})
	if err := s.RenameSession("p2", "ws-2", "sess-2", "the other project"); err != nil {
		t.Fatalf("RenameSession: %v", err)
	}
	// p1 re-projects with no sessions at all; p2's name must be untouched.
	if err := s.ReplaceProjection("p1", nil, []domain.Workstream{}, nil); err != nil {
		t.Fatalf("ReplaceProjection: %v", err)
	}
	if got := sessionTitle(t, s, "sess-2"); got != "the other project" {
		t.Errorf("other project's title = %q; want it kept", got)
	}
}

func TestRemoveSessionForgetsItsTitle(t *testing.T) {
	s := titledStore(t)
	if err := s.RenameSession("p1", "ws-1", "sess-1", "named"); err != nil {
		t.Fatalf("RenameSession: %v", err)
	}
	if err := s.RemoveSession("p1", "ws-1", "sess-1"); err != nil {
		t.Fatalf("RemoveSession: %v", err)
	}
	if len(s.sessionTitles) != 0 {
		t.Errorf("sessionTitles = %+v; want the removed session's name forgotten", s.sessionTitles)
	}
}

func TestRemoveWorkstreamForgetsItsSessionTitles(t *testing.T) {
	s := titledStore(t)
	if err := s.RenameSession("p1", "ws-1", "sess-1", "named"); err != nil {
		t.Fatalf("RenameSession: %v", err)
	}
	if err := s.RemoveWorkstream("p1", "ws-1"); err != nil {
		t.Fatalf("RemoveWorkstream: %v", err)
	}
	if len(s.sessionTitles) != 0 {
		t.Errorf("sessionTitles = %+v; want the removed workstream's session names forgotten", s.sessionTitles)
	}
}

func TestRemoveProjectForgetsItsSessionTitles(t *testing.T) {
	s := titledStore(t)
	if err := s.RenameSession("p1", "ws-1", "sess-1", "named"); err != nil {
		t.Fatalf("RenameSession: %v", err)
	}
	if _, err := s.RemoveProject("p1"); err != nil {
		t.Fatalf("RemoveProject: %v", err)
	}
	if len(s.sessionTitles) != 0 {
		t.Errorf("sessionTitles = %+v; want the removed project's session names forgotten", s.sessionTitles)
	}
}

// A rename is user data: it is written through to the state file and read back
// over the sessions restored from it, so a restart does not undo it.
func TestRenameIsReloadedAtStartup(t *testing.T) {
	s := titledStore(t)
	if err := s.RenameSession("p1", "ws-1", "sess-1", "Port the CLI"); err != nil {
		t.Fatalf("RenameSession: %v", err)
	}

	// A fresh Store over the saved snapshot is what a restart amounts to. The
	// session record is restored under the title the provider last reported;
	// the custom title is what has to be applied over it.
	snapshot := Saved(t, s)
	snapshot.Projects[0].Workstreams[0].Sessions[0].Title = "claude session"
	restarted := Restore(snapshot, &persist.MemorySaver{})
	if got := sessionTitle(t, restarted, "sess-1"); got != "Port the CLI" {
		t.Errorf("title after a restart = %q; want the rename to have survived", got)
	}
}

// twoSessionStore is one workstream holding two sessions that already show the
// same provider-given title — the by-design collision the rule must not touch.
func twoSessionStore(t *testing.T) *Store {
	t.Helper()
	return NewTestStore(t, []domain.Project{{
		ID: "p1", Name: "proj", Path: "/tmp/p1",
		Workstreams: []domain.Workstream{{
			ID: "ws-1", Name: "ws", Provider: "core", StrategyID: "in-place",
			WorkingDir: "/tmp/p1",
			Sessions: []domain.Session{
				{ID: "sess-1", Title: "claude session", Agent: "claude", State: domain.SessionRunning},
				{ID: "sess-2", Title: "claude session", Agent: "claude", State: domain.SessionIdle},
			},
		}, {
			ID: "ws-2", Name: "other", Provider: "core", StrategyID: "worktree",
			WorkingDir: "/tmp/p1-wt",
			Sessions: []domain.Session{
				{ID: "sess-3", Title: "review", Agent: "claude", State: domain.SessionIdle},
			},
		}},
	}})
}

// The reported bug: two sessions could be renamed to the same thing, leaving two
// rows a user cannot tell apart.
func TestRenameSessionRejectsATitleASiblingShows(t *testing.T) {
	s := twoSessionStore(t)
	if err := s.RenameSession("p1", "ws-1", "sess-1", "review pass"); err != nil {
		t.Fatalf("first rename: %v", err)
	}

	err := s.RenameSession("p1", "ws-1", "sess-2", "review pass")
	if !errors.Is(err, ErrDuplicateSessionTitle) {
		t.Fatalf("duplicate title err = %v, want ErrDuplicateSessionTitle", err)
	}
	if got := sessionTitle(t, s, "sess-2"); got != "claude session" {
		t.Errorf("title = %q, want the refused rename to have changed nothing", got)
	}
}

func TestRenameSessionTitleComparisonIsTrimmedAndCaseInsensitive(t *testing.T) {
	s := twoSessionStore(t)
	if err := s.RenameSession("p1", "ws-1", "sess-1", "Review"); err != nil {
		t.Fatalf("first rename: %v", err)
	}
	for _, title := range []string{"review", "REVIEW", "  Review  "} {
		if err := s.RenameSession("p1", "ws-1", "sess-2", title); !errors.Is(err, ErrDuplicateSessionTitle) {
			t.Errorf("renaming to %q err = %v, want ErrDuplicateSessionTitle", title, err)
		}
	}
	if err := s.RenameSession("p1", "ws-1", "sess-2", "Review 2"); err != nil {
		t.Errorf("distinct title: %v", err)
	}
}

// A rename also collides with a title no user chose: what is on screen is what
// the reader has to tell apart, whoever named it. (sess-1 is moved off the shared
// default first, so coming back to it is a real change rather than a no-op.)
func TestRenameSessionRejectsASiblingsProviderTitle(t *testing.T) {
	s := twoSessionStore(t)
	if err := s.RenameSession("p1", "ws-1", "sess-1", "temporary"); err != nil {
		t.Fatalf("rename away: %v", err)
	}
	if err := s.RenameSession("p1", "ws-1", "sess-1", "claude session"); !errors.Is(err, ErrDuplicateSessionTitle) {
		t.Errorf("renaming onto a sibling's provider title err = %v, want ErrDuplicateSessionTitle", err)
	}
}

// The corollary of "a rename must not *introduce* a duplicate": re-typing the
// title a row already shows is allowed even when a sibling shows it too, which is
// the only way the by-design collision stays renameable at all.
func TestRenameSessionAllowsRetypingASharedDefault(t *testing.T) {
	s := twoSessionStore(t)
	if err := s.RenameSession("p1", "ws-1", "sess-1", "claude session"); err != nil {
		t.Errorf("re-typing the title it already shows: %v", err)
	}
}

// The by-design collision is left alone: nothing renames these, so two Claude
// sessions keep the one title their provider gives them.
func TestProviderGivenTitlesMayCollide(t *testing.T) {
	s := twoSessionStore(t)
	if a, b := sessionTitle(t, s, "sess-1"), sessionTitle(t, s, "sess-2"); a != b {
		t.Fatalf("fixture titles = %q, %q; want the same provider title", a, b)
	}
	// And a rename away from it, then back, is allowed — the sibling is what
	// blocks, not the history.
	if err := s.RenameSession("p1", "ws-1", "sess-1", "temporary"); err != nil {
		t.Fatalf("rename away: %v", err)
	}
	if err := s.ResetSessionTitle("p1", "ws-1", "sess-1"); err != nil {
		t.Errorf("reset back to a colliding provider title: %v", err)
	}
}

// Scope is the workstream: two workstreams may each have a "review".
func TestSessionTitlesCollideOnlyWithinAWorkstream(t *testing.T) {
	s := twoSessionStore(t)
	if err := s.RenameSession("p1", "ws-1", "sess-1", "review"); err != nil {
		t.Errorf("same title as a session in another workstream: %v", err)
	}
}

// Renaming a session to the title it already has is not a conflict with itself.
func TestRenameSessionAllowsItsOwnTitle(t *testing.T) {
	s := twoSessionStore(t)
	if err := s.RenameSession("p1", "ws-1", "sess-1", "claude session"); err != nil {
		t.Errorf("renaming a session to its own title: %v", err)
	}
}

// Project hands out one project as Projects does: a copy, down to its sessions.
func TestProjectReturnsACopyOfOneProject(t *testing.T) {
	s := titledStore(t)
	got, err := s.Project("p1")
	if err != nil {
		t.Fatal(err)
	}
	got.Workstreams[0].Sessions[0].Title = "scribbled"
	if title := sessionTitle(t, s, "sess-1"); title != "claude session" {
		t.Errorf("a caller's edit reached the store: title %q", title)
	}
	if _, err := s.Project("nope"); err == nil {
		t.Error("Project of an unknown ID succeeded, want an error")
	}
}

// A projection computed before a workstream was removed does not bring it
// back; the one after that decides alone.
func TestReplaceProjectionDoesNotRestoreAWorkstreamRemovedMeanwhile(t *testing.T) {
	s := titledStore(t)
	inFlight := s.Projects()[0].Workstreams
	if err := s.RemoveWorkstream("p1", "ws-1"); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceProjection("p1", inFlight, inFlight, nil); err != nil {
		t.Fatal(err)
	}
	if got := s.Projects()[0].Workstreams; len(got) != 0 {
		t.Fatalf("workstreams = %+v, want the removed one not brought back", got)
	}
	if err := s.ReplaceProjection("p1", nil, s.Projects()[0].Workstreams, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.AddWorkstream("p1", domain.Workstream{ID: "ws-1", Name: "ws", WorkingDir: "/tmp/p1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceProjection("p1", nil, s.Projects()[0].Workstreams, nil); err != nil {
		t.Fatal(err)
	}
	if got := s.Projects()[0].Workstreams; len(got) != 1 {
		t.Errorf("workstreams = %+v, want one listed again kept", got)
	}
}

// A projection computed before a workstream was added does not drop it.
func TestReplaceProjectionKeepsAWorkstreamAddedMeanwhile(t *testing.T) {
	s := titledStore(t)
	inFlight := s.Projects()[0].Workstreams
	if err := s.AddWorkstream("p1", domain.Workstream{ID: "ws-2", Name: "two", WorkingDir: "/tmp/p1-two"}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceProjection("p1", nil, inFlight, nil); err != nil {
		t.Fatal(err)
	}
	if got := s.Projects()[0].Workstreams; len(got) != 2 {
		t.Errorf("workstreams = %+v, want ws-2, added meanwhile, kept", got)
	}
}
