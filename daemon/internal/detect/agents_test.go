package detect

import "testing"

func TestSupportedAgents(t *testing.T) {
	got := SupportedAgents()
	wantScreenAgents := []string{
		"amp", "agy", "claude", "cline", "codex", "copilot", "cursor",
		"devin", "droid", "gemini", "grok", "kiro", "letta",
		"maki", "muse", "qodercli", "qwen",
	}
	if len(got) != len(wantScreenAgents) {
		t.Fatalf("SupportedAgents returned %d agents, want %d screen agents: %+v", len(got), len(wantScreenAgents), got)
	}
	byLabel := make(map[string]AgentInfo, len(got))
	for _, a := range got {
		byLabel[a.Label] = a
	}

	// Every enabled manifest is screen-authoritative; hook-authoritative agents
	// need lifecycle integration HomeShell does not implement.
	for _, label := range wantScreenAgents {
		a, ok := byLabel[label]
		if !ok {
			t.Fatalf("SupportedAgents missing %q", label)
		}
		if a.HookAuthoritative {
			t.Errorf("%q must not be hook-authoritative", label)
		}
		if !Supported(label) {
			t.Errorf("Supported(%q) = false", label)
		}
	}

	// The claude-code alias resolves but is not listed as its own agent.
	if !Supported("claude-code") {
		t.Error("Supported(\"claude-code\") = false; alias should resolve")
	}
	if _, listed := byLabel["claude-code"]; listed {
		t.Error("alias claude-code should not appear as its own SupportedAgents entry")
	}

	if Supported("mastracode") {
		t.Error("mastracode has no screen manifest; must not be Supported")
	}

	// Vendoring herdr's complete catalog must not silently enable behavior for
	// agents HomeShell has not integrated. Hook-authoritative agents in
	// particular rely on lifecycle events that HomeShell does not receive.
	for _, label := range []string{"pi", "hermes", "opencode", "kilo", "kimi"} {
		if Supported(label) {
			t.Errorf("Supported(%q) = true; hook-authoritative agents need a HomeShell lifecycle integration", label)
		}
	}
}
