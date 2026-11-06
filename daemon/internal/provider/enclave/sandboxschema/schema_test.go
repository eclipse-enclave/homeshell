package sandboxschema

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/eclipsesource/homeshell/daemon/internal/provider"
)

// testBase is an Enclave whose config lists something in every list a sandbox
// can extend.
func testBase() Base {
	return Base{
		Config: EffectiveConfig{
			"features":          option(`["devtools","node-dev"]`, "default"),
			"add_dirs":          option(`["/work/shared"]`, "global"),
			"add_readonly_dirs": option(`["/work/docs"]`, "global"),
			"ports":             option(`["3000"]`, "project"),
			"bridge_ports":      option(`["5432"]`, "global"),
			"allow_domains":     option(`["api.example.com"]`, "global"),
			"host_config":       option(`"none"`, "default"),
		},
		Features: []Feature{
			{Name: "devtools", Description: "Core development tools"},
			{Name: "node-dev", Description: "Node.js development tools"},
			{Name: "playwright", DisplayName: "Playwright", Description: "Playwright browsers"},
		},
	}
}

// option is an option whose value `enclave config --json` renders as value.
func option(value, source string) ConfigOption {
	return ConfigOption{Value: json.RawMessage(value), Source: source}
}

// with is With for a schema that fits.
func with(t *testing.T, schema, group json.RawMessage) json.RawMessage {
	t.Helper()
	out, err := With(schema, group)
	if err != nil {
		t.Fatalf("With: %v", err)
	}
	return out
}

func TestEffectiveConfigReadsTypedValues(t *testing.T) {
	config := EffectiveConfig{
		"list":    option(`["A","B"]`, "global"),
		"empty":   option(`[]`, "default"),
		"null":    option(`null`, "default"),
		"on":      option(`true`, "cli"),
		"setting": option(`"passthrough"`, "project"),
	}
	if got := config.List("list"); !slices.Equal(got, []string{"A", "B"}) {
		t.Errorf("List(list) = %q", got)
	}
	for _, name := range []string{"empty", "null", "missing"} {
		if got := config.List(name); len(got) != 0 {
			t.Errorf("List(%s) = %q; want none", name, got)
		}
	}
	if !config.Bool("on") || config.Bool("missing") || config.Bool("setting") {
		t.Error("Bool reads only a true bool as true")
	}
	if config.String("setting") != "passthrough" || config.String("on") != "" {
		t.Error("String reads only a string")
	}
}

// A session's feature defaults are the set Enclave resolves for its tool — a
// tool override included — not the default features of the catalogue.
func TestSchemaOffersTheFeaturesEnclaveResolves(t *testing.T) {
	base := testBase()
	base.Config["features"] = option(`["devtools","node-dev","playwright"]`, "tool_override")
	_, props := schemaProps(t, Schema(base))
	if got := props["features"]; !equalJSON(got["default"], []any{"devtools", "node-dev", "playwright"}) || got["x-source"] != "Enclave tool settings" {
		t.Errorf("features = %v; want the tool's selection as the default, from its settings", got)
	}

	base.Config["features"] = option(`null`, "default")
	_, props = schemaProps(t, Schema(base))
	if got := props["features"]["default"]; !equalJSON(got, []any{}) {
		t.Errorf("features default = %v; want none when Enclave resolves none", got)
	}
}

// A session recorded with playwrightMcp, which Enclave 1.0 dropped, still
// validates, so it restarts without the setting; the form no longer offers it.
func TestSchemaAcceptsRetiredSettingsWithoutOfferingThem(t *testing.T) {
	group := Schema(testBase())
	_, props := schemaProps(t, group)
	if _, ok := props["playwrightMcp"]; ok {
		t.Error("the form still offers playwrightMcp")
	}
	schema := with(t, nil, group)
	for _, config := range []string{
		`{"enclave":{"playwrightMcp":true}}`,
		`{"enclave":{"playwrightMcp":false,"hostConfig":true}}`,
	} {
		if err := provider.ValidateConfig(schema, json.RawMessage(config)); err != nil {
			t.Errorf("%s was refused: %v", config, err)
		}
	}
}

// schemaProps decodes a schema's properties in document order.
func schemaProps(t *testing.T, schema json.RawMessage) (names []string, props map[string]map[string]any) {
	t.Helper()
	var top map[string]json.RawMessage
	if err := json.Unmarshal(schema, &top); err != nil {
		t.Fatalf("schema is not an object: %v\n%s", err, schema)
	}
	ordered, err := decodeOrderedProps(top["properties"])
	if err != nil {
		t.Fatalf("reading properties: %v", err)
	}
	props = map[string]map[string]any{}
	for _, prop := range ordered {
		names = append(names, prop.name)
		var value map[string]any
		if err := json.Unmarshal(prop.value.(json.RawMessage), &value); err != nil {
			t.Fatalf("property %q: %v", prop.name, err)
		}
		props[prop.name] = value
	}
	return names, props
}

