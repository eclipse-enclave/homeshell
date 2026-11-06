package agentspec

import "testing"

func TestAgentBindings(t *testing.T) {
	for _, tt := range []struct {
		name        string
		agent       Agent
		rules, tool string
	}{
		{"defaults use identity, not binary", Agent{ID: "cursor", Binary: "cursor-agent"}, "cursor", "cursor"},
		{"custom wrapper and profile", Agent{ID: "my-custom-claude", Binary: "/opt/wrappers/claude", RuleSetID: "claude", EnclaveTool: "team-claude"}, "claude", "team-claude"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.agent.DetectionID(); got != tt.rules {
				t.Errorf("DetectionID = %q, want %q", got, tt.rules)
			}
			if got := tt.agent.EnclaveToolName(); got != tt.tool {
				t.Errorf("EnclaveToolName = %q, want %q", got, tt.tool)
			}
		})
	}
}
