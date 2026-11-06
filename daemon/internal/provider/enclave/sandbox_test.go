//go:build dev

package enclave

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
	"github.com/eclipsesource/homeshell/daemon/internal/provider"
	"github.com/eclipsesource/homeshell/daemon/internal/provider/enclave/sandboxschema"
	"github.com/eclipsesource/homeshell/daemon/internal/store"
)

// testBase is an Enclave whose config already lists something in every list a
// sandbox can extend, so each test can tell "extended" from "replaced".
func testBase() sandboxschema.Base {
	return sandboxschema.Base{
		Config: sandboxschema.EffectiveConfig{
			"features":          option(`["devtools","node-dev"]`, "default"),
			"add_dirs":          option(`["/work/shared"]`, "global"),
			"add_readonly_dirs": option(`["/work/docs"]`, "global"),
			"ports":             option(`["3000"]`, "project"),
			"bridge_ports":      option(`["5432"]`, "global"),
			"allow_domains":     option(`["api.example.com"]`, "global"),
			"host_config":       option(`"none"`, "default"),
		},
		Features: []sandboxschema.Feature{
			{Name: "devtools", Description: "Core development tools"},
			{Name: "node-dev", Description: "Node.js development tools"},
			{Name: "playwright", DisplayName: "Playwright", Description: "Playwright browsers"},
		},
	}
}

// option is an option whose value `enclave config --json` renders as value.
func option(value, source string) sandboxschema.ConfigOption {
	return sandboxschema.ConfigOption{Value: json.RawMessage(value), Source: source}
}

func ptr[T any](value T) *T { return &value }

func TestSandboxFlags(t *testing.T) {
	tests := []struct {
		name   string
		choice sandboxChoice
		want   []string
	}{
		{name: "nothing chosen", choice: sandboxChoice{}, want: nil},
		{
			name: "everything at Enclave's values",
			choice: sandboxChoice{
				Features: ptr([]string{"node-dev", "devtools"}), HostConfig: ptr(false),
				Dirs: []sandboxschema.Dir{}, Ports: []string{}, BridgePorts: []int{}, AllowDomains: []string{},
			},
			want: nil,
		},
		{
			name:   "a feature added states the whole set",
			choice: sandboxChoice{Features: ptr([]string{"devtools", "node-dev", "playwright"})},
			want:   []string{"--features", "devtools,node-dev,playwright"},
		},
		{
			name:   "every feature off",
			choice: sandboxChoice{Features: ptr([]string{})},
			want:   []string{"--features", "none"},
		},
		{
			name:   "a domain added keeps the configured ones",
			choice: sandboxChoice{AllowDomains: []string{"cdn.example.com"}},
			want:   []string{"--allow-domain", "api.example.com", "--allow-domain", "cdn.example.com"},
		},
		{
			name:   "an extra that is already configured adds nothing",
			choice: sandboxChoice{AllowDomains: []string{"api.example.com", " "}},
			want:   nil,
		},
		{
			name:   "a read-only dir extends only the read-only list",
			choice: sandboxChoice{Dirs: []sandboxschema.Dir{{Path: "/data", Readonly: ptr(true)}, {Path: "  "}}},
			want:   []string{"--add-readonly-dir", "/work/docs", "--add-readonly-dir", "/data"},
		},
		{
			name:   "a dir that does not say is read-only, the schema's default",
			choice: sandboxChoice{Dirs: []sandboxschema.Dir{{Path: "/data"}}},
			want:   []string{"--add-readonly-dir", "/work/docs", "--add-readonly-dir", "/data"},
		},
		{
			name:   "a writable dir extends only the writable list",
			choice: sandboxChoice{Dirs: []sandboxschema.Dir{{Path: "/scratch", Readonly: ptr(false)}}},
			want:   []string{"--add-dir", "/work/shared", "--add-dir", "/scratch"},
		},
		{
			name:   "ports in both directions",
			choice: sandboxChoice{Ports: []string{"8080:80"}, BridgePorts: []int{6379}},
			want:   []string{"-p", "3000", "-p", "8080:80", "--bridge-port", "5432", "--bridge-port", "6379"},
		},
		{
			name:   "host config passed through",
			choice: sandboxChoice{HostConfig: ptr(true)},
			want:   []string{"--host-config", "passthrough"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sandboxFlags(tt.choice, testBase()); !slices.Equal(got, tt.want) {
				t.Errorf("sandboxFlags = %q; want %q", got, tt.want)
			}
		})
	}
}

