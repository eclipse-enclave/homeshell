package enclave

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/agentmodel"
	"github.com/eclipsesource/homeshell/daemon/internal/agentspec"
	"github.com/eclipsesource/homeshell/daemon/internal/provider"
	"github.com/eclipsesource/homeshell/daemon/internal/provider/enclave/sandboxschema"
)

// Session-creation knobs for the tools Enclave can start.
//
// `enclave run` appends whatever follows a `--` onto the tool's own command line
// (verified by reading the started container's argv: `claude … --model <id>`), so a
// setting is delivered the way the agent itself documents — the same flags the Local
// provider passes on the host. A tool whose setting has no flag is set through the
// environment variable it reads instead, injected with `--pass-env +NAME` (see
// realCLI.Run).
//
// **Which settings those are is not written here.** They are the same user-authored
// rows the Local provider reads (internal/agentspec, internal/agentcatalog), keyed by the
// agent id an `enclave tools` profile shares with the host agent — which is what
// makes a container of `claude` offer the same model list *and* the same knobs as
// `claude` on the host. This file is what remains provider-specific: asking a running
// container which models its tool has, and the one delivery Enclave cannot make.
//
// The rows are still an allow-list, and deliberately so: a profile with no rows keeps
// its bare option, exactly as every unlisted profile did before. That is what keeps
// arguments off a profile that runs no agent CLI (`theia` runs `sleep infinity`, and
// its model is chosen inside the IDE), where appending them would break it.
//
// agentspec.Appended is the other half of that honesty. Enclave appends to a command
// line the tool's own launcher assembles, and a **positional** value there is not
// verified the way a flag is — so a setting delivered positionally (the initial
// prompt, for Claude, Codex and pi) is left out of the container's create form
// altogether rather than offered and dropped.

// agentFor resolves an `enclave tools` profile to its agent description, so a
// container of a tool is configured from the same rows the host agent uses. Reports
// false for a profile nothing describes, which is a profile with no knobs.
func (p *Provider) agentFor(tool string) (agentspec.Agent, bool) {
	agent, ok := p.agentsByTool()[tool]
	return agent, ok
}

// agentsByTool resolves competing bindings in source order. Both configuration
// lookup and the per-pass detection index use this same first-match policy.
func (p *Provider) agentsByTool() map[string]agentspec.Agent {
	out := map[string]agentspec.Agent{}
	if p.specs == nil {
		return out
	}
	for _, agent := range p.specs.Agents() {
		tool := agent.EnclaveToolName()
		if _, exists := out[tool]; !exists {
			out[tool] = agent
		}
	}
	return out
}

// SessionSchema builds the schema of one tool's option alone, as SessionSchemas
// builds each, without asking Enclave about the others. A tool SessionSchemas
// would not list is refused. An Enclave that cannot list its tools, or report a
// tool's sandbox settings, is unavailable: a restart form without them would drop
// the session's.
func (p *Provider) SessionSchema(ctx context.Context, ref provider.WorkstreamRef, tool string) (json.RawMessage, error) {
	tools, err := p.cli.Tools(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: could not list Enclave's tools; try again (%v)", provider.ErrProviderUnavailable, err)
	}
	if len(tools) == 0 {
		tools = []Tool{{Name: defaultTool}}
	}
	if !slices.ContainsFunc(tools, func(offered Tool) bool { return offered.Name == tool }) {
		return nil, fmt.Errorf("%w (%q)", provider.ErrOptionNotOffered, tool)
	}
	schema, err := p.sessionSchema(ctx, ref, tool)
	if err != nil {
		return nil, err
	}
	return schema, nil
}

// listedSchema is a tool's schema as a create form lists it: one Enclave could
// not report the sandbox settings for still offers the agent's own.
func (p *Provider) listedSchema(ctx context.Context, ref provider.WorkstreamRef, tool string) json.RawMessage {
	schema, err := p.sessionSchema(ctx, ref, tool)
	if err != nil {
		slog.Debug("offering the session without sandbox settings", "tool", tool, "error", err)
	}
	return schema
}

