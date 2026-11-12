package provider

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
	"github.com/eclipsesource/homeshell/daemon/internal/store"
)

// checkout switches the branch checked out in the repository at dir.
func checkout(t *testing.T, dir string, args ...string) {
	t.Helper()
	if out, err := exec.Command("git", append([]string{"-C", dir, "checkout", "-q"}, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("git checkout %v: %v\n%s", args, err, out)
	}
}

func TestCoreCreatesInPlaceOnTheCheckedOutBranch(t *testing.T) {
	repo := gitProject(t)
	tests := []struct {
		name       string
		project    ProjectRef
		wsName     string
		wantName   string
		wantBranch string
	}{
		{name: "a name of the user's own is kept", project: ProjectRef{Path: repo, GitBacked: true},
			wsName: "mine", wantName: "mine", wantBranch: "feature"},
		{name: "outside git the folder names it", project: ProjectRef{Path: t.TempDir()},
			wantName: "", wantBranch: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws, err := (&core{root: t.TempDir()}).Create(context.Background(), CreateRequest{
				Project: tt.project, Name: tt.wsName, Config: []byte(`{"strategy":"in-place"}`),
			})
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			wantName := tt.wantName
			if wantName == "" {
				wantName = filepath.Base(tt.project.Path)
			}
			if ws.Name != wantName || ws.Branch != tt.wantBranch || ws.WorkingDir != tt.project.Path {
				t.Errorf("Create = %q on %q in %q, want %q on %q in the project folder",
					ws.Name, ws.Branch, ws.WorkingDir, wantName, tt.wantBranch)
			}
		})
	}
}

