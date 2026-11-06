package detect

import "testing"

func TestGateMatches(t *testing.T) {
	compile := func(g gate) compiledGate {
		cg, err := g.compile()
		if err != nil {
			t.Fatalf("compile: %v", err)
		}
		return cg
	}

	tests := []struct {
		name string
		gate gate
		text string
		want bool
	}{
		{"contains is case-insensitive", gate{Contains: []string{"do you want to proceed?"}}, "  Do You Want To Proceed?\n", true},
		{"contains requires all needles", gate{Contains: []string{"foo", "bar"}}, "foo only\n", false},
		{"regex matches whole text", gate{Regex: []string{`^\x{2733} `}}, "✳ Working on it", true},
		{"line_regex matches any single line", gate{LineRegex: []string{`^\s*❯`}}, "hello\n  ❯\nworld\n", true},
		{"any requires at least one", gate{Any: []gate{{Contains: []string{"x"}}, {Contains: []string{"esc to cancel"}}}}, "press esc to cancel", true},
		{"empty any is no constraint", gate{Contains: []string{"hi"}}, "hi there", true},
		{"not rejects when sub-gate matches", gate{Not: []gate{{Contains: []string{"nope"}}}}, "this has nope", false},
		{"all requires every sub-gate", gate{All: []gate{{Contains: []string{"a"}}, {Contains: []string{"b"}}}}, "a and b", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cg := compile(tt.gate)
			if got := cg.matches(tt.text, toLower(tt.text)); got != tt.want {
				t.Errorf("matches(%q) = %v, want %v", tt.text, got, tt.want)
			}
		})
	}

	t.Run("invalid regex returns error", func(t *testing.T) {
		if _, err := (gate{Regex: []string{"("}}).compile(); err == nil {
			t.Fatal("expected error for invalid regex")
		}
	})
}
