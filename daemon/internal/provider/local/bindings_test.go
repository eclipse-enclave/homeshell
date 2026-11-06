package local

import (
	"context"
	"testing"

	"github.com/eclipsesource/homeshell/daemon/internal/agentspec"
)

func TestCaptureChoosesRulesIndependentlyOfLaunchBinary(t *testing.T) {
	for _, tt := range []struct {
		name  string
		agent agentspec.Agent
		want  string
	}{
		{"standard renamed binary", agentspec.Agent{ID: "cursor", Binary: "/opt/renamed-cursor"}, "cursor"},
		{"custom wrapper", agentspec.Agent{ID: "custom-claude", Binary: "wrapper", RuleSetID: "claude"}, "claude"},
		{"custom without rules", agentspec.Agent{ID: "custom", Binary: "wrapper"}, "custom"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			runtime := newFakeRuntime()
			runtime.alive["target"] = true
			p := New(runtime, &fakeStore{})
			snap := sessionSnapshot{id: "session", agentID: tt.agent.ID, agent: tt.agent.Binary, target: "target"}
			p.captureSession(context.Background(), snap, agentIndex{tt.agent})
			if runtime.stateAgent != tt.want {
				t.Fatalf("detection used %q, want %q", runtime.stateAgent, tt.want)
			}
		})
	}
}
