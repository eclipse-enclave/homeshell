package provider_test

import (
	"context"
	"errors"
	"maps"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
	"github.com/eclipsesource/homeshell/daemon/internal/provider"
	"github.com/eclipsesource/homeshell/daemon/internal/settings"
	"github.com/eclipsesource/homeshell/daemon/internal/store"
)

// namedWS creates a workstream called name and waits for the real one to land.
func namedWS(t *testing.T, svc *provider.Service, st *store.Store, name, branch string) provider.WorkstreamRef {
	t.Helper()
	if _, err := svc.Create(context.Background(), provider.CreateInput{
		ProjectID: "p1", ProviderID: "fake", Name: name,
		Config: []byte(`{"branch":"` + branch + `"}`),
	}); err != nil {
		t.Fatalf("Create %q: %v", name, err)
	}
	ws := awaitNamedWorkstream(t, st, "p1", name)
	return provider.WorkstreamRef{ProjectID: "p1", WorkstreamID: ws.ID}
}

// The refusal arrives from Create itself, not minutes later on a failed
// workstream: the name is checked when the placeholder goes in, which is before
// the provider is asked to materialize anything.
func TestCreateRefusesADuplicateNameSynchronously(t *testing.T) {
	svc, st := newService(t)
	namedWS(t, svc, st, "feature", "main")

	_, err := svc.Create(context.Background(), provider.CreateInput{
		ProjectID: "p1", ProviderID: "fake", Name: "feature",
		Config: []byte(`{"branch":"other"}`),
	})
	if !errors.Is(err, store.ErrDuplicateName) {
		t.Fatalf("Create with a taken name err = %v, want ErrDuplicateName", err)
	}

	// And no placeholder was left behind for the user to clean up.
	project := projectOf(t, st, "p1")
	if got := len(project.Workstreams); got != 1 {
		t.Errorf("workstreams = %d, want only the original", got)
	}
}

// The refusal blames the Name field, quoting the name already there, so the form
// can say it under the input the user has to change.
func TestCreateRefusesADuplicateNameOnTheNameField(t *testing.T) {
	svc, st := newService(t)
	namedWS(t, svc, st, "Copy one", "main")

	_, err := svc.Create(context.Background(), provider.CreateInput{
		ProjectID: "p1", ProviderID: "fake", Name: "copy one",
		Config: []byte(`{"branch":"other"}`),
	})
	want := map[string]string{"/name": `"Copy one" is already used in this project; enter another`}
	if got := provider.FieldErrors(err); !maps.Equal(got, want) {
		t.Errorf("field errors = %v, want %v", got, want)
	}
}

// Case and surrounding space do not make a distinguishable name.
func TestCreateRefusesANameDifferingOnlyByCase(t *testing.T) {
	svc, st := newService(t)
	namedWS(t, svc, st, "Feature", "main")

	_, err := svc.Create(context.Background(), provider.CreateInput{
		ProjectID: "p1", ProviderID: "fake", Name: "feature",
		Config: []byte(`{"branch":"other"}`),
	})
	if !errors.Is(err, store.ErrDuplicateName) {
		t.Errorf("Create err = %v, want ErrDuplicateName", err)
	}
}

// Renaming is the bypass the rule would otherwise have, and the error has to
// reach the caller as itself — flattened to "workstream not found" it would read
// as a bug rather than as something the user can fix.
func TestRenameRefusesATakenName(t *testing.T) {
	svc, st := newService(t)
	namedWS(t, svc, st, "feature", "main")
	second := namedWS(t, svc, st, "other", "second")

	err := svc.Rename(context.Background(), second, "feature")
	if !errors.Is(err, store.ErrDuplicateName) {
		t.Fatalf("Rename onto a taken name err = %v, want ErrDuplicateName", err)
	}
	if errors.Is(err, provider.ErrWorkstreamNotFound) {
		t.Error("the refusal must not masquerade as a missing workstream")
	}

	// The workstream kept its own name.
	ws, err := st.Workstream("p1", second.WorkstreamID)
	if err != nil {
		t.Fatalf("reading the workstream back: %v", err)
	}
	if ws.Name != "other" {
		t.Errorf("name = %q, want the refused rename to have changed nothing", ws.Name)
	}
}

// A refused rename says so on the Name field in the words a refused create
// uses, quoting the name already there.
func TestRenameRefusesATakenNameOnTheNameField(t *testing.T) {
	svc, st := newService(t)
	namedWS(t, svc, st, "Copy one", "main")
	second := namedWS(t, svc, st, "other", "second")

	err := svc.Rename(context.Background(), second, "copy one")
	want := map[string]string{"/name": `"Copy one" is already used in this project; enter another`}
	if got := provider.FieldErrors(err); !maps.Equal(got, want) {
		t.Errorf("field errors = %v, want %v", got, want)
	}
}

