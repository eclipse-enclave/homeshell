package enclave

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/eclipsesource/homeshell/daemon/internal/agentcatalog"
	"github.com/eclipsesource/homeshell/daemon/internal/agentspec"
	"github.com/eclipsesource/homeshell/daemon/internal/provider"
)

func TestCustomProfileUsesAgentSettingsRulesAndModelDiscovery(t *testing.T) {
	specs := agentcatalog.NewMemory(agentcatalog.Defaults())
	a, _ := specs.Agent("opencode")
	a.EnclaveTool = "team-opencode"
	a.RuleSetID = "codex"
	if err := specs.UpdateAgent(a); err != nil {
		t.Fatal(err)
	}
	const dir = "/tmp/custom-profile"
	cli := &fakeCLI{available: true, tools: []string{"team-opencode"}, containers: map[string][]Container{dir: {
		{Name: "container", Tool: "team-opencode", Directory: dir, Status: "running", SessionName: "hs-custom"},
	}}, execOutput: map[string]string{"team-opencode": "vendor/custom-model\n"}}
	p := storedProject(t, cli, "project", dir)
	p.SetAgentSpecs(specs)
	options, err := p.SessionSchemas(context.Background(), provider.WorkstreamRef{ProjectID: "project"})
	if err != nil {
		t.Fatal(err)
	}
	if len(options) != 1 || options[0].ID != "team-opencode" {
		t.Fatalf("options = %+v", options)
	}
	if got := modelProp(t, options[0].Schema).Enum; !slices.Equal(got, []string{"vendor/custom-model"}) {
		t.Fatalf("models = %v", got)
	}
	if cli.execCalls["team-opencode"] != 1 {
		t.Fatalf("model lookup used wrong profile: %v", cli.execCalls)
	}
	args, _ := p.deliver("team-opencode", json.RawMessage(`{"model":"vendor/custom-model"}`))
	if !slices.Equal(args, []string{"--model", "vendor/custom-model"}) {
		t.Fatalf("profile lost agent knobs: %v", args)
	}
	if got := p.detectionAgents()["team-opencode"]; got != "codex" {
		t.Fatalf("detection rule set = %q; want codex", got)
	}
	if _, ok := p.agentFor("opencode"); ok {
		t.Fatal("old implicit tool binding still active")
	}
	// A profile remap takes effect immediately; the launch executable stays put.
	a.EnclaveTool, a.RuleSetID = "other-profile", "claude"
	if err := specs.UpdateAgent(a); err != nil {
		t.Fatal(err)
	}
	if got := p.detectionAgents()["other-profile"]; got != "claude" {
		t.Fatalf("remapped rule set = %q; want claude", got)
	}
	cli.tools = []string{"other-profile"}
	cli.containers[dir][0].Tool = "other-profile"
	cli.execOutput["other-profile"] = "vendor/other-model\n"
	options, err = p.SessionSchemas(context.Background(), provider.WorkstreamRef{ProjectID: "project"})
	if err != nil {
		t.Fatal(err)
	}
	if len(options) != 1 || !slices.Equal(modelProp(t, options[0].Schema).Enum, []string{"vendor/other-model"}) {
		t.Fatalf("remapped profile reused cached models: %+v", options)
	}
	if got, ok := p.detectionAgents()["unlisted"]; ok {
		t.Fatalf("unlisted profile got rule set %q", got)
	}
}

type orderedAgentSource []agentspec.Agent

func (s orderedAgentSource) Agents() []agentspec.Agent { return s }
func (s orderedAgentSource) Agent(id string) (agentspec.Agent, bool) {
	for _, agent := range s {
		if agent.ID == id {
			return agent, true
		}
	}
	return agentspec.Agent{}, false
}

func TestConfigurationAndDetectionShareFirstMatchingToolBinding(t *testing.T) {
	explicit := agentspec.Agent{ID: "custom", EnclaveTool: "shared", RuleSetID: "codex"}
	implicit := agentspec.Agent{ID: "shared", RuleSetID: "claude"}
	for _, source := range []orderedAgentSource{{explicit, implicit}, {implicit, explicit}, nil} {
		p := &Provider{specs: source}
		configured, ok := p.agentFor("shared")
		detection, detected := p.detectionAgents()["shared"]
		if len(source) == 0 {
			if ok || detected {
				t.Fatal("missing tool binding was invented")
			}
			continue
		}
		if !ok || configured.ID != source[0].ID || !detected || detection != configured.DetectionID() {
			t.Fatalf("configuration and detection disagree: agent=%+v rule=%q source=%+v", configured, detection, source)
		}
		if _, ok := p.agentFor("unknown"); ok {
			t.Fatal("unknown tool got a binding")
		}
	}
}
