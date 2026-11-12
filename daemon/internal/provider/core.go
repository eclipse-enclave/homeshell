package provider

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
	"github.com/eclipsesource/homeshell/daemon/internal/git"
	"github.com/eclipsesource/homeshell/daemon/internal/pathutil"
	"github.com/eclipsesource/homeshell/daemon/internal/persist"
	wsfs "github.com/eclipsesource/homeshell/daemon/internal/provider/workstream"
	"github.com/eclipsesource/homeshell/daemon/internal/settings"
)

// CoreProviderID is the stable identifier for the core's built-in workstream
// provider. It is stamped onto the Provider field of core-created workstreams
// and used as the ID in the workstream-provider pick-list.
const CoreProviderID = "core"
const coreProviderLabel = "Default"

// core is the one production WorkstreamProvider: it materializes a workstream's
// working directory with one of the standard filesystem strategies (in-place,
// git worktree, copy) and tears it down again. It owns workstream data only; it
// knows nothing about sessions.
//
// root is where the worktree/copy strategies materialize a managed directory
// when the user supplies no explicit destination path (one per workstream,
// under root, named after it).
//
// location, when set (via SetSettings), is read live to let the configured
// workstreams.location override root; an empty configured value keeps root.
//
// claimed holds the managed directories of creates in flight, which may not
// exist on disk yet.
type core struct {
	// SettingsHolder supplies the refresh interval the reconciler follows
	// in-place workstreams' branches at (Refresher).
	SettingsHolder
	root     string
	location func() string

	claimMu sync.Mutex
	claimed map[string]bool
}

var (
	_ WorkstreamProvider = (*core)(nil)
	_ SettingsAware      = (*core)(nil)
	_ ConfigValidator    = (*core)(nil)
	_ DraftResolver      = (*core)(nil)
	_ StrategyTeller     = (*core)(nil)
	_ ValidationSchemer  = (*core)(nil)
	_ Refresher          = (*core)(nil)
)

// suggestTimeout bounds all of a form's git queries together.
const suggestTimeout = 2 * time.Second

// SetSettings wires the settings store so the managed-directory root can honor
// the configured workstreams.location, read live at each create, and branches
// are followed at the configured refresh interval.
func (c *core) SetSettings(s *settings.Store) {
	c.SettingsHolder.SetSettings(s)
	c.location = func() string { return s.Get().Workstreams.Location }
}

// DefaultWorkstreamsRoot returns the default workstreams root: where the core
// provider puts a managed directory when no workstreams.location is configured.
// It follows the data directory (-home-dir, then $XDG_DATA_HOME, then the
// platform default); a configured location never changes it.
func DefaultWorkstreamsRoot() (string, error) {
	dataDir, err := persist.DefaultDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataDir, "workstreams"), nil
}

func newCore() *core {
	root, err := DefaultWorkstreamsRoot()
	if err != nil {
		root = filepath.Join(os.TempDir(), "homeshell", "workstreams")
	}
	return &core{root: root}
}

func (c *core) ID() string    { return CoreProviderID }
func (c *core) Label() string { return coreProviderLabel }

// Available always reports true: the core has no external backend. Individual
// strategies still fail at Create time if their preconditions are unmet.
func (c *core) Available(context.Context) (bool, error) { return true, nil }

// CanCreate reports true for every project: the in-place and copy strategies
// work anywhere, and the schema gates the worktree strategy on git.
func (c *core) CanCreate(context.Context, ProjectRef) bool { return true }

// Schema returns the core's creation form. Only a git-backed project offers the
// worktree strategy, and its form suggests the repository's refs; a failing git
// query drops the suggestions, not the form. A folder holds one workstream, so
// once the project folder is taken the form does not offer in place.
func (c *core) Schema(ctx context.Context, project ProjectRef) json.RawMessage {
	if !project.GitBacked {
		return plainSchemaFor(project.FolderTaken)
	}
	ctx, cancel := context.WithTimeout(ctx, suggestTimeout)
	defer cancel()
	refs, defaultBase := gitRefSuggestions(ctx, project.Path)
	return gitSchemaSuggesting(project.FolderTaken, refs, defaultBase)
}

// ValidationSchema is Schema without the suggestions, which validation ignores:
// a create need not wait on git for them. It keeps in place either way, so
// ValidateCreate's refusal can name the workstream that holds the folder.
func (c *core) ValidationSchema(_ context.Context, project ProjectRef) json.RawMessage {
	if !project.GitBacked {
		return plainSchemaFor(false)
	}
	return gitSchemaSuggesting(false, nil, "")
}

