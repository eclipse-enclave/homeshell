package store

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
	"github.com/eclipsesource/homeshell/daemon/internal/persist"
)

func decodeProjects(t *testing.T, data []byte) []domain.Project {
	t.Helper()
	var payload struct {
		Projects []domain.Project `json:"projects"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("payload is not valid JSON: %v", err)
	}
	return payload.Projects
}

func findProject(t *testing.T, projects []domain.Project, id string) domain.Project {
	t.Helper()
	for _, project := range projects {
		if project.ID == id {
			return project
		}
	}
	t.Fatalf("project %q not in payload", id)
	return domain.Project{}
}

func TestSeedDerivesStates(t *testing.T) {
	projects := decodeProjects(t, NewTestStore(t, Seed()).Snapshot().Data)
	wantSession := map[string]domain.SessionState{
		"proj-homeshell":     domain.SessionWaiting,
		"proj-webshop":       domain.SessionRunning,
		"proj-release-tools": domain.SessionDone,
		"proj-infra":         domain.SessionRunning,
		"proj-notes":         domain.SessionIdle,
	}
	if len(projects) != len(wantSession) {
		t.Fatalf("seed has %d projects, want %d", len(projects), len(wantSession))
	}
	for id, state := range wantSession {
		if got := findProject(t, projects, id).State; got != state {
			t.Errorf("project %q .State = %q, want %q", id, got, state)
		}
	}

	wantEffective := map[string]string{
		"proj-homeshell":     "waiting", // runtime running, defers to session
		"proj-webshop":       "running", // runtime running, defers to session
		"proj-release-tools": "done",    // runtime running > stopped, defers to session done
		"proj-infra":         "error",   // runtime error overrides running
		"proj-notes":         "idle",    // no workstreams
	}
	for id, eff := range wantEffective {
		if got := findProject(t, projects, id).Effective; got != eff {
			t.Errorf("project %q .Effective = %q, want %q", id, got, eff)
		}
	}
}

func TestEmptyCollectionsMarshalAsArrays(t *testing.T) {
	// Clients expect arrays, never null.
	data := string(NewTestStore(t, Seed()).Snapshot().Data)
	if strings.Contains(data, ":null") {
		t.Errorf("snapshot contains null collections: %s", data)
	}
	if !strings.Contains(data, `"workstreams":[]`) {
		t.Errorf("empty workstream list not marshaled as []: %s", data)
	}
}

func TestMutationsRecomputeAndBroadcast(t *testing.T) {
	s := NewTestStore(t, Seed())
	eventCh, cancel := s.Subscribe()
	defer cancel()

	session := domain.Session{ID: "sess-x", Title: "Demo", Agent: "Demo", State: domain.SessionRunning}
	if err := s.AddSession(DemoProjectID, DemoWorkstreamID, session); err != nil {
		t.Fatal(err)
	}
	event := receive(t, eventCh)
	if event.ID != 1 {
		t.Errorf("first event id = %d, want 1", event.ID)
	}
	project := findProject(t, decodeProjects(t, event.Data), DemoProjectID)
	if got := project.State; got != domain.SessionRunning {
		t.Errorf("after add: project session state = %q, want %q", got, domain.SessionRunning)
	}
	// The demo workstream is running, so effective defers to session aggregate.
	if got := project.Effective; got != "running" {
		t.Errorf("after add: project effective = %q, want %q", got, "running")
	}

	if err := s.SetSessionState(DemoProjectID, DemoWorkstreamID, "sess-x", domain.SessionDone); err != nil {
		t.Fatal(err)
	}
	event = receive(t, eventCh)
	if event.ID != 2 {
		t.Errorf("second event id = %d, want 2", event.ID)
	}
	if got := findProject(t, decodeProjects(t, event.Data), DemoProjectID).State; got != domain.SessionDone {
		t.Errorf("after done: project state = %q, want %q", got, domain.SessionDone)
	}

	if err := s.RemoveSession(DemoProjectID, DemoWorkstreamID, "sess-x"); err != nil {
		t.Fatal(err)
	}
	event = receive(t, eventCh)
	workstream := findProject(t, decodeProjects(t, event.Data), DemoProjectID).Workstreams[0]
	if len(workstream.Sessions) != 1 {
		t.Errorf("after remove: %d sessions, want the 1 seeded one", len(workstream.Sessions))
	}
}

func TestSetRuntimeState(t *testing.T) {
	s := NewTestStore(t, Seed())
	eventCh, cancel := s.Subscribe()
	defer cancel()

	if err := s.SetRuntimeState(DemoProjectID, DemoWorkstreamID, domain.RuntimeError); err != nil {
		t.Fatal(err)
	}
	event := receive(t, eventCh)
	project := findProject(t, decodeProjects(t, event.Data), DemoProjectID)
	ws := project.Workstreams[0]
	if ws.RuntimeState != domain.RuntimeError {
		t.Errorf("RuntimeState = %q, want %q", ws.RuntimeState, domain.RuntimeError)
	}
	if ws.Effective != "error" {
		t.Errorf("Effective = %q, want %q", ws.Effective, "error")
	}
	if project.Effective != "error" {
		t.Errorf("project.Effective = %q, want %q", project.Effective, "error")
	}
}

func TestSetRuntimeStateRejectsUnknownIDs(t *testing.T) {
	s := NewTestStore(t, Seed())
	if err := s.SetRuntimeState("nope", DemoWorkstreamID, domain.RuntimeRunning); err == nil {
		t.Error("unknown project not rejected")
	}
	if err := s.SetRuntimeState(DemoProjectID, "nope", domain.RuntimeRunning); err == nil {
		t.Error("unknown workstream not rejected")
	}
}

func TestSetStatusMessage(t *testing.T) {
	s := NewTestStore(t, Seed())
	eventCh, cancel := s.Subscribe()
	defer cancel()

	if err := s.SetStatusMessage(DemoProjectID, DemoWorkstreamID, "Building container image\u2026"); err != nil {
		t.Fatal(err)
	}
	event := receive(t, eventCh)
	project := findProject(t, decodeProjects(t, event.Data), DemoProjectID)
	for _, ws := range project.Workstreams {
		if ws.ID == DemoWorkstreamID {
			if ws.StatusMessage != "Building container image\u2026" {
				t.Errorf("StatusMessage = %q, want 'Building container image\u2026'", ws.StatusMessage)
			}
			return
		}
	}
	t.Fatal("workstream not found")
}

func TestSetStatusMessageRejectsUnknownIDs(t *testing.T) {
	s := NewTestStore(t, Seed())
	if err := s.SetStatusMessage("nope", DemoWorkstreamID, "x"); err == nil {
		t.Error("unknown project not rejected")
	}
	if err := s.SetStatusMessage(DemoProjectID, "nope", "x"); err == nil {
		t.Error("unknown workstream not rejected")
	}
}

func TestMutationsRejectUnknownIDs(t *testing.T) {
	s := NewTestStore(t, Seed())
	session := domain.Session{ID: "sess-x"}
	if err := s.AddSession("nope", DemoWorkstreamID, session); err == nil {
		t.Error("unknown project not rejected")
	}
	if err := s.AddSession(DemoProjectID, "nope", session); err == nil {
		t.Error("unknown workstream not rejected")
	}
	if err := s.SetSessionState(DemoProjectID, DemoWorkstreamID, "nope", domain.SessionDone); err == nil {
		t.Error("unknown session not rejected")
	}
	if err := s.RemoveSession(DemoProjectID, DemoWorkstreamID, "nope"); err == nil {
		t.Error("unknown session not rejected")
	}
}

func TestSlowSubscriberIsDropped(t *testing.T) {
	s := NewTestStore(t, Seed())
	eventCh, cancel := s.Subscribe()
	defer cancel()

	// Never drain: the buffer fills up and the store must close the channel
	// rather than block its mutations.
	session := domain.Session{ID: "sess-x", Title: "Demo", Agent: "Demo", State: domain.SessionRunning}
	if err := s.AddSession(DemoProjectID, DemoWorkstreamID, session); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < subscriberBuffer+4; i++ {
		state := domain.SessionRunning
		if i%2 == 0 {
			state = domain.SessionDone
		}
		if err := s.SetSessionState(DemoProjectID, DemoWorkstreamID, "sess-x", state); err != nil {
			t.Fatal(err)
		}
	}

	closed := false
	for range subscriberBuffer + 8 {
		select {
		case _, ok := <-eventCh:
			if !ok {
				closed = true
			}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for the channel to close")
		}
		if closed {
			break
		}
	}
	if !closed {
		t.Error("slow subscriber was not dropped")
	}
}

func receive(t *testing.T, eventCh <-chan Event) Event {
	t.Helper()
	select {
	case event, ok := <-eventCh:
		if !ok {
			t.Fatal("event channel closed unexpectedly")
		}
		return event
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for an event")
		return Event{}
	}
}

func TestAddProjectPersists(t *testing.T) {
	saver := &persist.MemorySaver{}
	s := New([]domain.Project{}, saver)

	proj := domain.Project{ID: "p1", Name: "alpha", Path: "/alpha", Workstreams: []domain.Workstream{}}
	if err := s.AddProject(&proj); err != nil {
		t.Fatal(err)
	}

	persisted := saver.Last.Projects
	if len(persisted) != 1 || persisted[0].ID != "p1" {
		t.Errorf("expected project p1 persisted, got %+v", persisted)
	}
}

func TestRemoveProjectPersists(t *testing.T) {
	saver := &persist.MemorySaver{}
	s := New([]domain.Project{}, saver)

	proj := domain.Project{ID: "p1", Name: "alpha", Path: "/alpha", Workstreams: []domain.Workstream{}}
	if err := s.AddProject(&proj); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RemoveProject("p1"); err != nil {
		t.Fatal(err)
	}

	if persisted := saver.Last.Projects; len(persisted) != 0 {
		t.Errorf("expected 0 projects after delete, got %d", len(persisted))
	}
}

func TestAddProject(t *testing.T) {
	s := NewTestStore(t, Seed())
	eventCh, cancel := s.Subscribe()
	defer cancel()

	project := domain.Project{
		ID:          "proj-new",
		Name:        "new-project",
		Path:        "/tmp/new-project",
		Workstreams: []domain.Workstream{},
	}
	if err := s.AddProject(&project); err != nil {
		t.Fatal(err)
	}
	event := receive(t, eventCh)
	projects := decodeProjects(t, event.Data)
	found := findProject(t, projects, "proj-new")
	if found.Name != "new-project" {
		t.Errorf("name = %q, want %q", found.Name, "new-project")
	}
	if found.Path != "/tmp/new-project" {
		t.Errorf("path = %q, want %q", found.Path, "/tmp/new-project")
	}
	if found.State != domain.SessionIdle {
		t.Errorf("state = %q, want %q", found.State, domain.SessionIdle)
	}
}

func TestAddProjectRejectsDuplicatePath(t *testing.T) {
	s := NewTestStore(t, Seed())
	project := domain.Project{
		ID:   "proj-dup",
		Name: "duplicate",
		Path: "~/Git/homeshell", // same path as seeded project
	}
	if err := s.AddProject(&project); err == nil {
		t.Error("duplicate path not rejected")
	}
}

func TestAddProjectNilWorkstreamsBecomesEmptySlice(t *testing.T) {
	s := NewTestStore(t, []domain.Project{})
	project := domain.Project{
		ID:          "proj-nil-ws",
		Name:        "nil-ws",
		Path:        "/tmp/nil-ws",
		Workstreams: nil,
	}
	if err := s.AddProject(&project); err != nil {
		t.Fatal(err)
	}
	data := string(s.Snapshot().Data)
	if strings.Contains(data, ":null") {
		t.Errorf("snapshot contains null after AddProject: %s", data)
	}
}

func TestRenameProject(t *testing.T) {
	s := NewTestStore(t, Seed())
	eventCh, cancel := s.Subscribe()
	defer cancel()

	if err := s.RenameProject(DemoProjectID, "renamed"); err != nil {
		t.Fatal(err)
	}
	event := receive(t, eventCh)
	found := findProject(t, decodeProjects(t, event.Data), DemoProjectID)
	if found.Name != "renamed" {
		t.Errorf("name = %q, want %q", found.Name, "renamed")
	}
}

func TestRenameProjectRejectsUnknownID(t *testing.T) {
	s := NewTestStore(t, Seed())
	if err := s.RenameProject("nope", "x"); err == nil {
		t.Error("unknown project not rejected")
	}
}

func TestRemoveProject(t *testing.T) {
	s := NewTestStore(t, Seed())
	eventCh, cancel := s.Subscribe()
	defer cancel()

	initial := decodeProjects(t, s.Snapshot().Data)
	count := len(initial)

	if _, err := s.RemoveProject(DemoProjectID); err != nil {
		t.Fatal(err)
	}
	event := receive(t, eventCh)
	projects := decodeProjects(t, event.Data)
	if len(projects) != count-1 {
		t.Fatalf("got %d projects, want %d", len(projects), count-1)
	}
	for _, project := range projects {
		if project.ID == DemoProjectID {
			t.Error("removed project still present")
		}
	}
}

func TestRemoveProjectRejectsUnknownID(t *testing.T) {
	s := NewTestStore(t, Seed())
	if _, err := s.RemoveProject("nope"); err == nil {
		t.Error("unknown project not rejected")
	}
}

func TestRenameProjectPersists(t *testing.T) {
	saver := &persist.MemorySaver{}
	s := New([]domain.Project{}, saver)

	proj := domain.Project{ID: "p1", Name: "old", Path: "/p", Workstreams: []domain.Workstream{}}
	if err := s.AddProject(&proj); err != nil {
		t.Fatal(err)
	}
	if err := s.RenameProject("p1", "new"); err != nil {
		t.Fatal(err)
	}

	if persisted := saver.Last.Projects; len(persisted) != 1 || persisted[0].Name != "new" {
		t.Errorf("expected renamed project, got %+v", persisted)
	}
}

// fakeDetector reports git-backing from an in-memory set of paths, so store
// tests exercise detection without shelling out to git.
type fakeDetector struct {
	backed map[string]bool
	calls  int
}

func (fake *fakeDetector) Detect(path string) bool {
	fake.calls++
	return fake.backed[path]
}

func TestAddProjectDetectsGitBacked(t *testing.T) {
	detector := &fakeDetector{backed: map[string]bool{"/repo": true}}
	s := NewTestStore(t, []domain.Project{}, WithGitDetector(detector))

	gitProject := domain.Project{ID: "proj-git", Name: "git", Path: "/repo"}
	if err := s.AddProject(&gitProject); err != nil {
		t.Fatal(err)
	}
	if !gitProject.GitBacked {
		t.Error("detected project's GitBacked not set on the passed struct")
	}
	if got := findProject(t, decodeProjects(t, s.Snapshot().Data), "proj-git"); !got.GitBacked {
		t.Error("git-backed project stored with GitBacked=false")
	}

	plainProject := domain.Project{ID: "proj-plain", Name: "plain", Path: "/plain"}
	if err := s.AddProject(&plainProject); err != nil {
		t.Fatal(err)
	}
	if plainProject.GitBacked {
		t.Error("plain project reported as git-backed")
	}
}

func TestAddProjectWithoutDetectorLeavesFlagAuthored(t *testing.T) {
	// No detector: the authored flag must be preserved (the demo seed relies
	// on this, since its paths are illustrative and must not be probed).
	s := NewTestStore(t, []domain.Project{})
	project := domain.Project{ID: "proj-authored", Name: "authored", Path: "/x", GitBacked: true}
	if err := s.AddProject(&project); err != nil {
		t.Fatal(err)
	}
	if got := findProject(t, decodeProjects(t, s.Snapshot().Data), "proj-authored"); !got.GitBacked {
		t.Error("authored GitBacked flag was overwritten without a detector")
	}
}

func TestAddWorkstream(t *testing.T) {
	s := NewTestStore(t, Seed())
	eventCh, cancel := s.Subscribe()
	defer cancel()

	ws := domain.Workstream{
		ID: "ws-new", Name: "new-ws", Provider: "test",
		RuntimeState: domain.RuntimeCreating,
		Sessions:     []domain.Session{},
	}
	if err := s.AddWorkstream(DemoProjectID, ws); err != nil {
		t.Fatal(err)
	}
	event := receive(t, eventCh)
	project := findProject(t, decodeProjects(t, event.Data), DemoProjectID)
	found := false
	for _, w := range project.Workstreams {
		if w.ID == "ws-new" {
			found = true
			if w.RuntimeState != domain.RuntimeCreating {
				t.Errorf("RuntimeState = %q, want creating", w.RuntimeState)
			}
		}
	}
	if !found {
		t.Error("added workstream not found in project")
	}
}

func TestAddWorkstreamRejectsUnknownProject(t *testing.T) {
	s := NewTestStore(t, Seed())
	ws := domain.Workstream{ID: "ws-new"}
	if err := s.AddWorkstream("nope", ws); err == nil {
		t.Error("unknown project not rejected")
	}
}

func TestAddWorkstreamRejectsDuplicateWorkingDir(t *testing.T) {
	s := NewTestStore(t, Seed())
	first := domain.Workstream{ID: "ws-a", Name: "a", Provider: "core", WorkingDir: "/work/x", Sessions: []domain.Session{}}
	if err := s.AddWorkstream(DemoProjectID, first); err != nil {
		t.Fatalf("first add: %v", err)
	}
	// Same working directory (even with a different id) is rejected.
	dup := domain.Workstream{ID: "ws-b", Name: "b", Provider: "core", WorkingDir: "/work/x", Sessions: []domain.Session{}}
	err := s.AddWorkstream(DemoProjectID, dup)
	if !errors.Is(err, ErrDuplicateWorkingDir) {
		t.Errorf("duplicate WorkingDir err = %v, want ErrDuplicateWorkingDir", err)
	}
	// The refusal says what is wrong once, naming the workstream already there.
	if want := `working directory taken: "a" already works in /work/x`; err == nil || err.Error() != want {
		t.Errorf("duplicate WorkingDir err = %v, want %q", err, want)
	}
	// A different directory is fine.
	other := domain.Workstream{ID: "ws-c", Name: "c", Provider: "core", WorkingDir: "/work/y", Sessions: []domain.Session{}}
	if err := s.AddWorkstream(DemoProjectID, other); err != nil {
		t.Errorf("distinct WorkingDir add: %v", err)
	}
	// Placeholders (empty WorkingDir) never collide.
	p1 := domain.Workstream{ID: "creating-1", WorkingDir: "", Sessions: []domain.Session{}}
	p2 := domain.Workstream{ID: "creating-2", WorkingDir: "", Sessions: []domain.Session{}}
	if err := s.AddWorkstream(DemoProjectID, p1); err != nil {
		t.Errorf("first placeholder add: %v", err)
	}
	if err := s.AddWorkstream(DemoProjectID, p2); err != nil {
		t.Errorf("second placeholder add: %v", err)
	}
}

func TestWorkingDirUser(t *testing.T) {
	s := NewTestStore(t, Seed())
	if err := s.AddWorkstream(DemoProjectID, domain.Workstream{ID: "ws-a", Name: "a", Provider: "core", WorkingDir: "/work/x", Sessions: []domain.Session{}}); err != nil {
		t.Fatalf("add: %v", err)
	}
	tests := []struct {
		project, dir, want string
	}{
		{project: DemoProjectID, dir: "/work/x", want: "a"},
		{project: DemoProjectID, dir: "/work/x/", want: "a"},
		{project: DemoProjectID, dir: "/work/y", want: ""},
		{project: DemoProjectID, dir: "", want: ""},
		{project: "nope", dir: "/work/x", want: ""},
	}
	for _, tt := range tests {
		if got := s.WorkingDirUser(tt.project, tt.dir); got != tt.want {
			t.Errorf("WorkingDirUser(%q, %q) = %q, want %q", tt.project, tt.dir, got, tt.want)
		}
	}
}

// The project folder is taken by whatever is shown in it: a pending or failed
// in-place create too, though it has no folder yet.
func TestTheProjectFolderIsTakenByWhatIsShownInIt(t *testing.T) {
	for _, failed := range []bool{false, true} {
		s := NewTestStore(t, []domain.Project{{ID: "p1", Name: "p", Path: "/work/p"}})
		placeholder := domain.Workstream{ID: "creating-1", Name: "here", StrategyID: "in-place",
			CreateFailed: failed, Sessions: []domain.Session{}}
		if err := s.AddWorkstream("p1", placeholder); err != nil {
			t.Fatalf("add placeholder: %v", err)
		}
		if got := s.WorkingDirUser("p1", "/work/p/"); got != "here" {
			t.Errorf("failed=%v: WorkingDirUser(project folder) = %q, want %q", failed, got, "here")
		}
		inPlace := domain.Workstream{ID: "ws-a", Name: "a", WorkingDir: "/work/p", Sessions: []domain.Session{}}
		if err := s.AddWorkstream("p1", inPlace); !errors.Is(err, ErrDuplicateWorkingDir) {
			t.Errorf("failed=%v: add in the project folder err = %v, want ErrDuplicateWorkingDir", failed, err)
		}
		elsewhere := domain.Workstream{ID: "ws-b", Name: "b", WorkingDir: "/work/b", Sessions: []domain.Session{}}
		if err := s.AddWorkstream("p1", elsewhere); err != nil {
			t.Errorf("failed=%v: add elsewhere: %v", failed, err)
		}
	}
}

func TestRemoveWorkstream(t *testing.T) {
	s := NewTestStore(t, Seed())
	eventCh, cancel := s.Subscribe()
	defer cancel()

	if err := s.RemoveWorkstream(DemoProjectID, DemoWorkstreamID); err != nil {
		t.Fatal(err)
	}
	event := receive(t, eventCh)
	project := findProject(t, decodeProjects(t, event.Data), DemoProjectID)
	for _, w := range project.Workstreams {
		if w.ID == DemoWorkstreamID {
			t.Error("removed workstream still present")
		}
	}
}

func TestRemoveWorkstreamRejectsUnknownIDs(t *testing.T) {
	s := NewTestStore(t, Seed())
	if err := s.RemoveWorkstream("nope", DemoWorkstreamID); err == nil {
		t.Error("unknown project not rejected")
	}
	if err := s.RemoveWorkstream(DemoProjectID, "nope"); err == nil {
		t.Error("unknown workstream not rejected")
	}
}

func TestUpdateWorkstream(t *testing.T) {
	s := NewTestStore(t, Seed())
	eventCh, cancel := s.Subscribe()
	defer cancel()

	updated := domain.Workstream{
		ID: DemoWorkstreamID, Name: "updated-name", Provider: "Remote",
		RuntimeState:  domain.RuntimeError,
		StatusMessage: "something went wrong",
		Sessions:      []domain.Session{},
	}
	if err := s.UpdateWorkstream(DemoProjectID, updated); err != nil {
		t.Fatal(err)
	}
	event := receive(t, eventCh)
	project := findProject(t, decodeProjects(t, event.Data), DemoProjectID)
	var found *domain.Workstream
	for i := range project.Workstreams {
		if project.Workstreams[i].ID == DemoWorkstreamID {
			found = &project.Workstreams[i]
		}
	}
	if found == nil {
		t.Fatal("updated workstream not found")
	}
	if found.Name != "updated-name" {
		t.Errorf("Name = %q, want updated-name", found.Name)
	}
	if found.RuntimeState != domain.RuntimeError {
		t.Errorf("RuntimeState = %q, want error", found.RuntimeState)
	}
	if found.StatusMessage != "something went wrong" {
		t.Errorf("StatusMessage = %q, want 'something went wrong'", found.StatusMessage)
	}
}

func TestUpdateWorkstreamRejectsUnknownIDs(t *testing.T) {
	s := NewTestStore(t, Seed())
	ws := domain.Workstream{ID: "nope"}
	if err := s.UpdateWorkstream("nope", ws); err == nil {
		t.Error("unknown project not rejected")
	}
	if err := s.UpdateWorkstream(DemoProjectID, ws); err == nil {
		t.Error("unknown workstream not rejected")
	}
}

func TestRefreshGitBacked(t *testing.T) {
	detector := &fakeDetector{backed: map[string]bool{}}
	s := NewTestStore(t, []domain.Project{}, WithGitDetector(detector))

	project := domain.Project{ID: "proj-refresh", Name: "refresh", Path: "/repo"}
	if err := s.AddProject(&project); err != nil {
		t.Fatal(err)
	}
	if project.GitBacked {
		t.Fatal("project should not be git-backed before git init")
	}

	// Simulate `git init` happening in the directory after the project was added.
	detector.backed["/repo"] = true

	eventCh, cancel := s.Subscribe()
	defer cancel()
	if err := s.RefreshGitBacked("proj-refresh"); err != nil {
		t.Fatal(err)
	}
	event := receive(t, eventCh)
	if got := findProject(t, decodeProjects(t, event.Data), "proj-refresh"); !got.GitBacked {
		t.Error("RefreshGitBacked did not mark the project git-backed")
	}
}

func TestRefreshGitBackedRejectsUnknownID(t *testing.T) {
	s := NewTestStore(t, []domain.Project{}, WithGitDetector(&fakeDetector{}))
	if err := s.RefreshGitBacked("nope"); err == nil {
		t.Error("unknown project not rejected")
	}
}
func TestReplaceProjectionPreservesSessionProviderData(t *testing.T) {
	providerData := json.RawMessage(`{"pid":42}`)
	seed := []domain.Project{{
		ID:   "p1",
		Name: "test",
		Path: "/tmp/test",
		Workstreams: []domain.Workstream{{
			ID:         "ws1",
			Name:       "main",
			Provider:   "test",
			StrategyID: "in-place",
			Sessions: []domain.Session{{
				ID:           "s1",
				Title:        "session",
				Agent:        "agent",
				State:        domain.SessionRunning,
				ProviderData: providerData,
			}},
		}},
	}}

	store := NewTestStore(t, seed)

	// Replace workstreams with the same set — simulates a refresh cycle.
	replacement := seed[0].Workstreams
	if err := store.ReplaceProjection("p1", nil, replacement, nil); err != nil {
		t.Fatal(err)
	}

	// Read back from the store snapshot and verify provider_data survived.
	projects := decodeProjects(t, store.Snapshot().Data)
	project := findProject(t, projects, "p1")
	if len(project.Workstreams) != 1 || len(project.Workstreams[0].Sessions) != 1 {
		t.Fatalf("unexpected structure: %d workstreams", len(project.Workstreams))
	}
	session := project.Workstreams[0].Sessions[0]
	if string(session.ProviderData) != string(providerData) {
		t.Errorf("ProviderData = %s, want %s", session.ProviderData, providerData)
	}
}

// The persisted projection is the whole store minus the daemon's own creation
// bookkeeping: a real workstream is written whichever door it came through, a
// placeholder never is, and a projection that changes nothing yields the same
// snapshot the saver can skip.
func TestSnapshotHoldsRealWorkstreamsAndNoPlaceholders(t *testing.T) {
	saver := &persist.MemorySaver{}
	s := New([]domain.Project{{ID: "p1", Name: "p1", Path: "/p1", Workstreams: []domain.Workstream{}}}, saver)

	placeholder := domain.Workstream{ID: PlaceholderIDPrefix + "abc", Name: "feature/x", Provider: "core", RuntimeState: domain.RuntimeCreating}
	if err := s.AddWorkstream("p1", placeholder); err != nil {
		t.Fatalf("AddWorkstream(placeholder): %v", err)
	}
	if got := saver.Last.Projects[0].Workstreams; len(got) != 0 {
		t.Errorf("a placeholder reached the snapshot: %+v", got)
	}
	real := domain.Workstream{ID: "ws-1", Name: "feature/x", Provider: "core", StrategyID: "in-place", WorkingDir: "/p1",
		Sessions: []domain.Session{{ID: "sess-1", Title: "t", Agent: "claude", Provider: "enclave", State: domain.SessionRunning}}}
	if err := s.PromoteWorkstream("p1", placeholder.ID, real); err != nil {
		t.Fatalf("PromoteWorkstream: %v", err)
	}
	got := saver.Last.Projects[0].Workstreams
	if len(got) != 1 || got[0].ID != "ws-1" || len(got[0].Sessions) != 1 || got[0].Sessions[0].ID != "sess-1" {
		t.Fatalf("snapshot after promotion = %+v; want the real workstream with its session", got)
	}
	if err := s.AddWorkstream("p1", domain.Workstream{ID: "ws-2", Name: "other", Provider: "core", StrategyID: "in-place", WorkingDir: "/p1/other"}); err != nil {
		t.Fatalf("AddWorkstream(real): %v", err)
	}
	if got := saver.Last.Projects[0].Workstreams; len(got) != 2 || got[1].ID != "ws-2" {
		t.Errorf("a real workstream added through AddWorkstream is not persisted: %+v", got)
	}

	// Re-projecting the same set is not even handed to the saver: a reconcile
	// tick that changes nothing must not encode the whole state. The first
	// projection after a promotion still drops the placeholder link, so the
	// quiet tick is the second one.
	same := func() []domain.Workstream {
		return []domain.Workstream{cloneWorkstream(real), {ID: "ws-2", Name: "other", Provider: "core", StrategyID: "in-place", WorkingDir: "/p1/other"}}
	}
	if err := s.ReplaceProjection("p1", nil, same(), nil); err != nil {
		t.Fatalf("ReplaceProjection: %v", err)
	}
	saves := saver.Saves
	if err := s.ReplaceProjection("p1", nil, same(), nil); err != nil {
		t.Fatalf("ReplaceProjection(again): %v", err)
	}
	if saver.Saves != saves {
		t.Errorf("an unchanged projection saved %d times, want none", saver.Saves-saves)
	}

	// A side table can change while the set does not: a custom title whose
	// session the providers no longer list is pruned, and that has to reach disk.
	if err := s.RenameSession("p1", "ws-1", "sess-1", "mine"); err != nil {
		t.Fatalf("RenameSession: %v", err)
	}
	if _, ok := saver.Last.SessionTitles["sess-1"]; !ok {
		t.Fatal("the rename was not persisted")
	}
	withoutSession := same()
	withoutSession[0].Sessions = nil
	if err := s.ReplaceProjection("p1", nil, withoutSession, nil); err != nil {
		t.Fatalf("ReplaceProjection(without session): %v", err)
	}
	if _, ok := saver.Last.SessionTitles["sess-1"]; ok {
		t.Error("a title whose session is gone survived in the snapshot")
	}
	if got := saver.Last.Projects[0].Workstreams[0].Sessions; len(got) != 0 {
		t.Errorf("the removed session is still in the snapshot: %+v", got)
	}
}

// A write that failed is retried on request, and only while it is still failed:
// otherwise the change would wait for an unrelated one.
func TestRetryUnsavedWrite(t *testing.T) {
	saver := &persist.MemorySaver{}
	s := New([]domain.Project{{ID: "p1", Name: "alpha", Path: "/alpha", Workstreams: []domain.Workstream{}}}, saver)

	s.RetryUnsavedWrite()
	if saver.Saves != 0 {
		t.Fatalf("a retry with nothing failed saved %d times, want none", saver.Saves)
	}

	// Removing the last project leaves nothing for a reconcile to project, so
	// only the retry can bring the write back.
	saver.Err = os.ErrPermission
	if _, err := s.RemoveProject("p1"); err != nil {
		t.Fatalf("RemoveProject: %v", err)
	}
	saver.Err = nil
	s.RetryUnsavedWrite()
	if saver.Saves != 1 || len(saver.Last.Projects) != 0 {
		t.Fatalf("after the retry: %d saves, projects %+v; want one save with no projects", saver.Saves, saver.Last.Projects)
	}

	s.RetryUnsavedWrite()
	if saver.Saves != 1 {
		t.Errorf("a retry after a successful one saved again (%d saves)", saver.Saves)
	}
}

// A mutation whose write fails is still live, so subscribers hear about it;
// otherwise they would show the old state until an unrelated change broadcast.
func TestFailedWriteStillBroadcasts(t *testing.T) {
	session := domain.Session{ID: "sess-1", Title: "t", Agent: "claude", Provider: "enclave", State: domain.SessionRunning}
	tests := []struct {
		name   string
		mutate func(s *Store) error
		want   string
	}{
		{"AddSession", func(s *Store) error { return s.AddSession("p1", "ws-1", session) }, "sess-1"},
		{"PromoteEphemeralSession", func(s *Store) error {
			return s.PromoteEphemeralSession("p1", "ws-1", StartingIDPrefix+"x", session, domain.LaunchConfig{})
		}, "sess-1"},
		{"RenameSession", func(s *Store) error { return s.RenameSession("p1", "ws-1", "sess-0", "renamed") }, "renamed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			saver := &persist.MemorySaver{}
			s := New([]domain.Project{{ID: "p1", Name: "p1", Path: "/p1", Workstreams: []domain.Workstream{{
				ID: "ws-1", Name: "ws", Provider: "core", WorkingDir: "/p1", Sessions: []domain.Session{
					{ID: "sess-0", Title: "old", Agent: "claude", Provider: "enclave"},
					{ID: StartingIDPrefix + "x", Title: "starting", Agent: "claude", Provider: "enclave"},
				},
			}}}}, saver)
			eventCh, cancel := s.Subscribe()
			defer cancel()

			saver.Err = os.ErrPermission
			if err := tt.mutate(s); err == nil {
				t.Fatal("the mutation reported success with a failing saver")
			}
			if event := receive(t, eventCh); !strings.Contains(string(event.Data), tt.want) {
				t.Errorf("broadcast does not carry %q: %s", tt.want, event.Data)
			}
		})
	}
}

// A store restored from its own snapshot serves the same projects, with every
// session idle, and persists through the saver it was given.
func TestRestoreRoundTrips(t *testing.T) {
	saver := &persist.MemorySaver{}
	s := New([]domain.Project{}, saver)
	project := domain.Project{ID: "p1", Name: "alpha", Path: "/alpha", GitBacked: true}
	if err := s.AddProject(&project); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceProjection("p1", nil, []domain.Workstream{{ID: "ws-1", Name: "ws", Provider: "core", StrategyID: "in-place", WorkingDir: "/alpha",
		Sessions: []domain.Session{{ID: "sess-1", Title: "t", Agent: "claude", Provider: "enclave", State: domain.SessionRunning}}}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.RenameSession("p1", "ws-1", "sess-1", "named"); err != nil {
		t.Fatal(err)
	}

	again := &persist.MemorySaver{}
	restored := Restore(saver.Last, again)
	projects := restored.Projects()
	if len(projects) != 1 || projects[0].Name != "alpha" || !projects[0].GitBacked || len(projects[0].Workstreams) != 1 {
		t.Fatalf("restored projects = %+v", projects)
	}
	session := projects[0].Workstreams[0].Sessions[0]
	if session.State != domain.SessionIdle || session.Title != "named" {
		t.Errorf("restored session = %+v; want idle, under its custom title", session)
	}
	if err := restored.RenameProject("p1", "beta"); err != nil {
		t.Fatal(err)
	}
	if again.Saves != 1 || again.Last.Projects[0].Name != "beta" {
		t.Errorf("the restored store did not persist through its saver: %d saves, %+v", again.Saves, again.Last.Projects)
	}
}

// endedAtWorkstream is a workstream holding the one session s1 in state, for the
// EndedAt tests.
func endedAtWorkstream(state domain.SessionState) domain.Workstream {
	return domain.Workstream{ID: "ws1", Name: "main", Provider: "test", StrategyID: "in-place",
		Sessions: []domain.Session{{ID: "s1", Title: "session", Agent: "agent", Provider: "test", State: state}}}
}

// observeEndedAt reports state for s1 and returns the EndedAt the store keeps.
func observeEndedAt(t *testing.T, s *Store, state domain.SessionState) *time.Time {
	t.Helper()
	if err := s.ReplaceProjection("p1", nil, []domain.Workstream{endedAtWorkstream(state)}, nil); err != nil {
		t.Fatal(err)
	}
	return s.Projects()[0].Workstreams[0].Sessions[0].EndedAt
}

func sameTime(got, want *time.Time) bool {
	return (got == nil) == (want == nil) && (got == nil || got.Equal(*want))
}

var (
	persistedEnd = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	stampClock   = time.Date(2026, 6, 7, 8, 9, 10, 0, time.UTC)
)

func TestReplaceProjectionStampsEndedAt(t *testing.T) {
	tests := []struct {
		name      string
		before    domain.SessionState // "": the session is new to this projection
		beforeEnd *time.Time
		after     domain.SessionState
		reported  *time.Time // the end the provider reports
		want      *time.Time
	}{
		{"running to stopped", domain.SessionRunning, nil, domain.SessionStopped, nil, &stampClock},
		{"idle to error", domain.SessionIdle, nil, domain.SessionError, nil, &stampClock},
		{"stopped stays stopped", domain.SessionStopped, &persistedEnd, domain.SessionStopped, nil, &persistedEnd},
		{"stopped to running", domain.SessionStopped, &persistedEnd, domain.SessionRunning, nil, nil},
		{"first seen done", "", nil, domain.SessionDone, nil, nil},
		{"first seen done keeps its reported end", "", nil, domain.SessionDone, &persistedEnd, &persistedEnd},
		{"done with an unknown end stays unknown", domain.SessionDone, nil, domain.SessionDone, nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			project := domain.Project{ID: "p1", Name: "test", Path: "/tmp/test", Workstreams: []domain.Workstream{
				{ID: "ws1", Name: "main", Provider: "test", StrategyID: "in-place"},
			}}
			if tt.before != "" {
				project.Workstreams[0] = endedAtWorkstream(tt.before)
				project.Workstreams[0].Sessions[0].EndedAt = tt.beforeEnd
			}
			s := NewTestStore(t, []domain.Project{project})
			s.now = func() time.Time { return stampClock }
			workstream := endedAtWorkstream(tt.after)
			workstream.Sessions[0].EndedAt = tt.reported
			if err := s.ReplaceProjection("p1", nil, []domain.Workstream{workstream}, nil); err != nil {
				t.Fatal(err)
			}
			if got := s.Projects()[0].Workstreams[0].Sessions[0].EndedAt; !sameTime(got, tt.want) {
				t.Errorf("EndedAt = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEndedAtStampDoesNotMove(t *testing.T) {
	s := NewTestStore(t, []domain.Project{{ID: "p1", Name: "test", Path: "/tmp/test",
		Workstreams: []domain.Workstream{endedAtWorkstream(domain.SessionRunning)}}})
	s.now = func() time.Time { return stampClock }
	observeEndedAt(t, s, domain.SessionStopped)

	s.now = func() time.Time { return stampClock.Add(time.Hour) }
	events, cancel := s.Subscribe()
	defer cancel()
	if got := observeEndedAt(t, s, domain.SessionStopped); !sameTime(got, &stampClock) {
		t.Errorf("EndedAt after a later identical report = %v, want %v", got, stampClock)
	}
	select {
	case event := <-events:
		t.Errorf("an identical ReplaceProjection broadcast %q", event.Type)
	default:
	}
}

// A restored session is an idle placeholder until a provider reports on it, so its
// first report says nothing about when it ended: the persisted time stands.
func TestRestoredSessionKeepsItsPersistedEnd(t *testing.T) {
	restore := func(t *testing.T, endedAt *time.Time) *Store {
		t.Helper()
		workstream := endedAtWorkstream(domain.SessionIdle)
		record := persist.RecordWorkstream(workstream)
		session := persist.RecordSession(workstream.Sessions[0])
		session.EndedAt = endedAt
		record.Sessions = []persist.SessionRecord{session}
		s := Restore(persist.Snapshot{Projects: []persist.ProjectRecord{{
			ID: "p1", Name: "test", Path: "/tmp/test", Workstreams: []persist.WorkstreamRecord{record},
		}}}, &persist.MemorySaver{})
		s.now = func() time.Time { return stampClock }
		return s
	}

	t.Run("persisted end kept on the first terminal report", func(t *testing.T) {
		if got := observeEndedAt(t, restore(t, &persistedEnd), domain.SessionStopped); !sameTime(got, &persistedEnd) {
			t.Errorf("EndedAt = %v, want %v", got, persistedEnd)
		}
	})
	t.Run("no end claimed without a persisted one", func(t *testing.T) {
		if got := observeEndedAt(t, restore(t, nil), domain.SessionStopped); got != nil {
			t.Errorf("EndedAt = %v, want none", got)
		}
	})
	t.Run("stamped once a report has shown it live", func(t *testing.T) {
		s := restore(t, &persistedEnd)
		observeEndedAt(t, s, domain.SessionRunning)
		if got := observeEndedAt(t, s, domain.SessionStopped); !sameTime(got, &stampClock) {
			t.Errorf("EndedAt = %v, want %v", got, stampClock)
		}
	})
	t.Run("a carried-over placeholder is not a report", func(t *testing.T) {
		s := restore(t, &persistedEnd)
		// What the reconciler sends for a session whose provider did not report: the
		// stored session as is, with the projection's derived fields filled in.
		carried := s.Projects()[0].Workstreams
		carried[0].Sessions[0].Restartable = true
		if err := s.ReplaceProjection("p1", nil, carried, nil); err != nil {
			t.Fatal(err)
		}
		if got := observeEndedAt(t, s, domain.SessionStopped); !sameTime(got, &persistedEnd) {
			t.Errorf("EndedAt = %v, want %v", got, persistedEnd)
		}
	})
	t.Run("a stamp survives a restart", func(t *testing.T) {
		saver := &persist.MemorySaver{}
		s := New([]domain.Project{{ID: "p1", Name: "test", Path: "/tmp/test",
			Workstreams: []domain.Workstream{endedAtWorkstream(domain.SessionRunning)}}}, saver)
		s.now = func() time.Time { return stampClock }
		observeEndedAt(t, s, domain.SessionStopped)
		restored := Restore(saver.Last, &persist.MemorySaver{})
		if got := restored.Projects()[0].Workstreams[0].Sessions[0].EndedAt; !sameTime(got, &stampClock) {
			t.Errorf("restored EndedAt = %v, want %v", got, stampClock)
		}
	})
}

// A projection is derived from a read of the store. What changed in the store
// since that read is newer than the projection and is kept; what nobody changed
// is the projection's to settle.
func TestReplaceProjectionKeepsChangesSinceTheRead(t *testing.T) {
	seed := func() []domain.Project {
		return []domain.Project{{ID: "p1", Path: "/p1", Workstreams: []domain.Workstream{{
			ID: "ws1", Name: "feature", Branch: "feature", WorkingDir: "/p1", StatusMessage: "Restarting failed",
			Sessions: []domain.Session{
				{ID: "s1", Provider: "prov", State: domain.SessionRunning},
				{ID: "s2", Provider: "prov", State: domain.SessionRunning},
			},
		}}}}
	}
	ws1 := func(t *testing.T, s *Store) domain.Workstream {
		t.Helper()
		ws, err := s.Workstream("p1", "ws1")
		if err != nil {
			t.Fatal(err)
		}
		return ws
	}
	tests := []struct {
		name   string
		change func(t *testing.T, s *Store)
		check  func(t *testing.T, s *Store)
	}{
		{
			name:   "nothing changed: the projection settles",
			change: func(*testing.T, *Store) {},
			check: func(t *testing.T, s *Store) {
				if ws := ws1(t, s); ws.Name != "main" || ws.Branch != "main" || len(ws.Sessions) != 2 {
					t.Errorf("workstream = %q on %q with %d sessions, want main on main with 2", ws.Name, ws.Branch, len(ws.Sessions))
				}
			},
		},
		{
			name: "a rename is kept",
			change: func(t *testing.T, s *Store) {
				ws := ws1(t, s)
				ws.Name = "my work"
				if err := s.UpdateWorkstream("p1", ws); err != nil {
					t.Fatal(err)
				}
			},
			check: func(t *testing.T, s *Store) {
				if ws := ws1(t, s); ws.Name != "my work" {
					t.Errorf("name = %q, want my work", ws.Name)
				}
			},
		},
		{
			name: "a dismissed status message stays dismissed",
			change: func(t *testing.T, s *Store) {
				if err := s.SetStatusMessage("p1", "ws1", ""); err != nil {
					t.Fatal(err)
				}
			},
			check: func(t *testing.T, s *Store) {
				if ws := ws1(t, s); ws.StatusMessage != "" {
					t.Errorf("status = %q, want it dismissed", ws.StatusMessage)
				}
			},
		},
		{
			name: "an added workstream stays, and its name is not given",
			change: func(t *testing.T, s *Store) {
				if err := s.AddWorkstream("p1", domain.Workstream{ID: "ws2", Name: "main", WorkingDir: "/p1/other"}); err != nil {
					t.Fatal(err)
				}
			},
			check: func(t *testing.T, s *Store) {
				if _, err := s.Workstream("p1", "ws2"); err != nil {
					t.Errorf("Workstream(ws2) err = %v, want the added workstream kept", err)
				}
				if ws := ws1(t, s); ws.Name != "feature" || ws.Branch != "main" {
					t.Errorf("workstream = %q on %q, want feature on main", ws.Name, ws.Branch)
				}
			},
		},
		{
			name: "a removed workstream stays removed",
			change: func(t *testing.T, s *Store) {
				if err := s.RemoveWorkstream("p1", "ws1"); err != nil {
					t.Fatal(err)
				}
			},
			check: func(t *testing.T, s *Store) {
				if ws, err := s.Workstream("p1", "ws1"); err == nil {
					t.Errorf("workstream = %+v, want it to stay removed", ws)
				}
			},
		},
		{
			name: "a removed session stays removed",
			change: func(t *testing.T, s *Store) {
				if err := s.RemoveSession("p1", "ws1", "s2"); err != nil {
					t.Fatal(err)
				}
			},
			check: func(t *testing.T, s *Store) {
				if ws := ws1(t, s); len(ws.Sessions) != 1 || ws.Sessions[0].ID != "s1" {
					t.Errorf("sessions = %+v, want s1 only", ws.Sessions)
				}
			},
		},
		{
			name: "a session's state set meanwhile is kept",
			change: func(t *testing.T, s *Store) {
				if err := s.SetSessionState("p1", "ws1", "s1", domain.SessionStopped); err != nil {
					t.Fatal(err)
				}
			},
			check: func(t *testing.T, s *Store) {
				if ws := ws1(t, s); ws.Sessions[0].State != domain.SessionStopped {
					t.Errorf("s1 state = %q, want stopped", ws.Sessions[0].State)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewTestStore(t, seed())
			read := s.Projects()[0].Workstreams
			projected := s.Projects()[0].Workstreams
			projected[0].Name, projected[0].Branch = "main", "main"
			tt.change(t, s)
			if err := s.ReplaceProjection("p1", read, projected, nil); err != nil {
				t.Fatalf("ReplaceProjection: %v", err)
			}
			tt.check(t, s)
		})
	}
}

func replacementSeed() []domain.Project {
	return []domain.Project{{ID: "p1", Name: "test", Path: "/tmp/test", Workstreams: []domain.Workstream{{
		ID: "ws1", Name: "main", Provider: "test", StrategyID: "in-place",
		Sessions: []domain.Session{{ID: "old", Title: "session", Agent: "agent", State: domain.SessionRunning}},
	}}}}
}

// listing is a workstream listing the given sessions, as a provider's projection would.
func listing(sessions ...domain.Session) []domain.Workstream {
	return []domain.Workstream{{ID: "ws1", Name: "main", Provider: "test", StrategyID: "in-place", Sessions: sessions}}
}

func session(id, replaces string) domain.Session {
	return domain.Session{ID: id, Title: "session", Agent: "agent", State: domain.SessionRunning, Replaces: replaces}
}

// The session that replaces another takes over its title and launch config, a
// title given while the pass that found the replacement ran included.
func TestReplacingSessionTakesOverTitleAndLaunchConfig(t *testing.T) {
	for _, renamedDuringPass := range []bool{false, true} {
		store := NewTestStore(t, replacementSeed())
		launch := domain.LaunchConfig{ProviderID: "enclave", OptionID: "codex", Config: json.RawMessage(`{"x":1}`)}
		if err := store.SetLaunchConfig("p1", "old", launch); err != nil {
			t.Fatal(err)
		}
		var read []domain.Workstream
		if renamedDuringPass {
			read = store.Projects()[0].Workstreams
		}
		if err := store.RenameSession("p1", "ws1", "old", "Mine"); err != nil {
			t.Fatal(err)
		}

		if err := store.ReplaceProjection("p1", read, listing(session("new", "old")), nil); err != nil {
			t.Fatal(err)
		}

		if got, ok := store.LaunchConfig("new"); !ok || got.OptionID != launch.OptionID || string(got.Config) != string(launch.Config) {
			t.Errorf("renamed during the pass %v: LaunchConfig(new) = %+v, %v; want %+v", renamedDuringPass, got, ok, launch)
		}
		if _, ok := store.LaunchConfig("old"); ok {
			t.Errorf("renamed during the pass %v: the replaced session keeps its launch config", renamedDuringPass)
		}
		sessions := findProject(t, decodeProjects(t, store.Snapshot().Data), "p1").Workstreams[0].Sessions
		if len(sessions) != 1 || sessions[0].ID != "new" || sessions[0].Title != "Mine" {
			t.Errorf("renamed during the pass %v: sessions = %+v, want only new, titled Mine", renamedDuringPass, sessions)
		}
		saved := Saved(t, store).SessionTitles
		if got := saved["new"].Title; got != "Mine" {
			t.Errorf("renamed during the pass %v: persisted title of the new session = %q, want Mine", renamedDuringPass, got)
		}
		if _, ok := saved["old"]; ok {
			t.Errorf("renamed during the pass %v: the replaced session's title is still persisted", renamedDuringPass)
		}
	}
}

func TestReplacementKeepsTheNewSessionsOwnRecords(t *testing.T) {
	store := NewTestStore(t, replacementSeed())
	if err := store.RenameSession("p1", "ws1", "old", "Old name"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetLaunchConfig("p1", "old", domain.LaunchConfig{ProviderID: "enclave", OptionID: "codex"}); err != nil {
		t.Fatal(err)
	}
	// The new ID was named and launched on its own before the projection carrying it replaced "old".
	if err := store.ReplaceProjection("p1", nil, listing(session("old", ""), session("new", "")), nil); err != nil {
		t.Fatal(err)
	}
	if err := store.RenameSession("p1", "ws1", "new", "New name"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetLaunchConfig("p1", "new", domain.LaunchConfig{ProviderID: "enclave", OptionID: "claude"}); err != nil {
		t.Fatal(err)
	}

	if err := store.ReplaceProjection("p1", nil, listing(session("new", "old")), nil); err != nil {
		t.Fatal(err)
	}

	if got, ok := store.LaunchConfig("new"); !ok || got.OptionID != "claude" {
		t.Errorf("LaunchConfig(new) = %+v, %v; want the claude option", got, ok)
	}
	if got := Saved(t, store).SessionTitles["new"].Title; got != "New name" {
		t.Errorf("persisted title of the new session = %q, want New name", got)
	}
}

func TestReplacementOfAHiddenUntrackedSessionCarriesOver(t *testing.T) {
	store := NewTestStore(t, replacementSeed())
	if err := store.RenameSession("p1", "ws1", "old", "Mine"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetLaunchConfig("p1", "old", domain.LaunchConfig{ProviderID: "enclave", OptionID: "codex"}); err != nil {
		t.Fatal(err)
	}
	untracked := []domain.UntrackedWorktree{{Name: "wt", Path: "/tmp/test-wt", Sessions: []domain.Session{session("new", "old")}}}

	if err := store.ReplaceProjection("p1", nil, listing(), untracked); err != nil {
		t.Fatal(err)
	}

	if got, ok := store.LaunchConfig("new"); !ok || got.OptionID != "codex" {
		t.Errorf("LaunchConfig(new) = %+v, %v; want the codex option", got, ok)
	}
	if got := Saved(t, store).SessionTitles["new"].Title; got != "Mine" {
		t.Errorf("persisted title of the new session = %q, want Mine", got)
	}
}

func TestReplacedSessionStillListedKeepsItsRecords(t *testing.T) {
	store := NewTestStore(t, replacementSeed())
	if err := store.RenameSession("p1", "ws1", "old", "Mine"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetLaunchConfig("p1", "old", domain.LaunchConfig{ProviderID: "enclave", OptionID: "codex"}); err != nil {
		t.Fatal(err)
	}

	if err := store.ReplaceProjection("p1", nil, listing(session("old", ""), session("new", "old")), nil); err != nil {
		t.Fatal(err)
	}

	if got, ok := store.LaunchConfig("old"); !ok || got.OptionID != "codex" {
		t.Errorf("LaunchConfig(old) = %+v, %v; want the codex option", got, ok)
	}
	if _, ok := store.LaunchConfig("new"); ok {
		t.Error("the new session took a launch config from a session still listed")
	}
	saved := Saved(t, store).SessionTitles
	if saved["old"].Title != "Mine" || saved["new"].Title != "" {
		t.Errorf("persisted titles = %+v; want only old = Mine", saved)
	}
}

// Enclave reuses its default container's ID: a second session restarted off it must take the records, not the
// first one, which still names the same ID as the session it replaced.
func TestReplacementOfAReusedIDCarriesOverToTheNewSessionOnly(t *testing.T) {
	store := NewTestStore(t, replacementSeed())
	if err := store.ReplaceProjection("p1", nil, listing(session("first", "old")), nil); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceProjection("p1", nil, listing(session("first", "old"), session("old", "")), nil); err != nil {
		t.Fatal(err)
	}
	if err := store.RenameSession("p1", "ws1", "old", "Bug hunt"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetLaunchConfig("p1", "old", domain.LaunchConfig{ProviderID: "enclave", OptionID: "codex"}); err != nil {
		t.Fatal(err)
	}

	if err := store.ReplaceProjection("p1", nil, listing(session("first", "old"), session("second", "old")), nil); err != nil {
		t.Fatal(err)
	}

	if got, ok := store.LaunchConfig("second"); !ok || got.OptionID != "codex" {
		t.Errorf("LaunchConfig(second) = %+v, %v; want the codex option", got, ok)
	}
	if _, ok := store.LaunchConfig("first"); ok {
		t.Error("the earlier replacement took the launch config")
	}
	saved := Saved(t, store).SessionTitles
	if saved["second"].Title != "Bug hunt" || saved["first"].Title != "" {
		t.Errorf("persisted titles = %+v; want only second = Bug hunt", saved)
	}
}
