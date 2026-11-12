package provider

import (
	"context"
	"encoding/json"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
	"github.com/eclipsesource/homeshell/daemon/internal/git"
	"github.com/eclipsesource/homeshell/daemon/internal/persist"
	"github.com/eclipsesource/homeshell/daemon/internal/settings"
)

func TestParseCoreConfig(t *testing.T) {
	tests := []struct {
		name                            string
		config                          string
		gitBacked                       bool
		strategy, branch, baseRef, dest string
	}{
		{"empty defaults to in-place", "", false, "in-place", "", "", ""},
		{"empty defaults to a worktree in git", "", true, "worktree", "", "", ""},
		{"strategy only", `{"strategy":"copy"}`, true, "copy", "", "", ""},
		{"all fields", `{"strategy":"worktree","branch":"feat","baseRef":"origin/main","path":"/tmp/x"}`, true, "worktree", "feat", "origin/main", "/tmp/x"},
		{"unparseable falls back", `not json`, false, "in-place", "", "", ""},
		{"branch dropped in place", `{"strategy":"in-place","branch":"feat","baseRef":"main"}`, true, "in-place", "", "", ""},
		{"branch dropped for copy", `{"strategy":"copy","branch":"feat","baseRef":"main","path":"/tmp/x"}`, true, "copy", "", "", "/tmp/x"},
		{"branch dropped with no strategy outside git", `{"branch":"feat"}`, false, "in-place", "", "", ""},
		{"branch kept with no strategy in git", `{"branch":"feat"}`, true, "worktree", "feat", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, b, br, d := parseCoreConfig([]byte(tc.config), tc.gitBacked)
			if s != tc.strategy || b != tc.branch || br != tc.baseRef || d != tc.dest {
				t.Errorf("parseCoreConfig(%q, %v) = (%q,%q,%q,%q); want (%q,%q,%q,%q)",
					tc.config, tc.gitBacked, s, b, br, d, tc.strategy, tc.branch, tc.baseRef, tc.dest)
			}
		})
	}
}

func TestCoreWorkstreamName(t *testing.T) {
	if got := coreWorkstreamName("/home/me/proj", "feature/x"); got != "feature/x" {
		t.Errorf("name = %q, want the branch", got)
	}
	if got := coreWorkstreamName("/home/me/proj", ""); got != "proj" {
		t.Errorf("name without a branch = %q, want the project basename", got)
	}
	if got := coreWorkstreamName("/", ""); got != "workstream" {
		t.Errorf("degenerate path name = %q, want the fallback", got)
	}
}

func TestStrategyLabel(t *testing.T) {
	tests := []struct{ strategyID, want string }{
		{"in-place", "in place"},
		{"copy", "folder"},
	}
	for _, tt := range tests {
		if got := domain.StrategyLabel(tt.strategyID); got != tt.want {
			t.Errorf("StrategyLabel(%q) = %q, want %q", tt.strategyID, got, tt.want)
		}
	}
}