func TestSchema(t *testing.T) {
	group := Schema(testBase())
	names, props := schemaProps(t, group)

	wantOrder := []string{"features", "dirs", "ports", "bridgePorts", "allowDomains", "hostConfig"}
	if !slices.Equal(names, wantOrder) {
		t.Errorf("field order = %q; want %q", names, wantOrder)
	}
	features := props["features"]
	if got := features["default"]; !equalJSON(got, []any{"devtools", "node-dev"}) {
		t.Errorf("features default = %v; want the ones Enclave enables", got)
	}
	choices := features["items"].(map[string]any)["oneOf"].([]any)
	if len(choices) != 3 || choices[2].(map[string]any)["title"] != "Playwright" {
		t.Errorf("feature choices = %v; want all three, titled by display name", choices)
	}
	if got := props["allowDomains"]["x-inherited"]; !equalJSON(got, map[string]any{
		"values": []any{"api.example.com"}, "source": "Enclave global config",
	}) {
		t.Errorf("allowDomains inherited = %v", got)
	}
	if got := props["dirs"]["x-inherited"]; !equalJSON(got, map[string]any{
		"values": []any{
			map[string]any{"path": "/work/shared", "readonly": false},
			map[string]any{"path": "/work/docs", "readonly": true},
		},
		"source": "Enclave global config",
	}) {
		t.Errorf("dirs inherited = %v", got)
	}
	if got := props["dirs"]["items"].(map[string]any)["title"]; got != "Directory" {
		t.Errorf("dirs item title = %v; want what a row is called", got)
	}
	if got := props["bridgePorts"]["x-inherited"].(map[string]any)["values"]; !equalJSON(got, []any{5432.0}) {
		t.Errorf("bridgePorts inherited = %v; want numbers", got)
	}
	if props["hostConfig"]["default"] != false || props["hostConfig"]["x-source"] != "Enclave defaults" {
		t.Errorf("hostConfig = %v", props["hostConfig"])
	}

	// The group must be a schema the daemon can validate a submitted config with.
	schema := with(t, nil, group)
	if err := provider.ValidateConfig(schema, json.RawMessage(`{"enclave":{"features":["playwright"],"dirs":[{"path":"/x","readonly":true}],"ports":["3000","0:3000","8080:80","127.0.0.1:8080:80","[::1]:8080:80","*:8080:80","65535"],"bridgePorts":[6379],"allowDomains":["cdn.example.com","API.Example.COM"],"hostConfig":true}}`)); err != nil {
		t.Errorf("a valid sandbox config was refused: %v", err)
	}
	for name, config := range map[string]string{
		"an unknown feature":        `{"enclave":{"features":["nope"]}}`,
		"a port out of range":       `{"enclave":{"bridgePorts":[70000]}}`,
		"a mistyped port":           `{"enclave":{"ports":["300O"]}}`,
		"a port range":              `{"enclave":{"ports":["3000-3005"]}}`,
		"a port above 65535":        `{"enclave":{"ports":["99999"]}}`,
		"container port 0":          `{"enclave":{"ports":["8080:0"]}}`,
		"a single-label domain":     `{"enclave":{"allowDomains":["localhost"]}}`,
		"an empty port":             `{"enclave":{"ports":[""]}}`,
		"a domain with a scheme":    `{"enclave":{"allowDomains":["https://example.com"]}}`,
		"a wildcard domain":         `{"enclave":{"allowDomains":["*.example.com"]}}`,
		"a domain with a port":      `{"enclave":{"allowDomains":["example.com:443"]}}`,
		"a dir with no path":        `{"enclave":{"dirs":[{"readonly":true}]}}`,
		"a setting the group lacks": `{"enclave":{"yolo":true}}`,
	} {
		if err := provider.ValidateConfig(schema, json.RawMessage(config)); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestSchemaWithoutFeaturesAndWithAllNetwork(t *testing.T) {
	base := testBase()
	base.Features = nil
	base.Config["allow_all_network"] = option(`true`, "global")
	_, props := schemaProps(t, Schema(base))
	if _, ok := props["features"]; ok {
		t.Error("an Enclave listing no features still gets a features field")
	}
	if !strings.Contains(props["allowDomains"]["description"].(string), "allows all network access") {
		t.Errorf("allowDomains description = %q; want it to say extra domains change nothing", props["allowDomains"]["description"])
	}
}

// A slim image installs no features whatever `--features` says, so a slim config
// offers none to choose.
func TestSchemaOfASlimConfigOffersNoFeatures(t *testing.T) {
	base := testBase()
	base.Config["slim"] = option(`true`, "project")
	_, props := schemaProps(t, Schema(base))
	if _, ok := props["features"]; ok {
		t.Error("a slim config offers a features field Enclave would ignore")
	}
}

func TestWith(t *testing.T) {
	group := json.RawMessage(`{"type":"object"}`)
	agent := json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"model":{"type":"string"},"effort":{"type":"string"}}}`)

	names, _ := schemaProps(t, with(t, agent, group))
	if want := []string{"model", "effort", Key}; !slices.Equal(names, want) {
		t.Errorf("properties = %q; want the agent's, in order, then the group", names)
	}
	names, _ = schemaProps(t, with(t, nil, group))
	if want := []string{Key}; !slices.Equal(names, want) {
		t.Errorf("properties = %q; want only the group", names)
	}
	clashing := json.RawMessage(`{"type":"object","properties":{"enclave":{"type":"string"}}}`)
	if got, err := With(clashing, group); err == nil || string(got) != string(clashing) {
		t.Errorf("an agent setting named like the group was replaced: %s", got)
	}
}

func equalJSON(got, want any) bool {
	a, _ := json.Marshal(got)
	b, _ := json.Marshal(want)
	return string(a) == string(b)
}
