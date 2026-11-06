package provider

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The rules the create form's inputs are held to, checked on the request rather
// than minutes later on a workstream nobody wanted. Each case is a value a user
// can type into the Branch or Destination-path box.
func TestCoreValidateCreate(t *testing.T) {
	tests := []struct {
		name      string
		wsName    string
		project   string
		config    string
		wantField string // "" means the config is accepted
		wantText  string // substring the reason must carry
	}{
		{name: "a plain branch", config: `{"strategy":"worktree","branch":"main"}`},
		{name: "a path-shaped branch", config: `{"strategy":"worktree","branch":"feature/self-update"}`},
		{name: "dots and dashes", config: `{"strategy":"worktree","branch":"release-1.2.3"}`},

		{name: "a space", config: `{"strategy":"worktree","branch":"my branch"}`,
			wantField: "/branch", wantText: "illegal characters"},
		{name: "a colon", config: `{"strategy":"worktree","branch":"my:branch"}`,
			wantField: "/branch", wantText: "illegal characters"},
		{name: "a tilde", config: `{"strategy":"worktree","branch":"my~branch"}`,
			wantField: "/branch", wantText: "illegal characters"},
		{name: "a backslash", config: `{"strategy":"worktree","branch":"my\\branch"}`,
			wantField: "/branch", wantText: "illegal characters"},
		{name: "a leading dash", config: `{"strategy":"worktree","branch":"-flag"}`,
			wantField: "/branch", wantText: "illegal characters"},
		{name: "double dots", config: `{"strategy":"worktree","branch":"my..branch"}`,
			wantField: "/branch", wantText: "forbidden pattern"},
		{name: "a .lock suffix", config: `{"strategy":"worktree","branch":"my.lock"}`,
			wantField: "/branch", wantText: "forbidden pattern"},
		// Rejected by the character set rather than the forbidden-pattern list —
		// `{` is not a legal refname character at all, so the pattern check never
		// gets a look. Either rule is a refusal; the test asserts the refusal.
		{name: "an @{ sequence", config: `{"strategy":"worktree","branch":"my@{branch"}`,
			wantField: "/branch"},
		{name: "a trailing slash", config: `{"strategy":"worktree","branch":"branch/"}`,
			wantField: "/branch", wantText: "illegal characters"},

		// The branch belongs to the worktree strategy. Under any other one it is
		// ignored entirely, so validating it would refuse a create it cannot affect.
		{name: "junk branch ignored in place", config: `{"strategy":"in-place","branch":"my branch"}`},
		{name: "junk branch ignored for copy", config: `{"strategy":"copy","branch":"my branch","path":"/tmp/x"}`},
		// An empty branch is derived from the workstream's name, or the project's
		// when the name is empty too; one nothing can be derived from is refused.
		{name: "empty branch derived from the name", wsName: "Fix login", config: `{"strategy":"worktree"}`},
		{name: "empty branch and name derived from the project", project: "/tmp/my-repo", config: `{"strategy":"worktree"}`},
		{name: "nothing to derive from the name", wsName: "日本語", config: `{"strategy":"worktree","branch":""}`,
			wantField: "/branch", wantText: `from "日本語"`},
		{name: "nothing to derive from the project", project: "/tmp/日本語", config: `{"strategy":"worktree"}`,
			wantField: "/branch", wantText: `from "日本語"`},

		// git would read such a base ref as an option; one it does not use is ignored.
		{name: "an option-like base ref", config: `{"strategy":"worktree","branch":"b","baseRef":"--no-checkout"}`,
			wantField: "/baseRef", wantText: "must not start"},
		{name: "a base ref", config: `{"strategy":"worktree","branch":"b","baseRef":"origin/main"}`},
		{name: "option-like base ref ignored in place", config: `{"strategy":"in-place","baseRef":"-x"}`},

		{name: "an absolute path", config: `{"strategy":"copy","path":"/tmp/somewhere"}`},
		{name: "a relative path", config: `{"strategy":"copy","path":"../up"}`,
			wantField: "/path"},
	}

	provider := &core{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := provider.ValidateCreate(context.Background(), CreateRequest{
				Project: ProjectRef{Path: tt.project},
				Name:    tt.wsName,
				Config:  json.RawMessage(tt.config),
			})
			if tt.wantField == "" {
				if err != nil {
					t.Fatalf("ValidateCreate() = %v, want it accepted", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("ValidateCreate() = nil, want a refusal on %s", tt.wantField)
			}
			fields := FieldErrors(err)
			if fields == nil {
				t.Fatalf("error %v carries no field detail; the client cannot say which input is wrong", err)
			}
			reason, ok := fields[tt.wantField]
			if !ok {
				t.Fatalf("fields = %v, want one keyed %s", fields, tt.wantField)
			}
			if tt.wantText != "" && !strings.Contains(reason, tt.wantText) {
				t.Errorf("reason = %q, want it to mention %q", reason, tt.wantText)
			}
		})
	}
}

