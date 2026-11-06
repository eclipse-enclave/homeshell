package agentspec

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// claudeLike is a description shaped like the seeded Claude row: a model flag, a
// positional prompt, an enum knob with a default, and the title/identity plumbing.
func claudeLike() Agent {
	return Agent{
		ID: "claude", Binary: "claude", Display: "Claude Code",
		SessionIDFlag: "--session-id", ResumeFlag: "--resume", Transcript: true,
		Settings: []Setting{
			{Field: "model", Role: RoleModel, Type: TypeString, Title: "Model",
				Delivery: Flag, DeliveryArg: "--model"},
			{Field: "prompt", Role: RolePrompt, Type: TypeString, Title: "Initial prompt",
				Format: "textarea", Delivery: Positional},
			{Field: "effort", Type: TypeString, Title: "Reasoning effort",
				Choices: []string{"low", "high"}, Delivery: Flag, DeliveryArg: "--effort"},
			{Field: "permissionMode", Type: TypeString, Title: "Permission mode",
				Choices: []string{"default", "plan"}, Default: "default",
				Delivery: Flag, DeliveryArg: "--permission-mode"},
			{Field: "title", Role: RoleTitle, Type: TypeString, Title: "Session title",
				Delivery: Flag, DeliveryArg: "--name"},
		},
	}
}

// A description is only useful if the form it generates is the one the client can
// render: the declared subset of JSON Schema, and the fields in row order — the create
// forms render properties in declaration order, and a reshuffled form is a regression
// nobody would see in a test that only checked the set of keys.
func TestSchemaRendersTheRowsInOrder(t *testing.T) {
	schema := Schema(claudeLike(), nil, Host)

	if got, want := propertyOrder(t, schema), []string{"model", "prompt", "effort", "permissionMode"}; !slices.Equal(got, want) {
		t.Errorf("properties = %v, want the rows in order %v", got, want)
	}
	props := properties(t, schema)
	if got := props["prompt"].Format; got != "textarea" {
		t.Errorf("prompt format = %q, want the row's rendering hint", got)
	}
	if got, want := props["effort"].Enum, []string{"low", "high"}; !slices.Equal(got, want) {
		t.Errorf("effort enum = %v, want the row's choices %v", got, want)
	}
	if got := props["permissionMode"].Default; string(got) != `"default"` {
		t.Errorf("permissionMode default = %s, want the row's default", got)
	}
	// A field with no default must carry none: an unset optional enum is what lets the
	// agent apply its own choice instead of the form picking the first one.
	if props["effort"].Default != nil {
		t.Errorf("effort carries default %s, want none", props["effort"].Default)
	}
}

// The title is the session's, not the user's, so it delivers without ever appearing
// as a field. A form that offered it would be asking the user to name something the
// daemon has already named.
func TestSchemaOmitsTheTitleRole(t *testing.T) {
	if props := properties(t, Schema(claudeLike(), nil, Host)); props["title"] != nil {
		t.Error("the title row rendered a field; its value comes from the session")
	}
}

// Which models are on offer is the caller's answer, not the row's — that is what lets
// what a CLI reported beat a stored list, and what keeps the same rows serving an
// agent whose models only its own installation can name.
func TestSchemaTakesTheModelChoicesFromTheCaller(t *testing.T) {
	a := claudeLike()

	offered := properties(t, Schema(a, []string{"opus", "sonnet"}, Host))
	if got, want := offered["model"].Enum, []string{"opus", "sonnet"}; !slices.Equal(got, want) {
		t.Errorf("model enum = %v, want the caller's list %v", got, want)
	}
	// Nothing offered leaves the field free text rather than dropping it.
	bare := properties(t, Schema(a, nil, Host))
	if bare["model"] == nil {
		t.Fatal("the model field disappeared when nothing was offered")
	}
	if bare["model"].Enum != nil {
		t.Errorf("model enum = %v, want none so the field stays typeable", bare["model"].Enum)
	}
	// A row's own choices are still honored when the caller has nothing: that is the
	// fallback for an agent whose models the user wrote down.
	a.Settings[0].Choices = []string{"from-the-row"}
	if got := properties(t, Schema(a, nil, Host))["model"].Enum; !slices.Equal(got, []string{"from-the-row"}) {
		t.Errorf("model enum = %v, want the row's own choices", got)
	}
}