func TestCoreRefreshFollowsTheCheckedOutBranch(t *testing.T) {
	repo := gitProject(t)
	project := ProjectRef{Path: repo, GitBacked: true}
	provider := &core{root: t.TempDir()}
	inPlace := func(t *testing.T, name string) domain.Workstream {
		t.Helper()
		ws, err := provider.Create(context.Background(), CreateRequest{
			Project: project, Name: name, Config: []byte(`{"strategy":"in-place"}`),
		})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		return ws
	}

	t.Run("the name follows the branch", func(t *testing.T) {
		ws := inPlace(t, "")
		checkout(t, repo, "main")
		t.Cleanup(func() { checkout(t, repo, "feature") })
		got := provider.Refresh(context.Background(), project, ws)
		if got.Name != "main" || got.Branch != "main" {
			t.Errorf("Refresh = %q on %q, want main on main", got.Name, got.Branch)
		}
		checkout(t, repo, "feature")
		if again := provider.Refresh(context.Background(), project, got); again.Name != "feature" {
			t.Errorf("Refresh after checking feature out again = %q, want feature", again.Name)
		}
	})

	t.Run("a name typed as the branch follows it too", func(t *testing.T) {
		ws := inPlace(t, "feature")
		checkout(t, repo, "main")
		t.Cleanup(func() { checkout(t, repo, "feature") })
		if got := provider.Refresh(context.Background(), project, ws); got.Name != "main" {
			t.Errorf("Refresh = %q, want main", got.Name)
		}
	})

	t.Run("a renamed workstream keeps its name", func(t *testing.T) {
		ws := inPlace(t, "")
		ws.Name = "my work"
		checkout(t, repo, "main")
		t.Cleanup(func() { checkout(t, repo, "feature") })
		got := provider.Refresh(context.Background(), project, ws)
		if got.Name != "my work" || got.Branch != "main" {
			t.Errorf("Refresh = %q on %q, want my work on main", got.Name, got.Branch)
		}
	})

	t.Run("a detached HEAD keeps the name and drops the branch", func(t *testing.T) {
		ws := inPlace(t, "")
		checkout(t, repo, "--detach")
		t.Cleanup(func() { checkout(t, repo, "feature") })
		got := provider.Refresh(context.Background(), project, ws)
		if got.Name != "feature" || got.Branch != "" {
			t.Errorf("Refresh = %q on %q, want feature on no branch", got.Name, got.Branch)
		}
		// And the name follows again once a branch is checked out.
		checkout(t, repo, "main")
		if again := provider.Refresh(context.Background(), project, got); again.Name != "main" {
			t.Errorf("Refresh after checkout = %q, want main", again.Name)
		}
	})

	t.Run("a project folder below the repository's root follows its branch", func(t *testing.T) {
		below := filepath.Join(repo, "below")
		if err := os.Mkdir(below, 0o755); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Remove(below) })
		ws := domain.Workstream{
			Name: "feature", Branch: "feature", WorkingDir: below,
			CreatorData: marshalCreatorData(coreCreatorData{Strategy: "in-place", NameFromBranch: "feature"}),
		}
		checkout(t, repo, "main")
		t.Cleanup(func() { checkout(t, repo, "feature") })
		got := provider.Refresh(context.Background(), ProjectRef{Path: below, GitBacked: true}, ws)
		if got.Name != "main" || got.Branch != "main" {
			t.Errorf("Refresh = %q on %q, want main on main", got.Name, got.Branch)
		}
	})

	t.Run("a worktree's branch follows a checkout in its folder, its name stays", func(t *testing.T) {
		ws, err := provider.Create(context.Background(), CreateRequest{
			Project: project, Name: "topic work", Config: []byte(`{"strategy":"worktree","branch":"topic"}`),
		})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		checkout(t, ws.WorkingDir, "-b", "topic-2")
		got := provider.Refresh(context.Background(), project, ws)
		if got.Name != "topic work" || got.Branch != "topic-2" {
			t.Errorf("Refresh = %q on %q, want topic work on topic-2", got.Name, got.Branch)
		}
	})

	t.Run("a copy without .git takes no branch from a repository around it", func(t *testing.T) {
		inside := filepath.Join(repo, "copy")
		if err := os.Mkdir(inside, 0o755); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Remove(inside) })
		ws := domain.Workstream{
			Name: "copy", Branch: "old", WorkingDir: inside,
			CreatorData: marshalCreatorData(coreCreatorData{Strategy: "copy"}),
		}
		if got := provider.Refresh(context.Background(), project, ws); got.Name != "copy" || got.Branch != "old" {
			t.Errorf("Refresh = %q on %q, want the copy unchanged", got.Name, got.Branch)
		}
	})
}

// renamingProvider is a Refresher that renames every workstream it is asked about.
type renamingProvider struct {
	stubWorkstreamProvider
	to string
}

func (p *renamingProvider) RefreshInterval() time.Duration { return 0 }

func (p *renamingProvider) Refresh(_ context.Context, _ ProjectRef, ws domain.Workstream) domain.Workstream {
	ws.Name, ws.Branch = p.to, p.to
	return ws
}

func TestReconcileRefreshesWorkstreamsButKeepsNamesUnique(t *testing.T) {
	st := store.NewTestStore(t, []domain.Project{{ID: "p1", Path: "/p1", Workstreams: []domain.Workstream{
		{ID: "ws1", Name: "old", Provider: "renaming", WorkingDir: "/p1", Sessions: []domain.Session{}},
		{ID: "ws2", Name: "taken", Provider: "core", WorkingDir: "/p1/other", Sessions: []domain.Session{}},
	}}})
	reg := NewRegistry()
	refresher := &renamingProvider{stubWorkstreamProvider: stubWorkstreamProvider{id: "renaming"}, to: "new"}
	if err := reg.RegisterWorkstreamProvider(refresher); err != nil {
		t.Fatal(err)
	}
	reconciler := NewReconciler(st, reg, 0)

	reconciler.Reconcile(context.Background())
	if got := st.Projects()[0].Workstreams[0]; got.Name != "new" || got.Branch != "new" {
		t.Errorf("refreshed = %q on %q, want new on new", got.Name, got.Branch)
	}

	refresher.to = "Taken"
	reconciler.Reconcile(context.Background())
	if got := st.Projects()[0].Workstreams[0]; got.Name != "new" || got.Branch != "Taken" {
		t.Errorf("refreshed = %q on %q, want the name kept (another answers to it) on Taken", got.Name, got.Branch)
	}
}

