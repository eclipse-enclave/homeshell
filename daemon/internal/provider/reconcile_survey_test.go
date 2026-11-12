package provider

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
	"github.com/eclipsesource/homeshell/daemon/internal/store"
)

// stubSurveyor is a workstream provider whose listing is set per project. It
// suggests every listed folder not claimed, named after its branch, vanishes
// the workstreams in vanished, and records what it was asked.
type stubSurveyor struct {
	stubWorkstreamProvider
	folders  map[string][]Folder
	vanished []string
	listErr  error
	// onList runs inside each ListFolders, as something landing while git lists.
	onList      func()
	lists       int
	projectList map[string]int
	claimed     []string
	surveyed    []domain.Workstream
	// listDeadline is the deadline the latest ListFolders was given; zero for none.
	listDeadline time.Time
}

func (s *stubSurveyor) ListFolders(ctx context.Context, project ProjectRef) ([]Folder, error) {
	s.lists++
	s.listDeadline, _ = ctx.Deadline()
	if s.projectList == nil {
		s.projectList = map[string]int{}
	}
	s.projectList[project.ID]++
	if s.onList != nil {
		s.onList()
	}
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.folders[project.ID], nil
}

func (s *stubSurveyor) Survey(_ ProjectRef, folders []Folder, workstreams []domain.Workstream, claimed []string) Survey {
	s.claimed, s.surveyed = claimed, workstreams
	survey := Survey{Vanished: s.vanished}
	for _, folder := range folders {
		if !slices.Contains(claimed, folder.Path) {
			survey.Untracked = append(survey.Untracked,
				domain.UntrackedWorktree{Name: folder.Branch, Branch: folder.Branch, Path: folder.Path})
		}
	}
	return survey
}

func (s *stubSurveyor) TrackFolder(domain.UntrackedWorktree, func(string) bool) domain.Workstream {
	return domain.Workstream{}
}

// surveyedReconciler reconciles projects with a surveyor listing /wt/a for p1,
// and a session provider "prov" reporting sessions.
func surveyedReconciler(t *testing.T, projects []domain.Project,
	sessions map[string][]DiscoveredSession) (*Reconciler, *store.Store, *stubSurveyor, *stubSessionProvider) {
	t.Helper()
	st := store.NewTestStore(t, projects)
	surveyor := &stubSurveyor{
		stubWorkstreamProvider: stubWorkstreamProvider{id: "core", available: true},
		folders:                map[string][]Folder{"p1": {{Path: "/wt/a", Branch: "a"}}},
	}
	sessionProvider := &stubSessionProvider{id: "prov", available: true, discovered: sessions}
	reg := NewRegistry()
	if err := reg.RegisterWorkstreamProvider(surveyor); err != nil {
		t.Fatal(err)
	}
	if err := reg.RegisterSessionProvider(sessionProvider); err != nil {
		t.Fatal(err)
	}
	return NewReconciler(st, reg, 0), st, surveyor, sessionProvider
}

func surveyedProject(workstreams ...domain.Workstream) domain.Project {
	return domain.Project{ID: "p1", Name: "p1", Path: "/p1", GitBacked: true, Workstreams: workstreams}
}

func suggestionAt(t *testing.T, st *store.Store, path string) domain.UntrackedWorktree {
	t.Helper()
	for _, suggestion := range st.Projects()[0].UntrackedWorktrees {
		if suggestion.Path == path {
			return suggestion
		}
	}
	t.Fatalf("no suggestion at %s: %+v", path, st.Projects()[0].UntrackedWorktrees)
	return domain.UntrackedWorktree{}
}

func sessionIDs(sessions []domain.Session) []string {
	ids := []string{}
	for _, session := range sessions {
		ids = append(ids, session.ID)
	}
	return ids
}

func TestReconcileSuggestsUntrackedWorktrees(t *testing.T) {
	reconciler, st, _, _ := surveyedReconciler(t, []domain.Project{surveyedProject()}, nil)
	reconciler.Reconcile(context.Background())

	got := st.Projects()[0].UntrackedWorktrees
	if len(got) != 1 || got[0].Name != "a" || got[0].Branch != "a" || got[0].Path != "/wt/a" ||
		len(got[0].Sessions) != 0 || got[0].SessionCount != 0 {
		t.Errorf("suggestions = %+v, want one named a, on branch a, at /wt/a, no sessions", got)
	}
}