// strategyProperty renders the form's strategy property: in place unless the
// project folder is taken, worktree on a git project, and copy. A git project
// defaults to worktree, a plain one to its first choice.
func strategyProperty(gitBacked, folderTaken bool) string {
	ids, labels := []string{}, []string{}
	offer := func(id, label string) { ids, labels = append(ids, id), append(labels, label) }
	if !folderTaken {
		offer("in-place", "In place")
	}
	if gitBacked {
		offer("worktree", "Worktree")
	}
	offer("copy", "Folder: a copy of the project")
	defaultID := ids[0]
	if gitBacked {
		defaultID = "worktree"
	}
	return fmt.Sprintf(`{
      "type": "string",
      "enum": %s,
      "enumNames": %s,
      "default": %s,
      "title": "Filesystem strategy",
      "description": "How the working directory is set up."
    }`, schemaValue(ids), schemaValue(labels), schemaValue(defaultID))
}

// plainSchemaFor renders plainSchema, leaving in place out of the strategies
// when folderTaken.
func plainSchemaFor(folderTaken bool) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(plainSchema, strategyProperty(false, folderTaken)))
}

// gitRefSuggestions lists the refs of the repository at dir and resolves its
// default base.
func gitRefSuggestions(ctx context.Context, dir string) ([]git.Ref, string) {
	refs, err := git.ListRefs(ctx, dir)
	if err != nil {
		slog.Debug("core schema: listing refs failed", "path", dir, "error", err)
	}
	return refs, git.DefaultBaseRef(ctx, dir)
}

// gitSchemaSuggesting renders gitSchema suggesting refs, with defaultBase as the
// base ref's default and among its suggestions.
func gitSchemaSuggesting(folderTaken bool, refs []git.Ref, defaultBase string) json.RawMessage {
	branches, refNames := []string{}, []string{}
	for _, ref := range refs {
		refNames = append(refNames, ref.Name)
		if ref.Branch != "" && !ref.CheckedOut {
			branches = append(branches, ref.Branch)
		}
	}
	if defaultBase != "" && !slices.Contains(refNames, defaultBase) {
		refNames = append([]string{defaultBase}, refNames...)
	}
	return json.RawMessage(fmt.Sprintf(gitSchema, strategyProperty(true, folderTaken),
		schemaValue(branches), schemaValue(defaultBase), schemaValue(refNames)))
}

// schemaValue renders a string or string slice into a schema template.
func schemaValue(value any) string {
	out, _ := json.Marshal(value)
	return string(out)
}

// gitSchema is the form for a git-backed project. Its verbs take, in order, the
// strategy property, the branch suggestions, the base ref's default and the base
// ref's suggestions.
const gitSchema = `{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "strategy": %s,
    "branch": {
      "type": "string",
      "title": "Branch",
      "description": "Branch for the worktree strategy; defaults to a branch named after the workstream. An existing branch is reused if it is not checked out in another worktree.",
      "examples": %s
    },
    "baseRef": {
      "type": "string",
      "title": "Base ref",
      "description": "Starting point for a new branch (commit, tag, or branch name). Ignored when using an existing branch.",
      "default": %s,
      "examples": %s
    },
    "path": {
      "type": "string",
      "title": "Destination path",
      "description": "Destination directory for worktree or copy."
    }
  }
}`

// plainSchema is the form for a project that is not git-backed. Its verb takes
// the strategy property.
const plainSchema = `{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "strategy": %s,
    "path": {
      "type": "string",
      "title": "Destination path",
      "description": "Destination directory for copy."
    }
  }
}`

