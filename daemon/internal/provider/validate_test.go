package provider

import (
	"encoding/json"
	"maps"
	"testing"
)

const testSchema = `{
  "type": "object",
  "additionalProperties": false,
  "required": ["branch"],
  "properties": {
    "branch": {"type": "string", "minLength": 1},
    "copyEnv": {"type": "boolean"},
    "agent": {"type": "string", "enum": ["claude", "codex"]}
  }
}`

func TestValidateConfig(t *testing.T) {
	tests := []struct {
		name      string
		config    string
		wantOK    bool
		wantField string // a substring expected among the offending field keys
	}{
		{name: "valid full", config: `{"branch":"main","copyEnv":true,"agent":"claude"}`, wantOK: true},
		{name: "valid minimal", config: `{"branch":"main"}`, wantOK: true},
		{name: "missing required", config: `{"copyEnv":true}`, wantField: "/branch"},
		{name: "wrong type", config: `{"branch":123}`, wantField: "/branch"},
		{name: "bad enum", config: `{"branch":"main","agent":"gemini"}`, wantField: "/agent"},
		{name: "unknown property", config: `{"branch":"main","nope":1}`, wantField: "/"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateConfig(json.RawMessage(testSchema), json.RawMessage(tc.config))
			if tc.wantOK {
				if err != nil {
					t.Fatalf("ValidateConfig = %v; want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("ValidateConfig = nil; want validation error")
			}
			fields := FieldErrors(err)
			if len(fields) == 0 {
				t.Fatalf("no field errors reported for %q", tc.config)
			}
			found := false
			for key := range fields {
				if key == tc.wantField || (tc.wantField == "/") {
					found = true
				}
			}
			if !found {
				t.Errorf("field errors %v do not include %q", fields, tc.wantField)
			}
		})
	}
}

func TestValidateConfigEmptySchemaAcceptsAnything(t *testing.T) {
	if err := ValidateConfig(nil, json.RawMessage(`{"anything":true}`)); err != nil {
		t.Errorf("empty schema rejected config: %v", err)
	}
}

func TestValidateConfigEmptyConfigAgainstRequired(t *testing.T) {
	if err := ValidateConfig(json.RawMessage(testSchema), nil); err == nil {
		t.Errorf("empty config passed a schema that requires branch")
	}
}

func TestValidateConfigWordsItsReasons(t *testing.T) {
	schema := `{
  "type": "object",
  "additionalProperties": false,
  "required": ["name"],
  "properties": {
    "name": {"type": "string"},
    "branch": {"type": "string", "minLength": 1},
    "code": {"type": "string", "minLength": 3, "maxLength": 4, "pattern": "^[a-z]+$"},
    "agent": {"type": "string", "enum": ["claude", "codex"]},
    "count": {"type": "integer"},
    "a/b": {"type": "string"},
    "ports": {"type": "array", "uniqueItems": true, "items": {"type": "integer", "minimum": 1, "maximum": 65535}},
    "features": {"type": "array", "items": {"type": "string", "oneOf": [{"const": "devtools"}, {"const": "playwright"}]}}
  }
}`
	tests := []struct {
		name   string
		config string
		want   map[string]string
	}{
		{name: "missing property", config: `{}`, want: map[string]string{"/name": "required"}},
		{name: "empty required text", config: `{"name":"x","branch":""}`, want: map[string]string{"/branch": "required"}},
		{name: "too short", config: `{"name":"x","code":"ab"}`, want: map[string]string{"/code": "must be at least 3 characters"}},
		{name: "too long", config: `{"name":"x","code":"abcde"}`, want: map[string]string{"/code": "must be at most 4 characters"}},
		{name: "pattern", config: `{"name":"x","code":"AB1"}`, want: map[string]string{"/code": "is not in the expected form"}},
		{name: "enum", config: `{"name":"x","agent":"gemini"}`, want: map[string]string{"/agent": "must be one of claude, codex"}},
		{name: "type", config: `{"name":"x","count":"many"}`, want: map[string]string{"/count": "must be a whole number"}},
		{name: "unknown property", config: `{"name":"x","nope":1}`, want: map[string]string{"/": "unknown settings: nope"}},
		{name: "below the minimum", config: `{"name":"x","ports":[0]}`, want: map[string]string{"/ports/0": "must be at least 1"}},
		{name: "above the maximum", config: `{"name":"x","ports":[70000]}`, want: map[string]string{"/ports/0": "must be at most 65535"}},
		{name: "a duplicate", config: `{"name":"x","ports":[22,22]}`, want: map[string]string{"/ports": "lists the same entry twice"}},
		{name: "not a choice on offer", config: `{"name":"x","features":["nope"]}`, want: map[string]string{"/features/0": "is not one of the choices on offer"}},
		{name: "escaped pointer", config: `{"name":"x","a/b":1}`, want: map[string]string{"/a~1b": "must be text"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fields := FieldErrors(ValidateConfig(json.RawMessage(schema), json.RawMessage(tt.config)))
			if !maps.Equal(fields, tt.want) {
				t.Errorf("field errors = %v, want %v", fields, tt.want)
			}
		})
	}
}
