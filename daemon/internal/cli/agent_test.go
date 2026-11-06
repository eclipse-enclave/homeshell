package cli

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// testAgentsJSON is the GET /api/agents payload the verb tests read: two agents, one
// resumable with a transcript and a full form, one that only takes a model.
const testAgentsJSON = `{"agents":[` +
	`{"id":"claude","binary":"claude","display":"Claude Code","sessionIdFlag":"--session-id",` +
	`"resumeFlag":"--resume","transcript":true,"listModels":null,"settings":[` +
	`{"id":"agent-setting-claude-model","agent":"claude","field":"model","role":"model","type":"string",` +
	`"title":"Model","description":"Which Claude model.","format":"","choices":null,"default":"",` +
	`"delivery":"flag","deliveryArg":"--model","deliveryKey":""},` +
	`{"id":"agent-setting-claude-effort","agent":"claude","field":"effort","role":"","type":"string",` +
	`"title":"Reasoning effort","description":"","format":"","choices":["low","high"],"default":"",` +
	`"delivery":"flag","deliveryArg":"--effort","deliveryKey":""},` +
	`{"id":"agent-setting-claude-reasoning","agent":"claude","field":"reasoning","role":"","type":"string",` +
	`"title":"Reasoning","description":"","format":"","choices":null,"default":"",` +
	`"delivery":"assign","deliveryArg":"-c","deliveryKey":"model_reasoning_effort"}]},` +
	`{"id":"opencode","binary":"opencode","display":"OpenCode","sessionIdFlag":"","resumeFlag":"",` +
	`"transcript":false,"listModels":["models"],"settings":[` +
	`{"id":"agent-setting-opencode-model","agent":"opencode","field":"model","role":"model","type":"string",` +
	`"title":"Model","description":"","format":"","choices":null,"default":"",` +
	`"delivery":"flag","deliveryArg":"--model","deliveryKey":""}]}]}`

const testAgentModelsJSON = `{"entries":[` +
	`{"id":"agent-model-claude-opus-5","agent":"claude","model":"claude-opus-5"},` +
	`{"id":"agent-model-claude-sonnet-5","agent":"claude","model":"claude-sonnet-5"},` +
	`{"id":"agent-model-codex-gpt-5.6","agent":"codex","model":"gpt-5.6"}]}`

// The listing answers "what will the daemon offer" — which agents, whether each can be
// resumed, and how many knobs its form has. The resumable column is derived from the
// resume flag, so the two cannot disagree.
func TestAgentList(t *testing.T) {
	newStubDaemon(t, map[string]stubResponse{
		"GET /api/agents": {http.StatusOK, testAgentsJSON},
	})

	code, stdout, stderr := runClient(agentList())
	if code != exitOK {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
	}
	for _, want := range []string{"ID", "BINARY", "RESUMABLE", "TRANSCRIPT", "LIST-MODELS", "SETTINGS",
		"claude", "Claude Code", "opencode", "models"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout = %q, want it to contain %q", stdout, want)
		}
	}
	for _, line := range strings.Split(stdout, "\n") {
		switch {
		case strings.HasPrefix(line, "claude"):
			if !strings.Contains(line, "yes") {
				t.Errorf("claude's row = %q, want it reported resumable", line)
			}
			if !strings.Contains(line, "3") {
				t.Errorf("claude's row = %q, want its three settings counted", line)
			}
		case strings.HasPrefix(line, "opencode"):
			if strings.Contains(line, "yes") {
				t.Errorf("opencode's row = %q, want it reported not resumable and no transcript", line)
			}
		}
	}
}

func TestAgentListJSONPassesThePayloadThrough(t *testing.T) {
	newStubDaemon(t, map[string]stubResponse{
		"GET /api/agents": {http.StatusOK, testAgentsJSON},
	})

	code, stdout, stderr := runClient(agentList(), "--json")
	if code != exitOK {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
	}
	if strings.TrimSpace(stdout) != testAgentsJSON {
		t.Errorf("stdout = %q, want the exact payload the daemon sent", stdout)
	}
}

// The settings listing prints how each knob reaches the agent, because that is the
// half a user cannot see anywhere else — an `assign` needs both its flag and its key.
func TestAgentSettingListPrintsTheDelivery(t *testing.T) {
	newStubDaemon(t, map[string]stubResponse{
		"GET /api/agents": {http.StatusOK, testAgentsJSON},
	})

	code, stdout, stderr := runClient(agentSettingList())
	if code != exitOK {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
	}
	for _, want := range []string{
		"flag --model", "flag --effort", "assign -c model_reasoning_effort=", "low,high",
		"agent-setting-claude-model", "agent-setting-opencode-model",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout = %q, want it to contain %q", stdout, want)
		}
	}
}

