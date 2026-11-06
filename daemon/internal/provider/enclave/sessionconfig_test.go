package enclave

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/eclipsesource/homeshell/daemon/internal/agentspec"
	"github.com/eclipsesource/homeshell/daemon/internal/domain"
	"github.com/eclipsesource/homeshell/daemon/internal/provider"
	"github.com/eclipsesource/homeshell/daemon/internal/provider/enclave/sandboxschema"
	"github.com/eclipsesource/homeshell/daemon/internal/store"
)

// Every profile that runs an agent CLI must arrive with a model knob — that is
// what makes a container session start on a chosen model instead of on whatever
// the image defaults to. The two shapes are both deliberate: an enum where the
// valid IDs are the vendor's and known here, free text where they come from a
// catalog or a container config HomeShell cannot see.
func TestSessionSchemasCarryAModelKnobPerAgent(t *testing.T) {
	tools := []string{"claude", "codex", "opencode", "pi", "mistral-vibe", "theia", "theia-next"}
	p := newForTest(t, &fakeCLI{available: true, tools: tools})

	opts, err := p.SessionSchemas(context.Background(), provider.WorkstreamRef{})
	if err != nil {
		t.Fatal(err)
	}
	schemas := map[string]json.RawMessage{}
	for _, o := range opts {
		schemas[o.ID] = o.Schema
	}

	for _, tt := range []struct {
		tool string
		enum []string // nil = a free-text model field
	}{
		{"claude", seededModels(t, "claude")},
		{"codex", seededModels(t, "codex")},
		{"opencode", nil},
		{"pi", nil},
		{"mistral-vibe", nil},
	} {
		t.Run(tt.tool, func(t *testing.T) {
			schema, ok := schemas[tt.tool]
			if !ok || len(schema) == 0 {
				t.Fatalf("%s carries no schema; options = %+v", tt.tool, opts)
			}
			prop := modelProp(t, schema)
			if prop.Type != "string" || prop.Title == "" || prop.Description == "" {
				t.Errorf("%s model field = %+v, want a titled, described string", tt.tool, prop)
			}
			if prop.Default != "" {
				t.Errorf("%s model default = %q; unset must stay a real answer", tt.tool, prop.Default)
			}
			if !slices.Equal(prop.Enum, tt.enum) {
				t.Errorf("%s model enum = %v, want %v", tt.tool, prop.Enum, tt.enum)
			}
		})
	}

	// Theia is an IDE, not an agent CLI: its container runs `sleep infinity`, so a
	// model would have nothing to reach and an argument would reach `sleep`.
	for _, tool := range []string{"theia", "theia-next"} {
		if len(schemas[tool]) != 0 {
			t.Errorf("%s carries a schema %s, want none", tool, schemas[tool])
		}
	}
}

// The fallback option (no tool list to read, because the CLI failed or is old)
// is still a claude session, so it offers the same choice.
func TestSessionSchemasFallbackCarriesTheModelKnob(t *testing.T) {
	p := newForTest(t, &fakeCLI{available: true, tools: nil})

	opts, err := p.SessionSchemas(context.Background(), provider.WorkstreamRef{})
	if err != nil {
		t.Fatal(err)
	}
	if len(opts) != 1 || opts[0].ID != "claude" {
		t.Fatalf("options = %+v, want the claude fallback", opts)
	}
	if len(opts[0].Schema) == 0 {
		t.Error("fallback claude option carries no schema")
	}
}

