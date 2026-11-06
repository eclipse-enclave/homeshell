package enclave

// exited reports whether a Docker container status (as `enclave ps` reports
// it) means the container's agent has ended.
func exited(dockerStatus string) bool {
	switch dockerStatus {
	case "exited", "dead", "removing":
		return true
	}
	return false
}

// detectionAgents snapshots authored tool-to-rule bindings once per capture
// pass. The agent catalog is not consulted again while classifying individual sessions.
func (p *Provider) detectionAgents() map[string]string {
	out := map[string]string{}
	for tool, agent := range p.agentsByTool() {
		out[tool] = agent.DetectionID()
	}
	return out
}
