package store

import (
	"errors"
	"strings"
	"testing"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
)

// nameFixture is two empty projects, so a name can be shown to collide inside
// one and not across both.
func nameFixture(t *testing.T) *Store {
	t.Helper()
	return NewTestStore(t, []domain.Project{
		{ID: "p1", Name: "one", Path: t.TempDir()},
		{ID: "p2", Name: "two", Path: t.TempDir()},
	})
}

func add(t *testing.T, s *Store, projectID, id, name, dir string) error {
	t.Helper()
	return s.AddWorkstream(projectID, domain.Workstream{
		ID: id, Name: name, Provider: "core", WorkingDir: dir,
		Sessions: []domain.Session{},
	})
}

func TestAddWorkstreamRejectsDuplicateName(t *testing.T) {
	s := nameFixture(t)
	if err := add(t, s, "p1", "ws-a", "feature", "/work/a"); err != nil {
		t.Fatalf("first add: %v", err)
	}

	// A different directory does not make it a different workstream to a reader.
	if err := add(t, s, "p1", "ws-b", "feature", "/work/b"); !errors.Is(err, ErrDuplicateName) {
		t.Errorf("duplicate name err = %v, want ErrDuplicateName", err)
	}
	// The refusal names the offender, so the message is actionable.
	err := add(t, s, "p1", "ws-b", "feature", "/work/b")
	if got := err.Error(); !strings.Contains(got, "feature") {
		t.Errorf("error = %q, want it to quote the name", got)
	}
	// And nothing was inserted.
	if got := len(projectByID(t, s, "p1").Workstreams); got != 1 {
		t.Errorf("workstreams = %d, want the refusal to leave the project alone", got)
	}
}

func TestWorkstreamNameComparisonIsTrimmedAndCaseInsensitive(t *testing.T) {
	s := nameFixture(t)
	if err := add(t, s, "p1", "ws-a", "Feature", "/work/a"); err != nil {
		t.Fatalf("first add: %v", err)
	}

	// The point of the rule is that a human can tell two cards apart; case and
	// stray spaces do not achieve that.
	for _, name := range []string{"feature", "FEATURE", "  Feature  ", "fEaTuRe"} {
		if err := add(t, s, "p1", "ws-b", name, "/work/b"); !errors.Is(err, ErrDuplicateName) {
			t.Errorf("adding %q err = %v, want ErrDuplicateName", name, err)
		}
	}
	// A genuinely different name is fine.
	if err := add(t, s, "p1", "ws-b", "feature-2", "/work/b"); err != nil {
		t.Errorf("distinct name add: %v", err)
	}
}

// Uniqueness is per project: two projects may each have a "main".
func TestWorkstreamNamesCollideOnlyWithinAProject(t *testing.T) {
	s := nameFixture(t)
	if err := add(t, s, "p1", "ws-a", "main", "/work/a"); err != nil {
		t.Fatalf("first add: %v", err)
	}
	if err := add(t, s, "p2", "ws-b", "main", "/work/b"); err != nil {
		t.Errorf("same name in another project: %v", err)
	}
}

// The placeholder holds the name its real workstream is about to take, so
// counting it would make every named create collide with itself. PromoteWorkstream
// is the door that swap comes through, and it excludes the placeholder it is
// replacing.
func TestAWorkstreamMayTakeTheNameOfItsOwnPlaceholder(t *testing.T) {
	s := nameFixture(t)
	placeholderID := PlaceholderIDPrefix + "abc"
	if err := s.AddWorkstream("p1", domain.Workstream{
		ID: placeholderID, Name: "feature", Provider: "core",
		RuntimeState: domain.RuntimeCreating, Sessions: []domain.Session{},
	}); err != nil {
		t.Fatalf("placeholder add: %v", err)
	}
	real := domain.Workstream{
		ID: "ws-real", Name: "feature", Provider: "core", WorkingDir: "/work/a",
		Sessions: []domain.Session{},
	}
	if err := s.PromoteWorkstream("p1", placeholderID, real); err != nil {
		t.Errorf("real workstream taking its placeholder's name: %v", err)
	}
	// The swap is one mutation: the placeholder is gone and the real one is here,
	// with no moment in between where a client would have seen both.
	names := map[string]bool{}
	for _, ws := range projectByID(t, s, "p1").Workstreams {
		names[ws.ID] = true
	}
	if names[placeholderID] {
		t.Error("the placeholder is still here after its workstream was promoted")
	}
	if !names["ws-real"] {
		t.Error("the promoted workstream is missing")
	}
}