// ValidateCreate checks the config the daemon cannot check from the schema, so a
// bad value is refused on the create request instead of surfacing minutes later
// as a failed workstream (see ConfigValidator).
//
// A branch *name* is decidable from the string alone (git's refname grammar),
// and so is whether a destination path is absolute. Whether the branch is
// checked out in another worktree, or the base ref names a commit, is a question
// about the repository as it is now: asked here so the answer comes on the form,
// and again by Prepare, which materializes against the repository as it is then.
func (c *core) ValidateCreate(ctx context.Context, req CreateRequest) error {
	strategy, branch, baseRef, destDir := parseCoreConfig(req.Config, req.Project.GitBacked)
	fields := map[string]string{}
	if wsfs.Strategy(strategy) == wsfs.Worktree {
		name, derived := coreNameAndBranch(ctx, req, strategy, branch)
		if derived == "" {
			fields["/branch"] = fmt.Sprintf("no branch name can be derived from %q; enter one", name)
		} else if err := git.ValidateBranchName(derived); err != nil {
			fields["/branch"] = err.Error()
		} else if req.Project.GitBacked {
			maps.Copy(fields, worktreeRefusals(ctx, req.Project.Path, derived, baseRef, branch == ""))
		}
	}
	// git would read such a base ref as an option.
	if strings.HasPrefix(baseRef, "-") {
		fields["/baseRef"] = fmt.Sprintf("base ref %q must not start with %q", baseRef, "-")
	}
	if wsfs.Strategy(strategy) == wsfs.InPlace {
		if user := workingDirUser(req, req.Project.Path); user != "" {
			alternatives := "copy"
			if req.Project.GitBacked {
				alternatives = "worktree or copy"
			}
			fields["/strategy"] = fmt.Sprintf("%q already works in this folder; choose %s", user, alternatives)
		}
	}
	if destDir != "" {
		if resolved, err := pathutil.Resolve(destDir); err != nil {
			fields["/path"] = err.Error()
		} else if user := workingDirUser(req, resolved); user != "" && wsfs.Strategy(strategy) != wsfs.InPlace {
			fields["/path"] = fmt.Sprintf("%q already works in this folder; enter another", user)
		}
	}
	if wsfs.Strategy(strategy) == wsfs.Copy && fields["/path"] == "" {
		if destDir != "" {
			if target, err := pathutil.Resolve(destDir); err == nil && pathutil.Within(target, req.Project.Path) {
				fields["/path"] = "a copy cannot be made inside the project it copies; choose a folder outside it"
			}
		} else if root, err := c.resolveRoot(req.Project.Path); err == nil && pathutil.Within(root, req.Project.Path) {
			fields["/path"] = fmt.Sprintf("the workstreams location setting (%q) puts copies inside the project; enter a folder outside it or change the setting", c.configuredLocation())
		}
	}
	if len(fields) == 0 {
		return nil
	}
	return &ValidationError{Message: "configuration is invalid", Fields: fields}
}

// ResolveDraft says what Create would derive for req (see DraftResolver). A
// managed directory under a root that cannot be resolved is left blank, as is
// a copy's under a root inside the project, which Create refuses.
func (c *core) ResolveDraft(ctx context.Context, req CreateRequest) Draft {
	strategy, branch, _, destDir := parseCoreConfig(req.Config, req.Project.GitBacked)
	name, branch := coreNameAndBranch(ctx, req, strategy, branch)
	if destDir == "" && wsfs.Strategy(strategy) != wsfs.InPlace {
		root, err := c.resolveRoot(req.Project.Path)
		if err == nil && !(wsfs.Strategy(strategy) == wsfs.Copy && pathutil.Within(root, req.Project.Path)) {
			c.claimMu.Lock()
			destDir = managedDir(root, req.Project.Path, name, c.claimed)
			c.claimMu.Unlock()
		}
	}
	return Draft{Name: name, Config: map[string]string{"branch": branch, "path": destDir}}
}

// CreateStrategy says the strategy Create would use for req (see StrategyTeller).
func (c *core) CreateStrategy(req CreateRequest) string {
	strategy, _, _, _ := parseCoreConfig(req.Config, req.Project.GitBacked)
	return strategy
}

// workingDirUser names the workstream already working in dir, or "".
func workingDirUser(req CreateRequest, dir string) string {
	if req.WorkingDirUser == nil {
		return ""
	}
	return req.WorkingDirUser(dir)
}