// gitProject returns a one-commit repository on main with a feature branch
// and a tag, checked out on feature.
func gitProject(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"commit", "-q", "--allow-empty", "-m", "initial"},
		{"tag", "v1.0"},
		{"checkout", "-q", "-b", "feature"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_CONFIG_SYSTEM=/dev/null",
			"GIT_AUTHOR_NAME=test",
			"GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test",
			"GIT_COMMITTER_EMAIL=test@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

// schemaProperties decodes a schema's properties.
func schemaProperties(t *testing.T, schema json.RawMessage) map[string]map[string]any {
	t.Helper()
	var decoded struct {
		Properties map[string]map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(schema, &decoded); err != nil {
		t.Fatalf("schema is not valid JSON: %v\n%s", err, schema)
	}
	return decoded.Properties
}

// The validation schema is the form without its suggestions, so it asks git
// nothing.
func TestCoreValidationSchema(t *testing.T) {
	props := schemaProperties(t, newCore().ValidationSchema(context.Background(), ProjectRef{Path: t.TempDir(), GitBacked: true}))
	if len(props["branch"]["examples"].([]any)) != 0 || len(props["baseRef"]["examples"].([]any)) != 0 || props["baseRef"]["default"] != "" {
		t.Errorf("branch = %v, baseRef = %v; want no suggestions", props["branch"], props["baseRef"])
	}
	plain := schemaProperties(t, newCore().ValidationSchema(context.Background(), ProjectRef{Path: t.TempDir()}))
	if _, ok := plain["branch"]; ok {
		t.Error("non-git validation schema has a branch property")
	}
}

func TestCoreSchema(t *testing.T) {
	t.Run("git-backed project includes worktree fields", func(t *testing.T) {
		props := schemaProperties(t, newCore().Schema(context.Background(), ProjectRef{Path: gitProject(t), GitBacked: true}))
		for _, key := range []string{"strategy", "branch", "baseRef", "path"} {
			if _, ok := props[key]; !ok {
				t.Errorf("git-backed schema missing property %q", key)
			}
		}
		strategies, _ := props["strategy"]["enum"].([]any)
		if !slices.Contains(strategies, any("worktree")) {
			t.Errorf("git-backed schema should include worktree strategy")
		}
	})

	t.Run("git-backed project suggests its refs", func(t *testing.T) {
		props := schemaProperties(t, newCore().Schema(context.Background(), ProjectRef{Path: gitProject(t), GitBacked: true}))
		branch, baseRef := props["branch"], props["baseRef"]
		if got := sortedStrings(branch["examples"]); !slices.Equal(got, []string{"main"}) {
			t.Errorf("branch examples = %v, want the local branches but the checked-out feature", got)
		}
		// main, not the checked-out feature branch.
		if baseRef["default"] != "main" {
			t.Errorf("baseRef default = %v, want %q", baseRef["default"], "main")
		}
		if got := sortedStrings(baseRef["examples"]); !slices.Equal(got, []string{"feature", "main", "v1.0"}) {
			t.Errorf("baseRef examples = %v, want every ref", got)
		}
	})

	t.Run("a failing git query drops the suggestions only", func(t *testing.T) {
		schema := newCore().Schema(context.Background(), ProjectRef{Path: t.TempDir(), GitBacked: true})
		props := schemaProperties(t, schema)
		if len(props["branch"]["examples"].([]any)) != 0 || len(props["baseRef"]["examples"].([]any)) != 0 {
			t.Errorf("examples = %v, %v; want none", props["branch"]["examples"], props["baseRef"]["examples"])
		}
		if err := ValidateConfig(schema, json.RawMessage(`{"strategy":"worktree","branch":"feature/x"}`)); err != nil {
			t.Errorf("ValidateConfig = %v; want the schema still usable", err)
		}
	})

	t.Run("the suggestion keywords do not get in the way of validation", func(t *testing.T) {
		schema := newCore().Schema(context.Background(), ProjectRef{Path: gitProject(t), GitBacked: true})
		for _, config := range []string{
			`{"strategy":"worktree","branch":"fix-login-bug","baseRef":"main"}`,
			`{"strategy":"worktree","branch":"a branch no example names","baseRef":"deadbeef"}`,
			`{"strategy":"in-place","baseRef":"main"}`,
		} {
			if err := ValidateConfig(schema, json.RawMessage(config)); err != nil {
				t.Errorf("ValidateConfig(%s) = %v; want nil", config, err)
			}
		}
	})

	t.Run("non-git project excludes worktree fields", func(t *testing.T) {
		props := schemaProperties(t, newCore().Schema(context.Background(), ProjectRef{Path: t.TempDir(), GitBacked: false}))
		for _, key := range []string{"strategy", "path"} {
			if _, ok := props[key]; !ok {
				t.Errorf("non-git schema missing property %q", key)
			}
		}
		for _, key := range []string{"branch", "baseRef"} {
			if _, ok := props[key]; ok {
				t.Errorf("non-git schema should not have property %q", key)
			}
		}
		strategies, _ := props["strategy"]["enum"].([]any)
		if slices.Contains(strategies, any("worktree")) {
			t.Errorf("non-git schema should not include worktree strategy")
		}
	})
}

// A folder holds one workstream: once the project folder is taken, the form
// stops offering in place, and a plain project defaults to copy instead.
func TestCoreSchemaStrategies(t *testing.T) {
	tests := []struct {
		name        string
		gitBacked   bool
		folderTaken bool
		wantIDs     []any
		wantLabels  []any
		wantDefault string
	}{
		{"git project, folder free", true, false, []any{"in-place", "worktree", "copy"},
			[]any{"In place", "Worktree", "Folder: a copy of the project"}, "worktree"},
		{"git project, folder taken", true, true, []any{"worktree", "copy"},
			[]any{"Worktree", "Folder: a copy of the project"}, "worktree"},
		{"plain project, folder free", false, false, []any{"in-place", "copy"},
			[]any{"In place", "Folder: a copy of the project"}, "in-place"},
		{"plain project, folder taken", false, true, []any{"copy"},
			[]any{"Folder: a copy of the project"}, "copy"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			project := ProjectRef{Path: t.TempDir(), GitBacked: tt.gitBacked, FolderTaken: tt.folderTaken}
			strategy := schemaProperties(t, newCore().Schema(context.Background(), project))["strategy"]
			if !reflect.DeepEqual(strategy["enum"], tt.wantIDs) || !reflect.DeepEqual(strategy["enumNames"], tt.wantLabels) ||
				strategy["default"] != tt.wantDefault {
				t.Errorf("strategy = %v, want enum %v, enumNames %v, default %q",
					strategy, tt.wantIDs, tt.wantLabels, tt.wantDefault)
			}
		})
	}
}

func TestGitSchemaSuggestsTheDefaultBase(t *testing.T) {
	// A default base missing from the listed refs (cut by the cap, say) is
	// suggested anyway, first.
	props := schemaProperties(t, gitSchemaSuggesting(false, []git.Ref{{Name: "feature", Branch: "feature"}}, "origin/main"))
	if got := props["baseRef"]["examples"]; !reflect.DeepEqual(got, []any{"origin/main", "feature"}) {
		t.Errorf("baseRef examples = %v, want the default base, then the refs", got)
	}
	// Listed already, it is not repeated.
	props = schemaProperties(t, gitSchemaSuggesting(false, []git.Ref{{Name: "main", Branch: "main"}}, "main"))
	if got := props["baseRef"]["examples"]; !reflect.DeepEqual(got, []any{"main"}) {
		t.Errorf("baseRef examples = %v, want main once", got)
	}
}

// A branch is suggested by its own name, and as a base by the name git reads
// as that branch alone; one checked out in a worktree cannot be reused.
func TestGitSchemaSuggestsBranches(t *testing.T) {
	props := schemaProperties(t, gitSchemaSuggesting(false, []git.Ref{
		{Name: "heads/main", Branch: "main"},
		{Name: "feature", Branch: "feature", CheckedOut: true},
		{Name: "tags/main"},
	}, "heads/main"))
	if got := props["branch"]["examples"]; !reflect.DeepEqual(got, []any{"main"}) {
		t.Errorf("branch examples = %v, want main alone", got)
	}
	if got := props["baseRef"]["examples"]; !reflect.DeepEqual(got, []any{"heads/main", "feature", "tags/main"}) {
		t.Errorf("baseRef examples = %v, want every ref by its unambiguous name", got)
	}
}

// sortedStrings returns a decoded JSON string array, sorted.
func sortedStrings(value any) []string {
	var out []string
	for _, item := range value.([]any) {
		out = append(out, item.(string))
	}
	slices.Sort(out)
	return out
}

func TestCoreCreateInPlace(t *testing.T) {
	project := t.TempDir()
	c := &core{root: t.TempDir()}
	ws, err := c.Create(context.Background(), CreateRequest{
		Project: ProjectRef{Path: project},
		Config:  []byte(`{"strategy":"in-place"}`),
	})
	if err != nil {
		t.Fatalf("Create in-place: %v", err)
	}
	if ws.WorkingDir != project {
		t.Errorf("WorkingDir = %q, want the project dir %q", ws.WorkingDir, project)
	}
	if ws.Name != filepath.Base(project) {
		t.Errorf("Name = %q, want project basename %q", ws.Name, filepath.Base(project))
	}
	if ws.StrategyID != "in-place" {
		t.Errorf("StrategyID = %q, want %q", ws.StrategyID, "in-place")
	}
	var cd coreCreatorData
	if err := json.Unmarshal(ws.CreatorData, &cd); err != nil || cd.Strategy != "in-place" {
		t.Errorf("CreatorData = %s (err %v); want strategy in-place", ws.CreatorData, err)
	}
}

// A form sends the branch it derived whatever the strategy; a workstream that
// is not a worktree must not show it.
func TestCoreCreateInPlaceIgnoresTheBranch(t *testing.T) {
	provider := &core{root: t.TempDir()}
	workstream, err := provider.Create(context.Background(), CreateRequest{
		Project: ProjectRef{Path: t.TempDir(), GitBacked: true},
		Config:  []byte(`{"strategy":"in-place","branch":"fix-login-bug","baseRef":"main"}`),
	})
	if err != nil {
		t.Fatalf("Create in-place: %v", err)
	}
	if workstream.Branch != "" {
		t.Errorf("Branch = %q, want none for an in-place workstream", workstream.Branch)
	}
}

func TestCoreCreateCopyManagedDirThenRemove(t *testing.T) {
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "hello.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	c := &core{root: root}

	// No explicit path: the core materializes a managed directory under root.
	ws, err := c.Create(context.Background(), CreateRequest{
		Project: ProjectRef{Path: project},
		Config:  []byte(`{"strategy":"copy"}`),
	})
	if err != nil {
		t.Fatalf("Create copy: %v", err)
	}
	// Named after the workstream, which a blank name names after the project.
	if want := filepath.Join(root, filepath.Base(project)); ws.WorkingDir != want {
		t.Fatalf("WorkingDir = %q, want %q", ws.WorkingDir, want)
	}
	if _, err := os.Stat(filepath.Join(ws.WorkingDir, "hello.txt")); err != nil {
		t.Errorf("copied working dir missing the project's file: %v", err)
	}

	if err := c.Remove(context.Background(), ProjectRef{Path: project}, ws, RemoveOptions{}); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(ws.WorkingDir); !os.IsNotExist(err) {
		t.Errorf("working dir still exists after Remove (err %v)", err)
	}
}

// The default workstreams root follows the data directory — -home-dir, then
// $XDG_DATA_HOME — and it is where the core provider puts a managed directory when
// no location is configured. The Enclave provider vouches for the same root, so
// both read it from DefaultWorkstreamsRoot.
func TestDefaultWorkstreamsRootFollowsTheDataDirectory(t *testing.T) {
	homeDir := t.TempDir()
	xdgDataHome := t.TempDir()
	tests := []struct {
		name    string
		homeDir string
		want    string
	}{
		{name: "an explicit home directory", homeDir: homeDir, want: filepath.Join(homeDir, "workstreams")},
		{name: "XDG_DATA_HOME", want: filepath.Join(xdgDataHome, "homeshell", "workstreams")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			previous := persist.HomeDir()
			persist.SetHomeDir(tt.homeDir)
			t.Cleanup(func() { persist.SetHomeDir(previous) })
			t.Setenv("XDG_DATA_HOME", xdgDataHome)

			got, err := DefaultWorkstreamsRoot()
			if err != nil {
				t.Fatalf("DefaultWorkstreamsRoot: %v", err)
			}
			if got != tt.want {
				t.Errorf("DefaultWorkstreamsRoot() = %q, want %q", got, tt.want)
			}
			if root := newCore().root; root != got {
				t.Errorf("core root = %q, want the default workstreams root %q", root, got)
			}
		})
	}
}

