package provider

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
	"github.com/eclipsesource/homeshell/daemon/internal/store"
)

// untrackedService is a service over a git project p1 with one linked
// worktree on branch wt-a, a session s1 running there, and a project p2
// outside git. It returns the repository and the worktree's paths.
func untrackedService(t *testing.T) (service *Service, projectStore *store.Store, repo, dir string) {
	t.Helper()
	repo = gitProject(t)
	dir = addWorktree(t, repo, "wt-a")
	projectStore = store.NewTestStore(t, []domain.Project{
		{ID: "p1", Name: "p1", Path: repo, GitBacked: true, Workstreams: []domain.Workstream{}},
		{ID: "p2", Name: "p2", Path: t.TempDir(), Workstreams: []domain.Workstream{}},
	})
	registry := NewRegistry()
	sessionProvider := &stubSessionProvider{id: "prov", available: true, discovered: map[string][]DiscoveredSession{
		"p1": {ds(dir, "s1", domain.SessionRunning)},
	}}
	if err := registry.RegisterSessionProvider(sessionProvider); err != nil {
		t.Fatal(err)
	}
	service = NewService(projectStore, registry, 0)
	service.Reconcile(context.Background())
	return service, projectStore, repo, dir
}

func mustProject(t *testing.T, projectStore *store.Store, projectID string) domain.Project {
	t.Helper()
	project, err := projectStore.Project(projectID)
	if err != nil {
		t.Fatal(err)
	}
	return project
}

func TestServiceAddUntrackedWorktree(t *testing.T) {
	tests := []struct {
		name string
		// takenName, when set, is held by another workstream beforehand.
		takenName string
		wantName  string
	}{
		{name: "named like its suggestion", wantName: "wt-a"},
		{name: "numbered when that name is taken", takenName: "wt-a", wantName: "wt-a-2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service, projectStore, _, dir := untrackedService(t)
			if tt.takenName != "" {
				if err := projectStore.AddWorkstream("p1", domain.Workstream{ID: "ws-other", Name: tt.takenName,
					Provider: CoreProviderID, WorkingDir: t.TempDir(), Sessions: []domain.Session{}}); err != nil {
					t.Fatal(err)
				}
			}

			added, err := service.AddUntrackedWorktree(context.Background(), "p1", dir)
			if err != nil {
				t.Fatalf("AddUntrackedWorktree: %v", err)
			}
			if added.Name != tt.wantName || added.Branch != "wt-a" || added.StrategyID != "worktree" ||
				added.Provider != CoreProviderID || added.WorkingDir != dir {
				t.Errorf("added = %+v, want %s on branch wt-a, a core worktree in %s", added, tt.wantName, dir)
			}
			if ids := sessionIDs(added.Sessions); !slices.Equal(ids, []string{"s1"}) {
				t.Errorf("sessions = %q, want [s1]", ids)
			}
			if suggestions := mustProject(t, projectStore, "p1").UntrackedWorktrees; len(suggestions) != 0 {
				t.Errorf("suggestions = %+v, want none once added", suggestions)
			}
		})
	}
}

func TestServiceAddUntrackedWorktreeRefuses(t *testing.T) {
	tests := []struct {
		name string
		// setup prepares the refusal and returns the project and path to add.
		setup func(t *testing.T, service *Service, repo, dir string) (projectID, path string)
	}{
		{name: "the project folder", setup: func(_ *testing.T, _ *Service, repo, _ string) (string, string) {
			return "p1", repo
		}},
		{name: "a folder no worktree", setup: func(t *testing.T, _ *Service, _, _ string) (string, string) {
			return "p1", t.TempDir()
		}},
		{name: "a worktree already added", setup: func(t *testing.T, service *Service, _, dir string) (string, string) {
			if _, err := service.AddUntrackedWorktree(context.Background(), "p1", dir); err != nil {
				t.Fatal(err)
			}
			return "p1", dir
		}},
		{name: "a worktree removed since it was listed",
			setup: func(t *testing.T, _ *Service, repo, dir string) (string, string) {
				if out, err := exec.Command("git", "-C", repo, "worktree", "remove", dir).CombinedOutput(); err != nil {
					t.Fatalf("git worktree remove: %v\n%s", err, out)
				}
				return "p1", dir
			}},
		{name: "a project outside git", setup: func(_ *testing.T, _ *Service, _, dir string) (string, string) {
			return "p2", dir
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service, projectStore, repo, dir := untrackedService(t)
			projectID, path := tt.setup(t, service, repo, dir)
			before := len(mustProject(t, projectStore, projectID).Workstreams)

			_, err := service.AddUntrackedWorktree(context.Background(), projectID, path)
			if !errors.Is(err, ErrNotUntrackedWorktree) {
				t.Errorf("AddUntrackedWorktree = %v, want ErrNotUntrackedWorktree", err)
			}
			if after := len(mustProject(t, projectStore, projectID).Workstreams); after != before {
				t.Errorf("workstreams = %d, want %d: nothing added", after, before)
			}
		})
	}

	t.Run("an unknown project", func(t *testing.T) {
		service, _, _, dir := untrackedService(t)
		if _, err := service.AddUntrackedWorktree(context.Background(), "nope", dir); !errors.Is(err, ErrProjectNotFound) {
			t.Errorf("AddUntrackedWorktree = %v, want ErrProjectNotFound", err)
		}
	})
}