// The offending value is quoted back, so a user reading the form sees what was
// rejected rather than a rule they have to apply themselves.
func TestCoreValidateCreateQuotesTheValue(t *testing.T) {
	provider := &core{}
	err := provider.ValidateCreate(context.Background(), CreateRequest{
		Config: json.RawMessage(`{"strategy":"worktree","branch":"my branch"}`),
	})
	if err == nil {
		t.Fatal("ValidateCreate() = nil, want a refusal")
	}
	if reason := FieldErrors(err)["/branch"]; !strings.Contains(reason, "my branch") {
		t.Errorf("reason = %q, want the rejected value quoted", reason)
	}
}

// Against a repository: a branch checked out already cannot be put in a worktree,
// and a new branch's base ref must be one the repository has. An existing branch
// that is free is reused, with the base ref ignored.
func TestCoreValidateCreateAgainstTheRepository(t *testing.T) {
	repo := gitProject(t)
	add := exec.Command("git", "worktree", "add", "-q", "-b", "linked", filepath.Join(t.TempDir(), "linked"))
	add.Dir = repo
	if out, err := add.CombinedOutput(); err != nil {
		t.Fatalf("git worktree add: %v\n%s", err, out)
	}
	tests := []struct {
		name      string
		wsName    string
		config    string
		wantField string
		wantText  string
	}{
		{name: "a free existing branch", config: `{"strategy":"worktree","branch":"main"}`},
		{name: "a free existing branch ignores the base ref", config: `{"strategy":"worktree","branch":"main","baseRef":"nope"}`},
		{name: "a new branch from a tag", config: `{"strategy":"worktree","branch":"topic","baseRef":"v1.0"}`},
		{name: "a new branch from the default base", config: `{"strategy":"worktree","branch":"topic"}`},
		{name: "a branch checked out in the project folder", config: `{"strategy":"worktree","branch":"feature"}`,
			wantField: "/branch", wantText: `branch "feature" is already checked out in the project folder; enter another`},
		{name: "a branch checked out in a linked worktree", config: `{"strategy":"worktree","branch":"linked"}`,
			wantField: "/branch", wantText: `branch "linked" is already checked out in another worktree; enter another`},
		{name: "a checked-out branch from the name", wsName: "feature", config: `{"strategy":"worktree"}`,
			wantField: "/branch", wantText: `branch "feature" (from the name) is already checked out in the project folder; enter another`},
		{name: "an unknown base ref", config: `{"strategy":"worktree","branch":"topic","baseRef":"nope"}`,
			wantField: "/baseRef", wantText: `unknown ref "nope"`},
	}
	provider := &core{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := provider.ValidateCreate(context.Background(), CreateRequest{
				Project: ProjectRef{Path: repo, GitBacked: true},
				Name:    tt.wsName,
				Config:  json.RawMessage(tt.config),
			})
			if tt.wantField == "" {
				if err != nil {
					t.Fatalf("ValidateCreate() = %v, want it accepted", err)
				}
				return
			}
			reason, ok := FieldErrors(err)[tt.wantField]
			if !ok {
				t.Fatalf("ValidateCreate() = %v, want a refusal on %s", err, tt.wantField)
			}
			if !strings.Contains(reason, tt.wantText) {
				t.Errorf("reason = %q, want it to mention %q", reason, tt.wantText)
			}
		})
	}
}

