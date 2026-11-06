package provider

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestWithoutDefaults(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{
		"model":{"type":"string","default":"sonnet"},
		"group":{"type":"object","required":["kept"],"properties":{
			"flag":{"type":"boolean","default":false},
			"kept":{"type":"boolean","default":false},
			"checklist":{"type":"array","items":{"type":"string","enum":["a","b","c"]},"default":["a","b"]},
			"unique":{"type":"array","uniqueItems":true,"items":{"type":"string"},"default":["x","y"]},
			"ordered":{"type":"array","items":{"type":"string"},"default":["x","y"]},
			"rows":{"type":"array","items":{"type":"object","properties":{"on":{"type":"boolean","default":true}}},"default":[]},
			"noDefault":{"type":"string"},
			"nested":{"type":"object","properties":{"level":{"type":"integer","default":1}}}
		}}
	}}`)
	tests := []struct {
		name     string
		config   string
		previous string
		want     string
	}{
		{name: "top-level settings are kept", config: `{"model":"sonnet"}`, want: `{"model":"sonnet"}`},
		{name: "a group member at its default is left out", config: `{"group":{"flag":false,"kept":false}}`, want: `{"group":{"kept":false}}`},
		{name: "a changed member is kept", config: `{"group":{"flag":true}}`, want: `{"group":{"flag":true}}`},
		{name: "a checklist is a set", config: `{"group":{"checklist":["b","a"]}}`, want: `{"group":{}}`},
		{name: "a checklist with a repeat is not the default", config: `{"group":{"checklist":["a","a"]}}`, want: `{"group":{"checklist":["a","a"]}}`},
		{name: "a changed checklist is kept", config: `{"group":{"checklist":["a"]}}`, want: `{"group":{"checklist":["a"]}}`},
		{name: "unique items are a set", config: `{"group":{"unique":["y","x"]}}`, want: `{"group":{}}`},
		{name: "an ordered list is compared in order", config: `{"group":{"ordered":["y","x"]}}`, want: `{"group":{"ordered":["y","x"]}}`},
		{name: "rows are kept as given", config: `{"group":{"rows":[{"on":true}]}}`, want: `{"group":{"rows":[{"on":true}]}}`},
		{name: "a member with no default is kept", config: `{"group":{"noDefault":""}}`, want: `{"group":{"noDefault":""}}`},
		{name: "a nested group at its defaults is left out", config: `{"group":{"nested":{"level":1}}}`, want: `{"group":{}}`},
		{name: "a changed nested group is kept", config: `{"group":{"nested":{"level":2}}}`, want: `{"group":{"nested":{"level":2}}}`},
		{name: "a recorded choice carried over unchanged is kept", config: `{"group":{"flag":false,"nested":{"level":1}}}`, previous: `{"group":{"flag":false,"nested":{"level":1}}}`, want: `{"group":{"flag":false,"nested":{"level":1}}}`},
		{name: "a recorded choice changed back to the default is left out", config: `{"group":{"flag":false}}`, previous: `{"group":{"flag":true}}`, want: `{"group":{}}`},
		{name: "an unknown member is kept", config: `{"group":{"other":1}}`, want: `{"group":{"other":1}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := withoutDefaults(schema, json.RawMessage(tt.config), json.RawMessage(tt.previous))
			var gotValue, wantValue any
			if json.Unmarshal(got, &gotValue) != nil || json.Unmarshal([]byte(tt.want), &wantValue) != nil || !reflect.DeepEqual(gotValue, wantValue) {
				t.Errorf("withoutDefaults(%s) = %s; want %s", tt.config, got, tt.want)
			}
		})
	}
	for _, config := range []string{``, `null`, `[1]`} {
		if got := withoutDefaults(schema, json.RawMessage(config), nil); string(got) != config {
			t.Errorf("withoutDefaults(%q) = %q; want it as given", config, got)
		}
	}
	if got := withoutDefaults(nil, json.RawMessage(`{"group":{"flag":false}}`), nil); string(got) != `{"group":{"flag":false}}` {
		t.Errorf("withoutDefaults with no schema = %s; want the config as given", got)
	}
}