// The schema is what the daemon validates a create request against before this
// provider ever runs. An enum has to accept every catalog model and refuse
// anything else; a free-text field has to take what the user typed — and both
// have to treat the model as optional.
//
// The enum is not in the rows: it is whatever the provider resolved for that agent
// (agentmodel.Offered), so the test builds the schema the same way the provider does —
// with the catalog for an agent that has one, and with nothing for an agent whose
// models only its own CLI can name.
func TestModelSchemaValidatesTheChoice(t *testing.T) {
	tests := []struct {
		name    string
		tool    string
		config  string
		wantErr bool
	}{
		{"a catalog model", "claude", `{"model":"claude-opus-5"}`, false},
		{"another catalog model", "claude", `{"model":"claude-haiku-4-5"}`, false},
		{"no model at all", "claude", `{}`, false},
		{"a model that is not on offer", "claude", `{"model":"claude-opus-9"}`, true},
		{"a family alias rather than a model", "claude", `{"model":"opus"}`, true},
		{"a knob this tool does not have", "claude", `{"sandbox":"none"}`, true},
		{"a codex model", "codex", `{"model":"gpt-5.6"}`, false},
		{"a codex model that is not on offer", "codex", `{"model":"gpt-4"}`, true},
		// Free text: the accepted values live in the agent's own catalog, so the
		// daemon checks the shape and lets the agent judge the value.
		{"any provider/model for opencode", "opencode", `{"model":"anthropic/claude-sonnet-5"}`, false},
		{"any provider/id for pi", "pi", `{"model":"openai/gpt-5.6:high"}`, false},
		{"any alias for vibe", "mistral-vibe", `{"model":"local"}`, false},
		{"a non-string model", "opencode", `{"model":5}`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			schema := agentspec.Schema(describedAgent(t, tt.tool), seededModels(t, tt.tool), agentspec.Appended)
			err := provider.ValidateConfig(schema, json.RawMessage(tt.config))
			if tt.wantErr {
				var verr *provider.ValidationError
				if !errors.As(err, &verr) {
					t.Fatalf("ValidateConfig(%s, %s) error = %v, want a validation error", tt.tool, tt.config, err)
				}
				return
			}
			if err != nil {
				t.Errorf("ValidateConfig(%s, %s) error = %v, want nil", tt.tool, tt.config, err)
			}
		})
	}
}

// Where the chosen model goes: onto the agent's own command line for a tool with
// a model flag, into the environment for one without, and nowhere at all for a
// profile that runs no agent.
func TestModelReachesTheRightPlacePerTool(t *testing.T) {
	tests := []struct {
		name     string
		tool     string
		config   string
		wantArgs []string
		wantEnv  map[string]string
	}{
		{
			"claude takes a flag",
			"claude", `{"model":"claude-opus-5"}`,
			[]string{"--model", "claude-opus-5"}, nil,
		},
		{
			"codex takes a flag",
			"codex", `{"model":"gpt-5.6"}`,
			[]string{"--model", "gpt-5.6"}, nil,
		},
		{
			"opencode takes a flag",
			"opencode", `{"model":"anthropic/claude-sonnet-5"}`,
			[]string{"--model", "anthropic/claude-sonnet-5"}, nil,
		},
		{
			"pi takes a flag",
			"pi", `{"model":"openai/gpt-5.6"}`,
			[]string{"--model", "openai/gpt-5.6"}, nil,
		},
		{
			// `vibe` has no model flag; VIBE_* overrides any config field.
			"mistral-vibe takes an environment variable",
			"mistral-vibe", `{"model":"devstral-small-latest"}`,
			nil, map[string]string{"VIBE_ACTIVE_MODEL": "devstral-small-latest"},
		},
		{"no model chosen", "claude", `{}`, nil, nil},
		{"an empty model", "claude", `{"model":""}`, nil, nil},
		{"no config at all", "claude", "", nil, nil},
		{"unparseable config", "claude", `{`, nil, nil},
		// The allow-list is the guard: an argument appended to theia would be read
		// by `sleep`, and a model set for a profile we know nothing about is a guess.
		{"theia runs no agent", "theia", `{"model":"claude-opus-5"}`, nil, nil},
		{"an unknown profile", "made-up", `{"model":"claude-opus-5"}`, nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newForTest(t, &fakeCLI{available: true})
			args, env := p.deliver(tt.tool, json.RawMessage(tt.config))
			if !slices.Equal(args, tt.wantArgs) {
				t.Errorf("deliver(%q, %s) args = %v, want %v", tt.tool, tt.config, args, tt.wantArgs)
			}
			if len(env) != len(tt.wantEnv) {
				t.Fatalf("deliver(%q, %s) env = %v, want %v", tt.tool, tt.config, env, tt.wantEnv)
			}
			for k, v := range tt.wantEnv {
				if env[k] != v {
					t.Errorf("deliver(%q, %s) env[%q] = %q, want %q", tt.tool, tt.config, k, env[k], v)
				}
			}
		})
	}
}