// What the config turns on, a session turns off explicitly.
func TestSandboxFlagsAgainstAConfigThatTurnsThingsOn(t *testing.T) {
	base := testBase()
	base.Config["host_config"] = option(`"passthrough"`, "global")
	got := sandboxFlags(sandboxChoice{HostConfig: ptr(false)}, base)
	if want := []string{"--host-config", "none"}; !slices.Equal(got, want) {
		t.Errorf("sandboxFlags = %q; want %q", got, want)
	}
}

// The features Enclave resolves for the tool produce no flag, in any order.
func TestSandboxFlagsAgainstTheToolsFeatureSelection(t *testing.T) {
	base := testBase()
	base.Config["features"] = option(`["devtools","node-dev","playwright"]`, "tool_override")
	if got := sandboxFlags(sandboxChoice{Features: ptr([]string{"playwright", "devtools", "node-dev"})}, base); got != nil {
		t.Errorf("sandboxFlags for the tool's own selection = %q; want none", got)
	}
	if got, want := sandboxFlags(sandboxChoice{Features: ptr([]string{"devtools", "node-dev"})}, base), []string{"--features", "devtools,node-dev"}; !slices.Equal(got, want) {
		t.Errorf("sandboxFlags without one = %q; want %q", got, want)
	}
}

func TestSandboxOf(t *testing.T) {
	if _, ok := sandboxOf(nil); ok {
		t.Error("no config: want no sandbox")
	}
	if _, ok := sandboxOf(json.RawMessage(`{"model":"opus"}`)); ok {
		t.Error("a config without the group: want no sandbox")
	}
	choice, ok := sandboxOf(json.RawMessage(`{"enclave":{"features":["devtools"],"bridgePorts":[5432],"hostConfig":true}}`))
	if !ok || choice.Features == nil || !slices.Equal(*choice.Features, []string{"devtools"}) ||
		!slices.Equal(choice.BridgePorts, []int{5432}) || choice.HostConfig == nil || !*choice.HostConfig {
		t.Errorf("sandboxOf = %+v, %v", choice, ok)
	}
}

func TestAnswerCache(t *testing.T) {
	var cache answerCache[string]
	loads := 0
	answer, failure := "first", error(nil)
	load := func(context.Context) (string, error) { loads++; return answer, failure }

	if got, err := cache.get(context.Background(), "key", load); got != "first" || err != nil || loads != 1 {
		t.Fatalf("get = %q, %v after %d loads; want the first answer, loaded once", got, err, loads)
	}
	answer = "second"
	if got, _ := cache.get(context.Background(), "key", load); got != "first" || loads != 1 {
		t.Errorf("get = %q after %d loads; want the fresh answer reused", got, loads)
	}
	age(&cache, "key", sandboxBaseTTL)
	if got, _ := cache.get(context.Background(), "key", load); got != "second" || loads != 2 {
		t.Errorf("get = %q after %d loads; want an expired answer asked again", got, loads)
	}

	failure = errors.New("enclave config: exit status 1")
	if _, err := cache.get(context.Background(), "broken", load); err == nil || loads != 3 {
		t.Fatalf("get = %v after %d loads; want the failure", err, loads)
	}
	if _, err := cache.get(context.Background(), "broken", load); err == nil || loads != 3 {
		t.Errorf("get = %v after %d loads; want the failure remembered for a moment", err, loads)
	}
	age(&cache, "broken", failedAnswerTTL)
	failure = nil
	if got, err := cache.get(context.Background(), "broken", load); err != nil || got != "second" || loads != 4 {
		t.Errorf("get = %q, %v after %d loads; want a failure forgotten sooner than an answer", got, err, loads)
	}
}

// age makes a cached answer older by d.
func age[T any](cache *answerCache[T], key string, d time.Duration) {
	answer := cache.entries[key]
	answer.at = answer.at.Add(-d)
	cache.entries[key] = answer
}

