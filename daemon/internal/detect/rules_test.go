package detect

import "testing"

func TestLoadRuleSet(t *testing.T) {
	raw := []byte(`id = "test"
aliases = ["test-alias"]
[[rules]]
id = "r_working"
state = "working"
priority = 100
region = "osc_title"
regex = ["^spin "]
[[rules]]
id = "r_idle"
state = "idle"
priority = 50
region = "whole_recent"
line_regex = ['^\s*❯']
`)
	rs, err := parseRuleSet(raw)
	if err != nil {
		t.Fatalf("parseRuleSet: %v", err)
	}
	if rs.id != "test" || len(rs.aliases) != 1 || rs.aliases[0] != "test-alias" || len(rs.rules) != 2 {
		t.Fatalf("got id=%q aliases=%v rules=%d", rs.id, rs.aliases, len(rs.rules))
	}
	if rs.rules[0].priority != 100 || rs.rules[0].state != "working" {
		t.Errorf("rule 0 = %+v", rs.rules[0])
	}
}

func TestParseRuleSetRejectsBadRegex(t *testing.T) {
	if _, err := parseRuleSet([]byte("id = \"x\"\n[[rules]]\nid = \"r\"\nstate = \"idle\"\nregion = \"whole_recent\"\nregex = [\"(\"]\n")); err == nil {
		t.Fatal("expected error for invalid regex in rule set")
	}
}

func TestParseRuleSetRejectsUnknownRegion(t *testing.T) {
	if _, err := parseRuleSet([]byte("id = \"x\"\n[[rules]]\nid = \"r\"\nstate = \"idle\"\nregion = \"nonsense\"\ncontains = [\"x\"]\n")); err == nil {
		t.Fatal("expected error for an unknown region spec")
	}
}
