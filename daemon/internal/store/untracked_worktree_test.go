//go:build dev

package store

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
)

func suggestion(sessions ...domain.Session) domain.UntrackedWorktree {
	return domain.UntrackedWorktree{Name: "wt", Branch: "feature", Path: "/tmp/p1-wt", Sessions: sessions}
}

func TestReplaceProjectionBroadcastsChangedSuggestions(t *testing.T) {
	s := titledStore(t)
	events, cancel := s.Subscribe()
	defer cancel()

	replace := func() {
		t.Helper()
		workstreams := s.Projects()[0].Workstreams
		if err := s.ReplaceProjection("p1", nil, workstreams, []domain.UntrackedWorktree{suggestion()}); err != nil {
			t.Fatalf("ReplaceProjection: %v", err)
		}
	}
	replace()
	event := receive(t, events)
	got := findProject(t, decodeProjects(t, event.Data), "p1").UntrackedWorktrees
	if len(got) != 1 || got[0].Path != "/tmp/p1-wt" || got[0].Branch != "feature" {
		t.Errorf("event suggestions = %+v, want the one added", got)
	}

	replace()
	select {
	case event := <-events:
		t.Errorf("an unchanged projection broadcast: %s", event.Data)
	default:
	}
}

func TestReplaceProjectionKeepsHiddenSessionsListed(t *testing.T) {
	s := titledStore(t)
	if err := s.RenameSession("p1", "ws-1", "sess-1", "Port the CLI"); err != nil {
		t.Fatal(err)
	}
	launch := domain.LaunchConfig{ProviderID: "enclave", OptionID: "claude"}
	if err := s.SetLaunchConfig("p1", "sess-1", launch); err != nil {
		t.Fatal(err)
	}
	hidden := domain.Session{ID: "sess-1", Title: "claude session", Agent: "claude", Provider: "enclave"}
	if err := s.ReplaceProjection("p1", nil, nil, []domain.UntrackedWorktree{suggestion(hidden)}); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.LaunchConfig("sess-1"); !ok {
		t.Error("launch config forgotten while the session is hidden in a suggestion")
	}
	got := s.Projects()[0].UntrackedWorktrees[0].Sessions[0].Title
	if got != "Port the CLI" {
		t.Errorf("hidden session title = %q, want the custom one", got)
	}

	if err := s.ReplaceProjection("p1", nil, []domain.Workstream{}, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.LaunchConfig("sess-1"); ok {
		t.Error("launch config of a session listed nowhere was not pruned")
	}
}

func TestSnapshotLeavesOutSuggestions(t *testing.T) {
	s := titledStore(t)
	if err := s.ReplaceProjection("p1", nil, s.Projects()[0].Workstreams,
		[]domain.UntrackedWorktree{suggestion()}); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(Saved(t, s))
	if err != nil {
		t.Fatal(err)
	}
	data := string(encoded)
	if strings.Contains(data, "untrackedWorktrees") || strings.Contains(data, "/tmp/p1-wt") {
		t.Errorf("saved state carries a suggestion: %s", data)
	}
}

// Projects hands out copies: a reader holding one must not race the store's
// setters, which write suggestions and their sessions in place.
func TestProjectsDoesNotShareSuggestionsWithMutations(t *testing.T) {
	s := titledStore(t)
	hidden := domain.Session{ID: "sess-h", Title: "hidden", State: domain.SessionRunning}
	if err := s.ReplaceProjection("p1", nil, s.Projects()[0].Workstreams,
		[]domain.UntrackedWorktree{suggestion(hidden)}); err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			_ = s.RenameSession("p1", "ws-1", "sess-1", fmt.Sprintf("name %d", i))
			project := s.Projects()[0]
			_ = s.ReplaceProjection("p1", nil, project.Workstreams, project.UntrackedWorktrees)
		}
	}()
	for i := 0; i < 200; i++ {
		for _, project := range s.Projects() {
			for _, worktree := range project.UntrackedWorktrees {
				_ = worktree.SessionCount
				for _, session := range worktree.Sessions {
					_ = session.Title
				}
			}
		}
	}
	<-done

	got := s.Projects()[0]
	got.UntrackedWorktrees[0].Sessions[0].Title = "scribbled"
	got.UntrackedWorktrees[0].SessionCount = 99
	again := s.Projects()[0].UntrackedWorktrees[0]
	if again.Sessions[0].Title != "hidden" || again.SessionCount != 1 {
		t.Errorf("a caller's edit reached the store: %+v", again)
	}
}