// A create in flight holds its name against *other* creates, which is what makes
// the check a reservation rather than a snapshot: without it two same-named
// creates both start, and the loser only discovers the clash minutes later, after
// its directory has been materialized and has to be torn back down.
func TestAnInFlightPlaceholderHoldsItsName(t *testing.T) {
	s := nameFixture(t)
	if err := s.AddWorkstream("p1", domain.Workstream{
		ID: PlaceholderIDPrefix + "first", Name: "feature", Provider: "core",
		RuntimeState: domain.RuntimeCreating, Sessions: []domain.Session{},
	}); err != nil {
		t.Fatalf("first placeholder add: %v", err)
	}
	err := s.AddWorkstream("p1", domain.Workstream{
		ID: PlaceholderIDPrefix + "second", Name: "Feature", Provider: "core",
		RuntimeState: domain.RuntimeCreating, Sessions: []domain.Session{},
	})
	if !errors.Is(err, ErrDuplicateName) {
		t.Errorf("second concurrent create of the same name err = %v, want ErrDuplicateName", err)
	}
}

// A name taken while the provider was working is still refused at promotion —
// the reservation is not a licence to overwrite whatever landed meanwhile.
func TestPromoteWorkstreamStillRefusesAnotherWorkstreamsName(t *testing.T) {
	s := nameFixture(t)
	placeholderID := PlaceholderIDPrefix + "abc"
	if err := s.AddWorkstream("p1", domain.Workstream{
		ID: placeholderID, Name: "feature", Provider: "core",
		RuntimeState: domain.RuntimeCreating, Sessions: []domain.Session{},
	}); err != nil {
		t.Fatalf("placeholder add: %v", err)
	}
	// Something else takes the name while the provider works — an adopted
	// directory, which arrives through the projection rather than the user-facing
	// door and so is not bound by the name rule (the placeholder rides along,
	// carried over as an in-flight create).
	if err := s.ReplaceWorkstreams("p1", nil, []domain.Workstream{{
		ID: "ws-other", Name: "feature", Provider: "core", WorkingDir: "/work/other",
		Sessions: []domain.Session{},
	}}); err != nil {
		t.Fatalf("projecting the interloper: %v", err)
	}
	real := domain.Workstream{
		ID: "ws-real", Name: "feature", Provider: "core", WorkingDir: "/work/a",
		Sessions: []domain.Session{},
	}
	if err := s.PromoteWorkstream("p1", placeholderID, real); !errors.Is(err, ErrDuplicateName) {
		t.Errorf("promoting onto a name taken meanwhile err = %v, want ErrDuplicateName", err)
	}
	// And the placeholder is left exactly as it was, for the caller to turn into
	// the record of the failure.
	if _, err := s.Workstream("p1", placeholderID); err != nil {
		t.Errorf("the placeholder is gone after a refused promotion: %v", err)
	}
}

// A create that failed leaves its placeholder behind in error state. It must not
// keep the name hostage — retrying the same name is the obvious next thing a user
// does, and the corpse of the last attempt is not a reason to refuse.
func TestAFailedPlaceholderDoesNotHoldItsNameHostage(t *testing.T) {
	s := nameFixture(t)
	failed := domain.Workstream{
		ID: PlaceholderIDPrefix + "dead", Name: "feature", Provider: "core",
		RuntimeState: domain.RuntimeError, StatusMessage: "boom",
		Sessions: []domain.Session{},
	}
	if err := s.AddWorkstream("p1", failed); err != nil {
		t.Fatalf("failed placeholder add: %v", err)
	}

	if err := add(t, s, "p1", PlaceholderIDPrefix+"retry", "feature", ""); err != nil {
		t.Errorf("retrying the same name after a failure: %v", err)
	}
}

// Renaming is the other door into the same rule; without this the rule would be
// one PATCH away from being bypassed.
func TestUpdateWorkstreamRejectsARenameOntoAnotherName(t *testing.T) {
	s := nameFixture(t)
	if err := add(t, s, "p1", "ws-a", "feature", "/work/a"); err != nil {
		t.Fatalf("first add: %v", err)
	}
	if err := add(t, s, "p1", "ws-b", "other", "/work/b"); err != nil {
		t.Fatalf("second add: %v", err)
	}

	renamed := domain.Workstream{
		ID: "ws-b", Name: "Feature", Provider: "core", WorkingDir: "/work/b",
		Sessions: []domain.Session{},
	}
	if err := s.UpdateWorkstream("p1", renamed); !errors.Is(err, ErrDuplicateName) {
		t.Errorf("rename onto a taken name err = %v, want ErrDuplicateName", err)
	}
	// The rejected rename changed nothing.
	for _, ws := range projectByID(t, s, "p1").Workstreams {
		if ws.ID == "ws-b" && ws.Name != "other" {
			t.Errorf("name = %q, want the refused rename to leave it alone", ws.Name)
		}
	}
}