func TestReconcileHidesSessionsInAnUntrackedWorktree(t *testing.T) {
	reconciler, st, _, _ := surveyedReconciler(t, []domain.Project{surveyedProject()}, map[string][]DiscoveredSession{
		"p1": {ds("/wt/a", "s1", domain.SessionRunning)},
	})
	reconciler.Reconcile(context.Background())

	if workstreams := st.Projects()[0].Workstreams; len(workstreams) != 0 {
		t.Errorf("workstreams = %+v, want none adopted", workstreams)
	}
	suggestion := suggestionAt(t, st, "/wt/a")
	if ids := sessionIDs(suggestion.Sessions); !slices.Equal(ids, []string{"s1"}) || suggestion.SessionCount != 1 {
		t.Errorf("suggestion sessions = %q, count %d; want [s1], 1", ids, suggestion.SessionCount)
	}
}

func TestReconcileStillAdoptsOtherFolders(t *testing.T) {
	reconciler, st, _, _ := surveyedReconciler(t, []domain.Project{surveyedProject()}, map[string][]DiscoveredSession{
		"p1": {ds("/elsewhere", "s1", domain.SessionRunning)},
	})
	reconciler.Reconcile(context.Background())

	workstreams := st.Projects()[0].Workstreams
	if len(workstreams) != 1 || workstreams[0].ID != adoptedWorkstreamID("/elsewhere") {
		t.Fatalf("workstreams = %+v, want the one adopted for /elsewhere", workstreams)
	}
	if suggestion := suggestionAt(t, st, "/wt/a"); len(suggestion.Sessions) != 0 {
		t.Errorf("suggestion sessions = %+v, want none", suggestion.Sessions)
	}
}

func TestReconcileDropsAnAdoptedWorkstreamInAnUntrackedWorktree(t *testing.T) {
	adopted := domain.Workstream{
		ID: adoptedWorkstreamID("/wt/a"), Name: "a", Provider: "prov", WorkingDir: "/wt/a",
		Sessions: []domain.Session{{ID: "s1", Provider: "prov", State: domain.SessionRunning}},
	}
	reconciler, st, _, sessionProvider := surveyedReconciler(t, []domain.Project{surveyedProject(adopted)}, nil)
	// The provider fails to report: the stored session is retained, on the suggestion.
	sessionProvider.discoverErr = errors.New("unreachable")
	reconciler.Reconcile(context.Background())

	if workstreams := st.Projects()[0].Workstreams; len(workstreams) != 0 {
		t.Errorf("workstreams = %+v, want the adopted one gone", workstreams)
	}
	if ids := sessionIDs(suggestionAt(t, st, "/wt/a").Sessions); !slices.Equal(ids, []string{"s1"}) {
		t.Errorf("suggestion sessions = %q, want [s1]", ids)
	}
}

func TestReconcileCountsOnlyLiveHiddenSessions(t *testing.T) {
	reconciler, st, _, _ := surveyedReconciler(t, []domain.Project{surveyedProject()}, map[string][]DiscoveredSession{
		"p1": {ds("/wt/a", "s1", domain.SessionRunning), ds("/wt/a", "s2", domain.SessionDone)},
	})
	reconciler.Reconcile(context.Background())

	if count := suggestionAt(t, st, "/wt/a").SessionCount; count != 1 {
		t.Errorf("session count = %d, want 1 (the ended one not counted)", count)
	}
}

func TestReconcileKeepsHiddenSessionsOnAScopedPass(t *testing.T) {
	reconciler, st, _, _ := surveyedReconciler(t, []domain.Project{surveyedProject()}, map[string][]DiscoveredSession{
		"p1": {ds("/wt/a", "s1", domain.SessionRunning)},
	})
	reconciler.Reconcile(context.Background())
	reconciler.reconcileOnly(context.Background(), dirtySet{ids: map[string]bool{"core": true}})

	if ids := sessionIDs(suggestionAt(t, st, "/wt/a").Sessions); !slices.Equal(ids, []string{"s1"}) {
		t.Errorf("suggestion sessions after a scoped pass = %q, want [s1]", ids)
	}
}