// A caller that cannot deliver a setting must not be handed a form that asks for it.
// This is the whole point of passing the kinds in: an offered knob that silently does
// nothing is worse than one that was never offered.
func TestSchemaOmitsWhatTheCallerCannotDeliver(t *testing.T) {
	props := properties(t, Schema(claudeLike(), nil, Appended))
	if props["prompt"] != nil {
		t.Error("a positional setting was offered to a caller that cannot deliver one")
	}
	if props["model"] == nil || props["effort"] == nil {
		t.Error("a flag setting was dropped for a caller that can deliver flags")
	}
}

// An agent with nothing to ask about has no schema at all, which is what a client
// already reads as "this option takes no configuration". An Enclave profile that runs
// no agent CLI is exactly this case.
func TestSchemaIsNilWhenNothingRenders(t *testing.T) {
	if got := Schema(Agent{ID: "theia", Binary: "theia", Display: "Theia"}, nil, Host); got != nil {
		t.Errorf("schema = %s, want nil for an agent with no settings", got)
	}
	onlyTitle := Agent{ID: "x", Settings: []Setting{
		{Field: "title", Role: RoleTitle, Type: TypeString, Title: "T", Delivery: Flag, DeliveryArg: "--name"},
	}}
	if got := Schema(onlyTitle, nil, Host); got != nil {
		t.Errorf("schema = %s, want nil when every row is one the form does not show", got)
	}
}

// The rows are the user's, so nothing in them may be pasted into the document
// unencoded — a title with a quote in it must not be able to produce a schema the
// client cannot parse.
func TestSchemaEncodesUserText(t *testing.T) {
	a := Agent{ID: "x", Settings: []Setting{{
		Field: "model", Role: RoleModel, Type: TypeString,
		Title:       `a "quoted" title`,
		Description: "back\\slash and \"quotes\"",
		Delivery:    Flag, DeliveryArg: "--model",
	}}}
	schema := Schema(a, []string{`a "quoted" model`}, Host)

	props := properties(t, schema) // fails the test if the document will not parse
	if got := props["model"].Title; got != `a "quoted" title` {
		t.Errorf("title round-tripped as %q", got)
	}
	if got := props["model"].Enum; !slices.Equal(got, []string{`a "quoted" model`}) {
		t.Errorf("enum round-tripped as %v", got)
	}
}

// The launch is the other half: every kind has to land where that CLI takes it, and
// positionals have to come last whatever order the rows are in — a prompt that landed
// between two flags would be read as one flag's value.
func TestDeliverPlacesEveryKind(t *testing.T) {
	a := Agent{
		ID: "mixed", Binary: "mixed",
		Settings: []Setting{
			{Field: "prompt", Role: RolePrompt, Type: TypeString, Title: "P", Delivery: Positional},
			{Field: "model", Role: RoleModel, Type: TypeString, Title: "M", Delivery: Flag, DeliveryArg: "--model"},
			{Field: "effort", Type: TypeString, Title: "E", Delivery: Assign, DeliveryArg: "-c", DeliveryKey: "reasoning"},
			{Field: "yolo", Type: TypeBoolean, Title: "Y", Delivery: BoolFlag, DeliveryArg: "--yolo"},
			{Field: "alias", Type: TypeString, Title: "A", Delivery: Env, DeliveryArg: "SOME_VAR"},
		},
	}
	cmd := Deliver(a, Launch{
		Model:  "m1",
		Config: json.RawMessage(`{"prompt":"do it","effort":"high","yolo":true,"alias":"x"}`),
	}, Host)

	want := []string{"--model", "m1", "-c", "reasoning=high", "--yolo", "do it"}
	if !slices.Equal(cmd.Args, want) {
		t.Errorf("args = %v, want %v", cmd.Args, want)
	}
	if cmd.Env["SOME_VAR"] != "x" {
		t.Errorf("env = %v, want SOME_VAR=x", cmd.Env)
	}
}