// The knob is only real if it reaches `enclave run`: this is the step between
// "the form offered a model" and "the container starts on it".
func TestCreateSessionPassesTheChosenModelToTheCLI(t *testing.T) {
	tests := []struct {
		tool     string
		config   string
		wantArgs []string
		wantEnv  string
	}{
		{"claude", `{"model":"claude-opus-5"}`, []string{"--model", "claude-opus-5"}, ""},
		{"codex", `{"model":"gpt-5.6"}`, []string{"--model", "gpt-5.6"}, ""},
		{"mistral-vibe", `{"model":"local"}`, nil, "local"},
	}
	for _, tt := range tests {
		t.Run(tt.tool, func(t *testing.T) {
			cli := &fakeCLI{available: true, runResult: "enclave-" + tt.tool + "-new123"}
			p := newForTest(t, cli)

			if _, err := p.CreateSession(context.Background(), provider.CreateSessionRequest{
				Workstream: provider.WorkstreamRef{ProjectID: "proj-1", WorkstreamID: "ws1"},
				WorkingDir: "/tmp/project",
				OptionID:   tt.tool,
				Config:     json.RawMessage(tt.config),
			}); err != nil {
				t.Fatalf("CreateSession: %v", err)
			}

			if len(cli.runCalls) != 1 {
				t.Fatalf("got %d run calls, want 1", len(cli.runCalls))
			}
			opts := cli.runCalls[0].opts
			if !slices.Equal(opts.ToolArgs, tt.wantArgs) {
				t.Errorf("ToolArgs = %v, want %v", opts.ToolArgs, tt.wantArgs)
			}
			if tt.wantEnv != "" && opts.Env["VIBE_ACTIVE_MODEL"] != tt.wantEnv {
				t.Errorf("Env = %v, want VIBE_ACTIVE_MODEL=%s", opts.Env, tt.wantEnv)
			}
			if tt.wantEnv == "" && len(opts.Env) != 0 {
				t.Errorf("Env = %v, want none", opts.Env)
			}
		})
	}
}

// A session adds its own variables to the pass_env the user configured and never
// re-passes the configured names: on the command line they would escape Enclave's
// no_api_key and ephemeral gates, which apply to configured entries.
func TestCreateSessionLeavesTheConfiguredPassEnvToEnclave(t *testing.T) {
	create := func(t *testing.T, cli *fakeCLI, tool, config string) RunOpts {
		t.Helper()
		if _, err := newForTest(t, cli).CreateSession(context.Background(), provider.CreateSessionRequest{
			Workstream: provider.WorkstreamRef{ProjectID: "proj-1", WorkstreamID: "ws1"},
			WorkingDir: "/tmp/project",
			OptionID:   tool,
			Config:     json.RawMessage(config),
		}); err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		return cli.runCalls[len(cli.runCalls)-1].opts
	}
	configured := map[string]sandboxschema.EffectiveConfig{
		"mistral-vibe": {"pass_env": option(`["GITHUB_TOKEN","ANTHROPIC_API_KEY"]`, "global")},
		"claude":       {"pass_env": option(`["GITHUB_TOKEN"]`, "global")},
	}

	t.Run("a session variable is added", func(t *testing.T) {
		opts := create(t, &fakeCLI{available: true, runResult: "enclave-mistral-vibe-new123", config: configured}, "mistral-vibe", `{"model":"local"}`)
		flags := strings.Join(opts.SessionFlags(), " ")
		if !strings.Contains(flags, "--pass-env +VIBE_ACTIVE_MODEL") || opts.Env["VIBE_ACTIVE_MODEL"] != "local" {
			t.Errorf("flags %q, Env %v: want the session's variable added with +NAME", flags, opts.Env)
		}
		if strings.Contains(flags, "GITHUB_TOKEN") || strings.Contains(flags, "ANTHROPIC_API_KEY") {
			t.Errorf("flags %q re-pass configured pass_env names", flags)
		}
	})

	t.Run("no variables, no flag", func(t *testing.T) {
		opts := create(t, &fakeCLI{available: true, runResult: "enclave-claude-new123", config: configured}, "claude", `{"model":"claude-opus-5"}`)
		if flags := strings.Join(opts.SessionFlags(), " "); strings.Contains(flags, "--pass-env") {
			t.Errorf("flags %q carry --pass-env for a session with no variables", flags)
		}
	})
}

