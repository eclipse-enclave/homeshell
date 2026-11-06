package provider

import (
	"bytes"
	"encoding/json"
	"reflect"
	"slices"
)

// schemaNode is the part of a JSON Schema withoutDefaults reads.
type schemaNode struct {
	Type        json.RawMessage        `json:"type"`
	Properties  map[string]*schemaNode `json:"properties"`
	Required    []string               `json:"required"`
	Default     json.RawMessage        `json:"default"`
	UniqueItems bool                   `json:"uniqueItems"`
	Items       *schemaNode            `json:"items"`
	OneOf       []json.RawMessage      `json:"oneOf"`
	Enum        []json.RawMessage      `json:"enum"`
}

func (node *schemaNode) isGroup() bool {
	return node != nil && bytes.Equal(node.Type, []byte(`"object"`)) && node.Properties != nil
}

// isSet reports whether the node is an array whose order means nothing: unique
// items, or a checklist of fixed choices.
func (node *schemaNode) isSet() bool {
	return bytes.Equal(node.Type, []byte(`"array"`)) &&
		(node.UniqueItems || (node.Items != nil && (len(node.Items.OneOf) > 0 || len(node.Items.Enum) > 0)))
}

// withoutDefaults is config as a launch record keeps it: a group's settings — the
// members of an object property — at their schema default are left out, so the
// record holds only what was changed and a default the provider changes later is
// not pinned. A setting a group requires is kept, and so is one with no default,
// and one the previous record holds with the same value: a choice carried over
// unchanged stays a choice. Top-level settings are kept as given: one left out can
// mean something else than its form default, as a model left out is the agent's
// own choice.
func withoutDefaults(schema, config, previous json.RawMessage) json.RawMessage {
	var root schemaNode
	var values map[string]json.RawMessage
	if json.Unmarshal(schema, &root) != nil || json.Unmarshal(config, &values) != nil {
		return config
	}
	previousValues := members(previous)
	changed := false
	for key, prop := range root.Properties {
		if value, ok := values[key]; ok && prop.isGroup() {
			if stripped, ok := prop.withoutDefaults(value, previousValues[key]); ok {
				values[key], changed = stripped, true
			}
		}
	}
	if !changed {
		return config
	}
	out, err := json.Marshal(values)
	if err != nil {
		return config
	}
	return out
}

// members is a JSON object's members, or none for anything else.
func members(value json.RawMessage) map[string]json.RawMessage {
	var decoded map[string]json.RawMessage
	if json.Unmarshal(value, &decoded) != nil {
		return nil
	}
	return decoded
}

// withoutDefaults is a group's value without its members at their default, and
// whether any was left out; previous is the group as last recorded. A nested
// group is stripped alike, and left out when nothing of it remains.
func (node *schemaNode) withoutDefaults(value, previous json.RawMessage) (json.RawMessage, bool) {
	values := members(value)
	if values == nil {
		return value, false
	}
	previousValues := members(previous)
	changed := false
	for key, member := range values {
		child, ok := node.Properties[key]
		if !ok || child == nil || slices.Contains(node.Required, key) {
			continue
		}
		if child.isGroup() {
			if stripped, ok := child.withoutDefaults(member, previousValues[key]); ok {
				values[key], changed = stripped, true
			}
			if emptyObject(values[key]) {
				delete(values, key)
				changed = true
			}
		} else if child.same(member, child.Default) && !child.same(member, previousValues[key]) {
			delete(values, key)
			changed = true
		}
	}
	if !changed {
		return value, false
	}
	out, err := json.Marshal(values)
	if err != nil {
		return value, false
	}
	return out, true
}

// same reports whether two values of the node are equal: structurally, or for a
// set the same members in any order. Nothing is the same as a missing value.
func (node *schemaNode) same(value, other json.RawMessage) bool {
	if len(value) == 0 || len(other) == 0 {
		return false
	}
	var got, want any
	if json.Unmarshal(value, &got) != nil || json.Unmarshal(other, &want) != nil {
		return false
	}
	gotItems, gotIsList := got.([]any)
	wantItems, wantIsList := want.([]any)
	if gotIsList && wantIsList && node.isSet() {
		return containsAll(gotItems, wantItems) && containsAll(wantItems, gotItems)
	}
	return reflect.DeepEqual(got, want)
}

// containsAll reports whether every item of want is among items.
func containsAll(items, want []any) bool {
	return !slices.ContainsFunc(want, func(item any) bool {
		return !slices.ContainsFunc(items, func(other any) bool { return reflect.DeepEqual(item, other) })
	})
}