// worktreeRefusals checks a worktree's branch and base ref against the
// repository at dir: an existing branch must not be checked out already, and a
// new one's base ref must name a commit. fromName says the branch was derived
// from the workstream's name, which the refusal then says. A git query that
// fails refuses nothing; Prepare has the last word.
func worktreeRefusals(ctx context.Context, dir, branch, baseRef string, fromName bool) map[string]string {
	exists, err := git.BranchExists(ctx, dir, branch)
	if err != nil {
		return nil
	}
	if exists {
		worktree, err := git.BranchWorktree(ctx, dir, branch)
		if err != nil || worktree == "" {
			return nil
		}
		origin := ""
		if fromName {
			origin = " (from the name)"
		}
		where := "in another worktree"
		if git.SamePath(worktree, dir) {
			where = "in the project folder"
		}
		return map[string]string{"/branch": fmt.Sprintf("branch %q%s is already checked out %s; enter another", branch, origin, where)}
	}
	if baseRef != "" && !strings.HasPrefix(baseRef, "-") && !git.Resolves(ctx, dir, baseRef) {
		return map[string]string{"/baseRef": fmt.Sprintf("unknown ref %q", baseRef)}
	}
	return nil
}

// Create prepares the working directory and returns the workstream with its
// Name, Strategy, Branch, WorkingDir, and CreatorData populated. The daemon
// assigns the ID and stamps Provider.
func (c *core) Create(ctx context.Context, req CreateRequest) (domain.Workstream, error) {
	strategy, branch, baseRef, destDir := parseCoreConfig(req.Config, req.Project.GitBacked)
	name, branch := coreNameAndBranch(ctx, req, strategy, branch)
	// Resolve a user-supplied destination path (tilde expansion, reject relative).
	if destDir != "" {
		resolved, err := pathutil.Resolve(destDir)
		if err != nil {
			return domain.Workstream{}, fmt.Errorf("invalid path: %w", err)
		}
		destDir = resolved
	}
	// Worktree/copy need a destination that does not yet exist. When the user
	// supplies none, materialize a managed directory under root, so these
	// strategies work without an explicit path.
	if destDir == "" && wsfs.Strategy(strategy) != wsfs.InPlace {
		root, err := c.resolveRoot(req.Project.Path)
		if err != nil {
			return domain.Workstream{}, err
		}
		if err := os.MkdirAll(root, 0o755); err != nil {
			return domain.Workstream{}, fmt.Errorf("creating workstream root %q: %w", root, err)
		}
		managed, release := c.claimManagedDir(root, req.Project.Path, name)
		defer release()
		destDir = managed
	}
	dir, err := wsfs.Prepare(ctx, wsfs.Strategy(strategy), req.Project.Path, branch, baseRef, destDir, req.Project.GitBacked)
	if err != nil {
		return domain.Workstream{}, err
	}
	creatorData := coreCreatorData{Strategy: strategy}
	if wsfs.Strategy(strategy) == wsfs.InPlace && branch != "" && name == branch {
		creatorData.NameFromBranch = name
	}
	return domain.Workstream{
		Name:        name,
		StrategyID:  strategy,
		Branch:      branch,
		WorkingDir:  dir,
		CreatorData: marshalCreatorData(creatorData),
		Sessions:    []domain.Session{},
	}, nil
}

// Refresh follows the branch checked out in a workstream's folder, which the
// user switches outside HomeShell: the workstream's branch is the one checked
// out now. An in-place workstream's name follows too while it still wears the
// one the core gave it from the branch — a rename keeps the user's name. A
// detached HEAD has no branch, and leaves the name as it was. A folder git does
// not answer for changes nothing.
func (c *core) Refresh(ctx context.Context, project ProjectRef, workstream domain.Workstream) domain.Workstream {
	creatorData := parseCreatorData(workstream)
	branch, ok := checkedOutBranch(ctx, project, workstream, creatorData)
	if !ok {
		return workstream
	}
	workstream.Branch = branch
	if branch != "" && creatorData.NameFromBranch != "" && workstream.Name == creatorData.NameFromBranch {
		workstream.Name = branch
		creatorData.NameFromBranch = branch
		workstream.CreatorData = marshalCreatorData(creatorData)
	}
	return workstream
}

// checkedOutBranch is the branch checked out in a workstream's folder, "" on a
// detached HEAD, and false where there is none to read: a project outside git,
// or a git that does not answer. Both read a HEAD file, as every one is polled
// at the refresh interval. In place it is the project's branch, found from a
// project folder below the repository's root too. A separate folder has one
// only as a repository or worktree of its own: a copy, made without .git, must
// not take the branch of a repository around it.
func checkedOutBranch(ctx context.Context, project ProjectRef, workstream domain.Workstream, creatorData coreCreatorData) (string, bool) {
	if !project.GitBacked {
		return "", false
	}
	dir, read := workstream.WorkingDir, git.HeadBranch
	if wsfs.Strategy(creatorData.Strategy) == wsfs.InPlace {
		dir, read = project.Path, git.EnclosingHeadBranch
	}
	branch, err := read(ctx, dir)
	if err != nil {
		slog.Debug("core: reading the branch failed", "path", dir, "error", err)
		return "", false
	}
	return branch, true
}