// A workstream may change the case of its own name.
func TestRenameToItsOwnNameInAnotherCaseSucceeds(t *testing.T) {
	svc, st := newService(t)
	ref := namedWS(t, svc, st, "feature", "main")

	if err := svc.Rename(context.Background(), ref, "Feature"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	ws, _ := st.Workstream("p1", ref.WorkstreamID)
	if ws.Name != "Feature" {
		t.Errorf("name = %q, want %q", ws.Name, "Feature")
	}
}

// Renaming to a free name still works — the rule is not a freeze.
func TestRenameToAFreeNameSucceeds(t *testing.T) {
	svc, st := newService(t)
	ref := namedWS(t, svc, st, "feature", "main")

	if err := svc.Rename(context.Background(), ref, "renamed"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	ws, _ := st.Workstream("p1", ref.WorkstreamID)
	if ws.Name != "renamed" {
		t.Errorf("name = %q, want %q", ws.Name, "renamed")
	}
}

// awaitNamedWorkstream polls until the real (non-placeholder) workstream with
// this name appears. Named rather than positional, because these tests hold
// several workstreams at once and need the right one.
func awaitNamedWorkstream(t *testing.T, st *store.Store, projectID, name string) domain.Workstream {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for a real workstream named %q", name)
		default:
		}
		time.Sleep(10 * time.Millisecond)
		for _, ws := range projectOf(t, st, projectID).Workstreams {
			if ws.Name == name && !strings.HasPrefix(ws.ID, store.PlaceholderIDPrefix) {
				return ws
			}
		}
	}
}

func projectOf(t *testing.T, st *store.Store, projectID string) domain.Project {
	t.Helper()
	for _, project := range st.Projects() {
		if project.ID == projectID {
			return project
		}
	}
	t.Fatalf("project %q not in the store", projectID)
	return domain.Project{}
}

// Two sessions in one workstream must not end up wearing the same title. The
// refusal has to arrive as itself rather than under a "renaming session:" prefix,
// which reads like an internal failure the user cannot act on.
func TestRenameSessionRefusesASiblingsTitle(t *testing.T) {
	svc, _ := serviceWithSessions(t, domain.SessionIdle, domain.SessionIdle)
	first := provider.SessionRef{ProjectID: "p1", WorkstreamID: "ws-1", SessionID: "a-sess"}
	second := provider.SessionRef{ProjectID: "p1", WorkstreamID: "ws-1", SessionID: "b-sess"}

	if err := svc.RenameSession(context.Background(), first, "review"); err != nil {
		t.Fatalf("first rename: %v", err)
	}
	err := svc.RenameSession(context.Background(), second, "Review")
	if !errors.Is(err, store.ErrDuplicateSessionTitle) {
		t.Fatalf("rename onto a sibling's title err = %v, want ErrDuplicateSessionTitle", err)
	}
	if strings.Contains(err.Error(), "renaming session:") {
		t.Errorf("error = %q, want the refusal unwrapped", err)
	}
}

// The rule must not make a session unrenameable: a free title still lands.
func TestRenameSessionToAFreeTitleSucceeds(t *testing.T) {
	svc, st := serviceWithSessions(t, domain.SessionIdle, domain.SessionIdle)
	ref := provider.SessionRef{ProjectID: "p1", WorkstreamID: "ws-1", SessionID: "b-sess"}

	if err := svc.RenameSession(context.Background(), ref, "second pass"); err != nil {
		t.Fatalf("RenameSession: %v", err)
	}
	ws, _ := st.Workstream("p1", "ws-1")
	for _, sess := range ws.Sessions {
		if sess.ID == "b-sess" && sess.Title != "second pass" {
			t.Errorf("title = %q, want %q", sess.Title, "second pass")
		}
	}
}

// newServiceWithCore wires a service whose project is a real git-backed temp dir,
// so the core provider is the one under test rather than the fake.
func newServiceWithCore(t *testing.T) (*provider.Service, *store.Store) {
	t.Helper()
	st := store.NewTestStore(t, []domain.Project{{
		ID: "p1", Name: "p1", Path: t.TempDir(), GitBacked: true,
		Workstreams: []domain.Workstream{},
	}})
	svc := provider.NewService(st, provider.NewRegistry(), 0)
	svc.Reconcile(context.Background())
	return svc, st
}