func TestCoreCreateCopyUsesConfiguredLocation(t *testing.T) {
	project := t.TempDir()
	loc := t.TempDir()
	st, err := settings.Load(filepath.Join(t.TempDir(), "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	next := settings.Defaults()
	next.Workstreams.Location = loc
	if _, err := st.Set(next); err != nil {
		t.Fatal(err)
	}

	// A different default root proves the configured location is what's used.
	c := &core{root: t.TempDir()}
	c.SetSettings(st)
	ws, err := c.Create(context.Background(), CreateRequest{
		Project: ProjectRef{Path: project},
		Config:  []byte(`{"strategy":"copy"}`),
	})
	if err != nil {
		t.Fatalf("Create copy: %v", err)
	}
	if !strings.HasPrefix(ws.WorkingDir, loc) {
		t.Errorf("WorkingDir = %q, want a managed dir under the configured location %q", ws.WorkingDir, loc)
	}
}

func TestCoreCreateCopyEmptyLocationFallsBackToRoot(t *testing.T) {
	project := t.TempDir()
	root := t.TempDir()
	c := &core{root: root, location: func() string { return "" }}
	ws, err := c.Create(context.Background(), CreateRequest{
		Project: ProjectRef{Path: project},
		Config:  []byte(`{"strategy":"copy"}`),
	})
	if err != nil {
		t.Fatalf("Create copy: %v", err)
	}
	if !strings.HasPrefix(ws.WorkingDir, root) {
		t.Errorf("WorkingDir = %q, want a managed dir under the default root %q (empty location)", ws.WorkingDir, root)
	}
}

func TestCoreCreateCopyUnderALocationRelativeToTheProject(t *testing.T) {
	parent := t.TempDir()
	project := filepath.Join(parent, "repo")
	if err := os.Mkdir(project, 0o755); err != nil {
		t.Fatal(err)
	}
	c := &core{root: t.TempDir(), location: func() string { return "../worktrees" }}
	ws, err := c.Create(context.Background(), CreateRequest{
		Project: ProjectRef{Path: project},
		Config:  []byte(`{"strategy":"copy"}`),
	})
	if err != nil {
		t.Fatalf("Create copy: %v", err)
	}
	if want := filepath.Join(parent, "worktrees") + string(filepath.Separator); !strings.HasPrefix(ws.WorkingDir, want) {
		t.Errorf("WorkingDir = %q, want a managed dir under %q", ws.WorkingDir, want)
	}
}

func TestCoreRemoveKeepFilesRetainsDirectory(t *testing.T) {
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "hello.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	core := &core{root: root}

	ws, err := core.Create(context.Background(), CreateRequest{
		Project: ProjectRef{Path: project},
		Config:  []byte(`{"strategy":"copy"}`),
	})
	if err != nil {
		t.Fatalf("Create copy: %v", err)
	}

	if err := core.Remove(context.Background(), ProjectRef{Path: project}, ws, RemoveOptions{KeepFiles: true}); err != nil {
		t.Fatalf("Remove with KeepFiles: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ws.WorkingDir, "hello.txt")); err != nil {
		t.Errorf("working dir should survive Remove with KeepFiles, but stat failed: %v", err)
	}
}

func TestCoreCreateCopyExplicitPath(t *testing.T) {
	project := t.TempDir()
	dest := filepath.Join(t.TempDir(), "explicit-copy")
	c := &core{root: t.TempDir()}
	ws, err := c.Create(context.Background(), CreateRequest{
		Project: ProjectRef{Path: project},
		Config:  []byte(`{"strategy":"copy","path":"` + dest + `"}`),
	})
	if err != nil {
		t.Fatalf("Create copy with explicit path: %v", err)
	}
	if ws.WorkingDir != dest {
		t.Errorf("WorkingDir = %q, want the explicit path %q", ws.WorkingDir, dest)
	}
}

func TestCoreCreateWorktreeRequiresGit(t *testing.T) {
	c := &core{root: t.TempDir()}
	_, err := c.Create(context.Background(), CreateRequest{
		Project: ProjectRef{Path: t.TempDir(), GitBacked: false},
		Config:  []byte(`{"strategy":"worktree","branch":"feat"}`),
	})
	if err == nil {
		t.Fatal("worktree on a non-git project should fail")
	}
}

// A worktree left without a branch is on one named after the workstream, and
// after the project when the name is left empty too.
func TestCoreCreateWorktreeDerivesTheBranch(t *testing.T) {
	tests := []struct {
		name       string
		wsName     string
		wantName   string
		wantBranch string
	}{
		{name: "from the name", wsName: "Fix login bug", wantName: "Fix login bug", wantBranch: "fix-login-bug"},
		{name: "from the project", wsName: "", wantName: "My Repo", wantBranch: "my-repo"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := gitProject(t)
			project := filepath.Join(filepath.Dir(repo), "My Repo")
			if err := os.Rename(repo, project); err != nil {
				t.Fatal(err)
			}
			provider := &core{root: t.TempDir()}
			workstream, err := provider.Create(context.Background(), CreateRequest{
				Project: ProjectRef{Path: project, GitBacked: true},
				Name:    tt.wsName,
				Config:  []byte(`{"strategy":"worktree"}`),
			})
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			if workstream.Name != tt.wantName || workstream.Branch != tt.wantBranch {
				t.Errorf("Create = name %q on %q, want %q on %q", workstream.Name, workstream.Branch, tt.wantName, tt.wantBranch)
			}
		})
	}
}

// A name left empty is numbered past the names the project already has, and a
// worktree's past the branches too, so the same blank form can be sent again.
// The project folder has "feature" checked out.
func TestCoreNameAndBranchNumbersADefaultName(t *testing.T) {
	repo := gitProject(t)
	project := filepath.Join(filepath.Dir(repo), "My Repo")
	if err := os.Rename(repo, project); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", project, "branch", "my-repo", "main").CombinedOutput(); err != nil {
		t.Fatalf("git branch: %v\n%s", err, out)
	}
	taken := func(names ...string) func(string) bool {
		return func(name string) bool { return slices.Contains(names, name) }
	}
	tests := []struct {
		name       string
		strategy   string
		branch     string
		nameTaken  func(string) bool
		wantName   string
		wantBranch string
	}{
		// In place, the workstream is on the branch checked out in the project.
		{name: "a free name", strategy: "in-place", wantName: "feature", wantBranch: "feature"},
		{name: "a taken name", strategy: "in-place", nameTaken: taken("feature", "feature-2"), wantName: "feature-3", wantBranch: "feature"},
		{name: "an existing branch", strategy: "worktree", wantName: "My Repo-2", wantBranch: "my-repo-2"},
		{name: "a taken name and branch", strategy: "worktree", nameTaken: taken("My Repo-2"), wantName: "My Repo-3", wantBranch: "my-repo-3"},
		{name: "a name the branch gives", strategy: "worktree", branch: "topic", nameTaken: taken("topic"), wantName: "topic-2", wantBranch: "topic"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name, branch := coreNameAndBranch(context.Background(), CreateRequest{
				Project:   ProjectRef{Path: project, GitBacked: true},
				NameTaken: tt.nameTaken,
			}, tt.strategy, tt.branch)
			if name != tt.wantName || branch != tt.wantBranch {
				t.Errorf("coreNameAndBranch = %q on %q, want %q on %q", name, branch, tt.wantName, tt.wantBranch)
			}
		})
	}
}

