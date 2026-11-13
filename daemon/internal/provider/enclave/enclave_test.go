package enclave

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/eclipsesource/homeshell/daemon/internal/agentcatalog"
	"github.com/eclipsesource/homeshell/daemon/internal/agentspec"
	"github.com/eclipsesource/homeshell/daemon/internal/domain"
	"github.com/eclipsesource/homeshell/daemon/internal/provider"
	"github.com/eclipsesource/homeshell/daemon/internal/provider/enclave/sandboxschema"
	"github.com/eclipsesource/homeshell/daemon/internal/sbridge"
	"github.com/eclipsesource/homeshell/daemon/internal/store"
	"github.com/eclipsesource/homeshell/daemon/internal/terminal"
)

// storedProject backs the provider with a store mapping projectID to dir, so
// SessionState/Prompt/SendKey can resolve the project path.
func storedProject(t *testing.T, cli CLI, projectID, dir string) *Provider {
	t.Helper()
	p := newForTest(t, cli)
	var workstreams []domain.Workstream
	if fake, ok := cli.(*fakeCLI); ok {
		for i, c := range fake.containers[dir] {
			if filepath.IsAbs(c.Directory) && c.Directory != dir {
				workstreams = append(workstreams, domain.Workstream{ID: fmt.Sprint("w", i), WorkingDir: c.Directory})
			}
		}
	}
	st := store.NewTestStore(t, []domain.Project{{ID: projectID, Path: dir, Workstreams: workstreams}})
	p.SetStore(st)
	return p
}

// The default agent descriptions and model catalog, built once for the whole test
// binary.
//
// This keeps default-agent tests on the rows that ship and custom-agent tests on the
// same path a user edit takes. Read-only after setup, so one in-memory catalog
// serves every test.
var (
	seedOnce    sync.Once
	seedCatalog *agentcatalog.Store
)

func seeded(t *testing.T) (*agentcatalog.Store, *agentcatalog.Store) {
	t.Helper()
	seedOnce.Do(func() { seedCatalog = agentcatalog.NewMemory(agentcatalog.Defaults()) })
	return seedCatalog, seedCatalog
}

// newForTest builds an Enclave provider with the seeded rows wired, in the order
// production wires them.
func newForTest(t *testing.T, cli CLI) *Provider {
	t.Helper()
	specs, models := seeded(t)
	p := bareProvider(t, cli)
	p.SetAgentSpecs(specs)
	p.SetModelCatalog(models)
	return p
}

// describedAgent returns one default or explicitly inserted test description.
func describedAgent(t *testing.T, id string) agentspec.Agent {
	t.Helper()
	specs, _ := seeded(t)
	a, ok := specs.Agent(id)
	if !ok {
		t.Fatalf("no test description for agent %q", id)
	}
	return a
}

// seededModels is one agent's shipped model catalog (agentcatalog.Defaults).
func seededModels(t *testing.T, agent string) []string {
	t.Helper()
	_, models := seeded(t)
	return models.Models(agent)
}

// --- fake CLI for tests ---

type fakeCLI struct {
	available  bool
	containers map[string][]Container // project dir → containers
	all        map[string][]Container // project dir → `ps --all` containers

	tools      []string
	toolLabels map[string]string
	listErrIn  map[string]error
	toolsErr   error
	runResult  string
	runErr     error
	listErr    error
	allErr     error
	stopErr    error
	stopped    []string
	// mu guards the call records: schemas and polls call the fake in parallel. Hooks run unlocked.
	mu         sync.Mutex
	listCalls  int
	allCalls   int
	toolCalls  int
	runCalls   []runCall
	execOutput map[string]string
	execErr    error
	execCalls  map[string]int
	execArgv   [][]string
	// config is the canned `enclave config` answer per tool, and features the
	// feature catalogue; the counts let a test prove an answer is remembered.
	// configWith, when set, answers a call that passes run flags instead.
	config       map[string]sandboxschema.EffectiveConfig
	configWith   func(tool string, flags []string) ResolvedConfig
	configErr    error
	configCalls  int
	features     []sandboxschema.Feature
	featuresErr  error
	featureCalls int
	// continueErr fails only `enclave continue`, and onRun runs inside every Run,
	// for a test that acts while a container is coming up.
	continueErr error
	onRun       func()
	// onList runs inside ListSessions, after the listing is taken.
	onList func()
	// onProgress runs inside every Run with its progress callback, for a test
	// that prints startup output.
	onProgress func(progress func(string))
	// runBase, when set, makes Run return the container Enclave names after the session: runBase, then
	// `-<session name>` when Run is given one.
	runBase string
	// conversations, when set, are the containers with a conversation to continue: Enclave keeps one per
	// session name, so a continue under a name without one fails, as the agent finds nothing to continue.
	// A run that starts fresh gives its container one.
	conversations map[string]bool
}

