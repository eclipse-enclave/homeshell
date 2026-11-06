// Package sandboxschema builds the create-form group for an Enclave container's
// sandbox settings — features, mounts, ports, domains, host config — from what
// Enclave resolves for a tool, with Enclave's values as the defaults. It is free
// of the Enclave provider so the fake provider's demo offers the same form.
//
// Two properties of Enclave's flags shape the form: a list flag replaces the
// configured list rather than adding to it (enclave#99), and no flag empties one.
// So a session can add to what Enclave's config lists but not drop from it: those
// entries are shown locked ("x-inherited"), and the field's value is only the
// session's additions. Features are the exception: `--features` states the whole
// set, so the checklist is freely editable.
package sandboxschema

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
)

// Key is the create-form property holding the sandbox group. Not "sandbox":
// Codex has a setting of that name for its own sandbox, a different thing.
const Key = "enclave"

// EffectiveConfig is Enclave's resolved options for one tool in one project,
// keyed by option name (`features`, `add_dirs`, `pass_env`…): the `options` of
// `enclave config --json`.
type EffectiveConfig map[string]ConfigOption

// ConfigOption is one resolved option: its typed value (a list as an array, a
// bool as a bool) and the layer it came from ("default", "global", "project",
// "tool_override" or "cli"). `features` holds the set a run enables, already
// resolved from its directives.
type ConfigOption struct {
	Value  json.RawMessage `json:"value"`
	Source string          `json:"source"`
}

// List reads a list option; an unset or null one is none.
func (c EffectiveConfig) List(name string) []string {
	var items []string
	_ = json.Unmarshal(c[name].Value, &items)
	return items
}

// Bool reads a bool option; an unset one is false.
func (c EffectiveConfig) Bool(name string) bool {
	var value bool
	_ = json.Unmarshal(c[name].Value, &value)
	return value
}

// String reads a string option; an unset one is "".
func (c EffectiveConfig) String(name string) string {
	var value string
	_ = json.Unmarshal(c[name].Value, &value)
	return value
}

// Feature is one feature Enclave can install into a container.
type Feature struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	Description string `json:"description"`
}

// Base is what Enclave resolves for one tool in one directory: the session's
// starting point, and what the user's choices are compared against.
type Base struct {
	Config   EffectiveConfig
	Features []Feature
}

// Dir is one directory row: a path mounted into the container. Readonly left out
// is read-only, the schema's default.
type Dir struct {
	Path     string `json:"path"`
	Readonly *bool  `json:"readonly,omitempty"`
}

// Writable reports whether the directory is mounted writable.
func (d Dir) Writable() bool {
	return d.Readonly != nil && !*d.Readonly
}

// publishedPortPattern matches what Enclave's `-p` publishes (its ParsePortSpec):
// a container port, optionally behind a host port (`0` lets Docker pick one) and
// the host address it binds — IPv4, a bracketed IPv6 address, or `*`.
const publishedPortPattern = `^(\[[^\]]*\]:|\*:|[0-9]{1,3}(\.[0-9]{1,3}){3}:)?((0|` + portNumberPattern + `):)?` + portNumberPattern + `$`

// portNumberPattern is a port number from 1 to 65535.
const portNumberPattern = `0*([1-9][0-9]{0,3}|[1-5][0-9]{4}|6[0-4][0-9]{3}|65[0-4][0-9]{2}|655[0-2][0-9]|6553[0-5])`

// domainNamePattern matches what Enclave's `--allow-domain` takes: a bare DNS name
// of at least two labels, any case. Schemes, paths, ports and wildcards are
// refused; Enclave's 253-character cap is the schema's maxLength.
const domainNamePattern = `^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)+$`

// sourceLabel names the layer an option came from, for the form's "From …"
// caption.
func sourceLabel(source string) string {
	switch source {
	case "global":
		return "Enclave global config"
	case "project":
		return "Enclave project config"
	case "tool_override":
		return "Enclave tool settings"
	case "cli":
		return "Enclave command line"
	default:
		return "Enclave defaults"
	}
}

