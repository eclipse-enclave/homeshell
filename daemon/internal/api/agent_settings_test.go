package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/eclipsesource/homeshell/daemon/internal/agentcatalog"
	"github.com/eclipsesource/homeshell/daemon/internal/agentspec"
	"github.com/eclipsesource/homeshell/daemon/internal/notify"
)

// agentRowsServer serves both row resources over one in-memory catalog: the
// descriptions and the models, which is how the daemon wires them.
func agentRowsServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(handlerForTest(Deps{
		AgentCatalog:  agentcatalog.NewMemory(agentcatalog.Defaults()),
		Notifications: notify.NewHub(),
	}))
	t.Cleanup(server.Close)
	return server
}

func listAgents(t *testing.T, server *httptest.Server) []agentspec.Agent {
	t.Helper()
	res, err := http.Get(server.URL + "/api/agents")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	var body struct {
		Agents []agentspec.Agent `json:"agents"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return body.Agents
}

func send(t *testing.T, method, url, payload string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, bytes.NewBufferString(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { res.Body.Close() })
	return res
}

// The listing is the whole picture in one request: the agents in selector order, each
// with the settings its create form shows, in form order.
func TestListAgentsServesTheSeededDescriptions(t *testing.T) {
	server := agentRowsServer(t)

	agents := listAgents(t, server)
	var ids []string
	for _, a := range agents {
		ids = append(ids, a.ID)
	}
	want := []string{"claude", "codex", "gemini", "opencode", "pi", "mistral-vibe", "amp", "agy", "cline", "copilot", "cursor", "devin", "droid", "grok", "kiro", "letta", "maki", "muse", "qodercli", "qwen"}

	if !slices.Equal(ids, want) {
		t.Fatalf("agents = %v, want %v", ids, want)
	}
	var claude agentspec.Agent
	for _, agent := range agents {
		if agent.ID == "claude" {
			claude = agent
			break
		}
	}
	var fields []string
	for _, s := range claude.Settings {
		fields = append(fields, s.Field)
	}
	if !slices.Equal(fields, []string{"model", "effort", "permissionMode", "title", "prompt"}) {
		t.Errorf("claude settings = %v, want the seeded rows in form order", fields)
	}
	if !claude.Transcript || claude.ResumeFlag != "--resume" {
		t.Errorf("claude = %+v, want the transcript and resume facts", claude)
	}
}

// Adding an agent is what the move bought: a new coding CLI is a row, not a release.
func TestCreateAgentAndItsSettings(t *testing.T) {
	server := agentRowsServer(t)

	res := send(t, http.MethodPost, server.URL+"/api/agents",
		`{"id":"aider","binary":"aider","display":"Aider","listModels":["--models"]}`)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", res.StatusCode)
	}
	var created agentspec.Agent
	if err := json.NewDecoder(res.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.ID != "aider" || !slices.Equal(created.ListModels, []string{"--models"}) {
		t.Errorf("created = %+v, want the agent as posted", created)
	}
	if created.Settings == nil {
		t.Error("settings = null, want an empty array")
	}

	res = send(t, http.MethodPost, server.URL+"/api/agent-settings",
		`{"agent":"aider","field":"model","role":"model","title":"Model",`+
			`"delivery":"flag","deliveryArg":"--model"}`)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", res.StatusCode)
	}
	var setting agentspec.Setting
	if err := json.NewDecoder(res.Body).Decode(&setting); err != nil {
		t.Fatal(err)
	}
	// A body that says nothing about the type gets the string default, not a type the
	// store would refuse.
	if setting.Type != agentspec.TypeString {
		t.Errorf("type = %q, want %q by default", setting.Type, agentspec.TypeString)
	}

	// The new agent is immediately a launchable description: it generates a form and a
	// launch, which is what the providers read.
	for _, a := range listAgents(t, server) {
		if a.ID != "aider" {
			continue
		}
		if len(agentspec.Schema(a, []string{"m1"}, agentspec.Host)) == 0 {
			t.Error("the new agent generates no session schema")
		}
		cmd := agentspec.Deliver(a, agentspec.Launch{Model: "m1"}, agentspec.Host)
		if !slices.Equal(cmd.Args, []string{"--model", "m1"}) {
			t.Errorf("args = %v, want the model delivered by the new row", cmd.Args)
		}
		return
	}
	t.Error("the new agent is not in the listing")
}

// The required fields are required, and the response blames the box rather than the
// request as a whole.
func TestCreateAgentRequiresTheLaunchFacts(t *testing.T) {
	server := agentRowsServer(t)

	res := send(t, http.MethodPost, server.URL+"/api/agents", `{"display":"No id"}`)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", res.StatusCode)
	}
	var body struct {
		Fields map[string]string `json:"fields"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"/id", "/binary"} {
		if body.Fields[field] == "" {
			t.Errorf("fields = %v, want a message for %s", body.Fields, field)
		}
	}
}