// The branch a blank form would name exists already, so the next free name is
// taken instead, and creating twice gives two workstreams on two branches.
func TestCoreCreateWorktreeTwiceFromABlankForm(t *testing.T) {
	repo := gitProject(t)
	provider := &core{root: t.TempDir()}
	var names []string
	for range 2 {
		request := CreateRequest{
			Project:   ProjectRef{Path: repo, GitBacked: true},
			NameTaken: func(name string) bool { return slices.Contains(names, name) },
			Config:    []byte(`{"strategy":"worktree"}`),
		}
		if err := provider.ValidateCreate(context.Background(), request); err != nil {
			t.Fatalf("ValidateCreate: %v", err)
		}
		workstream, err := provider.Create(context.Background(), request)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		names = append(names, workstream.Name)
	}
	base := filepath.Base(repo)
	if want := []string{base, base + "-2"}; !slices.Equal(names, want) {
		t.Errorf("names = %v, want %v", names, want)
	}
}

// A branch whose worktree folder was removed by hand, git's registration left
// behind, is checked out nowhere: another workstream can take it.
func TestCoreCreateWorktreeOnABranchWhoseFolderIsGone(t *testing.T) {
	repo := gitProject(t)
	provider := &core{root: t.TempDir()}
	request := func(name string) CreateRequest {
		return CreateRequest{
			Project: ProjectRef{Path: repo, GitBacked: true},
			Name:    name,
			Config:  []byte(`{"strategy":"worktree","branch":"feat"}`),
		}
	}
	first, err := provider.Create(context.Background(), request("first"))
	if err != nil {
		t.Fatalf("Create first: %v", err)
	}
	if err := os.RemoveAll(first.WorkingDir); err != nil {
		t.Fatal(err)
	}

	if err := provider.ValidateCreate(context.Background(), request("second")); err != nil {
		t.Fatalf("ValidateCreate second: %v", err)
	}
	second, err := provider.Create(context.Background(), request("second"))
	if err != nil {
		t.Fatalf("Create second: %v", err)
	}
	if second.Branch != "feat" || second.WorkingDir == first.WorkingDir {
		t.Errorf("second = %q on %q, want a folder of its own on feat", second.WorkingDir, second.Branch)
	}
}