// Unset means "the agent decides". `--model ""` is a different instruction from
// leaving `--model` out, and a false checkbox is not a flag.
func TestDeliverSkipsEmptyValues(t *testing.T) {
	a := claudeLike()
	a.Settings = append(a.Settings, Setting{
		Field: "yolo", Type: TypeBoolean, Title: "Y", Delivery: BoolFlag, DeliveryArg: "--yolo",
	})
	for _, config := range []string{``, `{}`, `{"model":"","prompt":"","effort":"","yolo":false}`, `{`} {
		cmd := Deliver(a, Launch{Config: json.RawMessage(config)}, Host)
		if len(cmd.Args) != 0 || len(cmd.Env) != 0 {
			t.Errorf("config %q delivered args %v env %v, want nothing", config, cmd.Args, cmd.Env)
		}
	}
	// A value of the wrong JSON type is absent rather than coerced: the schema said
	// what it must be, and a launch is not the place to argue.
	cmd := Deliver(a, Launch{Config: json.RawMessage(`{"effort":5,"yolo":"yes"}`)}, Host)
	if len(cmd.Args) != 0 {
		t.Errorf("args = %v, want nothing for values of the wrong type", cmd.Args)
	}
}

// The identity plumbing: a fresh launch hands the CLI the id we generated, a resume
// asks for that id back, and an agent with neither flag gets neither.
func TestDeliverIdentity(t *testing.T) {
	a := claudeLike()
	fresh := Deliver(a, Launch{SessionID: "uuid-1"}, Host)
	if got, want := fresh.Args[:2], []string{"--session-id", "uuid-1"}; !slices.Equal(got, want) {
		t.Errorf("fresh args start %v, want %v", got, want)
	}
	resumed := Deliver(a, Launch{SessionID: "uuid-1", Resume: true}, Host)
	if got, want := resumed.Args[:2], []string{"--resume", "uuid-1"}; !slices.Equal(got, want) {
		t.Errorf("resume args start %v, want %v", got, want)
	}
	// An agent that takes no id of ours: nothing is invented for it, and it is not
	// resumable, which is the same fact said twice.
	plain := Agent{ID: "codex", Binary: "codex"}
	if got := Deliver(plain, Launch{SessionID: "uuid-1", Resume: true}, Host); len(got.Args) != 0 {
		t.Errorf("args = %v, want none for an agent with no id flags", got.Args)
	}
	if plain.Resumable() {
		t.Error("an agent with no resume flag reported itself resumable")
	}
	if !a.Resumable() {
		t.Error("an agent with a resume flag reported itself not resumable")
	}
}

// A resume continues a conversation; it must not re-seed it. The prompt that started
// the session would be re-run, and the title the agent already has would be restated.
func TestDeliverDropsSeedingSettingsOnResume(t *testing.T) {
	cmd := Deliver(claudeLike(), Launch{
		Model: "m1", SessionID: "uuid-1", Title: "Session 1", Resume: true,
		Config: json.RawMessage(`{"prompt":"do it","effort":"high"}`),
	}, Host)

	joined := strings.Join(cmd.Args, " ")
	if strings.Contains(joined, "do it") {
		t.Errorf("resume args %v re-seeded the initial prompt", cmd.Args)
	}
	if strings.Contains(joined, "--name") {
		t.Errorf("resume args %v restated the session title", cmd.Args)
	}
	// The settings that describe *how* the session runs are re-passed, though: a
	// resumed session must run the way the session was created.
	if !strings.Contains(joined, "--model m1") || !strings.Contains(joined, "--effort high") {
		t.Errorf("resume args %v dropped the launch settings", cmd.Args)
	}
}

// Schema and Deliver must agree about which settings exist, or a form would offer a
// knob the launch drops on the floor.
func TestDeliverSkipsWhatTheCallerCannotDeliver(t *testing.T) {
	cmd := Deliver(claudeLike(), Launch{
		Model: "m1", Config: json.RawMessage(`{"prompt":"do it"}`),
	}, Appended)
	if slices.Contains(cmd.Args, "do it") {
		t.Errorf("args = %v, want no positional for a caller that cannot deliver one", cmd.Args)
	}
}