// An added worktree removed with its files is gone, and suggested no more.
// Kept, it is suggested again (TestRemoveKeepingFilesSuggestsTheWorktreeAgain).
func TestRemoveDeletingFilesSuggestsTheWorktreeNoMore(t *testing.T) {
	service, projectStore, _, dir := untrackedService(t)
	added, err := service.AddUntrackedWorktree(context.Background(), "p1", dir)
	if err != nil {
		t.Fatal(err)
	}

	workstreamRef := WorkstreamRef{ProjectID: "p1", WorkstreamID: added.ID}
	if err := service.Remove(context.Background(), workstreamRef, RemoveOptions{Force: true}); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	service.Reconcile(context.Background())

	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stat %s = %v, want the worktree deleted", dir, err)
	}
	if suggestions := mustProject(t, projectStore, "p1").UntrackedWorktrees; len(suggestions) != 0 {
		t.Errorf("suggestions = %+v, want none", suggestions)
	}
}

// windowProbe is the core, running probe once its Create has made the
// worktree, before the service stores the workstream.
type windowProbe struct {
	*core
	probe func()
}

func (p windowProbe) Create(ctx context.Context, req CreateRequest) (domain.Workstream, error) {
	created, err := p.core.Create(ctx, req)
	if err == nil {
		p.probe()
	}
	return created, err
}

// Between git making a worktree and the service storing its workstream, no
// workstream holds the folder: the core's claim does, so a pass in that window
// does not suggest it, and an Add there is refused.
func TestServiceCreateClaimsTheWorktreeUntilStored(t *testing.T) {
	repo := gitProject(t)
	projectStore := store.NewTestStore(t, []domain.Project{
		{ID: "p1", Name: "p1", Path: repo, GitBacked: true, Workstreams: []domain.Workstream{}},
	})
	registry := NewRegistry()
	provider := windowProbe{core: &core{root: t.TempDir()}}
	if err := registry.RegisterWorkstreamProvider(&provider); err != nil {
		t.Fatal(err)
	}
	service := NewService(projectStore, registry, 0)
	dest := filepath.Join(t.TempDir(), "wt")
	var suggestedInWindow []domain.UntrackedWorktree
	var addErr error
	probed := make(chan struct{})
	provider.probe = func() {
		defer close(probed)
		if err := service.reconciler.ReconcileProject(context.Background(), "p1"); err != nil {
			t.Errorf("ReconcileProject: %v", err)
		}
		suggestedInWindow = mustProject(t, projectStore, "p1").UntrackedWorktrees
		_, addErr = service.AddUntrackedWorktree(context.Background(), "p1", dest)
	}

	config := []byte(`{"strategy":"worktree","branch":"wt-b","path":"` + dest + `"}`)
	if _, err := service.Create(context.Background(), CreateInput{ProjectID: "p1", Config: config}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	<-probed
	waitFor(t, "the created workstream", func() bool {
		return slices.ContainsFunc(mustProject(t, projectStore, "p1").Workstreams, func(workstream domain.Workstream) bool {
			return workstream.WorkingDir == dest && !isPlaceholder(workstream)
		})
	})

	if len(suggestedInWindow) != 0 {
		t.Errorf("suggestions while creating = %+v, want none", suggestedInWindow)
	}
	if !errors.Is(addErr, ErrNotUntrackedWorktree) {
		t.Errorf("AddUntrackedWorktree while creating = %v, want ErrNotUntrackedWorktree", addErr)
	}
	if _, err := os.Stat(dest); err != nil {
		t.Errorf("stat %s = %v, want the created worktree kept", dest, err)
	}
	waitFor(t, "the claim's release", func() bool {
		provider.claimMu.Lock()
		defer provider.claimMu.Unlock()
		return len(provider.claimed) == 0
	})
}

// A project sharing the repository suggests a worktree added in another no
// more: adding it changes no listing, so the projects are surveyed again.
func TestServiceAddUntrackedWorktreeClaimsItInEveryProject(t *testing.T) {
	service, projectStore, repo, dir := untrackedService(t)
	sub := filepath.Join(repo, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := projectStore.AddProject(&domain.Project{ID: "p3", Name: "p3", Path: sub, GitBacked: true,
		Workstreams: []domain.Workstream{}}); err != nil {
		t.Fatal(err)
	}
	service.Reconcile(context.Background())
	if suggestions := mustProject(t, projectStore, "p3").UntrackedWorktrees; len(suggestions) != 1 {
		t.Fatalf("p3 suggestions = %+v, want the worktree", suggestions)
	}

	if _, err := service.AddUntrackedWorktree(context.Background(), "p1", dir); err != nil {
		t.Fatal(err)
	}
	service.reconciler.reconcileOnly(context.Background(), service.reconciler.takeDirty())

	if suggestions := mustProject(t, projectStore, "p3").UntrackedWorktrees; len(suggestions) != 0 {
		t.Errorf("p3 suggestions = %+v, want none once p1 added it", suggestions)
	}
}
