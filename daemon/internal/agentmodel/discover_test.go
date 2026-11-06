package agentmodel

import "testing"

// What a CLI prints is not a list of ids: it is a list of ids *or* an explanation
// of why it has none. Telling those apart is the whole job here, because prose
// turned into a selector would offer models that do not exist.
func TestParseModelList(t *testing.T) {
	tests := []struct {
		name string
		out  string
		want []string
	}{
		{
			"opencode's listing, one id per line",
			"opencode/big-pickle\nopencode/deepseek-v4-flash-free\nanthropic/claude-sonnet-5\n",
			[]string{"opencode/big-pickle", "opencode/deepseek-v4-flash-free", "anthropic/claude-sonnet-5"},
		},
		{
			// pi when no provider is authenticated. Every line is prose, so the
			// answer is "nothing to offer" and the form keeps its text box.
			"pi with nothing authenticated",
			"No models available. Use /login to log into a provider via OAuth or API key. See:\n" +
				"  /home/agent/.local/lib/node_modules/@earendil-works/pi-coding-agent/docs/providers.md\n",
			nil,
		},
		{
			"an id followed by columns keeps only the id",
			"anthropic/claude-sonnet-5   200k context   $3/Mtok\nopenai/gpt-5.6  400k context\n",
			[]string{"anthropic/claude-sonnet-5", "openai/gpt-5.6"},
		},
		{"blank output", "", nil},
		{"blank lines only", "\n\n  \n", nil},
		{"a bare word is not a model id", "sonnet\nopus\n", nil},
		{"a path is not a model id", "/usr/share/doc/models.md\n", nil},
		{"a nested path is not a model id", "a/b/c\n", nil},
		{"duplicates collapse", "openai/gpt-5.6\nopenai/gpt-5.6\n", []string{"openai/gpt-5.6"}},
		// The id reaches the tool's argv as the value of --model.
		{"an id shaped like an option is dropped", "-p/share\nopenai/gpt-5.6\n", []string{"openai/gpt-5.6"}},
		{
			"ids may carry dots, dashes, underscores and a thinking suffix",
			"openai/gpt-5.6:high\nvendor/model_v2.1-fast\n",
			[]string{"openai/gpt-5.6:high", "vendor/model_v2.1-fast"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseList([]byte(tt.out))
			if len(got) != len(tt.want) {
				t.Fatalf("ParseList(%q) = %v, want %v", tt.out, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("model %d = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// A pathological answer must not become an unusable selector.
func TestParseModelListCapsTheList(t *testing.T) {
	var out []byte
	for i := 0; i < MaxList*2; i++ {
		out = append(out, []byte("vendor/model-"+string(rune('a'+i%26))+"-"+string(rune('a'+i/26))+"\n")...)
	}
	if got := ParseList(out); len(got) > MaxList {
		t.Errorf("ParseList kept %d models, want at most %d", len(got), MaxList)
	}
}
