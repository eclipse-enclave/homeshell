package local

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/eclipsesource/homeshell/daemon/internal/agentspec"
	"github.com/eclipsesource/homeshell/daemon/internal/provider"
)

// The point of asking: the option the client renders offers what this host's CLI
// answered, not a list written into HomeShell.
func TestSessionSchemasOfferTheModelsTheCLIReports(t *testing.T) {
	rt := newFakeRuntime()
	rt.output = map[string]string{
		"opencode": "anthropic/claude-sonnet-5\nopenai/gpt-5.6\n",
		"pi":       "anthropic/claude-opus-5\n",
	}
	p := newForTest(t, rt, &fakeStore{})

	opts, err := p.SessionSchemas(context.Background(), provider.WorkstreamRef{})
	if err != nil {
		t.Fatalf("SessionSchemas: %v", err)
	}
	schemas := map[string]json.RawMessage{}
	for _, o := range opts {
		schemas[o.ID] = o.Schema
	}

	if got := modelEnumOf(t, schemas["opencode"]); len(got) != 2 || got[0] != "anthropic/claude-sonnet-5" || got[1] != "openai/gpt-5.6" {
		t.Errorf("opencode model enum = %v, want the two models it reported", got)
	}
	if got := modelEnumOf(t, schemas["pi"]); len(got) != 1 || got[0] != "anthropic/claude-opus-5" {
		t.Errorf("pi model enum = %v, want the one model it reported", got)
	}

	// An agent that cannot be asked is not left empty-handed: Claude's Model field
	// offers the curated catalog instead, so every agent's field is populated one
	// way or the other.
	if got := modelEnumOf(t, schemas["claude"]); len(got) == 0 {
		t.Error("claude offers no model choices at all")
	} else if got[0] != "claude-opus-5" {
		t.Errorf("claude's model choices = %v, want the catalog", got)
	}
}

// Every way asking can go wrong ends the same way: the field stays free text, so a
// model can still be typed. A missing list is not an error the user should see.
func TestSessionSchemasFallBackToFreeTextWhenTheCLICannotAnswer(t *testing.T) {
	tests := []struct {
		name string
		with func(rt *fakeRuntime)
	}{
		{"the listing command fails", func(rt *fakeRuntime) { rt.outputErr = errors.New("exit status 1") }},
		{"it prints nothing", func(rt *fakeRuntime) { rt.output = map[string]string{"opencode": ""} }},
		{"it explains instead of listing", func(rt *fakeRuntime) {
			rt.output = map[string]string{"opencode": "No models available. Use /login.\n"}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rt := newFakeRuntime()
			tt.with(rt)
			p := newForTest(t, rt, &fakeStore{})

			opts, err := p.SessionSchemas(context.Background(), provider.WorkstreamRef{})
			if err != nil {
				t.Fatalf("SessionSchemas: %v", err)
			}
			var schema json.RawMessage
			for _, o := range opts {
				if o.ID == "opencode" {
					schema = o.Schema
				}
			}
			if len(schema) == 0 {
				t.Fatal("opencode was not offered at all")
			}
			if enum := modelEnumOf(t, schema); enum != nil {
				t.Errorf("model enum = %v, want none so the field stays free text", enum)
			}
			if !schemaHasProperty(t, string(schema), "model") {
				t.Error("the model field disappeared; it must remain typeable")
			}
			// Typing a model must still be accepted, since nothing enumerated it.
			if err := provider.ValidateConfig(schema, json.RawMessage(`{"model":"anything/at-all"}`)); err != nil {
				t.Errorf("ValidateConfig rejected a typed model: %v", err)
			}
		})
	}
}

// The form is opened often and the CLI may reach the network to answer, so the
// answer is remembered rather than re-asked on every render.
func TestModelListIsCachedPerBinary(t *testing.T) {
	rt := newFakeRuntime()
	rt.output = map[string]string{"opencode": "anthropic/claude-sonnet-5\n"}
	p := newForTest(t, rt, &fakeStore{})

	for i := 0; i < 3; i++ {
		if _, err := p.SessionSchemas(context.Background(), provider.WorkstreamRef{}); err != nil {
			t.Fatalf("SessionSchemas: %v", err)
		}
	}
	if got := rt.outputCalls["opencode"]; got != 1 {
		t.Errorf("asked opencode %d times for 3 form opens, want 1", got)
	}
	// A failed answer is cached too, or a broken CLI would be re-run every time.
	if got := rt.outputCalls["pi"]; got != 1 {
		t.Errorf("asked pi %d times, want 1 (an empty answer must be remembered)", got)
	}
}

// The models a CLI reported become the model field's choices and nothing else in the
// form moves — the other knobs are what make each agent's form its own.
func TestReportedModelsBecomeTheModelChoicesOnly(t *testing.T) {
	pi := describedAgent(t, "pi")

	offered := agentspec.Schema(pi, []string{"openai/gpt-5.6"}, agentspec.Host)
	for _, prop := range []string{"model", "prompt", "thinking"} {
		if !schemaHasProperty(t, string(offered), prop) {
			t.Errorf("schema lost %q: %s", prop, offered)
		}
	}
	if enum := modelEnumOf(t, offered); len(enum) != 1 || enum[0] != "openai/gpt-5.6" {
		t.Errorf("model enum = %v, want the reported model", enum)
	}

	// Nothing to offer leaves the field free text rather than dropping it, and leaves
	// every other field exactly as it was.
	bare := agentspec.Schema(pi, nil, agentspec.Host)
	if enum := modelEnumOf(t, bare); enum != nil {
		t.Errorf("model enum = %v, want none so the field stays free text", enum)
	}
	for _, prop := range []string{"model", "prompt", "thinking"} {
		if !schemaHasProperty(t, string(bare), prop) {
			t.Errorf("schema without models lost %q: %s", prop, bare)
		}
	}
}

// modelEnumOf reads the model field's enum out of a session schema.
func modelEnumOf(t *testing.T, schema json.RawMessage) []string {
	t.Helper()
	var doc struct {
		Properties struct {
			Model struct {
				Enum []string `json:"enum"`
			} `json:"model"`
		} `json:"properties"`
	}
	if len(schema) == 0 {
		return nil
	}
	if err := json.Unmarshal(schema, &doc); err != nil {
		t.Fatalf("schema %s is not valid JSON: %v", schema, err)
	}
	return doc.Properties.Model.Enum
}
