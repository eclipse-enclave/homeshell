package provider

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
	"github.com/eclipsesource/homeshell/daemon/internal/git"
)

// addWorktree adds a linked worktree of the repository at repo in a fresh
// folder, on a new branch or, given none, detached, and returns its path.
func addWorktree(t *testing.T, repo, branch string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "wt")
	args := []string{"-C", repo, "worktree", "add", "-q", "--detach", dir}
	if branch != "" {
		args = []string{"-C", repo, "worktree", "add", "-q", "-b", branch, dir}
	}
	if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
		t.Fatalf("git worktree add: %v\n%s", err, out)
	}
	return dir
}

// surveyRepo surveys the repository at repo as the core sees it now.
func surveyRepo(t *testing.T, provider *core, repo string, workstreams []domain.Workstream, claimed []string) Survey {
	t.Helper()
	project := ProjectRef{Path: repo, GitBacked: true}
	folders, err := provider.ListFolders(context.Background(), project)
	if err != nil {
		t.Fatalf("ListFolders: %v", err)
	}
	return provider.Survey(project, folders, workstreams, claimed)
}

// worktreeWorkstream is a core worktree workstream working in dir.
func worktreeWorkstream(id, dir string) domain.Workstream {
	return domain.Workstream{ID: id, Provider: CoreProviderID, WorkingDir: dir,
		CreatorData: marshalCreatorData(coreCreatorData{Strategy: "worktree"})}
}

// outOfReach puts the linked worktree at dir out of reach, as an unmounted
// drive does: its parent folder goes, and git keeps it registered.
func outOfReach(t *testing.T, dir string) {
	t.Helper()
	if err := os.RemoveAll(filepath.Dir(dir)); err != nil {
		t.Fatal(err)
	}
}

// surveySetup prepares the repository at repo and returns the folder looked
// for, the claimed folders, and the suggestion wanted for it (nil: none).
type surveySetup func(t *testing.T, repo string) (string, []string, *domain.UntrackedWorktree)

func TestCoreSurveySuggestsLinkedWorktrees(t *testing.T) {
	tests := []struct {
		name  string
		setup surveySetup
	}{
		{"branch", func(t *testing.T, repo string) (string, []string, *domain.UntrackedWorktree) {
			dir := addWorktree(t, repo, "wt-a")
			return dir, nil, &domain.UntrackedWorktree{Name: "wt-a", Branch: "wt-a"}
		}},
		{"detached", func(t *testing.T, repo string) (string, []string, *domain.UntrackedWorktree) {
			dir := addWorktree(t, repo, "")
			return dir, nil, &domain.UntrackedWorktree{Name: filepath.Base(dir)}
		}},
		{"main checkout", func(t *testing.T, repo string) (string, []string, *domain.UntrackedWorktree) {
			return repo, nil, nil
		}},
		{"deleted folder", func(t *testing.T, repo string) (string, []string, *domain.UntrackedWorktree) {
			dir := addWorktree(t, repo, "wt-a")
			if err := os.RemoveAll(dir); err != nil {
				t.Fatal(err)
			}
			return dir, nil, nil
		}},
		{"folder out of reach", func(t *testing.T, repo string) (string, []string, *domain.UntrackedWorktree) {
			dir := addWorktree(t, repo, "wt-a")
			outOfReach(t, dir)
			return dir, nil, nil
		}},
		{"path in claimed", func(t *testing.T, repo string) (string, []string, *domain.UntrackedWorktree) {
			dir := addWorktree(t, repo, "wt-a")
			return dir, []string{dir}, nil
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := gitProject(t)
			dir, claimed, want := tt.setup(t, repo)
			survey := surveyRepo(t, &core{}, repo, nil, claimed)
			var got *domain.UntrackedWorktree
			for i, untracked := range survey.Untracked {
				if git.SamePath(untracked.Path, dir) {
					got = &survey.Untracked[i]
				}
			}
			if want == nil {
				if got != nil {
					t.Errorf("Survey suggests %+v; want no suggestion", *got)
				}
				return
			}
			if got == nil || got.Name != want.Name || got.Branch != want.Branch {
				t.Errorf("Survey.Untracked = %+v; want %+v in %s", survey.Untracked, *want, dir)
			}
		})
	}
}