// runCall records a fakeCLI.Run call, so a test can assert on the flags the
// provider chose — the session name above all, which is what keeps two containers
// in one workstream from colliding.
type runCall struct {
	dir  string
	opts RunOpts
}

// execOutput is the canned answer per (tool) for ExecOutput, and execCalls counts
// the asks so a test can prove an answer is remembered rather than re-fetched.
func (f *fakeCLI) ExecOutput(_ context.Context, _, _, tool string, argv []string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.execCalls == nil {
		f.execCalls = map[string]int{}
	}
	f.execCalls[tool]++
	f.execArgv = append(f.execArgv, argv)
	if f.execErr != nil {
		return nil, f.execErr
	}
	return []byte(f.execOutput[tool]), nil
}

func (f *fakeCLI) Available(context.Context) (bool, error) {
	if !f.available {
		return false, fmt.Errorf("enclave binary not found on PATH")
	}
	return true, nil
}

func (f *fakeCLI) ListSessions(_ context.Context, projectDir string) ([]Container, error) {
	f.mu.Lock()
	f.listCalls++
	containers := f.containers[projectDir]
	f.mu.Unlock()
	if f.onList != nil {
		f.onList()
	}
	if err := f.listErrIn[projectDir]; err != nil {
		return nil, err
	}
	return containers, f.listErr
}

func (f *fakeCLI) ListAllSessions(_ context.Context, projectDir string) ([]Container, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.allCalls++
	return f.all[projectDir], f.allErr
}

func (f *fakeCLI) Run(_ context.Context, dir string, opts RunOpts) (string, error) {
	f.mu.Lock()
	f.runCalls = append(f.runCalls, runCall{dir: dir, opts: opts})
	f.mu.Unlock()
	if f.onRun != nil {
		f.onRun()
	}
	if f.onProgress != nil && opts.Progress != nil {
		f.onProgress(opts.Progress)
	}
	if opts.Continue && f.continueErr != nil {
		return "", f.continueErr
	}
	if f.runBase != "" && f.runErr == nil {
		started := f.runBase
		if opts.SessionName != "" {
			started += "-" + opts.SessionName
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.conversations != nil {
			if opts.Continue && !f.conversations[started] {
				return "", fmt.Errorf("container %s exited: No conversation found to continue", started)
			}
			f.conversations[started] = true
		}
		return started, nil
	}
	return f.runResult, f.runErr
}

func (f *fakeCLI) Stop(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = append(f.stopped, name)
	return f.stopErr
}

func (f *fakeCLI) Config(ctx context.Context, dir, tool string, flags []string) (ResolvedConfig, error) {
	f.mu.Lock()
	f.configCalls++
	f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return ResolvedConfig{}, err
	}
	if f.configErr != nil {
		return ResolvedConfig{}, f.configErr
	}
	if flags != nil && f.configWith != nil {
		return f.configWith(tool, flags), nil
	}
	return ResolvedConfig{Options: f.config[tool], Valid: true}, nil
}

func (f *fakeCLI) Features(ctx context.Context, _ string) ([]sandboxschema.Feature, error) {
	f.mu.Lock()
	f.featureCalls++
	f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f.featuresErr != nil {
		return nil, f.featuresErr
	}
	return f.features, nil
}

func (f *fakeCLI) Tools(_ context.Context) ([]Tool, error) {
	f.mu.Lock()
	f.toolCalls++
	f.mu.Unlock()
	var tools []Tool
	for _, name := range f.tools {
		label := f.toolLabels[name]
		if label == "" {
			label = name
		}
		tools = append(tools, Tool{Name: name, Label: label})
	}
	return tools, f.toolsErr
}

