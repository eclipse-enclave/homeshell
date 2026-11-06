package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/eclipsesource/homeshell/daemon/internal/agentcatalog"
)

func listAgentModels(t *testing.T, server *httptest.Server) []agentcatalog.Model {
	t.Helper()
	res, err := http.Get(server.URL + "/api/agent-models")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	var body struct {
		Entries []agentcatalog.Model `json:"entries"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return body.Entries
}

// modelsFor is one agent's offered models, in the order the catalog serves them.
func modelsFor(entries []agentcatalog.Model, agent string) []string {
	var out []string
	for _, entry := range entries {
		if entry.Agent == agent {
			out = append(out, entry.Model)
		}
	}
	return out
}

// The catalog serves the seeded rows, grouped by agent and in each agent's own order —
// the order its Model field offers them in.
func TestListAgentModelsServesTheSeededCatalog(t *testing.T) {
	server := agentRowsServer(t)

	entries := listAgentModels(t, server)
	if got, want := modelsFor(entries, "claude"), []string{
		"claude-opus-5", "claude-sonnet-5", "claude-haiku-4-5", "claude-fable-5",
	}; !slices.Equal(got, want) {
		t.Errorf("claude models = %v, want the seeded catalog %v", got, want)
	}
	if got, want := modelsFor(entries, "codex"), []string{"gpt-5.6", "gpt-5.5", "gpt-5.4"}; !slices.Equal(got, want) {
		t.Errorf("codex models = %v, want %v", got, want)
	}
	// The agents whose CLI answers for itself are deliberately unseeded: what they
	// report beats any stored list.
	for _, agent := range []string{"opencode", "pi", "mistral-vibe"} {
		if got := modelsFor(entries, agent); got != nil {
			t.Errorf("%s models = %v, want none seeded", agent, got)
		}
	}
	for _, entry := range entries {
		if entry.ID == "" {
			t.Errorf("entry %+v has no id to address it by", entry)
		}
	}
}

// A model a vendor released is a row, which is the whole point of the move: it lands
// at the end of that agent's menu.
func TestCreateAgentModelAppendsToTheAgentsMenu(t *testing.T) {
	server := agentRowsServer(t)

	res := send(t, http.MethodPost, server.URL+"/api/agent-models",
		`{"agent":"claude","model":"claude-opus-6"}`)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", res.StatusCode)
	}
	var created agentcatalog.Model
	if err := json.NewDecoder(res.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.Agent != "claude" || created.Model != "claude-opus-6" {
		t.Errorf("created = %+v, want the entry as posted with an id", created)
	}
	models := modelsFor(listAgentModels(t, server), "claude")
	if last := models[len(models)-1]; last != "claude-opus-6" {
		t.Errorf("claude models = %v, want the new one last", models)
	}
}

// A model for an agent nothing describes would belong to no menu, so it is refused
// the way a setting for one is (POST /api/agent-settings).
func TestCreateAgentModelRefusesAnUnknownAgent(t *testing.T) {
	server := agentRowsServer(t)
	res := send(t, http.MethodPost, server.URL+"/api/agent-models",
		`{"agent":"aider","model":"whatever"}`)
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for an agent with no description", res.StatusCode)
	}
}

// Deleting an agent takes its models with it, as it takes its settings.
func TestDeleteAgentRemovesItsModels(t *testing.T) {
	server := agentRowsServer(t)
	if res := send(t, http.MethodDelete, server.URL+"/api/agents/claude", ""); res.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", res.StatusCode)
	}
	if got := modelsFor(listAgentModels(t, server), "claude"); len(got) != 0 {
		t.Errorf("claude models = %v, want none left behind", got)
	}
}

// Offering the same model twice would put it in the menu twice.
func TestCreateAgentModelRefusesADuplicate(t *testing.T) {
	server := agentRowsServer(t)
	res := send(t, http.MethodPost, server.URL+"/api/agent-models",
		`{"agent":"claude","model":"claude-opus-5"}`)
	if res.StatusCode != http.StatusConflict {
		t.Errorf("status = %d, want 409", res.StatusCode)
	}
}

func TestCreateAgentModelRequiresBothFields(t *testing.T) {
	server := agentRowsServer(t)

	res := send(t, http.MethodPost, server.URL+"/api/agent-models", `{"agent":"claude"}`)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", res.StatusCode)
	}
	var body struct {
		Fields map[string]string `json:"fields"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Fields["/model"] == "" {
		t.Errorf("fields = %v, want a message for /model", body.Fields)
	}
}

// Correcting a model id must not move it: a typo fix is not a reordering.
func TestUpdateAgentModelKeepsItsPlace(t *testing.T) {
	server := agentRowsServer(t)

	res := send(t, http.MethodPatch, server.URL+"/api/agent-models/agent-model-claude-sonnet-5",
		`{"model":"claude-sonnet-5-1"}`)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	// The response is the stored row, agent included, not an echo of the body.
	var updated agentcatalog.Model
	if err := json.NewDecoder(res.Body).Decode(&updated); err != nil {
		t.Fatal(err)
	}
	if want := (agentcatalog.Model{ID: "agent-model-claude-sonnet-5", Agent: "claude", Model: "claude-sonnet-5-1"}); updated != want {
		t.Errorf("PATCH response = %+v, want %+v", updated, want)
	}
	if got, want := modelsFor(listAgentModels(t, server), "claude"), []string{
		"claude-opus-5", "claude-sonnet-5-1", "claude-haiku-4-5", "claude-fable-5",
	}; !slices.Equal(got, want) {
		t.Errorf("claude models = %v, want the correction in place %v", got, want)
	}
}

func TestUpdateAgentModelReportsMissingAndColliding(t *testing.T) {
	server := agentRowsServer(t)

	if res := send(t, http.MethodPatch, server.URL+"/api/agent-models/nope",
		`{"model":"x"}`); res.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", res.StatusCode)
	}
	// Renaming one onto a model that agent already offers is the same duplicate.
	if res := send(t, http.MethodPatch, server.URL+"/api/agent-models/agent-model-claude-sonnet-5",
		`{"model":"claude-opus-5"}`); res.StatusCode != http.StatusConflict {
		t.Errorf("status = %d, want 409", res.StatusCode)
	}
	if res := send(t, http.MethodPatch, server.URL+"/api/agent-models/agent-model-claude-sonnet-5",
		`{"model":"   "}`); res.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for a blank model", res.StatusCode)
	}
}

// A model the user removed stays removed — including across a restart, which is why
// the seed is a migration rather than something the daemon writes at startup.
func TestDeleteAgentModel(t *testing.T) {
	server := agentRowsServer(t)

	res := send(t, http.MethodDelete, server.URL+"/api/agent-models/agent-model-claude-fable-5", "")
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", res.StatusCode)
	}
	if got := modelsFor(listAgentModels(t, server), "claude"); slices.Contains(got, "claude-fable-5") {
		t.Errorf("claude models = %v, want the deleted one gone", got)
	}
	if res := send(t, http.MethodDelete,
		server.URL+"/api/agent-models/agent-model-claude-fable-5", ""); res.StatusCode != http.StatusNotFound {
		t.Errorf("deleting it twice = %d, want 404", res.StatusCode)
	}
}