// A refresh follows the store's name rule: a create still running holds its
// name, a failed one does not.
func TestReconcileRefreshNamesFollowTheStoresRule(t *testing.T) {
	tests := []struct {
		name, to, wantName string
	}{
		{"a pending create holds its name", "pending", "old"},
		{"a failed create does not", "failed", "failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := store.NewTestStore(t, []domain.Project{{ID: "p1", Path: "/p1", Workstreams: []domain.Workstream{
				{ID: "ws1", Name: "old", Provider: "renaming", WorkingDir: "/p1", Sessions: []domain.Session{}},
				{ID: PlaceholderIDPrefix + "a", Name: "pending", Provider: "core",
					RuntimeState: domain.RuntimeCreating, Sessions: []domain.Session{}},
				{ID: PlaceholderIDPrefix + "b", Name: "failed", Provider: "core",
					RuntimeState: domain.RuntimeError, CreateFailed: true, Sessions: []domain.Session{}},
			}}})
			reg := NewRegistry()
			if err := reg.RegisterWorkstreamProvider(&renamingProvider{
				stubWorkstreamProvider: stubWorkstreamProvider{id: "renaming"}, to: tt.to,
			}); err != nil {
				t.Fatal(err)
			}
			NewReconciler(st, reg, 0).Reconcile(context.Background())
			if got, err := st.Workstream("p1", "ws1"); err != nil || got.Name != tt.wantName {
				t.Errorf("refreshed = %q, %v; want %q", got.Name, err, tt.wantName)
			}
		})
	}
}

// A branch switched with nothing else going on is noticed: the reconciler
// watches the branches a Refresher's workstreams are on.
func TestReconcilerRunFollowsABranchSwitchedOutsideHomeShell(t *testing.T) {
	repo := gitProject(t)
	project := ProjectRef{ID: "p1", Path: repo, GitBacked: true}
	provider := &core{root: t.TempDir()}
	ws, err := provider.Create(context.Background(), CreateRequest{Project: project, Config: []byte(`{"strategy":"in-place"}`)})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	ws.ID, ws.Provider = "ws1", CoreProviderID
	st := store.NewTestStore(t, []domain.Project{{ID: "p1", Path: repo, GitBacked: true, Workstreams: []domain.Workstream{ws}}})
	reg := NewRegistry()
	if err := reg.RegisterWorkstreamProvider(provider); err != nil {
		t.Fatal(err)
	}
	reconciler := NewReconciler(st, reg, 0)
	reconciler.refreshEvery = 20 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go reconciler.Run(ctx)

	checkout(t, repo, "main")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if got := st.Projects()[0].Workstreams[0]; got.Name == "main" && got.Branch == "main" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	got := st.Projects()[0].Workstreams[0]
	t.Errorf("workstream = %q on %q, want it to follow the switch to main", got.Name, got.Branch)
}

// gitDetectorFunc is a git.Detector answering by a function.
type gitDetectorFunc func(string) bool

func (detect gitDetectorFunc) Detect(path string) bool { return detect(path) }