// --- tests ---

func TestProviderIsPrompter(t *testing.T) {
	var _ provider.Prompter = (*Provider)(nil)
}

func TestProviderIdentity(t *testing.T) {
	p := newForTest(t, &fakeCLI{available: true})
	if p.ID() != "enclave" {
		t.Errorf("ID() = %q, want enclave", p.ID())
	}
	if p.Label() != "Eclipse Enclave" {
		t.Errorf("Label() = %q, want Eclipse Enclave", p.Label())
	}
}

func TestAvailableWhenInstalled(t *testing.T) {
	p := newForTest(t, &fakeCLI{available: true})
	got, err := p.Available(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got {
		t.Error("Available() = false, want true")
	}
}

func TestAvailableWhenNotInstalled(t *testing.T) {
	p := newForTest(t, &fakeCLI{available: false})
	got, err := p.Available(context.Background())
	if got {
		t.Error("Available() = true, want false")
	}
	if err == nil {
		t.Fatal("expected error explaining why unavailable")
	}
}

func TestDiscoverSessionsEmpty(t *testing.T) {
	p := newForTest(t, &fakeCLI{available: true, containers: map[string][]Container{}})
	ref := provider.ProjectRef{ID: "proj-1", Path: "/tmp/project"}
	ds, err := p.DiscoverSessions(context.Background(), ref)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ds) != 0 {
		t.Errorf("got %d discovered sessions, want 0", len(ds))
	}
}

func TestDiscoverSessionsFiltersToProject(t *testing.T) {
	// Without a store, only the container in the project directory matches.
	cli := &fakeCLI{
		available: true,
		containers: map[string][]Container{
			"/tmp/my-project": {
				{Name: "enclave-claude-abc123abc123-main", Tool: "claude", Directory: "/tmp/my-project", Status: "running"},
				{Name: "enclave-codex-def456def456", Tool: "codex", Directory: "/tmp/other-project", Status: "running"},
			},
		},
	}
	p := newForTest(t, cli)
	ref := provider.ProjectRef{ID: "proj-1", Path: "/tmp/my-project"}
	ds, err := p.DiscoverSessions(context.Background(), ref)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ds) != 1 {
		t.Fatalf("got %d discovered sessions, want 1", len(ds))
	}
	if ds[0].WorkingDir != "/tmp/my-project" || ds[0].Session.ID != "enclave-claude-abc123abc123-main" {
		t.Errorf("discovered = %+v; want the in-place container in /tmp/my-project", ds[0])
	}
}