// A session created without a model must be launched exactly as it was before
// the knob existed — nothing appended, nothing set.
func TestCreateSessionWithoutAModelPassesNothingExtra(t *testing.T) {
	cli := &fakeCLI{available: true, runResult: "enclave-claude-new123"}
	p := newForTest(t, cli)

	if _, err := p.CreateSession(context.Background(), provider.CreateSessionRequest{
		Workstream: provider.WorkstreamRef{ProjectID: "proj-1", WorkstreamID: "ws1"},
		WorkingDir: "/tmp/project",
		OptionID:   "claude",
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	if len(cli.runCalls) != 1 {
		t.Fatalf("got %d run calls, want 1", len(cli.runCalls))
	}
	if opts := cli.runCalls[0].opts; len(opts.ToolArgs) != 0 || len(opts.Env) != 0 {
		t.Errorf("ToolArgs = %v, Env = %v, want both empty", opts.ToolArgs, opts.Env)
	}
}

type modelField struct {
	Type        string   `json:"type"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Default     string   `json:"default"`
	Enum        []string `json:"enum"`
}

// modelProp reads the "model" property out of a session schema, so a test asserts
// on what the client will actually offer rather than on a string.
//
// Only that one property is decoded into modelField: a schema now carries the agent's
// other knobs too, and their defaults are not all strings (vibe's auto-approve is a
// boolean), so decoding every property into one shape would fail on a sibling field.
func modelProp(t *testing.T, schema json.RawMessage) modelField {
	t.Helper()
	var doc struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(schema, &doc); err != nil {
		t.Fatalf("schema %s is not valid JSON: %v", schema, err)
	}
	raw, ok := doc.Properties["model"]
	if !ok {
		return modelField{}
	}
	var field modelField
	if err := json.Unmarshal(raw, &field); err != nil {
		t.Fatalf("model property %s is not a field: %v", raw, err)
	}
	return field
}

// Asking a container is the only way Enclave can know what a tool actually runs,
// and it can only ask one that is already up. When one is, its answer becomes the
// Model field's choices — in place of the curated guess, because the container is
// the authority on what it can run.
func TestSessionSchemasOfferTheModelsARunningContainerReports(t *testing.T) {
	const dir = "/tmp/project"
	cli := &fakeCLI{
		available: true,
		tools:     []string{"opencode", "pi", "claude"},
		containers: map[string][]Container{dir: {
			{Name: "enclave-opencode-abc123abc123-hs-1", Tool: "opencode", Directory: dir, Status: "running", SessionName: "hs-1"},
		}},
		execOutput: map[string]string{
			// Through a pty, so the lines arrive with carriage returns.
			"opencode": "anthropic/claude-sonnet-5\r\nopenai/gpt-5.6\r\n",
			"pi":       "anthropic/claude-opus-5\r\n",
		},
	}
	p := storedProject(t, cli, "proj-1", dir)

	opts, err := p.SessionSchemas(context.Background(), provider.WorkstreamRef{ProjectID: "proj-1"})
	if err != nil {
		t.Fatal(err)
	}
	schemas := map[string]json.RawMessage{}
	for _, o := range opts {
		schemas[o.ID] = o.Schema
	}

	if got, want := modelProp(t, schemas["opencode"]).Enum, []string{"anthropic/claude-sonnet-5", "openai/gpt-5.6"}; !slices.Equal(got, want) {
		t.Errorf("opencode model choices = %v, want what its container reported %v", got, want)
	}
	// pi has no container running, so there was nobody to ask and its field keeps
	// no list rather than borrowing another tool's answer.
	if got := modelProp(t, schemas["pi"]).Enum; got != nil {
		t.Errorf("pi model choices = %v, want none: no pi container is running", got)
	}
	if cli.execCalls["pi"] != 0 {
		t.Errorf("pi was asked %d times with no container running", cli.execCalls["pi"])
	}
	// claude cannot list its models at all, so it keeps the curated catalog.
	if got := modelProp(t, schemas["claude"]).Enum; !slices.Equal(got, seededModels(t, "claude")) {
		t.Errorf("claude model choices = %v, want the catalog", got)
	}
}

// Everything that can go wrong with asking leaves the field as it was, and is
// remembered so a container that cannot answer is not re-asked on every form open.
func TestModelDiscoveryFallsBackAndIsRemembered(t *testing.T) {
	const dir = "/tmp/project"
	newCLI := func() *fakeCLI {
		return &fakeCLI{
			available: true,
			tools:     []string{"opencode"},
			containers: map[string][]Container{dir: {
				{Name: "enclave-opencode-abc123abc123-hs-1", Tool: "opencode", Directory: dir, Status: "running", SessionName: "hs-1"},
			}},
		}
	}
	tests := []struct {
		name string
		with func(cli *fakeCLI)
	}{
		{"the exec fails", func(cli *fakeCLI) { cli.execErr = errors.New("no running container found") }},
		{"the tool prints nothing", func(cli *fakeCLI) { cli.execOutput = map[string]string{"opencode": ""} }},
		{"the tool explains instead of listing", func(cli *fakeCLI) {
			cli.execOutput = map[string]string{"opencode": "No models available. Use /login.\r\n"}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cli := newCLI()
			tt.with(cli)
			p := storedProject(t, cli, "proj-1", dir)

			for i := 0; i < 3; i++ {
				opts, err := p.SessionSchemas(context.Background(), provider.WorkstreamRef{ProjectID: "proj-1"})
				if err != nil {
					t.Fatalf("SessionSchemas: %v", err)
				}
				if got := modelProp(t, opts[0].Schema).Enum; got != nil {
					t.Fatalf("model choices = %v, want none so the field stays typeable", got)
				}
			}
			if got := cli.execCalls["opencode"]; got != 1 {
				t.Errorf("asked the container %d times for 3 form opens, want 1", got)
			}
			// The field itself must survive: a model can still be typed.
			if err := provider.ValidateConfig(opts0(t, p), json.RawMessage(`{"model":"anything/at-all"}`)); err != nil {
				t.Errorf("ValidateConfig rejected a typed model: %v", err)
			}
		})
	}
}

// A stopped container is not somebody to ask: exec would fail, and the answer
// would be about a container that is no longer there.
func TestModelDiscoveryIgnoresAStoppedContainer(t *testing.T) {
	const dir = "/tmp/project"
	cli := &fakeCLI{
		available: true,
		tools:     []string{"opencode"},
		containers: map[string][]Container{dir: {
			{Name: "enclave-opencode-abc123abc123-hs-1", Tool: "opencode", Directory: dir, Status: "exited", SessionName: "hs-1"},
		}},
		execOutput: map[string]string{"opencode": "anthropic/claude-sonnet-5\r\n"},
	}
	p := storedProject(t, cli, "proj-1", dir)

	opts, err := p.SessionSchemas(context.Background(), provider.WorkstreamRef{ProjectID: "proj-1"})
	if err != nil {
		t.Fatal(err)
	}
	if got := modelProp(t, opts[0].Schema).Enum; got != nil {
		t.Errorf("model choices = %v, want none from a container that is not running", got)
	}
	if cli.execCalls["opencode"] != 0 {
		t.Error("a stopped container was asked anyway")
	}
}

// `enclave ps` lists every project's containers, so a running container of the
// tool in another project must not answer for this one, neither directly nor
// through a list cached when that project's form was opened.
func TestModelDiscoveryAsksOnlyTheProjectsOwnContainers(t *testing.T) {
	const dirA, dirB = "/tmp/project-a", "/tmp/project-b"
	containerA := Container{Name: "enclave-opencode-aaaaaaaaaaaa-hs-1", Tool: "opencode", Directory: dirA, Status: "running", SessionName: "hs-1"}
	cli := &fakeCLI{
		available:  true,
		tools:      []string{"opencode"},
		containers: map[string][]Container{dirA: {containerA}, dirB: {containerA}},
		execOutput: map[string]string{"opencode": "a/only-in-a\r\n"},
	}
	p := newForTest(t, cli)
	p.SetStore(store.NewTestStore(t, []domain.Project{{ID: "proj-a", Path: dirA}, {ID: "proj-b", Path: dirB}}))

	enumFor := func(projectID string) []string {
		t.Helper()
		opts, err := p.SessionSchemas(context.Background(), provider.WorkstreamRef{ProjectID: projectID})
		if err != nil {
			t.Fatalf("SessionSchemas(%s): %v", projectID, err)
		}
		return modelProp(t, opts[0].Schema).Enum
	}
	if got := enumFor("proj-b"); got != nil {
		t.Errorf("project B model choices = %v before A was asked, want none: B has no container", got)
	}
	if got := enumFor("proj-a"); !slices.Equal(got, []string{"a/only-in-a"}) {
		t.Errorf("project A model choices = %v, want its own container's list", got)
	}
	if got := enumFor("proj-b"); got != nil {
		t.Errorf("project B model choices = %v after A was asked, want none rather than A's cached list", got)
	}
}

// opts0 re-reads the first option's schema, for an assertion after the loop above.
func opts0(t *testing.T, p *Provider) json.RawMessage {
	t.Helper()
	opts, err := p.SessionSchemas(context.Background(), provider.WorkstreamRef{ProjectID: "proj-1"})
	if err != nil {
		t.Fatalf("SessionSchemas: %v", err)
	}
	return opts[0].Schema
}