// --json is the daemon's bytes, filtered but not re-encoded: a field the daemon
// adds to a setting must reach a script even though this CLI does not know it.
func TestAgentSettingListJSONKeepsTheDaemonsObjects(t *testing.T) {
	const payload = `{"agents":[` +
		`{"id":"claude","settings":[{"id":"agent-setting-claude-model","agent":"claude","addedLater":7}]},` +
		`{"id":"opencode","settings":[{"id":"agent-setting-opencode-model","agent":"opencode"}]}]}`
	newStubDaemon(t, map[string]stubResponse{
		"GET /api/agents": {http.StatusOK, payload},
	})

	code, stdout, stderr := runClient(agentSettingList(), "--json", "--agent", "claude")
	if code != exitOK {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
	}
	want := `[{"id":"agent-setting-claude-model","agent":"claude","addedLater":7}]`
	if got := strings.TrimSpace(stdout); got != want {
		t.Errorf("stdout = %s, want %s", got, want)
	}
}

func TestAgentSettingListFiltersByAgent(t *testing.T) {
	newStubDaemon(t, map[string]stubResponse{
		"GET /api/agents": {http.StatusOK, testAgentsJSON},
	})

	code, stdout, stderr := runClient(agentSettingList(), "--agent", "opencode")
	if code != exitOK {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
	}
	if strings.Contains(stdout, "agent-setting-claude-model") {
		t.Errorf("stdout = %q, want only opencode's settings", stdout)
	}
	if !strings.Contains(stdout, "agent-setting-opencode-model") {
		t.Errorf("stdout = %q, want opencode's setting", stdout)
	}
}

// A description is a POST of the flags that were given. The point of the verb is that
// a new agent needs no release, so this is the whole path from a script to a row.
func TestAgentAddPostsTheDescription(t *testing.T) {
	stub := newStubDaemon(t, map[string]stubResponse{
		"POST /api/agents": {http.StatusCreated, `{"id":"aider","binary":"aider","display":"Aider"}`},
	})

	code, stdout, stderr := runClient(agentAdd(), "aider",
		"--binary", "aider", "--display", "Aider", "--list-models", "--models --all")
	if code != exitOK {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
	}
	if strings.TrimSpace(stdout) != "aider" {
		t.Errorf("stdout = %q, want the new row's id", stdout)
	}
	body := decodeBody(t, stub, "POST", "/api/agents")
	if body["id"] != "aider" || body["binary"] != "aider" || body["display"] != "Aider" {
		t.Errorf("body = %v, want the agent as named", body)
	}
	// A space-separated argv suffix travels as a list, which is what the API takes.
	if got, ok := body["listModels"].([]any); !ok || len(got) != 2 || got[0] != "--models" {
		t.Errorf("body listModels = %v, want the suffix split into argv", body["listModels"])
	}
}

// A PATCH must carry only what the caller named: absent means "keep what is stored",
// and a `false` the caller never typed would turn a fact off.
func TestAgentSetSendsOnlyTheFlagsThatWereGiven(t *testing.T) {
	stub := newStubDaemon(t, map[string]stubResponse{
		"PATCH /api/agents/claude": {http.StatusOK, `{"id":"claude"}`},
	})

	code, _, stderr := runClient(agentSet(), "claude", "--display", "Claude")
	if code != exitOK {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
	}
	body := decodeBody(t, stub, "PATCH", "/api/agents/claude")
	if body["display"] != "Claude" {
		t.Errorf("body = %v, want the display name", body)
	}
	for _, unset := range []string{"binary", "transcript", "resumeFlag", "sessionIdFlag", "listModels", "id"} {
		if _, present := body[unset]; present {
			t.Errorf("body = %v, want no %q: the caller did not name it", body, unset)
		}
	}
}

// A boolean the caller did type must travel even when it is false — that is how a
// fact is turned off, and is exactly the case the "only what was given" rule must not
// swallow.
func TestAgentSetSendsAnExplicitFalse(t *testing.T) {
	stub := newStubDaemon(t, map[string]stubResponse{
		"PATCH /api/agents/claude": {http.StatusOK, `{"id":"claude"}`},
	})

	if code, _, stderr := runClient(agentSet(), "claude", "--transcript=false"); code != exitOK {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
	}
	body := decodeBody(t, stub, "PATCH", "/api/agents/claude")
	if got, present := body["transcript"]; !present || got != false {
		t.Errorf("body = %v, want transcript:false", body)
	}
}