// The model is lifted out of the config onto the session record, so which field holds
// it has to be readable from the rows rather than assumed.
func TestModelOf(t *testing.T) {
	a := claudeLike()
	if got := ModelOf(a, json.RawMessage(`{"model":"m1"}`)); got != "m1" {
		t.Errorf("ModelOf = %q, want m1", got)
	}
	if got := ModelOf(a, json.RawMessage(`{}`)); got != "" {
		t.Errorf("ModelOf = %q, want empty when none was chosen", got)
	}
	// An agent with no model setting has no model to read, whatever the config says —
	// so a caller cannot act on one that was never on offer.
	if got := ModelOf(Agent{ID: "theia"}, json.RawMessage(`{"model":"m1"}`)); got != "" {
		t.Errorf("ModelOf = %q, want empty for an agent with no model setting", got)
	}
}

// The listing command is assembled from the row, and an agent that cannot answer says
// so with nothing rather than with a bare binary that would launch a session.
func TestListModelsArgv(t *testing.T) {
	a := Agent{ID: "opencode", Binary: "opencode", ListModels: []string{"models"}}
	if got, want := ListModelsArgv(a), []string{"opencode", "models"}; !slices.Equal(got, want) {
		t.Errorf("ListModelsArgv = %v, want %v", got, want)
	}
	if got := ListModelsArgv(Agent{ID: "claude", Binary: "claude"}); got != nil {
		t.Errorf("ListModelsArgv = %v, want nil for a CLI that cannot be asked", got)
	}
}

func TestSplitArgv(t *testing.T) {
	for _, tt := range []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"   ", nil},
		{"models", []string{"models"}},
		{"  --list-models  ", []string{"--list-models"}},
		{"a b c", []string{"a", "b", "c"}},
	} {
		if got := SplitArgv(tt.in); !slices.Equal(got, tt.want) {
			t.Errorf("SplitArgv(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

// --- helpers ---

type schemaProp struct {
	Type        string          `json:"type"`
	Title       string          `json:"title"`
	Description string          `json:"description"`
	Format      string          `json:"format"`
	Enum        []string        `json:"enum"`
	Default     json.RawMessage `json:"default"`
}

// properties decodes a generated schema's fields, failing the test if the document
// will not parse — which is itself an assertion worth making on every call.
func properties(t *testing.T, schema json.RawMessage) map[string]*schemaProp {
	t.Helper()
	var doc struct {
		Type                 string                 `json:"type"`
		AdditionalProperties bool                   `json:"additionalProperties"`
		Properties           map[string]*schemaProp `json:"properties"`
	}
	if err := json.Unmarshal(schema, &doc); err != nil {
		t.Fatalf("schema %s is not valid JSON: %v", schema, err)
	}
	if doc.Type != "object" || doc.AdditionalProperties {
		t.Errorf("schema %s must be a closed object", schema)
	}
	return doc.Properties
}

// propertyOrder is the declaration order of a schema's properties, read off the raw
// document because a Go map cannot preserve it — and the create forms render in it.
func propertyOrder(t *testing.T, schema json.RawMessage) []string {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(string(schema)))
	depth := 0
	var order []string
	for {
		tok, err := decoder.Token()
		if err != nil {
			t.Fatalf("reading schema %s: %v", schema, err)
		}
		switch value := tok.(type) {
		case json.Delim:
			if value == '{' {
				depth++
			}
			if value == '}' {
				depth--
				if depth == 0 {
					return order
				}
			}
		case string:
			// The "properties" key sits at the top level of the document (depth 1); the
			// field names are the keys of the object that follows it.
			if depth == 1 && value == "properties" {
				if err := collectKeys(decoder, &order); err != nil {
					t.Fatalf("reading properties of %s: %v", schema, err)
				}
			}
		}
	}
}

// collectKeys reads the keys of the object the decoder is positioned on, skipping each
// value.
func collectKeys(decoder *json.Decoder, into *[]string) error {
	if _, err := decoder.Token(); err != nil { // the opening brace
		return err
	}
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return err
		}
		name, ok := key.(string)
		if !ok {
			continue
		}
		*into = append(*into, name)
		var skip json.RawMessage
		if err := decoder.Decode(&skip); err != nil {
			return err
		}
	}
	_, err := decoder.Token() // the closing brace
	return err
}