// A failed listing is no empty listing: the suggestions stay, with the
// sessions hidden there, but one whose folder a workstream took since, as
// the surveyors compare folders, symlinks resolved.
func TestReconcileOnAFailedListing(t *testing.T) {
	realDir := t.TempDir()
	linkDir := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		// folder is the one listed, then suggested.
		folder string
		// claimedAs is the folder a workstream added since works in; "" when none.
		claimedAs       string
		wantSuggested   bool
		wantWorkstreams int
	}{
		{name: "keeps the suggestion", folder: "/wt/a", wantSuggested: true},
		{name: "drops one claimed since", folder: "/wt/a", claimedAs: "/wt/a", wantWorkstreams: 1},
		{name: "drops one claimed through a symlink", folder: linkDir, claimedAs: realDir, wantWorkstreams: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reconciler, st, surveyor, sessionProvider := surveyedReconciler(t, []domain.Project{surveyedProject()},
				map[string][]DiscoveredSession{"p1": {ds(tt.folder, "s1", domain.SessionRunning)}})
			surveyor.folders["p1"] = []Folder{{Path: tt.folder, Branch: "a"}}
			reconciler.Reconcile(context.Background())
			if tt.claimedAs != "" {
				if err := st.AddWorkstream("p1", worktreeAt("ws-a", tt.claimedAs)); err != nil {
					t.Fatal(err)
				}
			}
			surveyor.listErr = errors.New("git timed out")
			// Not reported again either: s1 is the stored one, retained.
			sessionProvider.discoverErr = errors.New("unreachable")
			reconciler.forgetFolders("p1")
			reconciler.Reconcile(context.Background())

			if surveyor.lists != 2 {
				t.Errorf("listings = %d, want 2 (the cache forgotten)", surveyor.lists)
			}
			suggestions := st.Projects()[0].UntrackedWorktrees
			if tt.wantSuggested {
				if ids := sessionIDs(suggestionAt(t, st, tt.folder).Sessions); !slices.Equal(ids, []string{"s1"}) {
					t.Errorf("suggestion sessions = %q, want [s1] kept", ids)
				}
			} else if len(suggestions) != 0 {
				t.Errorf("suggestions = %+v, want none for a folder a workstream works in", suggestions)
			}
			workstreams := st.Projects()[0].Workstreams
			if len(workstreams) != tt.wantWorkstreams {
				t.Fatalf("workstreams = %+v, want %d, none adopted", workstreams, tt.wantWorkstreams)
			}
			if tt.wantWorkstreams == 1 && tt.claimedAs == tt.folder &&
				!slices.Equal(sessionIDs(workstreams[0].Sessions), []string{"s1"}) {
				t.Errorf("workstreams = %+v, want ws-a holding s1", workstreams)
			}
		})
	}
}

func TestReconcileExcludesFoldersOfOtherProjects(t *testing.T) {
	projectA := surveyedProject(domain.Workstream{
		ID: adoptedWorkstreamID("/wt/x"), Name: "x", Provider: "prov", WorkingDir: "/wt/x", Sessions: []domain.Session{},
	})
	projectB := domain.Project{ID: "p2", Name: "p2", Path: "/p2", GitBacked: true, Workstreams: []domain.Workstream{
		{ID: "ws-b", Name: "b", Provider: "core", WorkingDir: "/wt/a", Sessions: []domain.Session{}},
		{ID: PlaceholderIDPrefix + "1", Name: "c", Provider: "core", WorkingDir: "/wt/c", Sessions: []domain.Session{}},
	}}
	reconciler, st, surveyor, _ := surveyedReconciler(t, []domain.Project{projectA, projectB}, nil)
	if err := reconciler.ReconcileProject(context.Background(), "p1"); err != nil {
		t.Fatal(err)
	}

	if got := st.Projects()[0].UntrackedWorktrees; len(got) != 0 {
		t.Errorf("suggestions = %+v, want none (/wt/a is project B's)", got)
	}
	for _, want := range []string{"/p1", "/p2", "/wt/a"} {
		if !slices.Contains(surveyor.claimed, want) {
			t.Errorf("claimed = %q, want it to hold %s", surveyor.claimed, want)
		}
	}
	for _, unwanted := range []string{"/wt/x", "/wt/c"} {
		if slices.Contains(surveyor.claimed, unwanted) {
			t.Errorf("claimed = %q, want no adopted or placeholder folder %s", surveyor.claimed, unwanted)
		}
	}
}

