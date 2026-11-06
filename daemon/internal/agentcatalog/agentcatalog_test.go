package agentcatalog

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/eclipsesource/homeshell/daemon/internal/agentmodel"
	"github.com/eclipsesource/homeshell/daemon/internal/agentspec"
	"github.com/eclipsesource/homeshell/daemon/internal/detect"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	return NewMemory(Defaults())
}

// --- the file ---

func TestLoadWritesTheDefaultsOnFirstRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	store, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the defaults were not written: %v", err)
	}
	if !reflect.DeepEqual(store.Agents(), Defaults().Agents) {
		t.Error("a fresh catalog does not serve the defaults")
	}
	// A second load reads the file rather than re-seeding: a deleted agent stays
	// deleted, which is what makes the rows the user's.
	if err := store.DeleteAgent("codex"); err != nil {
		t.Fatalf("DeleteAgent: %v", err)
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatalf("second Load: %v", err)
	}
	if _, ok := reloaded.Agent("codex"); ok {
		t.Error("a deleted default came back on the next load")
	}
	if !reflect.DeepEqual(reloaded.Agents(), store.Agents()) || !reflect.DeepEqual(reloaded.ListModels(), store.ListModels()) {
		t.Error("the reloaded catalog differs from the one that was saved")
	}
}

func TestLoadRefusesABadFile(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{"corrupt JSON", "{not json", "parsing"},
		{"trailing data", `{"schemaVersion":1,"agents":[]} {}`, "unexpected data"},
		{"unknown top-level key", `{"schemaVersion":1,"agents":[],"modles":[]}`, `unknown field "modles"`},
		{"unknown agent key", `{"schemaVersion":1,"agents":[{"id":"x","binary":"x","display":"X","enclave_tool":"y"}]}`, `unknown field "enclave_tool"`},
		{"unknown setting key", `{"schemaVersion":1,"agents":[{"id":"x","binary":"x","display":"X","settings":[{"id":"s","agent":"x","field":"f","type":"string","title":"F","delivery":"positional","deliveryArgs":"-f"}]}]}`, `unknown field "deliveryArgs"`},
		{"unknown model key", `{"schemaVersion":1,"agents":[{"id":"x","binary":"x","display":"X"}],"models":[{"id":"m","agent":"x","model":"a","label":"A"}]}`, `unknown field "label"`},
		{"missing schema version", `{"agents":[]}`, "missing schemaVersion"},
		{"newer schema version", `{"schemaVersion":99,"agents":[]}`, "newer than this build"},
		{"agent without a binary", `{"schemaVersion":1,"agents":[{"id":"x","display":"X"}]}`, "needs a binary"},
		{"duplicate agent id", `{"schemaVersion":1,"agents":[{"id":"x","binary":"x","display":"X"},{"id":"x","binary":"y","display":"Y"}]}`, "already exists"},
		{"two agents on one Enclave tool", `{"schemaVersion":1,"agents":[{"id":"x","binary":"x","display":"X"},{"id":"y","binary":"y","display":"Y","enclaveTool":"x"}]}`, "Enclave tool"},
		{"duplicate setting id", `{"schemaVersion":1,"agents":[` +
			`{"id":"x","binary":"x","display":"X","settings":[{"id":"s","agent":"x","field":"f","type":"string","title":"F","delivery":"positional"}]},` +
			`{"id":"y","binary":"y","display":"Y","settings":[{"id":"s","agent":"y","field":"f","type":"string","title":"F","delivery":"positional"}]}]}`, "already exists"},
		{"setting under the wrong agent", `{"schemaVersion":1,"agents":[{"id":"x","binary":"x","display":"X","settings":[{"id":"s","agent":"y","field":"f","type":"string","title":"F","delivery":"positional"}]}]}`, "listed under"},
		{"invalid setting", `{"schemaVersion":1,"agents":[{"id":"x","binary":"x","display":"X","settings":[{"id":"s","agent":"x","field":"f","type":"number","title":"F","delivery":"positional"}]}]}`, "value type"},
		{"model of an unknown agent", `{"schemaVersion":1,"agents":[],"models":[{"id":"m","agent":"x","model":"a"}]}`, "no such agent"},
		{"model offered twice", `{"schemaVersion":1,"agents":[{"id":"x","binary":"x","display":"X"}],"models":[{"id":"m1","agent":"x","model":"a"},{"id":"m2","agent":"x","model":"a"}]}`, "already offered"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), FileName)
			if err := os.WriteFile(path, []byte(tt.content), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := Load(path)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Load error = %v; want one mentioning %q", err, tt.want)
			}
		})
	}
}