// sessionSchema is the session-config JSON Schema for a tool profile: the settings
// Enclave can deliver, with the models it should offer already resolved, followed by
// the container's sandbox settings with Enclave's own values as their defaults. When
// Enclave cannot report those, the group says so and has no fields, and the error
// says why. nil for a workstream whose directory is unknown and a profile with no
// settings, which is what an option with an empty schema means to a client.
func (p *Provider) sessionSchema(ctx context.Context, ref provider.WorkstreamRef, tool string) (json.RawMessage, error) {
	var schema json.RawMessage
	if agent, ok := p.agentFor(tool); ok {
		schema = agentspec.Schema(agent, p.offeredModels(ctx, ref.ProjectID, agent), agentspec.Appended)
	}
	dir, ok := p.workingDir(ref)
	if !ok {
		return schema, nil
	}
	base, err := p.sandboxBase(ctx, dir, tool)
	if err != nil {
		return withSandbox(schema, unavailableSandboxSchema), fmt.Errorf("Enclave did not report its container settings: %w", err)
	}
	return withSandbox(schema, sandboxschema.Schema(base)), nil
}

// withSandbox adds the sandbox group to a tool's session schema, or leaves it out
// when it does not fit (sandboxschema.With).
func withSandbox(schema, group json.RawMessage) json.RawMessage {
	out, err := sandboxschema.With(schema, group)
	if err != nil {
		slog.Warn("offering a session schema without sandbox settings", "error", err)
	}
	return out
}

// workingDir is the directory a workstream's sessions run in, which is where
// Enclave resolves their settings: the workstream's own, or its project's.
func (p *Provider) workingDir(ref provider.WorkstreamRef) (string, bool) {
	if p.store != nil {
		if workstream, err := p.store.Workstream(ref.ProjectID, ref.WorkstreamID); err == nil && workstream.WorkingDir != "" {
			return workstream.WorkingDir, true
		}
	}
	return p.projectPath(ref.ProjectID)
}

// sandboxFlagsFor is the `enclave run` flags for the sandbox settings in a session
// config: none when it carries no sandbox group, or one that sets nothing. The
// group is validated here against the schema Enclave's answer makes: a listing
// that could not read Enclave's settings offered the service no schema to check.
// Without Enclave's answer the group is not applied at all, since a list flag
// would drop what its config lists.
func (p *Provider) sandboxFlagsFor(ctx context.Context, dir, tool string, config json.RawMessage) ([]string, error) {
	choice, ok := sandboxOf(config)
	if !ok || choice.isEmpty() {
		return nil, nil
	}
	if err := checkDirs(choice.Dirs, dir); err != nil {
		return nil, err
	}
	base, err := p.sandboxBase(ctx, dir, tool)
	if err != nil {
		return nil, fmt.Errorf("%w: Enclave did not report its container settings, so the session's own cannot be applied; try again (%v)",
			provider.ErrProviderUnavailable, err)
	}
	groupOnly := `{"type":"object","properties":{"` + sandboxschema.Key + `":` + string(sandboxschema.Schema(base)) + `}}`
	if err := provider.ValidateConfig(json.RawMessage(groupOnly), config); err != nil {
		return nil, err
	}
	return sandboxFlags(choice, base), nil
}

// checkDirs refuses, per field, a directory that is not one here, so a restart is
// refused before the running container is stopped rather than failing after it.
// Which directories may be mounted is Enclave's call, made when the session starts.
func checkDirs(dirs []sandboxschema.Dir, workingDir string) error {
	fields := map[string]string{}
	for i, dir := range dirs {
		if !isDir(resolveDir(strings.TrimSpace(dir.Path), workingDir)) {
			fields[fmt.Sprintf("/%s/dirs/%d/path", sandboxschema.Key, i)] = "is not a directory on this machine"
		}
	}
	if len(fields) == 0 {
		return nil
	}
	return &provider.ValidationError{Message: "configuration is invalid", Fields: fields}
}