func TestReconcileSkipsSurveyOutsideGit(t *testing.T) {
	project := surveyedProject()
	project.GitBacked = false
	reconciler, st, surveyor, _ := surveyedReconciler(t, []domain.Project{project}, nil)
	reconciler.Reconcile(context.Background())

	if surveyor.lists != 0 {
		t.Errorf("listings = %d, want none outside git", surveyor.lists)
	}
	if got := st.Projects()[0].UntrackedWorktrees; len(got) != 0 {
		t.Errorf("suggestions = %+v, want none", got)
	}
}

// A workstream that took a suggested folder takes the sessions hidden there,
// even those whose provider is not asked this pass.
func TestReconcileHandsHiddenSessionsToAWorkstreamAddedInTheFolder(t *testing.T) {
	reconciler, st, _, _ := surveyedReconciler(t, []domain.Project{surveyedProject()}, map[string][]DiscoveredSession{
		"p1": {ds("/wt/a", "s1", domain.SessionRunning)},
	})
	reconciler.Reconcile(context.Background())
	if err := st.AddWorkstream("p1", domain.Workstream{
		ID: "ws-a", Name: "a", Provider: "core", WorkingDir: filepath.Join("/wt", "a"), Sessions: []domain.Session{},
	}); err != nil {
		t.Fatal(err)
	}
	reconciler.reconcileOnly(context.Background(), dirtySet{ids: map[string]bool{"core": true}})

	if got := st.Projects()[0].UntrackedWorktrees; len(got) != 0 {
		t.Errorf("suggestions = %+v, want none once added", got)
	}
	workstreams := st.Projects()[0].Workstreams
	if len(workstreams) != 1 || !slices.Equal(sessionIDs(workstreams[0].Sessions), []string{"s1"}) {
		t.Errorf("workstreams = %+v, want ws-a holding s1", workstreams)
	}
}

// worktreeAt is a workstream of the surveyor's working in dir.
func worktreeAt(id, dir string) domain.Workstream {
	return domain.Workstream{ID: id, Name: id, Provider: "core", WorkingDir: dir, Sessions: []domain.Session{}}
}

// retirements records the workstreams a reconciler retires.
func retirements(reconciler *Reconciler) *[]string {
	var retired []string
	reconciler.retire = func(_ context.Context, ref WorkstreamRef) {
		retired = append(retired, ref.WorkstreamID)
	}
	return &retired
}

// A vanished workstream is retired, and stays listed with the sessions in its
// folder until its removal drops it: no pass adopts them meanwhile.
func TestReconcileRetiresAVanishedWorktreeWorkstream(t *testing.T) {
	reconciler, st, surveyor, _ := surveyedReconciler(t, []domain.Project{surveyedProject(worktreeAt("ws-1", "/wt/gone"))},
		map[string][]DiscoveredSession{"p1": {ds("/wt/gone", "s1", domain.SessionRunning)}})
	surveyor.vanished = []string{"ws-1"}
	retired := retirements(reconciler)
	// The first pass lists before it first sees ws-1; the second lists anew.
	reconciler.Reconcile(context.Background())
	reconciler.Reconcile(context.Background())
	reconciler.Reconcile(context.Background())

	if !slices.Equal(*retired, []string{"ws-1", "ws-1"}) {
		t.Errorf("retired = %q, want ws-1 by each pass from a listing newer than it", *retired)
	}
	workstreams := st.Projects()[0].Workstreams
	if len(workstreams) != 1 || workstreams[0].ID != "ws-1" ||
		!slices.Equal(sessionIDs(workstreams[0].Sessions), []string{"s1"}) {
		t.Errorf("workstreams = %+v, want ws-1 holding s1 until removed, none adopted", workstreams)
	}
}