// A draft is named as Create would name it, past the project's names, and
// reserves nothing.
func TestResolveDraftNamesAsCreateWould(t *testing.T) {
	service, projectStore := newServiceWithCore(t)
	input := provider.CreateInput{ProjectID: "p1", Config: []byte(`{"strategy":"copy"}`)}
	base := filepath.Base(projectOf(t, projectStore, "p1").Path)

	draft, err := service.ResolveDraft(context.Background(), input)
	if err != nil || draft.Name != base {
		t.Fatalf("ResolveDraft = %+v, %v; want the name %q", draft, err, base)
	}
	if got := len(projectOf(t, projectStore, "p1").Workstreams); got != 0 {
		t.Fatalf("workstreams = %d after a draft, want 0", got)
	}

	if _, err := service.Create(context.Background(), input); err != nil {
		t.Fatalf("Create: %v", err)
	}
	awaitNamedWorkstream(t, projectStore, "p1", base)
	if draft, err := service.ResolveDraft(context.Background(), input); err != nil || draft.Name != base+"-2" {
		t.Errorf("ResolveDraft = %+v, %v; want the name %q", draft, err, base+"-2")
	}
}

// Settings injected after NewService reach the core it registers, so the
// configured workstreams location is honoured.
func TestResolveDraftUnderTheConfiguredLocation(t *testing.T) {
	projectStore := store.NewTestStore(t, []domain.Project{{
		ID: "p1", Name: "p1", Path: t.TempDir(), GitBacked: true,
		Workstreams: []domain.Workstream{},
	}})
	registry := provider.NewRegistry()
	service := provider.NewService(projectStore, registry, 0)
	service.Reconcile(context.Background())
	settingsStore, err := settings.Load(filepath.Join(t.TempDir(), "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	next := settings.Defaults()
	next.Workstreams.Location = "../worktrees"
	if _, err := settingsStore.Set(next); err != nil {
		t.Fatal(err)
	}
	registry.InjectSettings(settingsStore)

	draft, err := service.ResolveDraft(context.Background(), provider.CreateInput{ProjectID: "p1", Config: []byte(`{"strategy":"copy"}`)})
	want := filepath.Join(filepath.Dir(projectOf(t, projectStore, "p1").Path), "worktrees")
	if err != nil || filepath.Dir(draft.Config["path"]) != want {
		t.Errorf("ResolveDraft = %+v, %v; want a path under %q", draft, err, want)
	}
}

// A copy's draft leaves the path blank when the configured location puts it
// inside the project, as Create would refuse that path; a worktree's is still
// proposed there.
func TestResolveDraftLeavesACopyInsideTheProjectBlank(t *testing.T) {
	projectStore := store.NewTestStore(t, []domain.Project{{
		ID: "p1", Name: "p1", Path: t.TempDir(), GitBacked: true,
		Workstreams: []domain.Workstream{},
	}})
	registry := provider.NewRegistry()
	service := provider.NewService(projectStore, registry, 0)
	service.Reconcile(context.Background())
	settingsStore, err := settings.Load(filepath.Join(t.TempDir(), "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	next := settings.Defaults()
	next.Workstreams.Location = "copies"
	if _, err := settingsStore.Set(next); err != nil {
		t.Fatal(err)
	}
	registry.InjectSettings(settingsStore)

	copyDraft, err := service.ResolveDraft(context.Background(), provider.CreateInput{ProjectID: "p1", Config: []byte(`{"strategy":"copy"}`)})
	if err != nil || copyDraft.Config["path"] != "" {
		t.Errorf("copy ResolveDraft = %+v, %v; want a blank path", copyDraft, err)
	}
	worktreeDraft, err := service.ResolveDraft(context.Background(), provider.CreateInput{ProjectID: "p1", Config: []byte(`{"strategy":"worktree"}`)})
	want := filepath.Join(projectOf(t, projectStore, "p1").Path, "copies")
	if err != nil || filepath.Dir(worktreeDraft.Config["path"]) != want {
		t.Errorf("worktree ResolveDraft = %+v, %v; want a path under %q", worktreeDraft, err, want)
	}
}

func TestResolveDraftRefusesAnUnknownProject(t *testing.T) {
	service, _ := newServiceWithCore(t)
	if _, err := service.ResolveDraft(context.Background(), provider.CreateInput{ProjectID: "nope"}); !errors.Is(err, provider.ErrProjectNotFound) {
		t.Errorf("err = %v, want ErrProjectNotFound", err)
	}
}