// listSource is the caption for entries several list options contribute: the
// one layer they share, or Enclave's config in general when they differ.
func listSource(config EffectiveConfig, names ...string) string {
	label := ""
	for _, name := range names {
		if len(config.List(name)) == 0 {
			continue
		}
		next := sourceLabel(config[name].Source)
		if label != "" && label != next {
			return "Enclave config"
		}
		label = next
	}
	return label
}

// Schema is the sandbox group's JSON Schema, with Enclave's resolved values as its
// defaults. Property order is the form's field order.
func Schema(base Base) json.RawMessage {
	config := base.Config
	props := orderedProps{}

	choices := make([]map[string]string, 0, len(base.Features))
	for _, feature := range base.Features {
		title := feature.DisplayName
		if title == "" {
			title = feature.Name
		}
		choice := map[string]string{"const": feature.Name, "title": title}
		if feature.Description != "" {
			choice["description"] = feature.Description
		}
		choices = append(choices, choice)
	}
	defaults := config.List("features")
	if defaults == nil {
		defaults = []string{}
	}
	// An empty oneOf would not be a valid schema, and a slim image installs no
	// features whatever `--features` says.
	if len(choices) > 0 && !config.Bool("slim") {
		props = props.add("features", map[string]any{
			"type":        "array",
			"title":       "Features",
			"description": "Tools installed into the container image. Changing them rebuilds the image on start, which can take a few minutes.",
			"uniqueItems": true,
			"items":       map[string]any{"type": "string", "oneOf": choices},
			"default":     defaults,
			"x-source":    sourceLabel(config["features"].Source),
		})
	}

	writable, readonly := false, true
	var inheritedDirs []Dir
	for _, path := range config.List("add_dirs") {
		inheritedDirs = append(inheritedDirs, Dir{Path: path, Readonly: &writable})
	}
	for _, path := range config.List("add_readonly_dirs") {
		inheritedDirs = append(inheritedDirs, Dir{Path: path, Readonly: &readonly})
	}
	props = props.add("dirs", withInherited(map[string]any{
		"type":        "array",
		"title":       "Directories",
		"description": "Host directories mounted into the container, in addition to the workstream.",
		"items": map[string]any{
			"type":                 "object",
			"title":                "Directory",
			"additionalProperties": false,
			"required":             []string{"path"},
			"properties": orderedProps{}.
				add("path", map[string]any{"type": "string", "title": "Path", "minLength": 1, "format": "directory"}).
				add("readonly", map[string]any{"type": "boolean", "title": "Read-only", "default": true}),
		},
		"default": []Dir{},
	}, inheritedDirs, listSource(config, "add_dirs", "add_readonly_dirs")))

	props = props.add("ports", withInherited(map[string]any{
		"type":        "array",
		"title":       "Open from your browser",
		"description": "Container ports published to the host, e.g. 3000 or 8080:80.",
		"uniqueItems": true,
		"items":       map[string]any{"type": "string", "pattern": publishedPortPattern},
		"default":     []string{},
	}, config.List("ports"), listSource(config, "ports")))

	var inheritedBridge []int
	for _, port := range config.List("bridge_ports") {
		if number, err := strconv.Atoi(port); err == nil {
			inheritedBridge = append(inheritedBridge, number)
		}
	}
	props = props.add("bridgePorts", withInherited(map[string]any{
		"type":        "array",
		"title":       "Reach host services",
		"description": "Host ports the container can reach on localhost, e.g. a database running on your machine.",
		"uniqueItems": true,
		"items":       map[string]any{"type": "integer", "minimum": 1, "maximum": 65535},
		"default":     []int{},
	}, inheritedBridge, listSource(config, "bridge_ports")))

	domainsDescription := "Domains the container may reach, in addition to Enclave's allowlist for the agent and its features."
	if config.Bool("allow_all_network") {
		domainsDescription = "Enclave's config allows all network access for this project, so extra domains change nothing."
	}
	props = props.add("allowDomains", withInherited(map[string]any{
		"type":        "array",
		"title":       "Extra allowed domains",
		"description": domainsDescription,
		"uniqueItems": true,
		"items":       map[string]any{"type": "string", "pattern": domainNamePattern, "maxLength": 253},
		"default":     []string{},
	}, config.List("allow_domains"), listSource(config, "allow_domains")))

	props = props.add("hostConfig", map[string]any{
		"type":        "boolean",
		"title":       "Pass through my host agent config",
		"description": "Use the agent's configuration from your machine instead of the container's own.",
		"default":     config.String("host_config") == "passthrough",
		"x-source":    sourceLabel(config["host_config"].Source),
	})

	out, err := json.Marshal(map[string]any{
		"type":                 "object",
		"title":                "Container",
		"description":          "Enclave's container settings for this session.",
		"additionalProperties": false,
		"properties":           props,
		"patternProperties":    retiredSettings,
	})
	if err != nil {
		// Every value above is a plain map, slice or string; nothing can fail.
		panic(fmt.Sprintf("encoding the sandbox schema: %v", err))
	}
	return out
}