// A listing may predate a workstream's worktree: the workstream vanishes only
// from one begun after a pass first saw it.
func TestReconcileVanishesNothingFromAListingOlderThanTheWorkstream(t *testing.T) {
	reconciler, st, surveyor, _ := surveyedReconciler(t, []domain.Project{surveyedProject()}, nil)
	retired := retirements(reconciler)
	reconciler.Reconcile(context.Background())
	if err := st.AddWorkstream("p1", worktreeAt("ws-new", "/wt/new")); err != nil {
		t.Fatal(err)
	}
	surveyor.vanished = []string{"ws-new"}

	reconciler.Reconcile(context.Background())
	if len(*retired) != 0 || surveyor.lists != 1 {
		t.Fatalf("retired = %q after %d listings, want none from the listing cached before ws-new",
			*retired, surveyor.lists)
	}
	reconciler.Reconcile(context.Background())
	if !slices.Equal(*retired, []string{"ws-new"}) || surveyor.lists != 2 {
		t.Errorf("retired = %q after %d listings, want ws-new from the listing taken anew",
			*retired, surveyor.lists)
	}
}

func TestReconcileVanishesNothingWhenTheListingFails(t *testing.T) {
	reconciler, st, surveyor, _ := surveyedReconciler(t, []domain.Project{surveyedProject(worktreeAt("ws-1", "/wt/gone"))}, nil)
	surveyor.vanished = []string{"ws-1"}
	surveyor.listErr = errors.New("git timed out")
	retired := retirements(reconciler)
	reconciler.Reconcile(context.Background())
	reconciler.Reconcile(context.Background())

	if len(*retired) != 0 {
		t.Errorf("retired = %q, want none on a failed listing", *retired)
	}
	if workstreams := st.Projects()[0].Workstreams; len(workstreams) != 1 || workstreams[0].ID != "ws-1" {
		t.Errorf("workstreams = %+v, want ws-1 kept", workstreams)
	}
}

// A workstream landing while git lists, as one promoted then does, is surveyed:
// the listing is no older than the workstreams it is held against.
func TestReconcileSurveysAfterReReadingTheStore(t *testing.T) {
	reconciler, st, surveyor, _ := surveyedReconciler(t, []domain.Project{surveyedProject()}, nil)
	surveyor.onList = func() {
		if err := st.AddWorkstream("p1", worktreeAt("ws-new", "/wt/new")); err != nil {
			t.Fatal(err)
		}
	}
	reconciler.Reconcile(context.Background())

	if !slices.ContainsFunc(surveyor.surveyed, func(workstream domain.Workstream) bool { return workstream.ID == "ws-new" }) {
		t.Errorf("surveyed workstreams = %+v, want ws-new, added while listing", surveyor.surveyed)
	}
}

func TestRefreshListsWorktreesOncePerProject(t *testing.T) {
	projectB := domain.Project{ID: "p2", Name: "p2", Path: "/p2", GitBacked: true, Workstreams: []domain.Workstream{}}
	reconciler, st, surveyor, _ := surveyedReconciler(t, []domain.Project{surveyedProject(), projectB}, nil)
	surveyor.folders["p2"] = []Folder{{Path: "/wt/b", Branch: "b"}}

	if !reconciler.foldersChanged(context.Background(), st.Projects(), surveyor) {
		t.Fatal("foldersChanged = false, want true for listings not held yet")
	}
	reconciler.reconcileOnly(context.Background(), dirtySet{ids: map[string]bool{"core": true}})

	if surveyor.projectList["p1"] != 1 || surveyor.projectList["p2"] != 1 {
		t.Errorf("listings per project = %v, want one each", surveyor.projectList)
	}
}

// A refresh reports a change when a listing differs from the one held, or
// none was held; not when it is the same, or fails.
func TestRefreshReportsAChangedListing(t *testing.T) {
	tests := []struct {
		name    string
		change  func(surveyor *stubSurveyor, reconciler *Reconciler)
		changed bool
	}{
		{name: "same listing", change: func(*stubSurveyor, *Reconciler) {}},
		{name: "folder gone", changed: true, change: func(surveyor *stubSurveyor, _ *Reconciler) {
			surveyor.folders["p1"] = nil
		}},
		{name: "branch moved", changed: true, change: func(surveyor *stubSurveyor, _ *Reconciler) {
			surveyor.folders["p1"] = []Folder{{Path: "/wt/a", Branch: "b"}}
		}},
		{name: "none held", changed: true, change: func(_ *stubSurveyor, reconciler *Reconciler) {
			reconciler.forgetFolders("p1")
		}},
		{name: "listing fails", change: func(surveyor *stubSurveyor, _ *Reconciler) {
			surveyor.listErr = errors.New("git timed out")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reconciler, st, surveyor, _ := surveyedReconciler(t, []domain.Project{surveyedProject()}, nil)
			reconciler.Reconcile(context.Background())
			tt.change(surveyor, reconciler)
			if got := reconciler.foldersChanged(context.Background(), st.Projects(), surveyor); got != tt.changed {
				t.Errorf("foldersChanged = %v, want %v", got, tt.changed)
			}
		})
	}
}