// Two projects at one folder list the same sessions; one may hide a session
// on a suggestion that the other shows in a workstream. Removing the one that
// shows it moves its records to the one that still lists it.
func TestRemoveProjectRehomesASessionHiddenOnASuggestion(t *testing.T) {
	s := titledStore(t)
	if err := s.AddProject(&domain.Project{ID: "p2", Name: "other", Path: "/tmp/p2"}); err != nil {
		t.Fatal(err)
	}
	if err := s.RenameSession("p1", "ws-1", "sess-1", "Port the CLI"); err != nil {
		t.Fatal(err)
	}
	launch := domain.LaunchConfig{ProviderID: "enclave", OptionID: "claude"}
	if err := s.SetLaunchConfig("p1", "sess-1", launch); err != nil {
		t.Fatal(err)
	}
	hidden := domain.Session{ID: "sess-1", Title: "claude session", State: domain.SessionRunning}
	if err := s.ReplaceProjection("p2", nil, nil, []domain.UntrackedWorktree{suggestion(hidden)}); err != nil {
		t.Fatal(err)
	}

	rehomed, err := s.RemoveProject("p1")
	if err != nil {
		t.Fatal(err)
	}
	if home := rehomed["sess-1"]; home != (SessionHome{ProjectID: "p2"}) {
		t.Errorf("rehomed to %+v, want the suggestion in p2", home)
	}
	if got, ok := s.LaunchConfig("sess-1"); !ok || got.OptionID != "claude" {
		t.Errorf("launch config = %+v (%v), want kept", got, ok)
	}
	// p2 now holds the records: its next projection keeps them.
	if err := s.ReplaceProjection("p2", nil, nil, []domain.UntrackedWorktree{suggestion(hidden)}); err != nil {
		t.Fatal(err)
	}
	if got := s.Projects()[0].UntrackedWorktrees[0].Sessions[0].Title; got != "Port the CLI" {
		t.Errorf("hidden session title = %q, want the custom one", got)
	}
	if _, ok := s.LaunchConfig("sess-1"); !ok {
		t.Error("launch config pruned by the projection of the project it moved to")
	}
}

// A suggestion's sessions are not shown, so a change only to them is not news
// for subscribers; a change to how many are live is.
func TestReplaceProjectionBroadcastsOnlyWhatClientsSee(t *testing.T) {
	s := titledStore(t)
	running := domain.Session{ID: "sess-h", Title: "first", State: domain.SessionRunning}
	replace := func(hidden domain.Session) {
		t.Helper()
		err := s.ReplaceProjection("p1", nil, s.Projects()[0].Workstreams, []domain.UntrackedWorktree{suggestion(hidden)})
		if err != nil {
			t.Fatal(err)
		}
	}
	replace(running)
	events, cancel := s.Subscribe()
	defer cancel()

	retitled := running
	retitled.Title = "second"
	replace(retitled)
	select {
	case event := <-events:
		t.Errorf("a change to a hidden session's title broadcast: %s", event.Data)
	default:
	}

	ended := retitled
	ended.State = domain.SessionDone
	replace(ended)
	event := receive(t, events)
	if got := findProject(t, decodeProjects(t, event.Data), "p1").UntrackedWorktrees; got[0].SessionCount != 0 {
		t.Errorf("broadcast sessionCount = %d, want 0", got[0].SessionCount)
	}
}

