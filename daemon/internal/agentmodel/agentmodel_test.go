package agentmodel

import "testing"

// Which list an agent's Model field shows is the one rule both providers share, so
// it is tested once here rather than twice over there.
func TestOffered(t *testing.T) {
	authored := stubCatalog{"claude": {"claude-opus-5", "claude-fable-5"}}

	tests := []struct {
		name       string
		discovered []string
		catalog    Catalog
		agent      string
		want       []string
	}{
		// The agents that cannot be asked: the authored rows are the offer.
		{"authored rows when the CLI cannot answer", nil, authored, "claude",
			[]string{"claude-opus-5", "claude-fable-5"}},
		// The CLI answered, so it wins — it knows which providers this installation
		// has authenticated, and no stored list can.
		{"what the CLI reported wins", []string{"anthropic/claude-opus-5"}, authored, "claude",
			[]string{"anthropic/claude-opus-5"}},
		// A CLI that can answer but did not (an unauthenticated pi reports nothing)
		// falls back to whatever the user wrote down.
		{"authored rows when the CLI answered nothing", []string{}, authored, "claude",
			[]string{"claude-opus-5", "claude-fable-5"}},
		// Free text: nothing can say what the models are.
		{"nothing for an agent with no rows", nil, authored, "codex", nil},
		{"nothing without a catalog", nil, nil, "claude", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Offered(tt.discovered, tt.catalog, tt.agent)
			if len(got) != len(tt.want) {
				t.Fatalf("Offered() = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("Offered() = %v, want %v", got, tt.want)
				}
			}
		})
	}
}

type stubCatalog map[string][]string

func (c stubCatalog) Models(agent string) []string { return c[agent] }