// resolveDir is the path Enclave mounts for path: `~` against the home directory,
// a relative path against the working directory.
func resolveDir(path, workingDir string) string {
	if home, err := os.UserHomeDir(); err == nil {
		if path == "~" {
			path = home
		} else if rest, ok := strings.CutPrefix(path, "~/"); ok {
			path = filepath.Join(home, rest)
		}
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(workingDir, path)
	}
	return path
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// launchOpts is how a session with config starts as tool in dir, in the
// background: the settings the user chose, delivered as deliver says, and the
// container settings they changed from Enclave's own, as `enclave run` flags.
// The caller names the session and says whether it continues, and forgets the
// base first when the answer must be fresh (forgetBase).
func (p *Provider) launchOpts(ctx context.Context, dir, tool string, config json.RawMessage) (RunOpts, error) {
	toolArgs, containerEnv := p.deliver(tool, config)
	sandbox, err := p.sandboxFlagsFor(ctx, dir, tool, config)
	if err != nil {
		return RunOpts{}, err
	}
	return RunOpts{
		Tool:       tool,
		Background: true,
		ToolArgs:   toolArgs,
		Env:        containerEnv,
		// Enclave's sensitive-mount guard checks `continue` as it checks `run`.
		SensitiveDirs: p.sensitiveDirsFor(dir),
		ExtraFlags:    sandbox,
	}, nil
}

// deliver is what a new container's tool must be started with for the settings the
// user chose: arguments appended to its command line after the `--`, and variables
// passed into the container. Both are empty for a profile with no rows, or when
// nothing was chosen.
func (p *Provider) deliver(tool string, config json.RawMessage) ([]string, map[string]string) {
	a, ok := p.agentFor(tool)
	if !ok {
		return nil, nil
	}
	// No session id and no title: a container takes neither. Those settings deliver
	// nothing on an empty Launch, so they need no special case here.
	cmd := agentspec.Deliver(a, agentspec.Launch{
		Model:  agentspec.ModelOf(a, config),
		Config: config,
	}, agentspec.Appended)
	return cmd.Args, cmd.Env
}

// offeredModels is what a tool's Model field should list here: what a running
// container of it reported, or the user's authored rows for the same agent id
// (agentmodel.Offered — the rule is shared with the Local provider, so a tool offers
// the same models in a container as on the host).
func (p *Provider) offeredModels(ctx context.Context, projectID string, a agentspec.Agent) []string {
	return agentmodel.Offered(p.discoverModels(ctx, projectID, a), p.catalog, a.ID)
}

// modelListTimeout bounds asking a container for its models. The create form waits
// on it, so it is short: an `enclave exec` into a running container is fast, and a
// slow answer must degrade to the field the form had before rather than stall it.
const modelListTimeout = 5 * time.Second

// discoverModels asks a running container of this tool which models it has.
//
// It asks only what is already there: `enclave exec` needs a running container, and
// the provider will not start one to answer a form. So the first session of a tool
// is created with a typed model and every later form offers that container's list —
// remembered for agentmodel.TTL, keyed by project and tool, so a form open costs
// nothing once asked. nil means "nothing to offer", which leaves the field exactly as it was.
func (p *Provider) discoverModels(ctx context.Context, projectID string, a agentspec.Agent) []string {
	listModels := agentspec.ListModelsArgv(a)
	if len(listModels) == 0 {
		return nil
	}
	// Changing a profile binding or its model-list command must not reuse the
	// previous profile's cached answer under the same HomeShell agent ID.
	cacheKey := projectID + "\x00" + a.ID + "\x00" + a.EnclaveToolName() + "\x00" + strings.Join(listModels, "\x00")
	if models, ok := p.models.Get(cacheKey, time.Now()); ok {
		return models
	}
	projectDir, ok := p.projectPath(projectID)
	if !ok {
		return nil
	}
	containers, err := p.cli.ListSessions(ctx, projectDir)
	if err != nil {
		return nil
	}
	// `enclave ps` lists every project's containers; another project's must not answer here.
	project := provider.ProjectRef{ID: projectID, Path: projectDir}
	projects := p.storeProjects()
	containers = slices.DeleteFunc(containers, func(container Container) bool {
		return !ownsContainerIn(projects, project, container)
	})
	target, ok := runningContainerFor(containers, a.EnclaveToolName())
	if !ok {
		// Nothing of this tool is up, so there is nobody to ask. Deliberately not
		// remembered: the answer changes the moment a session starts.
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, modelListTimeout)
	defer cancel()
	out, err := p.cli.ExecOutput(ctx, projectDir, target.SessionName, a.EnclaveToolName(), listModels)
	if err != nil {
		slog.Debug("asking a container for its models failed; offering the field without a list",
			"tool", a.ID, "container", target.Name, "error", err)
		p.models.Put(cacheKey, time.Now(), nil)
		return nil
	}
	models := agentmodel.ParseList(out)
	p.models.Put(cacheKey, time.Now(), models)
	return models
}

// runningContainerFor picks a container of this tool that is up. Any of them will
// do: the tool's authentication is shared per project, so they all answer alike.
func runningContainerFor(containers []Container, tool string) (Container, bool) {
	for _, c := range containers {
		if c.Tool == tool && strings.Contains(strings.ToLower(c.Status), "running") {
			return c, true
		}
	}
	return Container{}, false
}