// Re-saving a workstream under the name it already has is not a conflict with
// itself — every update goes through this door, not only renames.
func TestUpdateWorkstreamAllowsAWorkstreamItsOwnName(t *testing.T) {
	s := nameFixture(t)
	if err := add(t, s, "p1", "ws-a", "feature", "/work/a"); err != nil {
		t.Fatalf("first add: %v", err)
	}
	same := domain.Workstream{
		ID: "ws-a", Name: "feature", Provider: "core", WorkingDir: "/work/a",
		RuntimeState: domain.RuntimeRunning, Sessions: []domain.Session{},
	}
	if err := s.UpdateWorkstream("p1", same); err != nil {
		t.Errorf("re-saving under its own name: %v", err)
	}
}

// An unnamed workstream is not yet named — the provider fills one in during
// creation, so an empty name must not collide with another empty one.
func TestEmptyWorkstreamNamesDoNotCollide(t *testing.T) {
	s := nameFixture(t)
	if err := add(t, s, "p1", "ws-a", "", "/work/a"); err != nil {
		t.Fatalf("first add: %v", err)
	}
	if err := add(t, s, "p1", "ws-b", "", "/work/b"); err != nil {
		t.Errorf("second unnamed add: %v", err)
	}
}

// projectByID reads one project out of the store's snapshot.
func projectByID(t *testing.T, s *Store, id string) domain.Project {
	t.Helper()
	for _, project := range s.Projects() {
		if project.ID == id {
			return project
		}
	}
	t.Fatalf("project %q not in the store", id)
	return domain.Project{}
}

// The failure path of a create writes the error onto the placeholder *in place*,
// keeping the name the attempt was made under. The name rule must not refuse that
// write — and the case where it would is exactly the case where it matters: a
// create that failed *because* the name was taken. Refusing leaves a "creating…"
// spinner nothing ever resolves, because the reconciler retains placeholders
// unconditionally and only this write ends one.
func TestAPlaceholderMayTakeItsErrorStateUnderATakenName(t *testing.T) {
	s := nameFixture(t)
	placeholderID := PlaceholderIDPrefix + "loser"
	if err := s.AddWorkstream("p1", domain.Workstream{
		ID: placeholderID, Name: "feature", Provider: "core",
		RuntimeState: domain.RuntimeCreating, Sessions: []domain.Session{},
	}); err != nil {
		t.Fatalf("placeholder add: %v", err)
	}
	// Stage the state the race produces: the placeholder was admitted, then the
	// winner landed under the same name, and the placeholder's create has just
	// failed.
	if err := s.ReplaceWorkstreams("p1", nil, []domain.Workstream{
		{ID: "ws-winner", Name: "feature", Provider: "core", WorkingDir: "/work/a", Sessions: []domain.Session{}},
		{ID: placeholderID, Name: "feature", Provider: "core", RuntimeState: domain.RuntimeCreating, Sessions: []domain.Session{}},
	}); err != nil {
		t.Fatalf("staging the race: %v", err)
	}

	errorWs := domain.Workstream{
		ID: placeholderID, Name: "feature", Provider: "core",
		RuntimeState: domain.RuntimeError, StatusMessage: `a workstream with this name already exists`,
		Sessions: []domain.Session{},
	}
	if err := s.UpdateWorkstream("p1", errorWs); err != nil {
		t.Fatalf("recording the failure on its placeholder: %v", err)
	}
	got, err := s.Workstream("p1", placeholderID)
	if err != nil {
		t.Fatalf("Workstream: %v", err)
	}
	if got.RuntimeState != domain.RuntimeError {
		t.Errorf("placeholder runtime state = %q, want it to have become the error record", got.RuntimeState)
	}
	if got.StatusMessage == "" {
		t.Error("the placeholder carries no explanation, so the spinner has nothing to replace it")
	}
}

// NameUser answers by the rule the doors enforce, so a provider picking a
// default name picks one they will let in, and quotes the workstream holding a
// name as it is spelled, not as asked.
func TestNameUserNamesTheWorkstreamHoldingAName(t *testing.T) {
	s := nameFixture(t)
	if err := add(t, s, "p1", "ws-a", "Feature", "/work/a"); err != nil {
		t.Fatalf("add: %v", err)
	}
	tests := []struct {
		name      string
		projectID string
		candidate string
		exceptIDs []string
		want      string
	}{
		{name: "the same name", projectID: "p1", candidate: "Feature", want: "Feature"},
		{name: "another case", projectID: "p1", candidate: " feature ", want: "Feature"},
		{name: "its own name", projectID: "p1", candidate: "feature", exceptIDs: []string{"ws-a"}, want: ""},
		{name: "another name", projectID: "p1", candidate: "other", want: ""},
		{name: "another project", projectID: "p2", candidate: "Feature", want: ""},
		{name: "no name", projectID: "p1", candidate: "", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := s.NameUser(tt.projectID, tt.candidate, tt.exceptIDs...); got != tt.want {
				t.Errorf("NameUser(%q, %q) = %q, want %q", tt.projectID, tt.candidate, got, tt.want)
			}
		})
	}
}