func TestCoreRemoveInPlaceIsNoop(t *testing.T) {
	project := t.TempDir()
	c := &core{root: t.TempDir()}
	ws, err := c.Create(context.Background(), CreateRequest{
		Project: ProjectRef{Path: project},
		Config:  []byte(`{"strategy":"in-place"}`),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := c.Remove(context.Background(), ProjectRef{Path: project}, ws, RemoveOptions{}); err != nil {
		t.Fatalf("Remove in-place: %v", err)
	}
	// The project directory must survive (in-place is not ours to delete).
	if _, err := os.Stat(project); err != nil {
		t.Errorf("in-place project dir removed by Remove: %v", err)
	}
}

// A draft is what Create would derive: the name, a worktree's branch from it,
// and a managed directory named after the project and the workstream.
func TestCoreResolveDraft(t *testing.T) {
	project := filepath.Join(t.TempDir(), "My Repo")
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "my-repo-taken"), 0o755); err != nil {
		t.Fatal(err)
	}
	provider := &core{root: root}
	tests := []struct {
		name, draftName, config string
		want                    Draft
	}{
		{"in place", "", `{}`, Draft{Name: "My Repo", Config: map[string]string{"branch": "", "path": ""}}},
		{"a copy", "", `{"strategy":"copy"}`, Draft{Name: "My Repo", Config: map[string]string{"branch": "", "path": filepath.Join(root, "my-repo")}}},
		{"a worktree", "Fix Login", `{"strategy":"worktree"}`, Draft{Name: "Fix Login", Config: map[string]string{"branch": "fix-login", "path": filepath.Join(root, "my-repo-fix-login")}}},
		{"a path of its own", "x", `{"strategy":"copy","path":"/elsewhere"}`, Draft{Name: "x", Config: map[string]string{"branch": "", "path": "/elsewhere"}}},
		{"a directory that exists", "taken", `{"strategy":"copy"}`, Draft{Name: "taken", Config: map[string]string{"branch": "", "path": filepath.Join(root, "my-repo-taken-2")}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := provider.ResolveDraft(context.Background(), CreateRequest{
				Project: ProjectRef{Path: project},
				Name:    tt.draftName,
				Config:  []byte(tt.config),
			})
			if got.Name != tt.want.Name || !maps.Equal(got.Config, tt.want.Config) {
				t.Errorf("ResolveDraft = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// A root that cannot be resolved leaves the draft its name and branch, so a
// blank create is still named, but no managed directory.
func TestCoreResolveDraftUnderAnInvalidLocation(t *testing.T) {
	provider := &core{root: t.TempDir(), location: func() string { return "~other/worktrees" }}
	got := provider.ResolveDraft(context.Background(), CreateRequest{
		Project: ProjectRef{Path: filepath.Join(t.TempDir(), "repo")},
		Name:    "Fix Login",
		Config:  []byte(`{"strategy":"worktree"}`),
	})
	want := Draft{Name: "Fix Login", Config: map[string]string{"branch": "fix-login", "path": ""}}
	if got.Name != want.Name || !maps.Equal(got.Config, want.Config) {
		t.Errorf("ResolveDraft = %+v, want %+v", got, want)
	}
}

// Creates whose names flatten to one directory each prepare a directory of
// their own, though another create's has yet to appear on disk.
func TestCoreCreatesClaimDistinctManagedDirs(t *testing.T) {
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "hello.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	provider := &core{root: root}
	inFlight, release := provider.claimManagedDir(root, project, "fix login")
	names := []string{"Fix Login", "fix-login"}
	dirs := make([]string, len(names))
	errs := make([]error, len(names))
	var wait sync.WaitGroup
	for i, name := range names {
		wait.Add(1)
		go func() {
			defer wait.Done()
			workstream, err := provider.Create(context.Background(), CreateRequest{
				Project: ProjectRef{Path: project},
				Name:    name,
				Config:  []byte(`{"strategy":"copy"}`),
			})
			dirs[i], errs[i] = workstream.WorkingDir, err
		}()
	}
	wait.Wait()
	release()
	for i := range names {
		if errs[i] != nil {
			t.Fatalf("Create %q: %v", names[i], errs[i])
		}
	}
	if dirs[0] == dirs[1] || slices.Contains(dirs, inFlight) {
		t.Fatalf("creates prepared %q beside the claimed %q; want distinct directories", dirs, inFlight)
	}
	if len(provider.claimed) != 0 {
		t.Errorf("claims left after the creates: %v", provider.claimed)
	}
}

// A branch derived from a long name is capped, at a separator where one is near
// the end, and keeps the number a name ends in; one typed is kept as it is.
func TestCoreNameAndBranchCapsADerivedBranch(t *testing.T) {
	tests := []struct {
		name, reqName, branch, wantBranch string
	}{
		{name: "a short name", reqName: "Fix Login", wantBranch: "fix-login"},
		{name: "words", reqName: strings.Repeat("words ", 60), wantBranch: strings.TrimPrefix(strings.Repeat("-words", 10), "-")},
		{name: "slashes", reqName: strings.Repeat("feats/", 20), wantBranch: strings.TrimPrefix(strings.Repeat("/feats", 10), "/")},
		{name: "no separator", reqName: strings.Repeat("x", 300), wantBranch: strings.Repeat("x", 64)},
		{name: "a number", reqName: strings.Repeat("x", 300) + "-12", wantBranch: strings.Repeat("x", 61) + "-12"},
		{name: "a typed branch", reqName: strings.Repeat("x", 300), branch: strings.Repeat("y", 100), wantBranch: strings.Repeat("y", 100)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, branch := coreNameAndBranch(context.Background(), CreateRequest{Name: tt.reqName}, "worktree", tt.branch)
			if branch != tt.wantBranch {
				t.Errorf("branch = %q, want %q", branch, tt.wantBranch)
			}
			if len(branch) <= maxNameBytes {
				if err := git.ValidateBranchName(branch); err != nil {
					t.Errorf("derived branch %q: %v", branch, err)
				}
			}
		})
	}
}

// A blank name in a project whose folder name is longer than a branch may be
// is numbered onto a branch of its own.
func TestCoreNameAndBranchNumbersPastALongCappedBranch(t *testing.T) {
	repo := gitProject(t)
	project := filepath.Join(filepath.Dir(repo), strings.Repeat("p", 80))
	if err := os.Rename(repo, project); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", project, "branch", strings.Repeat("p", 64), "main").CombinedOutput(); err != nil {
		t.Fatalf("git branch: %v\n%s", err, out)
	}
	_, branch := coreNameAndBranch(context.Background(), CreateRequest{
		Project: ProjectRef{Path: project, GitBacked: true},
	}, "worktree", "")
	if want := strings.Repeat("p", 62) + "-2"; branch != want {
		t.Errorf("branch = %q, want %q", branch, want)
	}
}

func TestManagedDirName(t *testing.T) {
	tests := []struct{ project, name, want string }{
		{"/src/homeshell", "feature", "homeshell-feature"},
		{"/src/homeshell", "Feat/Login", "homeshell-feat-login"},
		{"/src/homeshell", "homeshell", "homeshell"},
		{"/src/homeshell", "homeshell-2", "homeshell-2"},
		{"/src/homeshell", "homeshellish", "homeshell-homeshellish"},
		{"/src/homeshell", "!!!", "homeshell"},
		{"/", "", "workstream"},
		{"/src/homeshell", strings.Repeat("word ", 60), "homeshell" + strings.Repeat("-word", 11)},
		{"/src/homeshell", strings.Repeat("words ", 60), "homeshell" + strings.Repeat("-words", 9)},
		{"/src/homeshell", strings.Repeat("x", 300), "homeshell-" + strings.Repeat("x", 54)},
		{"/src/" + strings.Repeat("p", 70), "", strings.Repeat("p", 64)},
	}
	for _, tt := range tests {
		if got := filepath.Base(managedDir(t.TempDir(), tt.project, tt.name, nil)); got != tt.want {
			t.Errorf("managedDir(%q, %q) = %q, want %q", tt.project, tt.name, got, tt.want)
		}
	}

	root := t.TempDir()
	long := strings.Repeat("x", 300)
	first := managedDir(root, "/src/homeshell", long, nil)
	if err := os.Mkdir(first, 0o755); err != nil {
		t.Fatal(err)
	}
	if got, want := managedDir(root, "/src/homeshell", long, nil), filepath.Join(root, "homeshell-"+strings.Repeat("x", 52)+"-2"); got != want {
		t.Errorf("managedDir beside %q = %q, want %q", first, got, want)
	}
}