func TestServiceAddProjectGivesItAnInPlaceWorkstream(t *testing.T) {
	repo := gitProject(t)
	plain := t.TempDir()
	st := store.NewTestStore(t, nil, store.WithGitDetector(gitDetectorFunc(func(path string) bool { return path == repo })))
	svc := NewService(st, NewRegistry(), 0)

	tests := []struct {
		name, path, wantName, wantBranch string
	}{
		{"a git project's is named after the branch", repo, "feature", "feature"},
		{"another project's after its folder", plain, filepath.Base(plain), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			project := domain.Project{ID: "proj-" + tt.wantName, Name: "p", Path: tt.path}
			if err := svc.AddProject(context.Background(), &project); err != nil {
				t.Fatalf("AddProject: %v", err)
			}
			// The echo carries the default workstream, as the store does.
			if len(project.Workstreams) != 1 {
				t.Fatalf("workstreams = %+v, want the default one", project.Workstreams)
			}
			ws := project.Workstreams[0]
			if ws.Name != tt.wantName || ws.Branch != tt.wantBranch || ws.StrategyID != "in-place" ||
				ws.Provider != CoreProviderID || ws.WorkingDir != tt.path {
				t.Errorf("default workstream = %+v, want %q on %q, in place in the project folder", ws, tt.wantName, tt.wantBranch)
			}
			stored, err := st.Workstream(project.ID, ws.ID)
			if err != nil || stored.Name != tt.wantName {
				t.Errorf("stored = %+v, %v; want the default workstream", stored, err)
			}
			// It is persisted: a session can be added to it straight away.
			if err := st.AddSession(project.ID, ws.ID, domain.Session{ID: "s-" + ws.ID, State: domain.SessionIdle}); err != nil {
				t.Errorf("AddSession: %v", err)
			}
		})
	}
}

func TestServiceAddProjectRefusesADuplicatePath(t *testing.T) {
	dir := t.TempDir()
	st := store.NewTestStore(t, nil)
	svc := NewService(st, NewRegistry(), 0)
	first := domain.Project{ID: "p1", Name: "p", Path: dir}
	if err := svc.AddProject(context.Background(), &first); err != nil {
		t.Fatalf("AddProject: %v", err)
	}
	second := domain.Project{ID: "p2", Name: "p", Path: dir}
	if err := svc.AddProject(context.Background(), &second); err == nil {
		t.Error("AddProject(same path) err = nil, want a refusal")
	}
	if got := len(st.Projects()); got != 1 {
		t.Errorf("projects = %d, want 1", got)
	}
}