// A taken id is a conflict, not a server error: the request is well-formed and the
// state refuses it.
func TestCreateAgentReportsATakenID(t *testing.T) {
	server := agentRowsServer(t)
	res := send(t, http.MethodPost, server.URL+"/api/agents",
		`{"id":"claude","binary":"claude","display":"Mine"}`)
	if res.StatusCode != http.StatusConflict {
		t.Errorf("status = %d, want 409", res.StatusCode)
	}
}

// A PATCH keeps what it does not mention, so fixing a display name does not require
// restating how the agent resumes.
func TestUpdateAgentKeepsUnmentionedFields(t *testing.T) {
	server := agentRowsServer(t)

	res := send(t, http.MethodPatch, server.URL+"/api/agents/claude", `{"display":"Claude"}`)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	var updated agentspec.Agent
	if err := json.NewDecoder(res.Body).Decode(&updated); err != nil {
		t.Fatal(err)
	}
	if updated.Display != "Claude" {
		t.Errorf("display = %q, want the edit", updated.Display)
	}
	if updated.ResumeFlag != "--resume" || !updated.Transcript || updated.Binary != "claude" {
		t.Errorf("updated = %+v, want the unmentioned facts kept", updated)
	}
	if len(updated.Settings) == 0 {
		t.Error("the response dropped the agent's settings")
	}
}

// Blanking a field the daemon needs is a per-field 400, and an unknown agent is a 404.
func TestUpdateAgentRefusesBlanksAndUnknownIDs(t *testing.T) {
	server := agentRowsServer(t)

	if res := send(t, http.MethodPatch, server.URL+"/api/agents/claude", `{"binary":"  "}`); res.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for a blanked binary", res.StatusCode)
	}
	if res := send(t, http.MethodPatch, server.URL+"/api/agents/nope", `{"display":"X"}`); res.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404 for an unknown agent", res.StatusCode)
	}
}

// Deleting an agent takes its knobs with it — they would have nowhere to appear.
func TestDeleteAgentTakesItsSettings(t *testing.T) {
	server := agentRowsServer(t)

	res := send(t, http.MethodDelete, server.URL+"/api/agents/claude", "")
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", res.StatusCode)
	}
	for _, a := range listAgents(t, server) {
		if a.ID == "claude" {
			t.Fatal("claude survived the delete")
		}
	}
	// Its settings are addressable no more.
	if res := send(t, http.MethodPatch,
		server.URL+"/api/agent-settings/agent-setting-claude-effort", `{"title":"X"}`); res.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404 for a setting whose agent is gone", res.StatusCode)
	}
	if res := send(t, http.MethodDelete, server.URL+"/api/agents/claude", ""); res.StatusCode != http.StatusNotFound {
		t.Errorf("deleting it twice = %d, want 404", res.StatusCode)
	}
}

// One knob is what a client editing a single field addresses, and an edit must not
// move the field or hand it to another agent.
func TestUpdateAgentSetting(t *testing.T) {
	server := agentRowsServer(t)

	res := send(t, http.MethodPatch, server.URL+"/api/agent-settings/agent-setting-claude-effort",
		`{"title":"Thinking budget","choices":["low","max"],"agent":"codex"}`)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	var updated agentspec.Setting
	if err := json.NewDecoder(res.Body).Decode(&updated); err != nil {
		t.Fatal(err)
	}
	if updated.Title != "Thinking budget" || !slices.Equal(updated.Choices, []string{"low", "max"}) {
		t.Errorf("updated = %+v, want the edit", updated)
	}
	if updated.Agent != "claude" {
		t.Errorf("agent = %q; a knob may not change agents", updated.Agent)
	}
	// The flag it delivers through is untouched by an edit that did not mention it.
	if updated.Delivery != agentspec.Flag || updated.DeliveryArg != "--effort" {
		t.Errorf("delivery = %q %q, want the unmentioned delivery kept", updated.Delivery, updated.DeliveryArg)
	}
	// And it is still where it was in claude's form.
	for _, a := range listAgents(t, server) {
		if a.ID != "claude" {
			continue
		}
		if a.Settings[1].Field != "effort" {
			t.Errorf("claude's form = %+v, want effort still second", a.Settings)
		}
	}
}