// A session promoted since the last swap is carried through a projection that
// lacks it, but not one that hides it on a suggestion: it would be listed twice.
func TestReplaceProjectionDoesNotCarryAPromotedSessionItHides(t *testing.T) {
	s := titledStore(t)
	if err := s.AddEphemeralSession("p1", "ws-1", domain.Session{
		ID: StartingIDPrefix + "x", State: domain.SessionStarting,
	}); err != nil {
		t.Fatal(err)
	}
	promoted := domain.Session{ID: "sess-new", State: domain.SessionRunning}
	if err := s.PromoteEphemeralSession("p1", "ws-1", StartingIDPrefix+"x", promoted, domain.LaunchConfig{}); err != nil {
		t.Fatal(err)
	}
	stale := []domain.Workstream{{ID: "ws-1", Name: "ws", Provider: "core", StrategyID: "in-place",
		WorkingDir: "/tmp/p1", Sessions: []domain.Session{}}}
	if err := s.ReplaceProjection("p1", nil, stale, []domain.UntrackedWorktree{suggestion(promoted)}); err != nil {
		t.Fatal(err)
	}
	if sessions := s.Projects()[0].Workstreams[0].Sessions; hasSession(sessions, "sess-new") {
		t.Errorf("workstream sessions = %+v, want the hidden session left on the suggestion", sessions)
	}
}

// A projection computed before a workstream was added in a folder may still
// suggest that folder. The workstream takes the folder and its hidden sessions,
// so the view never shows both.
func TestReplaceProjectionHandsASuggestionToTheWorkstreamAddedThere(t *testing.T) {
	s := titledStore(t)
	stale := s.Projects()[0].Workstreams
	added := domain.Workstream{ID: "ws-added", Name: "wt", Provider: "core", StrategyID: "worktree",
		WorkingDir: "/tmp/p1-wt", Sessions: []domain.Session{}}
	if err := s.AddWorkstream("p1", added); err != nil {
		t.Fatal(err)
	}
	hidden := domain.Session{ID: "sess-h", Title: "hidden", State: domain.SessionRunning}
	if err := s.ReplaceProjection("p1", nil, stale, []domain.UntrackedWorktree{suggestion(hidden)}); err != nil {
		t.Fatal(err)
	}

	project := s.Projects()[0]
	if len(project.UntrackedWorktrees) != 0 {
		t.Errorf("suggestions = %+v, want none beside the workstream added there", project.UntrackedWorktrees)
	}
	workstream, err := s.Workstream("p1", "ws-added")
	if err != nil {
		t.Fatal(err)
	}
	if !hasSession(workstream.Sessions, "sess-h") || len(workstream.Sessions) != 1 {
		t.Errorf("added workstream sessions = %+v, want the hidden one", workstream.Sessions)
	}
}

// A session added since the pass's read stays out of a workstream when the
// projection hides it on a suggestion: it is listed there, and once is enough.
func TestReplaceProjectionKeepsASessionAddedSinceTheReadHiddenOnce(t *testing.T) {
	s := titledStore(t)
	read := s.Projects()[0].Workstreams
	added := domain.Session{ID: "sess-2", Provider: "enclave", State: domain.SessionRunning}
	if err := s.AddSession("p1", "ws-1", added); err != nil {
		t.Fatal(err)
	}
	projected := s.Projects()[0].Workstreams
	projected[0].Sessions = projected[0].Sessions[:1]
	if err := s.ReplaceProjection("p1", read, projected, []domain.UntrackedWorktree{suggestion(added)}); err != nil {
		t.Fatal(err)
	}

	project := s.Projects()[0]
	for _, session := range project.Workstreams[0].Sessions {
		if session.ID == "sess-2" {
			t.Errorf("sess-2 is on ws-1 as well as hidden on the suggestion")
		}
	}
	if got := project.UntrackedWorktrees; len(got) != 1 || len(got[0].Sessions) != 1 {
		t.Errorf("suggestions = %+v, want the one hiding sess-2", got)
	}
}