// A folder another workstream already works in is refused on the input that
// chose it: the strategy for in-place, the destination path otherwise.
func TestCoreValidateCreateRefusesATakenFolder(t *testing.T) {
	const projectDir, takenDir = "/work/proj", "/work/taken"
	tests := []struct {
		name      string
		gitBacked bool
		config    string
		wantField string
		wantText  string
	}{
		{name: "in place in a git project", gitBacked: true, config: `{"strategy":"in-place"}`,
			wantField: "/strategy", wantText: `"checkout-service-3" already works in this folder; choose worktree or copy`},
		{name: "in place in a plain project", config: `{}`,
			wantField: "/strategy", wantText: `"checkout-service-3" already works in this folder; choose copy`},
		{name: "a copy into a taken folder", config: `{"strategy":"copy","path":"/work/taken/"}`,
			wantField: "/path", wantText: `"other" already works in this folder; enter another`},
		{name: "a worktree into a taken folder", gitBacked: true, config: `{"strategy":"worktree","branch":"b","path":"/work/taken"}`,
			wantField: "/path", wantText: `"other" already works in this folder; enter another`},
		{name: "a copy into a free folder", config: `{"strategy":"copy","path":"/work/free"}`},
		{name: "a copy into a managed folder", config: `{"strategy":"copy"}`},
	}
	users := map[string]string{projectDir: "checkout-service-3", takenDir: "other"}
	provider := &core{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := provider.ValidateCreate(context.Background(), CreateRequest{
				Project:        ProjectRef{Path: projectDir, GitBacked: tt.gitBacked},
				WorkingDirUser: func(dir string) string { return users[filepath.Clean(dir)] },
				Config:         json.RawMessage(tt.config),
			})
			if tt.wantField == "" {
				if err != nil {
					t.Fatalf("ValidateCreate() = %v, want it accepted", err)
				}
				return
			}
			if got := FieldErrors(err)[tt.wantField]; got != tt.wantText {
				t.Errorf("%s refusal = %q (err %v), want %q", tt.wantField, got, err, tt.wantText)
			}
		})
	}
}

// A copy inside the project would copy itself, and every copy beside it; a
// worktree there is git's own business.
func TestCoreValidateCreateRefusesACopyInsideTheProject(t *testing.T) {
	const projectDir = "/work/proj"
	const refusal = "a copy cannot be made inside the project it copies; choose a folder outside it"
	tests := []struct {
		name     string
		location string
		config   string
		want     string
	}{
		{name: "a path inside", config: `{"strategy":"copy","path":"/work/proj/copies/x"}`, want: refusal},
		{name: "a location inside", location: "copies", config: `{"strategy":"copy"}`, want: `the workstreams location setting ("copies") puts copies inside the project; enter a folder outside it or change the setting`},
		{name: "a location beside", location: "../copies", config: `{"strategy":"copy"}`},
		{name: "a sibling sharing the prefix", config: `{"strategy":"copy","path":"/work/proj-copy"}`},
		{name: "a worktree inside", location: ".worktrees", config: `{"strategy":"worktree","branch":"b"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := &core{location: func() string { return tt.location }}
			err := provider.ValidateCreate(context.Background(), CreateRequest{
				Project: ProjectRef{Path: projectDir},
				Config:  json.RawMessage(tt.config),
			})
			if got := FieldErrors(err)["/path"]; got != tt.want {
				t.Errorf("/path refusal = %q (err %v), want %q", got, err, tt.want)
			}
		})
	}
}