// A file read back serves what the API promises: settings as an array, never
// null, and empty choices as null (a free-text field) rather than [].
func TestLoadNormalizesCollections(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	content := `{"schemaVersion":1,"agents":[{"id":"x","binary":"x","display":"X"},{"id":"y","binary":"y","display":"Y","settings":[{"id":"s","agent":"y","field":"f","type":"string","title":"F","choices":[],"delivery":"positional"}]}]}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	agents := store.Agents()
	if agents[0].Settings == nil {
		t.Error("an agent without settings serves nil, want an empty slice")
	}
	if agents[1].Settings[0].Choices != nil {
		t.Error("empty choices were kept as [], want nil for a free-text field")
	}
	if store.ListModels() == nil {
		t.Error("a file without models serves nil, want an empty slice")
	}
}

// Every edit is written, so the order survives a reload exactly.
func TestOrderSurvivesSaveAndLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	store, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := store.InsertAgent(agentspec.Agent{ID: "aider", Binary: "aider", Display: "Aider"}); err != nil {
		t.Fatal(err)
	}
	if err := store.InsertSetting(agentspec.Setting{ID: "aider-model", Agent: "aider", Field: "model", Role: agentspec.RoleModel, Type: agentspec.TypeString, Title: "Model", Delivery: agentspec.Flag, DeliveryArg: "--model"}); err != nil {
		t.Fatal(err)
	}
	if err := store.InsertModel(Model{ID: "am-aider", Agent: "aider", Model: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteSetting("agent-setting-claude-effort"); err != nil {
		t.Fatal(err)
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !reflect.DeepEqual(ids(reloaded.Agents()), ids(store.Agents())) {
		t.Errorf("agent order after reload = %v, want %v", ids(reloaded.Agents()), ids(store.Agents()))
	}
	if !reflect.DeepEqual(reloaded.Agents(), store.Agents()) || !reflect.DeepEqual(reloaded.ListModels(), store.ListModels()) {
		t.Error("the reloaded catalog differs from the saved one")
	}
}

// A release that describes new agents appends them through a schema bump, and
// only them: the user's edits, deletions and order are theirs, and a default
// from an earlier version the user deleted stays deleted.
func TestUpgradeAppendsOnlyTheNewDefaults(t *testing.T) {
	old := Defaults()
	old.SchemaVersion = schemaVersion - 1
	old.Agents = slices.DeleteFunc(old.Agents, func(agent agentspec.Agent) bool {
		return agent.ID == "codex" || agent.ID == "qwen" || agent.ID == "gemini"
	})
	old.Models = slices.DeleteFunc(old.Models, func(model Model) bool { return model.Agent == "codex" || model.Agent == "gemini" })
	old.Agents[0].Binary = "/opt/custom-claude"
	old.Agents = append(old.Agents, agentspec.Agent{ID: "mine", Binary: "mine", Display: "Mine"})
	added := map[int]func(File) File{schemaVersion: func(file File) File { return appendDefaultAgents(file, "codex", "qwen") }}

	upgraded := upgrade(old, added)
	if upgraded.SchemaVersion != schemaVersion {
		t.Errorf("schema version = %d, want %d", upgraded.SchemaVersion, schemaVersion)
	}
	got := ids(upgraded.Agents)
	want := append(ids(old.Agents), "codex", "qwen")
	if !slices.Equal(got, want) {
		t.Errorf("agents after upgrade = %v, want %v (the user's order, then the new defaults; gemini stays deleted)", got, want)
	}
	if upgraded.Agents[0].Binary != "/opt/custom-claude" {
		t.Error("the user's edit was replaced by the default")
	}
	codexModels := 0
	for _, model := range upgraded.Models {
		if model.Agent == "codex" {
			codexModels++
		}
	}
	if codexModels != 3 {
		t.Errorf("codex came back with %d models, want its 3 defaults", codexModels)
	}
	// Idempotent: upgrading the result again changes nothing.
	if again := upgrade(upgraded, added); !reflect.DeepEqual(again, upgraded) {
		t.Error("a second upgrade changed the catalog")
	}
	if err := validateFile(upgraded); err != nil {
		t.Errorf("the upgraded catalog is invalid: %v", err)
	}
}

// A new default that collides with a user's row is skipped, so the upgrade
// cannot produce a file the next Load would refuse.
func TestUpgradeSkipsADefaultThatCollides(t *testing.T) {
	codex, _ := NewMemory(Defaults()).AgentByID("codex")
	tests := []struct {
		name string
		mine agentspec.Agent
	}{
		{"same Enclave tool", agentspec.Agent{ID: "mine", Binary: "mine", Display: "Mine", EnclaveTool: codex.EnclaveToolName()}},
		{"same setting id", agentspec.Agent{ID: "mine", Binary: "mine", Display: "Mine", Settings: []agentspec.Setting{
			{ID: codex.Settings[0].ID, Agent: "mine", Field: "f", Type: agentspec.TypeString, Title: "F", Delivery: agentspec.Positional}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			old := File{SchemaVersion: schemaVersion - 1, Agents: []agentspec.Agent{tt.mine}}
			upgraded := appendDefaultAgents(old, "codex")
			if got := ids(upgraded.Agents); !slices.Equal(got, []string{"mine"}) {
				t.Errorf("agents after upgrade = %v, want only the user's", got)
			}
			if err := validateFile(upgraded); err != nil {
				t.Errorf("the upgraded catalog is invalid: %v", err)
			}
		})
	}
}

// A release can ship a new model for an agent the user already has. It is
// appended once, and not to an agent the user deleted or that already offers it.
func TestAppendDefaultModels(t *testing.T) {
	defaults := Defaults()
	var claudeModel, codexModel Model
	for _, model := range defaults.Models {
		switch {
		case model.Agent == "claude" && claudeModel.ID == "":
			claudeModel = model
		case model.Agent == "codex" && codexModel.ID == "":
			codexModel = model
		}
	}
	old := Defaults()
	old.Agents = slices.DeleteFunc(old.Agents, func(agent agentspec.Agent) bool { return agent.ID == "codex" })
	old.Models = slices.DeleteFunc(old.Models, func(model Model) bool { return model.ID == claudeModel.ID || model.Agent == "codex" })

	upgraded := appendDefaultModels(old, claudeModel.ID, codexModel.ID, "no-such-model")
	if got := upgraded.Models[len(upgraded.Models)-1]; got != claudeModel {
		t.Errorf("last model = %+v, want the new claude default %+v", got, claudeModel)
	}
	if len(upgraded.Models) != len(old.Models)+1 {
		t.Errorf("%d models appended, want 1 (codex is deleted, the unknown id is skipped)", len(upgraded.Models)-len(old.Models))
	}
	if again := appendDefaultModels(upgraded, claudeModel.ID); len(again.Models) != len(upgraded.Models) {
		t.Error("a model already offered was appended again")
	}
}

// The file format is its own record types, so every field of the API structs
// has to be carried across. A field added to agentspec without a record field
// fails here instead of being silently dropped on save.
func TestRecordsCarryEveryField(t *testing.T) {
	pairs := []struct{ api, record any }{
		{agentspec.Agent{}, agentRecord{}},
		{agentspec.Setting{}, settingRecord{}},
		{Model{}, modelRecord{}},
	}
	for _, pair := range pairs {
		if api, record := reflect.TypeOf(pair.api), reflect.TypeOf(pair.record); api.NumField() != record.NumField() {
			t.Errorf("%s has %d fields, %s has %d", api, api.NumField(), record, record.NumField())
		}
	}
	full := File{SchemaVersion: schemaVersion, Agents: []agentspec.Agent{{
		ID: "a", Binary: "b", RuleSetID: "r", EnclaveTool: "e", Display: "D", SessionIDFlag: "--id", ResumeFlag: "--resume",
		Transcript: true, ListModels: []string{"models"}, Settings: []agentspec.Setting{{
			ID: "s", Agent: "a", Field: "f", Role: agentspec.RoleModel, Type: agentspec.TypeString, Title: "T", Description: "d",
			Format: "textarea", Choices: []string{"x"}, Default: "x", Delivery: agentspec.Assign, DeliveryArg: "-c", DeliveryKey: "k",
		}},
	}}, Models: []Model{{ID: "m", Agent: "a", Model: "x"}}}
	if got := recordFile(full).file(); !reflect.DeepEqual(got, full) {
		t.Errorf("round trip through the record lost something:\n got %+v\nwant %+v", got, full)
	}
}

// A failed write leaves memory and disk agreeing on the old catalog.
func TestAFailedSaveLeavesTheCatalogUnchanged(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	store, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Make the directory unwritable, so the temp file cannot be created.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	if os.Getuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	if err := store.DeleteAgent("codex"); err == nil {
		t.Fatal("DeleteAgent succeeded against an unwritable directory")
	}
	if _, ok := store.Agent("codex"); !ok {
		t.Error("the agent was deleted in memory although the write failed")
	}
}

// --- the defaults ---

// The defaults are the contract with everything downstream: the providers
// generate every create form and every launch command from these rows, so a
// wrong one is a wrong session with no compile error anywhere.
func TestDefaultsDescribeTheShippedAgents(t *testing.T) {
	store := newTestStore(t)

	agents := store.Agents()
	want := []string{"claude", "codex", "gemini", "opencode", "pi", "mistral-vibe", "amp", "agy", "cline", "copilot", "cursor", "devin", "droid", "grok", "kiro", "letta", "maki", "muse", "qodercli", "qwen"}
	if got := ids(agents); !slices.Equal(got, want) {
		t.Fatalf("default agents = %v, want %v in selector order", got, want)
	}

	byID := map[string]agentspec.Agent{}
	for _, agent := range agents {
		byID[agent.ID] = agent
	}
	for _, supported := range detect.SupportedAgents() {
		agent, ok := byID[supported.Label]
		if !ok || !detect.Supported(agent.DetectionID()) {
			t.Errorf("supported manifest %q has no default agent binding", supported.Label)
		}
	}
	for id, binary := range map[string]string{"cursor": "cursor-agent", "kiro": "kiro-cli", "mistral-vibe": "vibe"} {
		if byID[id].Binary != binary {
			t.Errorf("%s binary = %s, want %s", id, byID[id].Binary, binary)
		}
	}
	// Claude is the one agent whose sessions can be resumed and whose spend can be
	// read, and both facts are rows rather than Go booleans.
	claude := byID["claude"]
	if !claude.Resumable() || !claude.Transcript {
		t.Errorf("claude: resumable = %v, transcript = %v, want both true", claude.Resumable(), claude.Transcript)
	}
	for _, id := range want {
		if id == "claude" {
			continue
		}
		if byID[id].Resumable() {
			t.Errorf("%s reports itself resumable; no session id of ours reaches it", id)
		}
		if byID[id].Transcript {
			t.Errorf("%s reports a readable transcript; only Claude writes one", id)
		}
	}
	for id := range byID {
		var want []string
		if id == "opencode" {
			want = []string{"opencode", "models"}
		}
		if id == "pi" {
			want = []string{"pi", "--list-models"}
		}
		if got := agentspec.ListModelsArgv(byID[id]); !slices.Equal(got, want) {
			t.Errorf("%s list-models argv = %v, want %v", id, got, want)
		}
	}
}

// Every default setting must be one the daemon can act on: a row it would skip
// is a field the form shows and the launch ignores. Agents with no settings
// deliberately launch their canonical command as-is.
func TestDefaultSettingsAreAllDeliverable(t *testing.T) {
	store := newTestStore(t)

	for _, agent := range store.Agents() {
		if len(agent.Settings) > 0 {
			if _, ok := agent.ModelSetting(); !ok {
				t.Errorf("agent %q offers no model", agent.ID)
			}
		}
		for _, setting := range agent.Settings {
			if err := validateSetting(setting); err != nil {
				t.Errorf("default setting %q is not one the daemon can act on: %v", setting.ID, err)
			}
			if setting.Agent != agent.ID {
				t.Errorf("setting %q belongs to %q but was read under %q", setting.ID, setting.Agent, agent.ID)
			}
		}
		if len(agent.Settings) > 0 && len(agentspec.Schema(agent, nil, agentspec.Host)) == 0 {
			t.Errorf("agent %q generates no session schema", agent.ID)
		}
	}
}

// The defaults are hand-written data, so what a test can hold them to is the
// shape every consumer relies on: ids that are unique, and settings and models
// that would survive their own constraints.
func TestDefaultsSatisfyTheirOwnConstraints(t *testing.T) {
	defaults := Defaults()
	if err := validateFile(defaults); err != nil {
		t.Error(err)
	}
	for _, entry := range defaults.Models {
		switch {
		case entry.Model == "":
			t.Errorf("%q holds an empty model ID", entry.ID)
		case strings.ContainsAny(entry.Model, " \t\"'"):
			t.Errorf("model ID %q holds whitespace or quotes; it goes on a command line and into a schema", entry.Model)
		}
	}
}

// For the agents that were Go before, the defaults keep the flags that shipped,
// so the launch they produce has to be the launch that shipped. This is the
// test that would catch a typo'd flag or a delivery on the wrong row.
func TestDefaultAgentsLaunchAsTheyShipped(t *testing.T) {
	store := newTestStore(t)

	config := json.RawMessage(`{"prompt":"do it","effort":"high","permissionMode":"plan",` +
		`"reasoningEffort":"high","approval":"never","sandbox":"read-only",` +
		`"approvalMode":"yolo","thinking":"max","autoApprove":true}`)
	launch := agentspec.Launch{Model: "M", SessionID: "UUID", Title: "TITLE", Config: config}

	for _, tt := range []struct {
		agent    string
		wantArgs []string
		wantEnv  map[string]string
	}{
		{"claude", []string{"--session-id", "UUID", "--model", "M",
			"--effort", "high", "--permission-mode", "plan", "--name", "TITLE", "do it"}, nil},
		{"codex", []string{"--model", "M", "-c", "model_reasoning_effort=high",
			"-a", "never", "-s", "read-only", "do it"}, nil},
		{"gemini", []string{"--model", "M", "--approval-mode", "yolo", "-i", "do it"}, nil},
		{"pi", []string{"--model", "M", "--thinking", "max", "--name", "TITLE", "do it"}, nil},
		{"mistral-vibe", []string{"--auto-approve", "do it"}, map[string]string{"VIBE_ACTIVE_MODEL": "M"}},
	} {
		t.Run(tt.agent, func(t *testing.T) {
			agent, ok := store.Agent(tt.agent)
			if !ok {
				t.Fatalf("no default description for %q", tt.agent)
			}
			cmd := agentspec.Deliver(agent, launch, agentspec.Host)
			if !slices.Equal(cmd.Args, tt.wantArgs) {
				t.Errorf("args = %v, want %v", cmd.Args, tt.wantArgs)
			}
			if len(cmd.Env) != len(tt.wantEnv) {
				t.Fatalf("env = %v, want %v", cmd.Env, tt.wantEnv)
			}
			for name, value := range tt.wantEnv {
				if cmd.Env[name] != value {
					t.Errorf("env[%q] = %q, want %q", name, cmd.Env[name], value)
				}
			}
		})
	}
}

// The rows are shared, so the same agent must offer the same knobs in a
// container as on the host, minus only what a container cannot be handed. That
// parity is the reason the two providers read one catalog.
func TestDefaultSettingsAreSharedBetweenHostAndContainer(t *testing.T) {
	store := newTestStore(t)
	claude, _ := store.Agent("claude")

	host := fieldsOf(t, agentspec.Schema(claude, nil, agentspec.Host))
	container := fieldsOf(t, agentspec.Schema(claude, nil, agentspec.Appended))

	for _, field := range []string{"model", "effort", "permissionMode"} {
		if !slices.Contains(host, field) || !slices.Contains(container, field) {
			t.Errorf("field %q is not offered in both places: host = %v, container = %v",
				field, host, container)
		}
	}
	// The prompt is Claude's one positional, the one delivery Enclave does not
	// make, so it is absent there rather than offered and dropped.
	if !slices.Contains(host, "prompt") {
		t.Errorf("host fields = %v, want the initial prompt", host)
	}
	if slices.Contains(container, "prompt") {
		t.Errorf("container fields = %v, want no initial prompt", container)
	}
}

// The initial prompt is the last row of every form that has one: the one
// multi-line box these forms carry, so anywhere but the bottom puts a tall,
// usually empty field between the compact selects.
func TestTheInitialPromptIsTheLastFieldOfEveryForm(t *testing.T) {
	store := newTestStore(t)

	forms := 0
	for _, agent := range store.Agents() {
		at := slices.IndexFunc(agent.Settings, func(setting agentspec.Setting) bool {
			return setting.Role == agentspec.RolePrompt
		})
		if at < 0 {
			continue
		}
		forms++
		if at != len(agent.Settings)-1 {
			fields := make([]string, 0, len(agent.Settings))
			for _, setting := range agent.Settings {
				fields = append(fields, setting.Field)
			}
			t.Errorf("%s form = %v, want the initial prompt last", agent.ID, fields)
		}
	}
	if forms != 5 {
		t.Errorf("checked %d prompt rows, want the 5 the defaults carry", forms)
	}
}

// --- agents ---

func TestInsertAgentAppendsToTheSelector(t *testing.T) {
	store := newTestStore(t)

	agent := agentspec.Agent{ID: "aider", Binary: "aider", Display: "Aider"}
	if err := store.InsertAgent(agent); err != nil {
		t.Fatalf("InsertAgent: %v", err)
	}
	agents := store.Agents()
	if last := agents[len(agents)-1]; last.ID != "aider" {
		t.Errorf("new agent landed at %v, want the end of the selector", last.ID)
	}
	// Its settings are an empty list, never nil: this is served as JSON.
	got, ok := store.Agent("aider")
	if !ok {
		t.Fatal("the inserted agent cannot be read back")
	}
	if got.Settings == nil {
		t.Error("a new agent's settings are nil; a client promised an array should get one")
	}
}

// The listing serves an agent with no settings as an empty array, never null,
// for a new agent and for one whose last setting was just deleted.
func TestListServesAnEmptySettingsListNotNull(t *testing.T) {
	store := newTestStore(t)

	if err := store.InsertAgent(agentspec.Agent{ID: "aider", Binary: "aider", Display: "Aider"}); err != nil {
		t.Fatalf("InsertAgent: %v", err)
	}
	for _, setting := range settingsInListing(t, store, "aider") {
		t.Fatalf("a new agent has settings: %+v", setting)
	}
	setting := agentspec.Setting{
		ID: "aider-model", Agent: "aider", Field: "model", Role: agentspec.RoleModel,
		Type: agentspec.TypeString, Title: "Model", Delivery: agentspec.Flag, DeliveryArg: "--model",
	}
	if err := store.InsertSetting(setting); err != nil {
		t.Fatalf("InsertSetting: %v", err)
	}
	if err := store.DeleteSetting(setting.ID); err != nil {
		t.Fatalf("DeleteSetting: %v", err)
	}
	if got := settingsInListing(t, store, "aider"); got == nil {
		t.Error("an agent whose last setting was deleted lists nil settings, want an empty slice")
	}
}

func settingsInListing(t *testing.T, store *Store, id string) []agentspec.Setting {
	t.Helper()
	for _, agent := range store.Agents() {
		if agent.ID != id {
			continue
		}
		if agent.Settings == nil {
			t.Fatalf("agent %q lists nil settings, want an empty slice", id)
		}
		return agent.Settings
	}
	t.Fatalf("agent %q is not in the listing", id)
	return nil
}

func mustAgent(t *testing.T, store *Store, id string) agentspec.Agent {
	t.Helper()
	agent, ok := store.Agent(id)
	if !ok {
		t.Fatalf("no description for agent %q", id)
	}
	return agent
}

// An id is what a settings row and a persisted session refer to an agent by, so
// a second one is refused rather than merged.
func TestInsertAgentRefusesADuplicateID(t *testing.T) {
	store := newTestStore(t)
	for _, tool := range []string{"", "unused-profile"} {
		t.Run("tool="+tool, func(t *testing.T) {
			err := store.InsertAgent(agentspec.Agent{ID: "claude", Binary: "claude", Display: "Mine", EnclaveTool: tool})
			if !errors.Is(err, ErrDuplicate) || err.Error() != ErrDuplicate.Error()+`: agent "claude"` {
				t.Errorf("InsertAgent error = %v, want ErrDuplicate naming the agent ID", err)
			}
			if got := mustAgent(t, store, "claude"); got.Display == "Mine" || got.EnclaveTool != "" {
				t.Errorf("failed insert changed existing agent: %+v", got)
			}
		})
	}
}

func TestInsertAgentRefusesADescriptionNothingCouldLaunch(t *testing.T) {
	store := newTestStore(t)
	for _, tt := range []struct {
		name  string
		agent agentspec.Agent
	}{
		{"no id", agentspec.Agent{Binary: "x", Display: "X"}},
		{"no binary", agentspec.Agent{ID: "x", Display: "X"}},
		{"no display name", agentspec.Agent{ID: "x", Binary: "x"}},
		{"a blank binary", agentspec.Agent{ID: "x", Binary: "   ", Display: "X"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := store.InsertAgent(tt.agent); !errors.Is(err, ErrInvalid) {
				t.Errorf("InsertAgent error = %v, want ErrInvalid", err)
			}
		})
	}
}

// Editing an agent must not move it, and must not touch its settings.
func TestUpdateAgentKeepsItsPlaceAndSettings(t *testing.T) {
	store := newTestStore(t)

	before := store.Agents()
	codex, _ := store.Agent("codex")
	originalSettings := len(codex.Settings)
	codex.Display = "OpenAI Codex"
	codex.ListModels = []string{"list", "models"}
	codex.Settings = nil // an edit to the agent carries no settings
	if err := store.UpdateAgent(codex); err != nil {
		t.Fatalf("UpdateAgent: %v", err)
	}

	after := store.Agents()
	if !slices.Equal(ids(before), ids(after)) {
		t.Fatalf("agent order changed: %v -> %v", ids(before), ids(after))
	}
	got, _ := store.Agent("codex")
	if got.Display != "OpenAI Codex" {
		t.Errorf("display = %q, want the edit", got.Display)
	}
	if want := []string{"list", "models"}; !slices.Equal(got.ListModels, want) {
		t.Errorf("list-models = %v, want %v", got.ListModels, want)
	}
	if len(got.Settings) != originalSettings {
		t.Errorf("settings changed from %d to %d", originalSettings, len(got.Settings))
	}
}

func TestUpdateAgentReportsAMissingRow(t *testing.T) {
	store := newTestStore(t)
	err := store.UpdateAgent(agentspec.Agent{ID: "nope", Binary: "nope", Display: "Nope"})
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("UpdateAgent error = %v, want ErrNotFound", err)
	}
}

// The knobs and the menu of an agent nothing can launch have nowhere to appear,
// so they go with it.
func TestDeleteAgentTakesItsSettingsAndModels(t *testing.T) {
	store := newTestStore(t)

	if err := store.DeleteAgent("claude"); err != nil {
		t.Fatalf("DeleteAgent: %v", err)
	}
	if _, ok := store.Agent("claude"); ok {
		t.Error("claude is still described after being deleted")
	}
	if _, err := store.SettingByID("agent-setting-claude-effort"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a claude setting survived the agent: %v", err)
	}
	if got := store.ListModelsFor("claude"); len(got) != 0 {
		t.Errorf("claude models after deleting the agent = %v, want none", got)
	}
	if got := store.Models("codex"); len(got) != 3 {
		t.Errorf("Models(codex) = %v, want another agent's rows untouched", got)
	}
	if err := store.DeleteAgent("nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("DeleteAgent error = %v, want ErrNotFound", err)
	}
}

// What a caller is handed is a copy: editing it must not reach into the catalog.
func TestReadsAreCopies(t *testing.T) {
	store := newTestStore(t)
	claude, _ := store.Agent("claude")
	claude.Settings[0].Title = "hacked"
	claude.Settings[1].Choices[0] = "hacked"
	if again, _ := store.Agent("claude"); again.Settings[0].Title == "hacked" || again.Settings[1].Choices[0] == "hacked" {
		t.Error("editing a returned agent changed the catalog")
	}
	agents := store.Agents()
	agents[0].ID = "hacked"
	if again := store.Agents(); again[0].ID == "hacked" {
		t.Error("editing a returned listing changed the catalog")
	}
}

// --- settings ---

func TestInsertSettingAppendsToTheForm(t *testing.T) {
	store := newTestStore(t)

	setting := agentspec.Setting{
		ID: "agent-setting-claude-verbose", Agent: "claude", Field: "verbose",
		Type: agentspec.TypeBoolean, Title: "Verbose",
		Delivery: agentspec.BoolFlag, DeliveryArg: "--verbose",
	}
	if err := store.InsertSetting(setting); err != nil {
		t.Fatalf("InsertSetting: %v", err)
	}

	claude, _ := store.Agent("claude")
	if last := claude.Settings[len(claude.Settings)-1]; last.Field != "verbose" {
		t.Errorf("the new setting landed at %q, want the end of the form", last.Field)
	}
	if !slices.Contains(fieldsOf(t, agentspec.Schema(claude, nil, agentspec.Host)), "verbose") {
		t.Error("the new setting is not offered in the create form")
	}
	cmd := agentspec.Deliver(claude, agentspec.Launch{Config: json.RawMessage(`{"verbose":true}`)}, agentspec.Host)
	if !slices.Contains(cmd.Args, "--verbose") {
		t.Errorf("args = %v, want the new setting delivered", cmd.Args)
	}
}

func TestInsertSettingRefusesADuplicateFieldOrID(t *testing.T) {
	store := newTestStore(t)
	err := store.InsertSetting(agentspec.Setting{
		ID: "agent-setting-claude-second-model", Agent: "claude", Field: "model",
		Type: agentspec.TypeString, Title: "Model again", Delivery: agentspec.Flag, DeliveryArg: "--model",
	})
	if !errors.Is(err, ErrDuplicate) {
		t.Errorf("InsertSetting error = %v, want ErrDuplicate", err)
	}
	err = store.InsertSetting(agentspec.Setting{
		ID: "agent-setting-claude-effort", Agent: "codex", Field: "other",
		Type: agentspec.TypeString, Title: "Other", Delivery: agentspec.Flag, DeliveryArg: "--other",
	})
	if !errors.Is(err, ErrDuplicate) {
		t.Errorf("InsertSetting with a taken id = %v, want ErrDuplicate", err)
	}
}

func TestInsertSettingRefusesAnUnknownAgent(t *testing.T) {
	store := newTestStore(t)
	err := store.InsertSetting(agentspec.Setting{
		ID: "agent-setting-ghost-model", Agent: "ghost", Field: "model",
		Type: agentspec.TypeString, Title: "Model", Delivery: agentspec.Flag, DeliveryArg: "--model",
	})
	if !errors.Is(err, ErrUnknownAgent) {
		t.Errorf("InsertSetting error = %v, want ErrUnknownAgent", err)
	}
}

// The store refuses what the providers would skip: an unknown kind or type, and
// a delivery missing the name it needs.
func TestInsertSettingRefusesWhatNothingCouldActOn(t *testing.T) {
	store := newTestStore(t)
	base := func() agentspec.Setting {
		return agentspec.Setting{
			ID: "agent-setting-claude-x", Agent: "claude", Field: "x",
			Type: agentspec.TypeString, Title: "X", Delivery: agentspec.Flag, DeliveryArg: "--x",
		}
	}
	tests := []struct {
		name string
		with func(setting *agentspec.Setting)
	}{
		{"no field name", func(setting *agentspec.Setting) { setting.Field = "" }},
		{"no title", func(setting *agentspec.Setting) { setting.Title = "" }},
		{"an unknown value type", func(setting *agentspec.Setting) { setting.Type = "number" }},
		{"an unknown role", func(setting *agentspec.Setting) { setting.Role = "secret" }},
		{"an unknown delivery", func(setting *agentspec.Setting) { setting.Delivery = "telepathy" }},
		{"a flag with no flag", func(setting *agentspec.Setting) { setting.DeliveryArg = "" }},
		{"an env with no variable", func(setting *agentspec.Setting) {
			setting.Delivery, setting.DeliveryArg = agentspec.Env, ""
		}},
		{"an assign with no key", func(setting *agentspec.Setting) {
			setting.Delivery, setting.DeliveryArg, setting.DeliveryKey = agentspec.Assign, "-c", ""
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setting := base()
			tt.with(&setting)
			if err := store.InsertSetting(setting); !errors.Is(err, ErrInvalid) {
				t.Errorf("InsertSetting error = %v, want ErrInvalid", err)
			}
		})
	}
	// A positional needs no name, and is the one delivery that may leave it empty.
	setting := base()
	setting.Delivery, setting.DeliveryArg = agentspec.Positional, ""
	if err := store.InsertSetting(setting); err != nil {
		t.Errorf("InsertSetting refused a positional with no flag: %v", err)
	}
}

func TestUpdateSettingKeepsItsPlaceAndItsAgent(t *testing.T) {
	store := newTestStore(t)

	before, _ := store.Agent("claude")
	position := slices.IndexFunc(before.Settings, func(setting agentspec.Setting) bool { return setting.Field == "effort" })

	effort, err := store.SettingByID("agent-setting-claude-effort")
	if err != nil {
		t.Fatalf("SettingByID: %v", err)
	}
	effort.Title = "Thinking budget"
	effort.Choices = []string{"low", "max"}
	effort.Agent = "codex" // must be ignored
	if err := store.UpdateSetting(effort); err != nil {
		t.Fatalf("UpdateSetting: %v", err)
	}

	after, _ := store.Agent("claude")
	if got := slices.IndexFunc(after.Settings, func(setting agentspec.Setting) bool { return setting.Field == "effort" }); got != position {
		t.Errorf("the field moved from position %d to %d", position, got)
	}
	got, _ := store.SettingByID("agent-setting-claude-effort")
	if got.Agent != "claude" {
		t.Errorf("the setting changed agents to %q", got.Agent)
	}
	if got.Title != "Thinking budget" || !slices.Equal(got.Choices, []string{"low", "max"}) {
		t.Errorf("the edit did not stick: %+v", got)
	}
	// Clearing the choices makes the field free text: stored as nil, not [].
	got.Choices = []string{}
	if err := store.UpdateSetting(got); err != nil {
		t.Fatalf("UpdateSetting: %v", err)
	}
	if again, _ := store.SettingByID("agent-setting-claude-effort"); again.Choices != nil {
		t.Errorf("choices = %v, want nil for a free-text field", again.Choices)
	}
}

func TestUpdateSettingTellsMissingFromColliding(t *testing.T) {
	store := newTestStore(t)

	missing := agentspec.Setting{
		ID: "agent-setting-nope", Field: "x", Type: agentspec.TypeString, Title: "X",
		Delivery: agentspec.Flag, DeliveryArg: "--x",
	}
	if err := store.UpdateSetting(missing); !errors.Is(err, ErrNotFound) {
		t.Errorf("UpdateSetting error = %v, want ErrNotFound", err)
	}
	effort, _ := store.SettingByID("agent-setting-claude-effort")
	effort.Field = "model" // claude already has one
	if err := store.UpdateSetting(effort); !errors.Is(err, ErrDuplicate) {
		t.Errorf("UpdateSetting error = %v, want ErrDuplicate", err)
	}
	effort.Field = ""
	if err := store.UpdateSetting(effort); !errors.Is(err, ErrInvalid) {
		t.Errorf("UpdateSetting with no field = %v, want ErrInvalid", err)
	}
}

func TestDeleteSettingRemovesItFromTheForm(t *testing.T) {
	store := newTestStore(t)

	if err := store.DeleteSetting("agent-setting-claude-effort"); err != nil {
		t.Fatalf("DeleteSetting: %v", err)
	}
	claude, _ := store.Agent("claude")
	if slices.Contains(fieldsOf(t, agentspec.Schema(claude, nil, agentspec.Host)), "effort") {
		t.Error("the deleted setting is still offered")
	}
	cmd := agentspec.Deliver(claude, agentspec.Launch{Config: json.RawMessage(`{"effort":"high"}`)}, agentspec.Host)
	if slices.Contains(cmd.Args, "--effort") {
		t.Errorf("args = %v, want nothing for a deleted setting", cmd.Args)
	}
	if err := store.DeleteSetting("agent-setting-claude-effort"); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleting it twice error = %v, want ErrNotFound", err)
	}
}

func TestSettingByID(t *testing.T) {
	store := newTestStore(t)
	effort, err := store.SettingByID("agent-setting-claude-effort")
	if err != nil {
		t.Fatalf("SettingByID: %v", err)
	}
	if effort.Agent != "claude" || effort.Field != "effort" || effort.DeliveryArg != "--effort" {
		t.Errorf("SettingByID = %+v, want claude's effort row", effort)
	}
	if _, err := store.SettingByID("agent-setting-nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("SettingByID(missing) = %v, want ErrNotFound", err)
	}
}

// --- bindings ---

func TestAgentBindingsRoundTrip(t *testing.T) {
	store := newTestStore(t)
	agent := agentspec.Agent{ID: "custom", Binary: "/opt/claude-wrapper", Display: "Custom", RuleSetID: "claude", EnclaveTool: "team-claude"}
	if err := store.InsertAgent(agent); err != nil {
		t.Fatal(err)
	}
	assert := func(rules, tool string) {
		t.Helper()
		got, err := store.AgentByID(agent.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.RuleSetID != rules || got.EnclaveTool != tool {
			t.Fatalf("bindings = %q, %q; want %q, %q", got.RuleSetID, got.EnclaveTool, rules, tool)
		}
	}
	assert("claude", "team-claude")
	agent.RuleSetID, agent.EnclaveTool = "codex", "team-codex"
	if err := store.UpdateAgent(agent); err != nil {
		t.Fatal(err)
	}
	assert("codex", "team-codex")
	agent.RuleSetID, agent.EnclaveTool = "", ""
	if err := store.UpdateAgent(agent); err != nil {
		t.Fatal(err)
	}
	assert("", "")
}

func TestEnclaveToolBindingIsUnambiguous(t *testing.T) {
	store := newTestStore(t)
	agent := agentspec.Agent{ID: "custom", Binary: "wrapper", Display: "Custom", EnclaveTool: "claude"}
	if err := store.InsertAgent(agent); !errors.Is(err, ErrDuplicate) || err.Error() != ErrDuplicate.Error()+`: Enclave tool "claude"` {
		t.Fatalf("InsertAgent = %v, want duplicate naming the Enclave tool", err)
	}
	if _, err := store.AgentByID(agent.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("failed insert left an agent: %v", err)
	}
	agent.EnclaveTool = "custom-profile"
	if err := store.InsertAgent(agent); err != nil {
		t.Fatal(err)
	}
	agent.EnclaveTool = "claude"
	if err := store.UpdateAgent(agent); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("UpdateAgent = %v, want duplicate", err)
	}
	got, _ := store.Agent(agent.ID)
	if got.EnclaveTool != "custom-profile" {
		t.Fatalf("failed update changed binding: %+v", got)
	}
}

func TestInsertAgentReportsAnImplicitEnclaveToolConflict(t *testing.T) {
	store := newTestStore(t)
	owner := agentspec.Agent{ID: "owner", Binary: "wrapper", Display: "Owner", EnclaveTool: "custom"}
	if err := store.InsertAgent(owner); err != nil {
		t.Fatal(err)
	}
	// With no explicit binding, the new agent's ID is its effective Enclave tool.
	agent := agentspec.Agent{ID: "custom", Binary: "wrapper", Display: "Custom"}
	if err := store.InsertAgent(agent); !errors.Is(err, ErrDuplicate) || err.Error() != ErrDuplicate.Error()+`: Enclave tool "custom"` {
		t.Fatalf("InsertAgent = %v, want duplicate naming the effective Enclave tool", err)
	}
	if _, err := store.AgentByID(agent.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("failed insert left an agent: %v", err)
	}
}

// --- models ---

func TestTheCuratedModelsAreTheDefaults(t *testing.T) {
	store := newTestStore(t)
	tests := []struct {
		agent string
		want  []string
	}{
		{"claude", []string{"claude-opus-5", "claude-sonnet-5", "claude-haiku-4-5", "claude-fable-5"}},
		{"codex", []string{"gpt-5.6", "gpt-5.5", "gpt-5.4"}},
		{"gemini", []string{"gemini-3-pro-preview", "gemini-2.5-flash"}},
		{"opencode", []string{}},
		{"pi", []string{}},
		{"mistral-vibe", []string{}},
		{"no-such-agent", []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.agent, func(t *testing.T) {
			if got := store.Models(tt.agent); !slices.Equal(got, tt.want) {
				t.Errorf("Models(%q) = %v, want %v", tt.agent, got, tt.want)
			}
		})
	}
}

// Every default entry is addressable, which is what makes the catalog editable:
// the API reaches an entry by the id the list served.
func TestDefaultModelsCarryStableIDs(t *testing.T) {
	store := newTestStore(t)
	entries := store.ListModelsFor("claude")
	if len(entries) == 0 {
		t.Fatal("no default claude entries")
	}
	for _, entry := range entries {
		if entry.ID == "" || entry.Agent != "claude" {
			t.Errorf("entry %+v has no id or the wrong agent", entry)
		}
	}
	if got := entries[0].ID; got != "agent-model-claude-opus-5" {
		t.Errorf("first id = %q, want agent-model-claude-opus-5", got)
	}
}

func TestInsertModelAppendsToThatAgentsList(t *testing.T) {
	store := newTestStore(t)
	if err := store.InsertModel(Model{ID: "am-new", Agent: "claude", Model: "claude-opus-6"}); err != nil {
		t.Fatalf("InsertModel: %v", err)
	}
	if got := store.Models("claude"); len(got) != 5 || got[4] != "claude-opus-6" {
		t.Errorf("Models(claude) = %v, want the new model last", got)
	}
	if got := store.Models("codex"); len(got) != 3 {
		t.Errorf("Models(codex) = %v, want the default three untouched", got)
	}
	for _, model := range []string{"anthropic/claude-opus-5", "openai/gpt-5.6"} {
		if err := store.InsertModel(Model{ID: "am-" + model, Agent: "opencode", Model: model}); err != nil {
			t.Fatalf("InsertModel(%q): %v", model, err)
		}
	}
	if got, want := store.Models("opencode"), []string{"anthropic/claude-opus-5", "openai/gpt-5.6"}; !slices.Equal(got, want) {
		t.Errorf("Models(opencode) = %v, want %v in insert order", got, want)
	}
}

// The same model twice would put it in the menu twice, so it is refused. The
// same model for a different agent is a different offer and is allowed.
func TestInsertModelRefusesAModelAnAgentAlreadyOffers(t *testing.T) {
	store := newTestStore(t)
	err := store.InsertModel(Model{ID: "am-dup", Agent: "claude", Model: "claude-opus-5"})
	if !errors.Is(err, ErrModelDuplicate) {
		t.Errorf("InsertModel error = %v, want ErrModelDuplicate", err)
	}
	if got := store.Models("claude"); len(got) != 4 {
		t.Errorf("Models(claude) = %v, want the default four unchanged", got)
	}
	if err := store.InsertModel(Model{ID: "am-other", Agent: "codex", Model: "claude-opus-5"}); err != nil {
		t.Errorf("InsertModel for another agent error = %v, want it allowed", err)
	}
	if err := store.InsertModel(Model{ID: "am-other", Agent: "gemini", Model: "x"}); !errors.Is(err, ErrModelDuplicate) {
		t.Errorf("InsertModel with a taken id = %v, want ErrModelDuplicate", err)
	}
	if err := store.InsertModel(Model{ID: "am-aider", Agent: "aider", Model: "whatever"}); !errors.Is(err, ErrUnknownAgent) {
		t.Errorf("InsertModel for an undescribed agent = %v, want ErrUnknownAgent", err)
	}
}

func TestUpdateModelCorrectsAModelInPlace(t *testing.T) {
	store := newTestStore(t)
	updated, err := store.UpdateModel("agent-model-claude-sonnet-5", "claude-sonnet-6")
	if err != nil {
		t.Fatalf("UpdateModel: %v", err)
	}
	if want := (Model{ID: "agent-model-claude-sonnet-5", Agent: "claude", Model: "claude-sonnet-6"}); updated != want {
		t.Errorf("UpdateModel = %+v, want %+v", updated, want)
	}
	want := []string{"claude-opus-5", "claude-sonnet-6", "claude-haiku-4-5", "claude-fable-5"}
	if got := store.Models("claude"); !slices.Equal(got, want) {
		t.Errorf("Models(claude) = %v, want %v", got, want)
	}
	if _, err := store.UpdateModel("am-missing", "claude-opus-5"); !errors.Is(err, ErrModelNotFound) {
		t.Errorf("UpdateModel on a missing entry = %v, want ErrModelNotFound", err)
	}
	if _, err := store.UpdateModel("agent-model-claude-sonnet-5", "claude-opus-5"); !errors.Is(err, ErrModelDuplicate) {
		t.Errorf("UpdateModel onto an offered model = %v, want ErrModelDuplicate", err)
	}
}

func TestDeleteModelRemovesOneEntryAndReportsAMissingOne(t *testing.T) {
	store := newTestStore(t)
	if err := store.DeleteModel("agent-model-claude-fable-5"); err != nil {
		t.Fatalf("DeleteModel: %v", err)
	}
	if got, want := store.Models("claude"), []string{"claude-opus-5", "claude-sonnet-5", "claude-haiku-4-5"}; !slices.Equal(got, want) {
		t.Errorf("Models(claude) = %v, want %v", got, want)
	}
	if err := store.DeleteModel("agent-model-claude-fable-5"); !errors.Is(err, ErrModelNotFound) {
		t.Errorf("DeleteModel again = %v, want ErrModelNotFound", err)
	}
}

// Deleting the last model and adding more keeps insertion order: nothing is
// keyed on a position that could be reused.
func TestModelOrderIsInsertionOrder(t *testing.T) {
	store := newTestStore(t)
	if err := store.DeleteModel("agent-model-codex-gpt-5.4"); err != nil {
		t.Fatalf("DeleteModel: %v", err)
	}
	for _, model := range []string{"gpt-5.7", "gpt-5.8"} {
		if err := store.InsertModel(Model{ID: "am-" + model, Agent: "codex", Model: model}); err != nil {
			t.Fatalf("InsertModel(%q): %v", model, err)
		}
	}
	if got, want := store.Models("codex"), []string{"gpt-5.6", "gpt-5.5", "gpt-5.7", "gpt-5.8"}; !slices.Equal(got, want) {
		t.Errorf("Models(codex) = %v, want %v", got, want)
	}
}

// ListModels is what the API serves: every entry, grouped by agent, each
// agent's in its own order.
func TestListModelsGroupsByAgent(t *testing.T) {
	store := newTestStore(t)
	if err := store.InsertModel(Model{ID: "am-late", Agent: "claude", Model: "claude-late"}); err != nil {
		t.Fatal(err)
	}
	var agents []string
	for _, entry := range store.ListModels() {
		if len(agents) == 0 || agents[len(agents)-1] != entry.Agent {
			agents = append(agents, entry.Agent)
		}
	}
	if !slices.Equal(agents, []string{"claude", "codex", "gemini"}) {
		t.Errorf("agents in list order = %v, want each agent's entries together", agents)
	}
	if got := store.ListModels()[4].Model; got != "claude-late" {
		t.Errorf("the late claude entry sits at %q, want it last among claude's", got)
	}
}

// The catalog is read through the ports both providers hold.
func TestStoreImplementsThePorts(t *testing.T) {
	var _ agentmodel.Catalog = (*Store)(nil)
	var _ agentspec.Source = (*Store)(nil)
}

// --- helpers ---

func ids(agents []agentspec.Agent) []string {
	out := make([]string, 0, len(agents))
	for _, agent := range agents {
		out = append(out, agent.ID)
	}
	return out
}

// fieldsOf is the property names a generated schema offers.
func fieldsOf(t *testing.T, schema json.RawMessage) []string {
	t.Helper()
	if len(schema) == 0 {
		return nil
	}
	var doc struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(schema, &doc); err != nil {
		t.Fatalf("schema %s is not valid JSON: %v", schema, err)
	}
	out := make([]string, 0, len(doc.Properties))
	for field := range doc.Properties {
		out = append(out, field)
	}
	return out
}