func TestCreateSessionRunsContainerAndIsDiscoverable(t *testing.T) {
	cli := &fakeCLI{available: true, runResult: "enclave-claude-new123"}
	p := newForTest(t, cli)
	sess, err := p.CreateSession(context.Background(), provider.CreateSessionRequest{
		Workstream: provider.WorkstreamRef{ProjectID: "proj-1", WorkstreamID: "ws1"},
		WorkingDir: "/tmp/project",
		OptionID:   "claude",
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if sess.ID != "enclave-claude-new123" {
		t.Errorf("session ID = %q, want the container name", sess.ID)
	}
	// Discovery uses the owned record, without querying container inventory.
	ds, _ := p.DiscoverSessions(context.Background(), provider.ProjectRef{ID: "proj-1", Path: "/tmp/project"})
	if len(ds) != 1 || ds[0].Session.ID != "enclave-claude-new123" || ds[0].WorkingDir != "/tmp/project" {
		t.Errorf("owned discovery = %+v; want the created session", ds)
	}
}

type recordingSink struct{ changed int }

func (s *recordingSink) Changed() { s.changed++ }

func TestCreateSessionRequiresWorkingDir(t *testing.T) {
	p := newForTest(t, &fakeCLI{available: true})
	_, err := p.CreateSession(context.Background(), provider.CreateSessionRequest{})
	if err == nil {
		t.Fatal("expected error when no working directory is given")
	}
}

func TestRemoveSessionStopsContainer(t *testing.T) {
	p, cli, _, project := bridgeProvider(t)
	created := createBridge(t, p, project)
	if err := p.RemoveSession(context.Background(), provider.SessionRef{ProjectID: project.ID, SessionID: created.ID}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(cli.stopped, []string{created.ID}) {
		t.Fatalf("stopped=%v", cli.stopped)
	}
}

func TestSessionSchemasReturnTools(t *testing.T) {
	p := newForTest(t, &fakeCLI{available: true, tools: []string{"claude", "codex"}, toolLabels: map[string]string{"claude": "Claude Code", "codex": "OpenAI Codex CLI"}})
	opts, err := p.SessionSchemas(context.Background(), provider.WorkstreamRef{})
	if err != nil {
		t.Fatal(err)
	}
	if len(opts) != 2 {
		t.Fatalf("expected 2 session options, got %d", len(opts))
	}
	if opts[0].ID != "claude" || opts[1].ID != "codex" {
		t.Errorf("options = %+v, want claude and codex", opts)
	}
	if opts[0].Label != "Claude Code" || opts[1].Label != "OpenAI Codex CLI" {
		t.Errorf("labels = %q, %q; want Enclave's display names", opts[0].Label, opts[1].Label)
	}
}

func TestSessionSchemasFallback(t *testing.T) {
	p := newForTest(t, &fakeCLI{available: true, tools: nil})
	opts, err := p.SessionSchemas(context.Background(), provider.WorkstreamRef{})
	if err != nil {
		t.Fatal(err)
	}
	if len(opts) != 1 || opts[0].ID != "claude" {
		t.Errorf("expected fallback to claude, got %+v", opts)
	}
}

type fakeLauncher struct {
	launched [][]string
}

func (f *fakeLauncher) Launch(_ context.Context, cmd []string, _ string) error {
	f.launched = append(f.launched, cmd)
	return nil
}

// Focus is unused here: Enclave attaches a fresh terminal on every open (it has
// no way to ask a container whether one is already attached — GAP), so nothing
// asks for a window to be raised.
func (f *fakeLauncher) Focus(context.Context, string) error { return nil }

func TestOpenTerminal(t *testing.T) {
	p, _, bridge, project := bridgeProvider(t)
	created := createBridge(t, p, project)
	launcher := &fakeLauncher{}
	p.term = launcher
	result, err := p.Open(context.Background(), provider.SessionRef{ProjectID: project.ID, SessionID: created.ID}, "terminal")
	if err != nil || result.Mode != "terminal" {
		t.Fatalf("open=%v err=%v", result, err)
	}
	want := terminal.HoldOnFailure(bridge.AttachCommand(bridgeTarget(created.ID))...)
	if len(launcher.launched) != 1 || !slices.Equal(launcher.launched[0], want) {
		t.Fatalf("launched=%v want=%v", launcher.launched, want)
	}
}

func TestOpenUnsupportedMode(t *testing.T) {
	p := newForTest(t, &fakeCLI{available: true})
	_, err := p.Open(context.Background(), provider.SessionRef{}, "ide")
	if err == nil {
		t.Fatal("expected error for unsupported mode")
	}
}

func TestCapabilities(t *testing.T) {
	p := newForTest(t, &fakeCLI{available: true})
	caps := p.Capabilities()
	if len(caps.OpenModes) != 1 || caps.OpenModes[0] != "terminal" {
		t.Errorf("OpenModes = %v, want [terminal]", caps.OpenModes)
	}
	// Unlimited: one container holds one agent, but nothing makes a workstream hold
	// one container. What used to make the cap necessary was the CLI's deterministic
	// container name, which CreateSession now displaces with a unique --name.
	if caps.MaxConcurrentSessions != 0 {
		t.Errorf("MaxConcurrentSessions = %d, want 0 (unlimited)", caps.MaxConcurrentSessions)
	}
}

func TestOpenShellExecsAShellInTheContainer(t *testing.T) {
	container := "enclave-claude-abcdef012345-7"
	f := &fakeCLI{available: true, containers: map[string][]Container{
		"/proj": {{Name: container, SessionName: "7", Tool: "claude", Directory: "/proj"}},
	}}
	p := storedProject(t, f, "proj1", "/proj")
	fl := &fakeLauncher{}
	p.term = fl

	res, err := p.OpenShell(context.Background(), provider.SessionRef{
		ProjectID: "proj1", WorkstreamID: "w", SessionID: container,
	})
	if err != nil {
		t.Fatalf("OpenShell: %v", err)
	}
	if res.Mode != "terminal" {
		t.Errorf("mode = %q, want terminal", res.Mode)
	}
	if !strings.Contains(string(res.Data), `"target":"container"`) {
		t.Errorf("data = %s, want it to report the container target", res.Data)
	}
	if len(fl.launched) != 1 {
		t.Fatalf("launched %d times, want 1", len(fl.launched))
	}
	// Run from the project directory, which is how the CLI scopes the lookup;
	// `enclave shell` would build a *new* container. Held on a startup failure:
	// the workstream's plain "Open" lands here, and an exec that fails at once
	// must not close the window it just opened.
	want := terminal.HoldOnStartupFailureIn("/proj", "enclave", "exec", "--name", container, "--tool", "claude", "--", "bash")
	if !slices.Equal(fl.launched[0], want) {
		t.Errorf("launched = %q, want %q", fl.launched[0], want)
	}
}

// Exec runs where the session does: from the project root, `enclave exec
// --name` rebuilds a worktree session's container name wrongly ("no running
// container found"). A session without a directory runs from the root.
func TestExecRunsFromTheSessionsOwnDirectory(t *testing.T) {
	container := "enclave-claude-0123456789ab-hs-eeaa4332b7b2c7d9"
	for _, tc := range []struct {
		name      string
		directory string
		want      string
	}{
		{"session in a worktree", "/proj/.worktrees/ws", "/proj/.worktrees/ws"},
		{"session in the project root", "/proj", "/proj"},
		{"no directory reported", "", "/proj"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeCLI{
				available: true,
				containers: map[string][]Container{
					"/proj": {{Name: container, Tool: "claude", Directory: tc.directory, SessionName: "hs-eeaa4332b7b2c7d9", Status: "running"}},
				},
			}
			p := storedProject(t, f, "proj1", "/proj")
			fl := &fakeLauncher{}
			p.term = fl
			ref := provider.SessionRef{ProjectID: "proj1", WorkstreamID: "w", SessionID: container}

			if _, err := p.OpenShell(context.Background(), ref); err != nil {
				t.Fatalf("OpenShell: %v", err)
			}
			want := terminal.HoldOnStartupFailureIn(tc.want, "enclave", "exec", "--name", container, "--tool", "claude", "--", "bash")
			if len(fl.launched) != 1 || !slices.Equal(fl.launched[0], want) {
				t.Errorf("launched = %q, want [%q]", fl.launched, want)
			}

		})
	}
}

// A container the CLI no longer lists is a fallback signal, not a failure.
func TestOpenShellReportsNoEnvironmentWhenTheContainerIsGone(t *testing.T) {
	f := &fakeCLI{available: true, containers: map[string][]Container{"/proj": {}}}
	p := storedProject(t, f, "proj1", "/proj")
	fl := &fakeLauncher{}
	p.term = fl

	_, err := p.OpenShell(context.Background(), provider.SessionRef{
		ProjectID: "proj1", WorkstreamID: "w", SessionID: "enclave-claude-gone-1",
	})
	if !errors.Is(err, provider.ErrNoShellEnvironment) {
		t.Errorf("error = %v, want ErrNoShellEnvironment", err)
	}
	if len(fl.launched) != 0 {
		t.Errorf("launched %d terminals, want none", len(fl.launched))
	}
}

func TestProviderIsShellOpener(t *testing.T) {
	var _ provider.ShellOpener = (*Provider)(nil)
}

// --- more than one session per workstream ---

// The enabling change: every container gets a unique `--name`. Without one the
// CLI derives `enclave-<tool>-<projectHash>` from the directory, so a second
// same-tool container in the same workstream would ask Docker for a name that is
// already taken — which is what the old MaxConcurrentSessions: 1 was working
// around.
func TestCreateSessionNamesEachContainerUniquely(t *testing.T) {
	cli := &fakeCLI{available: true, runResult: "enclave-claude-abc123abc123-hs-1"}
	p := storedProject(t, cli, "p1", "/tmp/project")

	req := provider.CreateSessionRequest{
		Workstream: provider.WorkstreamRef{ProjectID: "p1", WorkstreamID: "ws-1"},
		WorkingDir: "/tmp/project",
		OptionID:   "claude",
	}
	if _, err := p.CreateSession(context.Background(), req); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	if _, err := p.CreateSession(context.Background(), req); err != nil {
		t.Fatalf("second CreateSession() error = %v", err)
	}

	if len(cli.runCalls) != 2 {
		t.Fatalf("Run calls = %d, want 2", len(cli.runCalls))
	}
	first, second := cli.runCalls[0].opts.SessionName, cli.runCalls[1].opts.SessionName
	if first == "" || second == "" {
		t.Fatalf("session names = %q, %q; want both set", first, second)
	}
	if first == second {
		t.Errorf("both containers got the session name %q; they would collide", first)
	}
	// Same tool and directory both times — the name is the only thing separating
	// the two containers.
	for i, call := range cli.runCalls {
		if call.opts.Tool != "claude" || call.dir != "/tmp/project" {
			t.Errorf("call %d = tool %q in %q, want claude in /tmp/project", i, call.opts.Tool, call.dir)
		}
	}
}

// The name has to survive the round trip through the container name, or
// `enclave exec --name` cannot target the container it belongs to.
func TestSessionNameRoundTripsThroughTheContainerName(t *testing.T) {
	for i := 0; i < 50; i++ {
		name := newSessionName()
		if !strings.HasPrefix(name, sessionNamePrefix) {
			t.Fatalf("newSessionName() = %q, want the %q prefix", name, sessionNamePrefix)
		}
		// Docker container names allow [a-zA-Z0-9][a-zA-Z0-9_.-]*; the generated
		// half must not smuggle anything else in.
		for _, r := range name {
			if !(r == '-' || r == '_' || r == '.' ||
				(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
				t.Fatalf("newSessionName() = %q contains %q, which is not safe in a container name", name, r)
			}
		}
		container := "enclave-claude-abc123abc123-" + name
		if got := parseSessionName(container, "claude"); got != name {
			t.Fatalf("parseSessionName(%q) = %q, want %q", container, got, name)
		}
	}
}

// Two containers in one directory are two sessions on that workstream, each
// identified by its own container name — the shape the cap used to forbid.
func TestDiscoverTwoSessionsInOneWorkstream(t *testing.T) {
	cli := &fakeCLI{
		available: true,
		containers: map[string][]Container{
			"/tmp/project": {
				{Name: "enclave-claude-abc123abc123-hs-a", SessionName: "hs-a", Tool: "claude", Directory: "/tmp/project", Status: "running"},
				{Name: "enclave-codex-abc123abc123-hs-b", SessionName: "hs-b", Tool: "codex", Directory: "/tmp/project", Status: "running"},
			},
		},
	}
	p := storedProject(t, cli, "p1", "/tmp/project")

	sessions, err := p.DiscoverSessions(context.Background(), provider.ProjectRef{ID: "p1", Path: "/tmp/project"})
	if err != nil {
		t.Fatalf("DiscoverSessions() error = %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("discovered %d sessions, want 2", len(sessions))
	}
	ids := map[string]bool{}
	for _, s := range sessions {
		if s.WorkingDir != "/tmp/project" {
			t.Errorf("session %q reports dir %q, want /tmp/project", s.Session.ID, s.WorkingDir)
		}
		ids[s.Session.ID] = true
	}
	if len(ids) != 2 {
		t.Errorf("session IDs = %v, want two distinct container names", ids)
	}
}

// Each session's actions must reach its own container, not the workstream's
// "first" one.
func TestPerSessionActionsTargetTheirOwnContainer(t *testing.T) {
	p, cli, bridge, project := bridgeProvider(t)
	first := createBridge(t, p, project)
	cli.runResult = "enclave-codex-0123456789ab-hs-second"
	second := createBridge(t, p, project)
	ref := provider.SessionRef{ProjectID: project.ID, SessionID: second.ID}
	if err := p.Prompt(context.Background(), ref, "only second"); err != nil {
		t.Fatal(err)
	}
	if bridge.inputTarget != bridgeTarget(second.ID) || bridge.inputTarget == bridgeTarget(first.ID) {
		t.Fatalf("input target=%s", bridge.inputTarget)
	}
	if err := p.RemoveSession(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	if len(cli.stopped) != 1 || cli.stopped[0] != second.ID {
		t.Fatalf("stopped=%v", cli.stopped)
	}
}

func bareProvider(t *testing.T, cli CLI) *Provider {
	t.Helper()
	bridge := &testBridge{live: true, snap: sbridge.Snapshot{Screen: "agent output"}}
	p, err := NewWithBridge(cli, bridge, filepath.Join(t.TempDir(), "enclave-provider.json"))
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "workstreams")
	p.workstreamsRoot = func() (string, error) { return root, nil }
	if fake, ok := cli.(*fakeCLI); ok {
		for dir, containers := range fake.containers {
			for _, c := range containers {
				if c.Directory == "" {
					c.Directory = dir
				}
				r := bridgeRecord{Container: c}
				if exited(c.Status) {
					r.State = domain.SessionDone
				}
				p.bridges.records[c.Name] = r
			}
		}
	}
	return p
}

func (f *fakeCLI) AttachCommand(container string) []string {
	return []string{"enclave", "attach", container}
}

// A session in a default-root workstream directory asks Enclave to mount it
// despite its sensitive-mount guard, which refuses Homeshell's own data directory;
// nothing else is vouched for. The decision is by path alone, with symlinks
// resolved on both sides — Enclave resolves the path it is given, so a link that
// leads out of the root would exempt wherever it leads — and the directory is
// passed as given.
func TestCreateSessionVouchesOnlyForDefaultRootWorkstreamDirectories(t *testing.T) {
	tests := []struct {
		name string
		// root is the default workstreams root relative to the test's base
		// directory, "root" when empty; rootErr makes looking it up fail.
		root       string
		rootErr    error
		workingDir string
		vouched    bool
	}{
		{name: "a directory in the root", workingDir: "root/proj-x", vouched: true},
		{name: "a directory deeper in the root", workingDir: "root/a/b", vouched: true},
		{name: "the root reached through a symlink", root: "root-link", workingDir: "root/proj-x", vouched: true},
		{name: "the root itself", workingDir: "root"},
		{name: "a sibling sharing the root's prefix", workingDir: "root-other/x"},
		{name: "a directory outside the root", workingDir: "outside"},
		{name: "a symlink outside the root into it", workingDir: "link-in", vouched: true},
		{name: "a symlink in the root leading out of it", workingDir: "root/link-out"},
		{name: "a working directory that does not exist", workingDir: "root/missing"},
		{name: "a root that does not exist", root: "absent", workingDir: "root/proj-x"},
		{name: "a root that cannot be looked up", rootErr: errors.New("no home directory"), workingDir: "root/proj-x"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := t.TempDir()
			for _, dir := range []string{"root/proj-x", "root/a/b", "root-other/x", "outside"} {
				if err := os.MkdirAll(filepath.Join(base, dir), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			for link, target := range map[string]string{
				"root-link":     "root",
				"link-in":       "root/proj-x",
				"root/link-out": "outside",
			} {
				if err := os.Symlink(filepath.Join(base, target), filepath.Join(base, link)); err != nil {
					t.Fatal(err)
				}
			}
			root := tt.root
			if root == "" {
				root = "root"
			}
			cli := &fakeCLI{available: true, runResult: "enclave-claude-new123"}
			p := newForTest(t, cli)
			p.workstreamsRoot = func() (string, error) {
				if tt.rootErr != nil {
					return "", tt.rootErr
				}
				return filepath.Join(base, root), nil
			}
			workingDir := filepath.Join(base, tt.workingDir)

			if _, err := p.CreateSession(context.Background(), provider.CreateSessionRequest{
				Workstream: provider.WorkstreamRef{ProjectID: "proj-1", WorkstreamID: "ws1"},
				WorkingDir: workingDir,
				OptionID:   "claude",
			}); err != nil {
				t.Fatalf("CreateSession: %v", err)
			}

			if len(cli.runCalls) != 1 {
				t.Fatalf("Run calls = %d, want 1", len(cli.runCalls))
			}
			var want []string
			if tt.vouched {
				want = []string{workingDir}
			}
			if got := cli.runCalls[0].opts.SensitiveDirs; !slices.Equal(got, want) {
				t.Errorf("SensitiveDirs = %q, want %q", got, want)
			}
		})
	}
}