// A project registered at a linked worktree lists the repository's other
// worktrees, its own folder among them: the folder it sits in is held, and the
// main checkout is git's first entry.
func TestCoreSurveyOfAProjectAtALinkedWorktree(t *testing.T) {
	repo := gitProject(t)
	projectDir, other := addWorktree(t, repo, "wt-a"), addWorktree(t, repo, "wt-b")
	survey := surveyRepo(t, &core{}, projectDir, nil, []string{projectDir})
	if len(survey.Untracked) != 1 || !git.SamePath(survey.Untracked[0].Path, other) {
		t.Errorf("Survey.Untracked = %+v; want only %s", survey.Untracked, other)
	}
}

// A worktree out of reach cannot be added, so it is not suggested, and may come
// back, so its workstream stays.
func TestCoreSurveyKeepsAWorktreeOutOfReach(t *testing.T) {
	repo := gitProject(t)
	tracked, untracked := addWorktree(t, repo, "wt-a"), addWorktree(t, repo, "wt-b")
	outOfReach(t, tracked)
	outOfReach(t, untracked)
	survey := surveyRepo(t, &core{}, repo, []domain.Workstream{worktreeWorkstream("ws", tracked)}, nil)
	if len(survey.Untracked) != 0 || len(survey.Vanished) != 0 {
		t.Errorf("Survey = %+v; want nothing untracked and nothing vanished", survey)
	}
}

func TestCoreSurveyVanishesWorktreeWorkstreams(t *testing.T) {
	listed, gone, unlisted := t.TempDir(), t.TempDir(), t.TempDir()
	folders := []Folder{
		{Path: listed, Branch: "a", Missing: true},
		{Path: gone, Branch: "b", Missing: true, Gone: true},
	}
	withStrategy := func(strategy string) domain.Workstream {
		workstream := worktreeWorkstream("ws", unlisted)
		workstream.CreatorData = marshalCreatorData(coreCreatorData{Strategy: strategy})
		return workstream
	}
	otherProvider := worktreeWorkstream("ws", unlisted)
	otherProvider.Provider = "fake"
	tests := []struct {
		name         string
		workstream   domain.Workstream
		wantVanished bool
	}{
		{"worktree whose folder is unlisted", worktreeWorkstream("ws", unlisted), true},
		{"worktree whose folder is listed gone", worktreeWorkstream("ws", gone), true},
		{"worktree whose folder is out of reach", worktreeWorkstream("ws", listed), false},
		{"in place", withStrategy("in-place"), false},
		{"copy", withStrategy("copy"), false},
		{"another provider's", otherProvider, false},
		{"placeholder", worktreeWorkstream(PlaceholderIDPrefix+"1", unlisted), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			survey := (&core{}).Survey(ProjectRef{}, folders, []domain.Workstream{tt.workstream}, nil)
			if got := slices.Contains(survey.Vanished, tt.workstream.ID); got != tt.wantVanished {
				t.Errorf("vanished = %v (%v); want %v", got, survey.Vanished, tt.wantVanished)
			}
		})
	}
}

func TestCoreSurveyResolvesSymlinks(t *testing.T) {
	repo := gitProject(t)
	tracked, claimedDir := addWorktree(t, repo, "wt-a"), addWorktree(t, repo, "wt-b")
	links := t.TempDir()
	linkTo := func(target, name string) string {
		link := filepath.Join(links, name)
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		return link
	}
	workstream := worktreeWorkstream("ws", linkTo(tracked, "tracked"))
	survey := surveyRepo(t, &core{}, repo, []domain.Workstream{workstream},
		[]string{linkTo(tracked, "tracked-claim"), linkTo(claimedDir, "claimed")})
	if len(survey.Untracked) != 0 || len(survey.Vanished) != 0 {
		t.Errorf("Survey = %+v; want nothing untracked and nothing vanished", survey)
	}
}

func TestCoreSurveySkipsAFolderBeingCreated(t *testing.T) {
	repo := gitProject(t)
	dir := addWorktree(t, repo, "wt-a")
	provider := &core{}
	release := provider.claimDir(dir)
	defer release()
	if survey := surveyRepo(t, provider, repo, nil, nil); len(survey.Untracked) != 0 {
		t.Errorf("Survey.Untracked = %+v; want none while a create claims the folder", survey.Untracked)
	}
}

func TestCoreListFoldersOutsideGit(t *testing.T) {
	if _, err := (&core{}).ListFolders(context.Background(), ProjectRef{Path: t.TempDir()}); err == nil {
		t.Error("ListFolders on a project outside git succeeded")
	}
}