// A refresh whose listing fails keeps the cached one.
func TestRefreshKeepsTheCachedListingWhenListingFails(t *testing.T) {
	reconciler, st, surveyor, _ := surveyedReconciler(t, []domain.Project{surveyedProject()}, nil)
	reconciler.Reconcile(context.Background())
	surveyor.listErr = errors.New("git timed out")
	reconciler.foldersChanged(context.Background(), st.Projects(), surveyor)
	surveyor.listErr = nil
	reconciler.Reconcile(context.Background())

	if surveyor.lists != 2 {
		t.Errorf("listings = %d, want 2 (the pass after the failed refresh served from the cache)", surveyor.lists)
	}
	if got := st.Projects()[0].UntrackedWorktrees; len(got) != 1 {
		t.Errorf("suggestions = %+v, want the cached one kept", got)
	}
}

func TestReconcilerRunPicksUpAWorktreeAddedOutsideHomeShell(t *testing.T) {
	repo := gitProject(t)
	st := store.NewTestStore(t, []domain.Project{{ID: "p1", Path: repo, GitBacked: true, Workstreams: []domain.Workstream{}}})
	reg := NewRegistry()
	if err := reg.RegisterWorkstreamProvider(&core{root: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	reconciler := NewReconciler(st, reg, 0)
	reconciler.refreshEvery = 20 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go reconciler.Run(ctx)

	dir := addWorktree(t, repo, "wt-a")
	awaitSuggestions(t, st, func(got []domain.UntrackedWorktree) bool {
		return len(got) == 1 && got[0].Name == "wt-a" && got[0].Path == dir
	}, "the worktree wt-a suggested")

	if out, err := exec.Command("git", "-C", repo, "worktree", "remove", dir).CombinedOutput(); err != nil {
		t.Fatalf("git worktree remove: %v\n%s", err, out)
	}
	awaitSuggestions(t, st, func(got []domain.UntrackedWorktree) bool { return len(got) == 0 }, "no suggestion")
}

// awaitSuggestions waits for the suggestions of the store's first project to
// satisfy ok.
func awaitSuggestions(t *testing.T, st *store.Store, ok func([]domain.UntrackedWorktree) bool, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ok(st.Projects()[0].UntrackedWorktrees) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("suggestions = %+v, want %s", st.Projects()[0].UntrackedWorktrees, want)
}

// A vanished workstream is removed with its sessions, unless a removal of it
// is under way already, or it is gone.
func TestServiceRetiresAVanishedWorkstreamOnce(t *testing.T) {
	workstream := worktreeAt("ws-1", "/wt/gone")
	workstream.Sessions = []domain.Session{{ID: "s1", Provider: "prov", State: domain.SessionRunning}}
	ref := WorkstreamRef{ProjectID: "p1", WorkstreamID: "ws-1"}
	tests := []struct {
		name        string
		stored      []domain.Workstream
		removing    bool
		wantRemoved []string
		wantLeft    int
	}{
		{name: "alone", stored: []domain.Workstream{workstream}, wantRemoved: []string{"s1"}},
		{name: "while removed", stored: []domain.Workstream{workstream}, removing: true, wantLeft: 1},
		{name: "gone already"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sessionProvider := &stubSessionProvider{id: "prov", available: true}
			reg := NewRegistry()
			if err := reg.RegisterSessionProvider(sessionProvider); err != nil {
				t.Fatal(err)
			}
			st := store.NewTestStore(t, []domain.Project{surveyedProject(tt.stored...)})
			svc := NewService(st, reg, 0)
			if tt.removing {
				svc.beginWorkstreamRemoval(ref)
				defer svc.endWorkstreamRemoval(ref)
			}
			svc.retireVanished(context.Background(), ref)
			svc.background.Wait()

			if !slices.Equal(sessionProvider.removed, tt.wantRemoved) {
				t.Errorf("removed sessions = %q, want %q", sessionProvider.removed, tt.wantRemoved)
			}
			if left := st.Projects()[0].Workstreams; len(left) != tt.wantLeft {
				t.Errorf("workstreams = %+v, want %d", left, tt.wantLeft)
			}
		})
	}
}

// Removing a workstream whose record is gone fails as not found, as Remove's
// callers are told.
func TestRemoveWorkstreamOfAGoneRecordIsNotFound(t *testing.T) {
	svc := NewService(store.NewTestStore(t, []domain.Project{surveyedProject()}), NewRegistry(), 0)
	ref := WorkstreamRef{ProjectID: "p1", WorkstreamID: "ws-1"}
	err := svc.removeWorkstream(context.Background(), ref, worktreeAt("ws-1", "/wt/gone"), RemoveOptions{Force: true})
	if !errors.Is(err, ErrWorkstreamNotFound) {
		t.Errorf("removeWorkstream = %v, want ErrWorkstreamNotFound", err)
	}
}

// passOnTrigger runs the pass the reconciler was asked for, as Run would, and
// fails when none was asked for.
func passOnTrigger(t *testing.T, reconciler *Reconciler) {
	t.Helper()
	select {
	case <-reconciler.trigger:
	case <-time.After(time.Second):
		t.Fatal("no pass was asked for")
	}
	reconciler.reconcileOnly(context.Background(), reconciler.takeDirty())
}

// drainTrigger drops a pass asked for so far.
func drainTrigger(reconciler *Reconciler) {
	select {
	case <-reconciler.trigger:
	default:
	}
	reconciler.takeDirty()
}

// gitService is a service over a git project p1 with one linked worktree,
// whose path it returns.
func gitService(t *testing.T, workstreams ...domain.Workstream) (*Service, *store.Store, string) {
	t.Helper()
	repo := gitProject(t)
	dir := addWorktree(t, repo, "wt-a")
	st := store.NewTestStore(t, []domain.Project{{ID: "p1", Name: "p1", Path: repo, GitBacked: true,
		Workstreams: workstreams}})
	return NewService(st, NewRegistry(), 0), st, dir
}

// A worktree whose workstream is removed with its files kept is suggested
// again, though a refresh tick held a listing from before the removal: the
// listing is the same, but the removal asks for a pass itself.
func TestRemoveKeepingFilesSuggestsTheWorktreeAgain(t *testing.T) {
	svc, st, dir := gitService(t)
	svc.Reconcile(context.Background())
	folder := suggestionAt(t, st, dir)
	workstream := (&core{}).TrackFolder(folder, nil)
	workstream.ID, workstream.Provider = "ws-a", CoreProviderID
	if err := st.AddWorkstream("p1", workstream); err != nil {
		t.Fatal(err)
	}
	svc.Reconcile(context.Background())
	if got := st.Projects()[0].UntrackedWorktrees; len(got) != 0 {
		t.Fatalf("suggestions = %+v, want none while ws-a works there", got)
	}
	key := listingKey{providerID: CoreProviderID, projectID: "p1"}
	tickListing, _ := svc.reconciler.cachedListings.get(key)
	drainTrigger(svc.reconciler)

	ref := WorkstreamRef{ProjectID: "p1", WorkstreamID: "ws-a"}
	if err := svc.Remove(context.Background(), ref, RemoveOptions{KeepFiles: true}); err != nil {
		t.Fatal(err)
	}
	// The tick listed before the removal and holds its listing after it.
	svc.reconciler.cachedListings.put(key, folderListing{folders: tickListing.folders, takenAt: time.Now()})
	passOnTrigger(t, svc.reconciler)

	suggestionAt(t, st, folder.Path)
}

// A project added in a linked worktree of another's repository claims that
// folder, and frees it once removed: the other project is re-projected both
// times, though its listing stays the same.
func TestAddingAndRemovingAProjectReprojectsTheOthers(t *testing.T) {
	svc, st, dir := gitService(t)
	svc.Reconcile(context.Background())
	suggestionAt(t, st, dir)
	drainTrigger(svc.reconciler)

	if err := svc.AddProject(context.Background(), &domain.Project{ID: "p2", Name: "p2", Path: dir}); err != nil {
		t.Fatal(err)
	}
	passOnTrigger(t, svc.reconciler)
	if got := st.Projects()[0].UntrackedWorktrees; len(got) != 0 {
		t.Fatalf("suggestions = %+v, want none once a project is at %s", got, dir)
	}

	if err := svc.RemoveProject(context.Background(), "p2"); err != nil {
		t.Fatal(err)
	}
	passOnTrigger(t, svc.reconciler)
	suggestionAt(t, st, dir)
}

// A Remove arriving while a retire tears the workstream down waits for it,
// and tears down nothing again; it does the removal itself only when the
// retire left the workstream behind.
func TestRemoveWaitsForARetireUnderWay(t *testing.T) {
	tests := []struct {
		name        string
		retireDrops bool
		wantRemoved []string
	}{
		{name: "retire done", retireDrops: true},
		{name: "retire failed", wantRemoved: []string{"s1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sessionProvider := &stubSessionProvider{id: "prov", available: true}
			reg := NewRegistry()
			if err := reg.RegisterSessionProvider(sessionProvider); err != nil {
				t.Fatal(err)
			}
			workstream := worktreeAt("ws-1", "/wt/gone")
			workstream.Sessions = []domain.Session{{ID: "s1", Provider: "prov", State: domain.SessionDone}}
			st := store.NewTestStore(t, []domain.Project{surveyedProject(workstream)})
			svc := NewService(st, reg, 0)
			ref := WorkstreamRef{ProjectID: "p1", WorkstreamID: "ws-1"}
			if !svc.tryBeginRetire(ref) {
				t.Fatal("the retire was refused")
			}

			removed := make(chan error, 1)
			go func() { removed <- svc.Remove(context.Background(), ref, RemoveOptions{}) }()
			select {
			case err := <-removed:
				t.Fatalf("Remove returned %v during the retire, want it to wait", err)
			case <-time.After(50 * time.Millisecond):
			}
			if tt.retireDrops {
				if err := st.RemoveWorkstream("p1", "ws-1"); err != nil {
					t.Fatal(err)
				}
			}
			svc.endWorkstreamRemoval(ref)

			if err := <-removed; err != nil {
				t.Errorf("Remove = %v, want success", err)
			}
			if !slices.Equal(sessionProvider.removed, tt.wantRemoved) {
				t.Errorf("removed sessions = %q, want %q", sessionProvider.removed, tt.wantRemoved)
			}
			if left := st.Projects()[0].Workstreams; len(left) != 0 {
				t.Errorf("workstreams = %+v, want none", left)
			}
		})
	}
}

// Nothing is kept of a removed project: not by its removal, nor from a pass
// in flight that listed it meanwhile.
func TestReconcilerKeepsNothingOfARemovedProject(t *testing.T) {
	reconciler, st, surveyor, _ := surveyedReconciler(t, []domain.Project{surveyedProject()}, nil)
	reconciler.Reconcile(context.Background())
	reconciler.forgetProject("p1")
	gone := listingKey{providerID: "core", projectID: "gone"}
	reconciler.cachedListings.put(gone, folderListing{takenAt: time.Now()})
	reconciler.foldersChanged(context.Background(), st.Projects(), surveyor)

	if _, kept := reconciler.projects["p1"]; kept {
		t.Error("the pass state of p1 is kept after its removal")
	}
	if _, held := reconciler.cachedListings.get(gone); held {
		t.Error("a listing of a project gone is held after a refresh")
	}
}

// A pass leaves the listing's bound to the surveyor, which bounds a refresh's
// tightly. The user's Add waits on its listing, which gets a create's bound.
func TestUntrackedListsUnderACreatesBound(t *testing.T) {
	reconciler, st, surveyor, _ := surveyedReconciler(t, []domain.Project{surveyedProject()}, nil)
	reconciler.Reconcile(context.Background())
	if !surveyor.listDeadline.IsZero() {
		t.Errorf("a pass listed with deadline %v, want none: the surveyor bounds it", surveyor.listDeadline)
	}

	start := time.Now()
	if _, err := reconciler.untracked(context.Background(), st.Projects()[0]); err != nil {
		t.Fatal(err)
	}
	if got := surveyor.listDeadline.Sub(start); (got - createCheckTimeout).Abs() > time.Second {
		t.Errorf("an Add listed within %v, want %v", got, createCheckTimeout)
	}
}