// Enclave's answer is shared, so a caller that goes away mid-question — a form
// request the client abandoned — does not fail it, nor have the failure
// remembered for the next caller.
func TestSandboxBaseOutlivesACancelledCaller(t *testing.T) {
	cli := &fakeCLI{available: true}
	p := sandboxProvider(t, cli)
	abandoned, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.sandboxBase(abandoned, "/tmp/project/wt", "claude"); err != nil {
		t.Errorf("sandboxschema.Base for a cancelled caller: %v; want Enclave asked regardless", err)
	}
	if _, err := p.sandboxBase(context.Background(), "/tmp/project/wt", "claude"); err != nil {
		t.Errorf("sandboxschema.Base after a cancelled caller: %v; want Enclave's answer", err)
	}
}

// schemaProps decodes a schema's properties, and their names in document order.
func schemaProps(t *testing.T, schema json.RawMessage) (names []string, props map[string]map[string]any) {
	t.Helper()
	var top struct {
		Properties json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(schema, &top); err != nil {
		t.Fatalf("schema is not an object: %v\n%s", err, schema)
	}
	if err := json.Unmarshal(top.Properties, &props); err != nil {
		t.Fatalf("reading properties: %v", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(top.Properties))
	for depth := 0; ; {
		token, err := decoder.Token()
		if err != nil {
			break
		}
		switch token {
		case json.Delim('{'), json.Delim('['):
			depth++
		case json.Delim('}'), json.Delim(']'):
			depth--
		default:
			if name, ok := token.(string); ok && depth == 1 {
				names = append(names, name)
				var skip json.RawMessage
				_ = decoder.Decode(&skip)
			}
		}
	}
	return names, props
}

// sandboxProvider is a provider over a store holding one workstream, whose
// Enclave reports testBase for every tool.
func sandboxProvider(t *testing.T, cli *fakeCLI) *Provider {
	t.Helper()
	base := testBase()
	cli.config = map[string]sandboxschema.EffectiveConfig{"claude": base.Config, "codex": base.Config}
	cli.features = base.Features
	p := newForTest(t, cli)
	p.SetStore(store.NewTestStore(t, []domain.Project{{
		ID: "proj-1", Path: "/tmp/project",
		Workstreams: []domain.Workstream{{ID: "ws1", Name: "ws", Provider: "core", StrategyID: "in-place", WorkingDir: "/tmp/project/wt"}},
	}}))
	return p
}

func TestSessionSchemasCarryTheSandboxGroup(t *testing.T) {
	cli := &fakeCLI{available: true, tools: []string{"claude", "codex"}}
	p := sandboxProvider(t, cli)
	ref := provider.WorkstreamRef{ProjectID: "proj-1", WorkstreamID: "ws1"}

	options, err := p.SessionSchemas(context.Background(), ref)
	if err != nil {
		t.Fatalf("SessionSchemas: %v", err)
	}
	for _, option := range options {
		names, _ := schemaProps(t, option.Schema)
		if names[len(names)-1] != sandboxschema.Key {
			t.Errorf("%s: properties = %q; want the sandbox group last", option.ID, names)
		}
	}
	if cli.featureCalls != 1 {
		t.Errorf("enclave features list asked %d times; want once for every tool", cli.featureCalls)
	}
	// The form opens again, then the create follows: Enclave is asked once.
	calls := cli.configCalls
	if _, err := p.SessionSchemas(context.Background(), ref); err != nil {
		t.Fatalf("SessionSchemas: %v", err)
	}
	if cli.configCalls != calls {
		t.Errorf("enclave config asked %d more times; want the answer remembered", cli.configCalls-calls)
	}
}

// An Enclave that cannot report its settings keeps the agent's own settings, and
// a sandbox group with no fields that says so.
func TestSessionSchemasWithoutEnclaveSettings(t *testing.T) {
	cli := &fakeCLI{available: true, tools: []string{"codex"}}
	p := sandboxProvider(t, cli)
	cli.configErr = errors.New("unknown command \"config\"")

	options, err := p.SessionSchemas(context.Background(), provider.WorkstreamRef{ProjectID: "proj-1", WorkstreamID: "ws1"})
	if err != nil {
		t.Fatalf("SessionSchemas: %v", err)
	}
	names, props := schemaProps(t, options[0].Schema)
	if !slices.Contains(names, "model") {
		t.Errorf("properties = %q; want the agent's own settings kept", names)
	}
	group := props[sandboxschema.Key]
	fields, _ := group["properties"].(map[string]any)
	description, _ := group["description"].(string)
	title, _ := group["title"].(string)
	if group == nil || len(fields) != 0 || !strings.Contains(description, "did not report") || !strings.Contains(title, "did not report") {
		t.Errorf("sandbox group = %v; want no fields, and a title and description saying why", group)
	}
}

// A config whose sandbox group Enclave's settings were not read for is refused as
// Enclave being unavailable — not as unknown settings, nor the user's mistake —
// and one that sets nothing starts as Enclave's own.
func TestSandboxFlagsForWithoutEnclaveSettings(t *testing.T) {
	cli := &fakeCLI{available: true}
	p := sandboxProvider(t, cli)
	cli.configErr = errors.New("unknown command \"config\"")

	_, err := p.sandboxFlagsFor(context.Background(), "/tmp/project/wt", "claude", json.RawMessage(`{"enclave":{"allowDomains":["cdn.example.com"]}}`))
	if !errors.Is(err, provider.ErrProviderUnavailable) || provider.FieldErrors(err) != nil || !strings.Contains(err.Error(), "unknown command") {
		t.Errorf("sandboxFlagsFor = %v; want the group refused for Enclave's settings being unreadable", err)
	}
	if flags, err := p.sandboxFlagsFor(context.Background(), "/tmp/project/wt", "claude", json.RawMessage(`{"enclave":{}}`)); err != nil || flags != nil {
		t.Errorf("sandboxFlagsFor(empty group) = %q, %v; want no flags", flags, err)
	}
}

// Flags are made only from a group that fits the schema Enclave's answer makes,
// whether or not the service could check it first.
func TestSandboxFlagsForValidatesTheGroup(t *testing.T) {
	cli := &fakeCLI{available: true}
	p := sandboxProvider(t, cli)
	_, err := p.sandboxFlagsFor(context.Background(), "/tmp/project/wt", "claude", json.RawMessage(`{"enclave":{"allowDomains":["--privileged"]}}`))
	var invalid *provider.ValidationError
	if !errors.As(err, &invalid) {
		t.Fatalf("sandboxFlagsFor = %v; want a ValidationError", err)
	}
	if _, ok := invalid.Fields["/enclave/allowDomains/0"]; !ok {
		t.Errorf("fields = %v; want the domain named", invalid.Fields)
	}
}

// A session recorded with playwrightMcp, from before Enclave 1.0 dropped
// `--playwright-mcp` for its playwright-mcp feature, starts without the flag
// rather than failing on it; the setting is dropped, not mapped to the feature.
func TestSandboxFlagsForIgnoresPlaywrightMCP(t *testing.T) {
	cli := &fakeCLI{available: true}
	p := sandboxProvider(t, cli)
	tests := []struct {
		config string
		want   []string
	}{
		{`{"enclave":{"playwrightMcp":true}}`, nil},
		{`{"enclave":{"playwrightMcp":true,"allowDomains":["cdn.example.com"]}}`, []string{"--allow-domain", "api.example.com", "--allow-domain", "cdn.example.com"}},
	}
	for _, tt := range tests {
		flags, err := p.sandboxFlagsFor(context.Background(), "/tmp/project/wt", "claude", json.RawMessage(tt.config))
		if err != nil || !slices.Equal(flags, tt.want) {
			t.Errorf("sandboxFlagsFor(%s) = %q, %v; want %q", tt.config, flags, err, tt.want)
		}
	}
}

func TestCreateSessionPassesTheSandboxAsFlags(t *testing.T) {
	cli := &fakeCLI{available: true, runResult: "enclave-claude-abc123abc123-hs-1"}
	p := sandboxProvider(t, cli)
	_, err := p.CreateSession(context.Background(), provider.CreateSessionRequest{
		Workstream: provider.WorkstreamRef{ProjectID: "proj-1", WorkstreamID: "ws1"},
		WorkingDir: "/tmp/project/wt",
		OptionID:   "claude",
		Config:     json.RawMessage(`{"enclave":{"features":["devtools","node-dev","playwright"],"allowDomains":["cdn.example.com"]}}`),
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	want := []string{"--features", "devtools,node-dev,playwright", "--allow-domain", "api.example.com", "--allow-domain", "cdn.example.com"}
	if got := cli.runCalls[0].opts.ExtraFlags; !slices.Equal(got, want) {
		t.Errorf("ExtraFlags = %q; want %q", got, want)
	}
}

// Without Enclave's own values a sandbox cannot be applied faithfully — a list
// flag would drop what its config lists — so the start fails rather than guess.
func TestCreateSessionRefusesASandboxItCannotApply(t *testing.T) {
	cli := &fakeCLI{available: true, runResult: "enclave-claude-x"}
	p := sandboxProvider(t, cli)
	cli.configErr = errors.New("enclave config: exit status 1")
	_, err := p.CreateSession(context.Background(), provider.CreateSessionRequest{
		Workstream: provider.WorkstreamRef{ProjectID: "proj-1", WorkstreamID: "ws1"},
		WorkingDir: "/tmp/project/wt",
		OptionID:   "claude",
		Config:     json.RawMessage(`{"enclave":{"allowDomains":["cdn.example.com"]}}`),
	})
	if err == nil {
		t.Fatal("CreateSession: want an error")
	}
	if len(cli.runCalls) != 0 {
		t.Error("the container was started anyway")
	}
}

// One tool's schema is the one SessionSchemas lists for it; a tool Enclave does
// not list is refused rather than given a form.
func TestSessionSchemaOfOneTool(t *testing.T) {
	cli := &fakeCLI{available: true, tools: []string{"claude", "codex"}}
	p := sandboxProvider(t, cli)
	ref := provider.WorkstreamRef{ProjectID: "proj-1", WorkstreamID: "ws1"}

	options, err := p.SessionSchemas(context.Background(), ref)
	if err != nil {
		t.Fatalf("SessionSchemas: %v", err)
	}
	schema, err := p.SessionSchema(context.Background(), ref, "codex")
	if err != nil {
		t.Fatalf("SessionSchema: %v", err)
	}
	if want := options[slices.IndexFunc(options, func(option provider.SessionSchemaOption) bool { return option.ID == "codex" })].Schema; string(schema) != string(want) {
		t.Errorf("SessionSchema(codex) = %s; want the listed %s", schema, want)
	}
	if _, err := p.SessionSchema(context.Background(), ref, "retired"); !errors.Is(err, provider.ErrOptionNotOffered) {
		t.Errorf("SessionSchema of a tool Enclave does not list = %v; want ErrOptionNotOffered", err)
	}
}

// One tool's schema is what a restart form is built from, so an Enclave that
// cannot report its settings refuses it rather than offer a form without them.
func TestSessionSchemaWithoutEnclaveSettings(t *testing.T) {
	cli := &fakeCLI{available: true, tools: []string{"claude"}}
	p := sandboxProvider(t, cli)
	cli.configErr = errors.New("enclave config: exit status 1")
	if schema, err := p.SessionSchema(context.Background(), provider.WorkstreamRef{ProjectID: "proj-1", WorkstreamID: "ws1"}, "claude"); err == nil {
		t.Errorf("SessionSchema = %s; want an error", schema)
	}
}

// An Enclave that cannot list its tools is unavailable, not refusing the tool.
func TestSessionSchemaWhenEnclaveCannotListItsTools(t *testing.T) {
	cli := &fakeCLI{available: true, toolsErr: errors.New("enclave tools: exit status 1")}
	p := sandboxProvider(t, cli)
	_, err := p.SessionSchema(context.Background(), provider.WorkstreamRef{ProjectID: "proj-1", WorkstreamID: "ws1"}, "codex")
	if !errors.Is(err, provider.ErrProviderUnavailable) || errors.Is(err, provider.ErrOptionNotOffered) {
		t.Errorf("SessionSchema = %v; want ErrProviderUnavailable", err)
	}
}