// Naming nothing to change is a usage error rather than an empty PATCH: the daemon
// would happily accept it and nothing would have happened.
func TestAgentSetRefusesAnEmptyChange(t *testing.T) {
	newStubDaemon(t, map[string]stubResponse{})

	code, _, stderr := runClient(agentSet(), "claude")
	if code != exitUsage {
		t.Errorf("exit code = %d, want %d (stderr: %q)", code, exitUsage, stderr)
	}
}

// A launch knob is a row too, and its delivery is what makes it real.
func TestAgentSettingAddPostsTheKnob(t *testing.T) {
	stub := newStubDaemon(t, map[string]stubResponse{
		"POST /api/agent-settings": {http.StatusCreated, `{"id":"agent-setting-claude-verbose"}`},
	})

	code, stdout, stderr := runClient(agentSettingAdd(),
		"--agent", "claude", "--field", "verbose", "--title", "Verbose",
		"--type", "boolean", "--delivery", "bool-flag", "--delivery-arg", "--verbose")
	if code != exitOK {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
	}
	if strings.TrimSpace(stdout) != "agent-setting-claude-verbose" {
		t.Errorf("stdout = %q, want the new row's id", stdout)
	}
	body := decodeBody(t, stub, "POST", "/api/agent-settings")
	for field, want := range map[string]string{
		"agent": "claude", "field": "verbose", "title": "Verbose",
		"type": "boolean", "delivery": "bool-flag", "deliveryArg": "--verbose",
	} {
		if body[field] != want {
			t.Errorf("body[%q] = %v, want %q", field, body[field], want)
		}
	}
}

// Choices are typed as a comma-separated list and travel as the array the API takes.
func TestAgentSettingSetSendsChoicesAsAList(t *testing.T) {
	stub := newStubDaemon(t, map[string]stubResponse{
		"PATCH /api/agent-settings/agent-setting-claude-effort": {http.StatusOK, `{"id":"agent-setting-claude-effort"}`},
	})

	code, _, stderr := runClient(agentSettingSet(), "agent-setting-claude-effort",
		"--choices", "low, medium ,high")
	if code != exitOK {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
	}
	body := decodeBody(t, stub, "PATCH", "/api/agent-settings/agent-setting-claude-effort")
	got, ok := body["choices"].([]any)
	if !ok || len(got) != 3 || got[1] != "medium" {
		t.Errorf("body choices = %v, want the three values trimmed", body["choices"])
	}
	// Clearing the enum is an empty list, not an absent key: a free-text field is a
	// change the caller asked for.
	stub2 := newStubDaemon(t, map[string]stubResponse{
		"PATCH /api/agent-settings/agent-setting-claude-effort": {http.StatusOK, `{"id":"x"}`},
	})
	if code, _, stderr := runClient(agentSettingSet(), "agent-setting-claude-effort",
		"--choices", ""); code != exitOK {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
	}
	body = decodeBody(t, stub2, "PATCH", "/api/agent-settings/agent-setting-claude-effort")
	// An empty array, not null: the daemon reads null as "not supplied" and would
	// keep the stored choices while the verb reported success.
	if cleared, ok := body["choices"].([]any); !ok || len(cleared) != 0 {
		t.Errorf("body choices = %#v, want an empty array", body["choices"])
	}
}

func TestAgentSettingRemove(t *testing.T) {
	stub := newStubDaemon(t, map[string]stubResponse{
		"DELETE /api/agent-settings/agent-setting-claude-effort": {http.StatusNoContent, ""},
	})

	code, stdout, stderr := runClient(agentSettingRemove(), "agent-setting-claude-effort")
	if code != exitOK {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
	}
	if strings.TrimSpace(stdout) != "done" {
		t.Errorf("stdout = %q, want a confirmation for a body-less response", stdout)
	}
	if calls := stub.Calls(); len(calls) != 1 || calls[0].Method != http.MethodDelete {
		t.Errorf("calls = %+v, want one DELETE", calls)
	}
}