// A row nothing could act on is the client's fault, not the daemon's: it would be a
// field the form shows and the launch drops.
func TestAgentSettingRefusesWhatNothingCouldActOn(t *testing.T) {
	server := agentRowsServer(t)

	for _, tt := range []struct {
		name, payload string
		wantStatus    int
	}{
		{"an unknown delivery", `{"agent":"claude","field":"x","title":"X","delivery":"telepathy"}`, http.StatusBadRequest},
		{"a flag with no flag", `{"agent":"claude","field":"x","title":"X","delivery":"flag"}`, http.StatusBadRequest},
		{"an unknown value type", `{"agent":"claude","field":"x","title":"X","type":"number","delivery":"positional"}`, http.StatusBadRequest},
		{"no agent", `{"field":"x","title":"X","delivery":"positional"}`, http.StatusBadRequest},
		{"an agent nothing describes", `{"agent":"ghost","field":"x","title":"X","delivery":"positional"}`, http.StatusBadRequest},
		{"a field that agent already has", `{"agent":"claude","field":"model","title":"X","delivery":"positional"}`, http.StatusConflict},
		{"not JSON at all", `{`, http.StatusBadRequest},
	} {
		t.Run(tt.name, func(t *testing.T) {
			res := send(t, http.MethodPost, server.URL+"/api/agent-settings", tt.payload)
			if res.StatusCode != tt.wantStatus {
				t.Errorf("status = %d, want %d", res.StatusCode, tt.wantStatus)
			}
		})
	}
}

// Removing a knob removes it from the form the next create reads.
func TestDeleteAgentSetting(t *testing.T) {
	server := agentRowsServer(t)

	res := send(t, http.MethodDelete, server.URL+"/api/agent-settings/agent-setting-claude-effort", "")
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", res.StatusCode)
	}
	for _, a := range listAgents(t, server) {
		if a.ID != "claude" {
			continue
		}
		for _, s := range a.Settings {
			if s.Field == "effort" {
				t.Error("the deleted setting is still in claude's form")
			}
		}
	}
	if res := send(t, http.MethodDelete,
		server.URL+"/api/agent-settings/agent-setting-claude-effort", ""); res.StatusCode != http.StatusNotFound {
		t.Errorf("deleting it twice = %d, want 404", res.StatusCode)
	}
}

// A daemon built without persistence says so on every one of these routes, rather than
// failing in a way that suggests the request was wrong.
func TestAgentRowsReportUnavailableWithoutPersistence(t *testing.T) {
	server := httptest.NewServer(handlerForTest(Deps{Notifications: notify.NewHub()}))
	t.Cleanup(server.Close)

	for _, tt := range []struct{ method, path, payload string }{
		{http.MethodGet, "/api/agents", ""},
		{http.MethodPost, "/api/agents", `{"id":"x","binary":"x","display":"X"}`},
		{http.MethodPatch, "/api/agents/claude", `{"display":"X"}`},
		{http.MethodDelete, "/api/agents/claude", ""},
		{http.MethodPost, "/api/agent-settings", `{"agent":"claude","field":"x","title":"X","delivery":"positional"}`},
		{http.MethodPatch, "/api/agent-settings/x", `{"title":"X"}`},
		{http.MethodDelete, "/api/agent-settings/x", ""},
		{http.MethodGet, "/api/agent-models", ""},
		{http.MethodPost, "/api/agent-models", `{"agent":"claude","model":"m"}`},
		{http.MethodPatch, "/api/agent-models/x", `{"model":"m"}`},
		{http.MethodDelete, "/api/agent-models/x", ""},
	} {
		res := send(t, tt.method, server.URL+tt.path, tt.payload)
		if res.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("%s %s status = %d, want 503", tt.method, tt.path, res.StatusCode)
		}
	}
}
