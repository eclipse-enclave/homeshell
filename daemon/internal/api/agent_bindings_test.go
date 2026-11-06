package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/eclipsesource/homeshell/daemon/internal/agentspec"
)

func TestAgentBindingsAPI(t *testing.T) {
	server := agentRowsServer(t)
	res := send(t, http.MethodPost, server.URL+"/api/agents", `{"id":"custom","binary":"wrapper","display":"Custom","ruleSetId":" claude ","enclaveTool":" team-claude "}`)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create = %d", res.StatusCode)
	}
	var created agentspec.Agent
	if err := json.NewDecoder(res.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.RuleSetID != "claude" || created.EnclaveTool != "team-claude" {
		t.Fatalf("created = %+v", created)
	}
	for _, payload := range []string{`{"display":"Renamed"}`, `{"ruleSetId":"codex","enclaveTool":"team-codex"}`, `{"ruleSetId":"","enclaveTool":""}`} {
		res := send(t, http.MethodPatch, server.URL+"/api/agents/custom", payload)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("patch %s = %d", payload, res.StatusCode)
		}
		var got agentspec.Agent
		if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		switch payload {
		case `{"display":"Renamed"}`:
			if got.RuleSetID != "claude" || got.EnclaveTool != "team-claude" {
				t.Fatalf("unrelated patch lost bindings: %+v", got)
			}
		case `{"ruleSetId":"codex","enclaveTool":"team-codex"}`:
			if got.RuleSetID != "codex" || got.EnclaveTool != "team-codex" {
				t.Fatalf("patch lost bindings: %+v", got)
			}
		default:
			if got.DetectionID() != "custom" || got.EnclaveToolName() != "custom" {
				t.Fatalf("reset = %+v", got)
			}
		}
	}
}