func TestCoreTrackFolderNumbersATakenName(t *testing.T) {
	tests := []struct {
		name     string
		taken    []string
		wantName string
	}{
		{"free", nil, "wt-a"},
		{"taken", []string{"wt-a"}, "wt-a-2"},
		{"taken twice", []string{"wt-a", "wt-a-2"}, "wt-a-3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			folder := domain.UntrackedWorktree{Name: "wt-a", Path: "/wt"}
			got := (&core{}).TrackFolder(folder, func(name string) bool { return slices.Contains(tt.taken, name) })
			if got.Name != tt.wantName {
				t.Errorf("TrackFolder name = %q; want %q", got.Name, tt.wantName)
			}
		})
	}
}

func TestCoreTrackFolderCapsALongNumberedName(t *testing.T) {
	long := strings.Repeat("long-branch-name-", 5) + "end"
	folder := domain.UntrackedWorktree{Name: long, Path: "/wt"}
	got := (&core{}).TrackFolder(folder, func(name string) bool { return name == long }).Name
	if len(got) > maxNameBytes || !strings.HasSuffix(got, "-2") ||
		!strings.HasPrefix(long, strings.TrimSuffix(got, "-2")) {
		t.Errorf("TrackFolder name = %q; want a prefix of the name numbered -2, within %d bytes", got, maxNameBytes)
	}
}

// A worktree added from a suggestion is one like the core creates: it follows
// its branch, keeps its name, and Remove deletes the worktree.
func TestCoreTrackFolderMakesAWorktreeWorkstream(t *testing.T) {
	repo := gitProject(t)
	project := ProjectRef{Path: repo, GitBacked: true}
	provider := &core{}
	branched := addWorktree(t, repo, "wt-a")
	survey := surveyRepo(t, provider, repo, nil, nil)
	if len(survey.Untracked) != 1 {
		t.Fatalf("Survey.Untracked = %+v; want %s", survey.Untracked, branched)
	}

	workstream := provider.TrackFolder(survey.Untracked[0], nil)
	if workstream.Name != "wt-a" || workstream.Branch != "wt-a" || !git.SamePath(workstream.WorkingDir, branched) ||
		workstream.StrategyID != "worktree" {
		t.Errorf("TrackFolder = %q on %q in %q (%s); want wt-a on wt-a in %s (worktree)",
			workstream.Name, workstream.Branch, workstream.WorkingDir, workstream.StrategyID, branched)
	}
	if refreshed := provider.Refresh(context.Background(), project, workstream); refreshed.Branch != "wt-a" {
		t.Errorf("Refresh = branch %q; want wt-a", refreshed.Branch)
	}
	// Its name was the branch's, but only an in-place workstream's name follows
	// the branch (coreCreatorData.GivenName), as for a worktree the core created.
	if out, err := exec.Command("git", "-C", branched, "checkout", "-q", "-b", "wt-b").CombinedOutput(); err != nil {
		t.Fatalf("git checkout: %v\n%s", err, out)
	}
	if refreshed := provider.Refresh(context.Background(), project, workstream); refreshed.Branch != "wt-b" ||
		refreshed.Name != "wt-a" {
		t.Errorf("Refresh after a switch = %q on %q; want wt-a on wt-b", refreshed.Name, refreshed.Branch)
	}
	if err := provider.Remove(context.Background(), project, workstream, RemoveOptions{}); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if pathExists(branched) {
		t.Errorf("%s still exists after Remove", branched)
	}
	out, err := exec.Command("git", "-C", repo, "worktree", "list").CombinedOutput()
	if err != nil {
		t.Fatalf("git worktree list: %v\n%s", err, out)
	}
	if strings.Contains(string(out), git.RealPath(branched)) {
		t.Errorf("git worktree list still lists %s:\n%s", branched, out)
	}
}

func TestCoreListingContext(t *testing.T) {
	tests := []struct {
		name       string
		callerTime time.Duration // the caller's deadline; 0: none
		wantAtMost time.Duration
		wantOver   time.Duration
	}{
		{name: "a refresh, bounded like a terminal read", wantAtMost: time.Second},
		{name: "an Add, bounded by its caller", callerTime: 5 * time.Second, wantAtMost: 5 * time.Second,
			wantOver: 4 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			if tt.callerTime > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tt.callerTime)
				defer cancel()
			}
			listingCtx, cancel := (&core{}).listingContext(ctx)
			defer cancel()
			deadline, ok := listingCtx.Deadline()
			if left := time.Until(deadline); !ok || left > tt.wantAtMost || left <= tt.wantOver {
				t.Errorf("listing deadline in %v (set %v); want within (%v, %v]", left, ok, tt.wantOver, tt.wantAtMost)
			}
		})
	}
}