// Remove discards the working directory the core materialized unless
// opts.KeepFiles is set. In-place workstreams share the project directory,
// so there is nothing to discard regardless.
func (c *core) Remove(_ context.Context, project ProjectRef, ws domain.Workstream, opts RemoveOptions) error {
	if opts.KeepFiles {
		return nil
	}
	creatorData := parseCreatorData(ws)
	if creatorData.Strategy == "" || creatorData.Strategy == string(wsfs.InPlace) || ws.WorkingDir == "" {
		return nil
	}
	return wsfs.Discard(wsfs.Strategy(creatorData.Strategy), project.Path, ws.WorkingDir)
}

// claimManagedDir is the managed directory a Create prepares, held from other
// creates until release: names that flatten alike must not share one.
func (c *core) claimManagedDir(root, projectDir, name string) (string, func()) {
	c.claimMu.Lock()
	defer c.claimMu.Unlock()
	dir := managedDir(root, projectDir, name, c.claimed)
	if c.claimed == nil {
		c.claimed = map[string]bool{}
	}
	c.claimed[dir] = true
	return dir, func() {
		c.claimMu.Lock()
		defer c.claimMu.Unlock()
		delete(c.claimed, dir)
	}
}

// managedDir is the directory under root a worktree or copy of the project at
// projectDir takes when given no path: "<project>-<name>", without the prefix
// where the name already starts with it, capped with any number it takes past
// one that exists or is claimed — the strategies require a destination that
// does not.
func managedDir(root, projectDir, name string, claimed map[string]bool) string {
	project, base := dirName(filepath.Base(projectDir)), dirName(name)
	if base == "" {
		base = project
	} else if project != "" && base != project && !strings.HasPrefix(base, project+"-") {
		base = project + "-" + base
	}
	if base == "" {
		base = "workstream"
	}
	dir := filepath.Join(root, capWithSuffix(base, ""))
	for number := 2; pathExists(dir) || claimed[dir]; number++ {
		dir = filepath.Join(root, capWithSuffix(base, fmt.Sprintf("-%d", number)))
	}
	return dir
}

// maxNameBytes caps a derived branch or managed directory name, well inside
// git's and filesystems' limits however long the workstream's name.
const maxNameBytes = 64

// capName cuts name to limit bytes, at a "-" or "/" where one is near the end,
// ending in no separator.
func capName(name string, limit int) string {
	if len(name) <= limit {
		return name
	}
	cut := limit
	if at := strings.LastIndexAny(name[:limit+1], "-/"); at > limit/2 {
		cut = at
	}
	return strings.TrimRight(name[:cut], "-./_")
}

// branchFromName is the branch a worktree named name takes when given none,
// capped but keeping a number the name ends in, so numbered names stay apart.
func branchFromName(name string) string {
	branch := git.BranchNameFrom(name)
	number := trailingNumber.FindString(branch)
	return capWithSuffix(strings.TrimSuffix(branch, number), number)
}

// capWithSuffix is name capped so that, with suffix, it fits maxNameBytes.
func capWithSuffix(name, suffix string) string {
	return capName(name, maxNameBytes-len(suffix)) + suffix
}

var trailingNumber = regexp.MustCompile(`-[0-9]{1,9}$`)

// dirName is text as one directory name: a branch name, flattened.
func dirName(text string) string {
	return strings.ReplaceAll(git.BranchNameFrom(text), "/", "-")
}

func pathExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// resolveRoot picks the base directory for managed workstream directories: the
// configured workstreams.location when set (tilde-expanded, and relative to the
// project's directory unless absolute), otherwise the default root.
func (c *core) resolveRoot(projectDir string) (string, error) {
	if loc := c.configuredLocation(); loc != "" {
		resolved, err := pathutil.ResolveFrom(loc, projectDir)
		if err != nil {
			return "", fmt.Errorf("invalid workstreams.location: %w", err)
		}
		return resolved, nil
	}
	if c.root != "" {
		return c.root, nil
	}
	return filepath.Join(os.TempDir(), "homeshell", "workstreams"), nil
}