// model-list --json keeps the daemon's `{"entries": [...]}` envelope and its
// objects' bytes, with and without --agent, so a script reads one shape.
func TestAgentModelListJSONKeepsTheDaemonsShape(t *testing.T) {
	const payload = `{"entries":[` +
		`{"id":"agent-model-claude-opus-5","agent":"claude","model":"claude-opus-5","addedLater":true},` +
		`{"id":"agent-model-codex-gpt-5.6","agent":"codex","model":"gpt-5.6"}]}`
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"unfiltered is the payload itself", []string{"--json"}, payload},
		{
			"filtered keeps the envelope and the bytes",
			[]string{"--json", "--agent", "claude"},
			`{"entries":[{"id":"agent-model-claude-opus-5","agent":"claude","model":"claude-opus-5","addedLater":true}]}`,
		},
		{"a filter that matches nothing is an empty list", []string{"--json", "--agent", "none"}, `{"entries":[]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			newStubDaemon(t, map[string]stubResponse{
				"GET /api/agent-models": {http.StatusOK, payload},
			})
			code, stdout, stderr := runClient(agentModelList(), tt.args...)
			if code != exitOK {
				t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
			}
			if got := strings.TrimSpace(stdout); got != tt.want {
				t.Errorf("stdout = %s, want %s", got, tt.want)
			}
		})
	}
}

// The model catalog is the third row resource, and the one a user touches most: a
// model a vendor released is `agent model-add`.
func TestAgentModelListAndAdd(t *testing.T) {
	newStubDaemon(t, map[string]stubResponse{
		"GET /api/agent-models": {http.StatusOK, testAgentModelsJSON},
	})

	code, stdout, stderr := runClient(agentModelList(), "--agent", "claude")
	if code != exitOK {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
	}
	if !strings.Contains(stdout, "claude-opus-5") || !strings.Contains(stdout, "claude-sonnet-5") {
		t.Errorf("stdout = %q, want claude's models", stdout)
	}
	if strings.Contains(stdout, "gpt-5.6") {
		t.Errorf("stdout = %q, want only claude's models", stdout)
	}

	stub := newStubDaemon(t, map[string]stubResponse{
		"POST /api/agent-models": {http.StatusCreated, `{"id":"agent-model-claude-opus-6","agent":"claude","model":"claude-opus-6"}`},
	})
	code, stdout, stderr = runClient(agentModelAdd(), "claude", "claude-opus-6")
	if code != exitOK {
		t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, stderr)
	}
	if strings.TrimSpace(stdout) != "agent-model-claude-opus-6" {
		t.Errorf("stdout = %q, want the new row's id", stdout)
	}
	body := decodeBody(t, stub, "POST", "/api/agent-models")
	if body["agent"] != "claude" || body["model"] != "claude-opus-6" {
		t.Errorf("body = %v, want the agent and the model", body)
	}
}

// The daemon's refusal is reported in its own words rather than re-worded here, and a
// refusal is not a usage error.
func TestAgentModelAddReportsTheDaemonsRefusal(t *testing.T) {
	newStubDaemon(t, map[string]stubResponse{
		"POST /api/agent-models": {http.StatusConflict,
			`{"error":"that model is already offered for this agent"}`},
	})

	code, _, stderr := runClient(agentModelAdd(), "claude", "claude-opus-5")
	if code == exitOK || code == exitUsage {
		t.Fatalf("exit code = %d, want a failure that is not a usage error", code)
	}
	if !strings.Contains(stderr, "already offered") {
		t.Errorf("stderr = %q, want the daemon's own message", stderr)
	}
}

// Every subcommand that names a row requires the name.
func TestAgentSubcommandsRequireTheirArguments(t *testing.T) {
	newStubDaemon(t, map[string]stubResponse{})

	for _, tt := range []struct {
		name    string
		command clientCommand
	}{
		{"agent add", agentAdd()},
		{"agent set", agentSet()},
		{"agent remove", agentRemove()},
		{"setting-set", agentSettingSet()},
		{"setting-remove", agentSettingRemove()},
		{"model-remove", agentModelRemove()},
		{"model-add", agentModelAdd()},
		{"model-set", agentModelSet()},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if code, _, _ := runClient(tt.command); code != exitUsage {
				t.Errorf("exit code = %d, want %d", code, exitUsage)
			}
		})
	}
}

// decodeBody is the JSON body of the one call the stub recorded for a route.
func decodeBody(t *testing.T, stub *stubDaemon, method, path string) map[string]any {
	t.Helper()
	for _, call := range stub.Calls() {
		if call.Method != method || call.Path != path {
			continue
		}
		var body map[string]any
		if err := json.Unmarshal([]byte(call.Body), &body); err != nil {
			t.Fatalf("body %q is not JSON: %v", call.Body, err)
		}
		return body
	}
	t.Fatalf("no %s %s call was made; calls = %+v", method, path, stub.Calls())
	return nil
}