// An in-place create is listed in the project folder from its placeholder on,
// before the create has made the workstream that works there.
func TestCreatePlaceholderOfAnInPlaceCreateIsInTheProjectFolder(t *testing.T) {
	tests := []struct {
		name, config string
		want         bool
	}{
		{"in place", `{"strategy":"in-place"}`, true},
		{"a copy", `{"strategy":"copy"}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			st := store.NewTestStore(t, []domain.Project{{ID: "p1", Name: "p", Path: dir}})
			svc := NewService(st, NewRegistry(), 0)
			placeholder, err := svc.Create(context.Background(), CreateInput{
				ProjectID: "p1", ProviderID: CoreProviderID, Name: "work", Config: []byte(tt.config),
			})
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			if placeholder.InProjectFolder != tt.want {
				t.Errorf("placeholder InProjectFolder = %v, want %v", placeholder.InProjectFolder, tt.want)
			}
		})
	}
}

// A project without a default workstream (one added before them, or whose
// default was deleted) can be given one, but only while its folder is free.
func TestServiceAddDefaultWorkstream(t *testing.T) {
	dir := t.TempDir()
	st := store.NewTestStore(t, []domain.Project{{ID: "p1", Name: "p", Path: dir}})
	svc := NewService(st, NewRegistry(), 0)

	created, err := svc.AddDefaultWorkstream(context.Background(), "p1")
	if err != nil {
		t.Fatalf("AddDefaultWorkstream: %v", err)
	}
	if created.ID == "" || created.StrategyID != "in-place" || !created.InProjectFolder || created.Name != filepath.Base(dir) {
		t.Errorf("created = %+v, want one in place in the project folder, named after it", created)
	}

	assertFolderTaken(t, svc, "p1")
	if _, err := svc.AddDefaultWorkstream(context.Background(), "nope"); !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("AddDefaultWorkstream(unknown project) err = %v, want ErrProjectNotFound", err)
	}
}

// blockedDiscovery is a session provider whose discovery waits for release.
type blockedDiscovery struct {
	stubSessionProvider
	release chan struct{}
}

func (p *blockedDiscovery) DiscoverSessions(ctx context.Context, project ProjectRef) ([]DiscoveredSession, error) {
	<-p.release
	return p.stubSessionProvider.DiscoverSessions(ctx, project)
}

// The default workstream is answered before session discovery, which can be
// slow, and the session already running in the project folder is attached after.
func TestServiceAddDefaultWorkstreamAttachesSessionsAfterAnswering(t *testing.T) {
	dir := t.TempDir()
	st := store.NewTestStore(t, []domain.Project{{ID: "p1", Name: "p", Path: dir}})
	reg := NewRegistry()
	sessions := &blockedDiscovery{release: make(chan struct{}), stubSessionProvider: stubSessionProvider{
		id: "prov", available: true, discovered: map[string][]DiscoveredSession{
			"p1": {ds(dir, "s1", domain.SessionRunning)},
		},
	}}
	if err := reg.RegisterSessionProvider(sessions); err != nil {
		t.Fatal(err)
	}
	svc := NewService(st, reg, 0)
	t.Cleanup(func() { svc.Shutdown(context.Background()) })

	answered := make(chan domain.Workstream, 1)
	go func() {
		created, err := svc.AddDefaultWorkstream(context.Background(), "p1")
		if err != nil {
			t.Errorf("AddDefaultWorkstream: %v", err)
		}
		answered <- created
	}()
	var created domain.Workstream
	select {
	case created = <-answered:
	case <-time.After(5 * time.Second):
		close(sessions.release)
		t.Fatal("AddDefaultWorkstream waited for session discovery")
	}
	close(sessions.release)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ws, err := st.Workstream("p1", created.ID); err == nil && len(ws.Sessions) == 1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Error("the session running in the project folder was not attached to the default workstream")
}

// inPlaceStub is a workstream provider whose creates are in place.
type inPlaceStub struct{ stubWorkstreamProvider }

func (*inPlaceStub) CreateStrategy(CreateRequest) string { return "in-place" }

// A failed in-place create stays where its placeholder was listed.
func TestAFailedInPlaceCreateStaysInTheProjectFolder(t *testing.T) {
	st := store.NewTestStore(t, []domain.Project{{ID: "p1", Name: "p", Path: t.TempDir()}})
	reg := NewRegistry()
	failing := &inPlaceStub{stubWorkstreamProvider{id: "failing", available: true, canCreate: true, createErr: errors.New("no")}}
	if err := reg.RegisterWorkstreamProvider(failing); err != nil {
		t.Fatal(err)
	}
	svc := NewService(st, reg, 0)
	placeholder, err := svc.Create(context.Background(), CreateInput{ProjectID: "p1", ProviderID: "failing", Name: "work"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if failed, err := st.Workstream("p1", placeholder.ID); err == nil && failed.CreateFailed {
			if !failed.InProjectFolder {
				t.Errorf("failed create = %+v, want it in the project folder", failed)
			}
			assertFolderTaken(t, svc, "p1")
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the create never failed")
}

// assertFolderTaken checks that nothing more can work in the project folder:
// the form leaves In place out and a default workstream is refused.
func assertFolderTaken(t *testing.T, svc *Service, projectID string) {
	t.Helper()
	descriptors, err := svc.WorkstreamProvidersFor(context.Background(), projectID)
	if err != nil {
		t.Fatalf("WorkstreamProvidersFor: %v", err)
	}
	for _, descriptor := range descriptors {
		if descriptor.ID == CoreProviderID && strings.Contains(string(descriptor.WorkstreamSchema), `"in-place"`) {
			t.Errorf("core schema offers in-place while the folder is taken: %s", descriptor.WorkstreamSchema)
		}
	}
	if _, err := svc.AddDefaultWorkstream(context.Background(), projectID); !errors.Is(err, store.ErrDuplicateWorkingDir) {
		t.Errorf("AddDefaultWorkstream err = %v, want ErrDuplicateWorkingDir", err)
	}
}