// retiredSettings are settings the group no longer offers. They are accepted on
// create and restart, kept in the record but not passed to Enclave, so a session
// recorded with one still restarts, without the setting; the form shows only
// properties, so it never offers them. playwrightMcp passed `--playwright-mcp`,
// which Enclave 1.0 dropped for its playwright-mcp feature (eclipse-enclave/enclave#183).
var retiredSettings = map[string]any{"^playwrightMcp$": map[string]any{}}

// withInherited adds the entries Enclave's config already lists to a list
// property, locked in the form, with the layer they came from.
func withInherited[T any](prop map[string]any, values []T, source string) map[string]any {
	if len(values) > 0 {
		prop["x-inherited"] = map[string]any{"values": values, "source": source}
	}
	return prop
}

// With adds the sandbox group to a tool's session schema, after the agent's own
// settings. A schema with no settings becomes one with only the group. An agent
// setting named like the group keeps its field, and the group is left out; so
// does a schema that cannot be read, and the error says why.
func With(schema, group json.RawMessage) (json.RawMessage, error) {
	if len(bytes.TrimSpace(schema)) == 0 {
		return json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"` + Key + `":` + string(group) + `}}`), nil
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(schema, &top); err != nil {
		return schema, fmt.Errorf("the session schema is not a JSON object: %w", err)
	}
	props, err := decodeOrderedProps(top["properties"])
	if err != nil {
		return schema, fmt.Errorf("the session schema's properties are unreadable: %w", err)
	}
	for _, prop := range props {
		if prop.name == Key {
			return schema, fmt.Errorf("an agent setting is named %q, like the sandbox group", Key)
		}
	}
	props = append(props, namedProp{name: Key, value: group})
	encoded, err := json.Marshal(props)
	if err != nil {
		return schema, err
	}
	top["properties"] = encoded
	out, err := json.Marshal(top)
	if err != nil {
		return schema, err
	}
	return out, nil
}

// namedProp is one schema property, kept in document order: the order of a
// schema's properties is the order a form shows its fields in, which a Go map
// would sort alphabetically.
type namedProp struct {
	name  string
	value any
}

// orderedProps is a JSON object whose keys keep the order they were added in.
type orderedProps []namedProp

func (props orderedProps) add(name string, value any) orderedProps {
	return append(props, namedProp{name: name, value: value})
}

func (props orderedProps) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, prop := range props {
		if i > 0 {
			buf.WriteByte(',')
		}
		key, err := json.Marshal(prop.name)
		if err != nil {
			return nil, err
		}
		value, err := json.Marshal(prop.value)
		if err != nil {
			return nil, err
		}
		buf.Write(key)
		buf.WriteByte(':')
		buf.Write(value)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// decodeOrderedProps reads a JSON object's members in document order.
func decodeOrderedProps(raw json.RawMessage) (orderedProps, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return orderedProps{}, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return nil, fmt.Errorf("properties is not an object")
	}
	var props orderedProps
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		name, ok := token.(string)
		if !ok {
			return nil, fmt.Errorf("unexpected token %v", token)
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		props = append(props, namedProp{name: name, value: value})
	}
	return props, nil
}