// configuredLocation is the workstreams.location setting, trimmed; empty when
// unset or when no settings are wired.
func (c *core) configuredLocation() string {
	if c.location == nil {
		return ""
	}
	return strings.TrimSpace(c.location())
}

// coreCreatorData is the core provider's private teardown bookkeeping stored in
// domain.Workstream.CreatorData. Only the core reads it. The materialized
// directory itself lives in the public WorkingDir field.
//
// NameFromBranch is the name an in-place workstream was last given from the
// branch checked out in its folder; while the workstream still wears it, the
// name follows the branch (Refresh). Empty for one named anything else.
type coreCreatorData struct {
	Strategy       string `json:"strategy"`
	NameFromBranch string `json:"nameFromBranch,omitempty"`
}

// parseCreatorData reads the core's bookkeeping off workstream; unreadable or
// absent data reads as zero.
func parseCreatorData(workstream domain.Workstream) coreCreatorData {
	var creatorData coreCreatorData
	if len(workstream.CreatorData) > 0 {
		_ = json.Unmarshal(workstream.CreatorData, &creatorData)
	}
	return creatorData
}

func marshalCreatorData(cd coreCreatorData) json.RawMessage {
	b, _ := json.Marshal(cd)
	return b
}

// parseCoreConfig reads a core config. The branch and base ref only mean
// anything to the worktree strategy, so under any other they come back empty:
// a form may send them regardless, and a workstream must not claim a branch it
// is not on. A config naming no strategy takes the schema's default: a
// worktree for a git-backed project, else in place.
func parseCoreConfig(config []byte, gitBacked bool) (strategy, branch, baseRef, destDir string) {
	strategy = string(wsfs.InPlace)
	if gitBacked {
		strategy = string(wsfs.Worktree)
	}
	if len(config) == 0 {
		return
	}
	var raw struct {
		Strategy string `json:"strategy"`
		Branch   string `json:"branch"`
		BaseRef  string `json:"baseRef"`
		Path     string `json:"path"`
	}
	if json.Unmarshal(config, &raw) == nil {
		if raw.Strategy != "" {
			strategy = raw.Strategy
		}
		if wsfs.Strategy(strategy) == wsfs.Worktree {
			branch, baseRef = raw.Branch, raw.BaseRef
		}
		destDir = raw.Path
	}
	return
}

// coreNameAndBranch resolves a workstream's name and branch: an empty name
// follows the branch, else the project, numbered past the names the project's
// workstreams already take — and, when the name is to give the branch too, past
// the branches the repository already has. A worktree's empty branch is derived
// from the name, capped like a managed directory; an in-place workstream's is
// the one checked out in the project folder.
func coreNameAndBranch(ctx context.Context, req CreateRequest, strategy, branch string) (string, string) {
	worktree := wsfs.Strategy(strategy) == wsfs.Worktree
	if wsfs.Strategy(strategy) == wsfs.InPlace && req.Project.GitBacked {
		branch, _ = git.CurrentBranch(ctx, req.Project.Path)
	}
	name := req.Name
	if name == "" {
		base := coreWorkstreamName(req.Project.Path, branch)
		name = base
		for number := 2; !coreNameFree(ctx, req, name, worktree && branch == ""); number++ {
			name = fmt.Sprintf("%s-%d", base, number)
		}
	}
	if worktree && branch == "" {
		branch = branchFromName(name)
	}
	return name, branch
}

// coreNameFree reports whether name is not taken in the project and, when it
// names the branch too, whether that branch does not exist yet. A git query
// that fails leaves the name free, rather than numbering on against a git that
// does not answer; Prepare has the last word.
func coreNameFree(ctx context.Context, req CreateRequest, name string, namesBranch bool) bool {
	if req.NameTaken != nil && req.NameTaken(name) {
		return false
	}
	branch := branchFromName(name)
	if !namesBranch || !req.Project.GitBacked || branch == "" {
		return true
	}
	exists, err := git.BranchExists(ctx, req.Project.Path, branch)
	return err != nil || !exists
}

// coreWorkstreamName is the name a workstream given none starts from: its
// branch, else the project folder's name.
func coreWorkstreamName(projectDir, branch string) string {
	if branch != "" {
		return branch
	}
	if base := filepath.Base(projectDir); base != "" && base != "." && base != string(filepath.Separator) {
		return base
	}
	return "workstream"
}

func generateWorkstreamID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return fmt.Sprintf("ws-%x", b), nil
}
