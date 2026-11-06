package detect

import "testing"

func TestCleanTitle(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{"✳ Codex", "Codex"},
		{"⠙ Fix the parser", "Fix the parser"},
		{"  My session  ", "My session"},
		{"✳ Überprüfung", "Überprüfung"},
		{"✳", ""},
		{"", ""},
		// Agent-set titles reach terminals and notifications: no controls, no bidi overrides.
		{"Fix \u009b2Ktests", "Fix 2Ktests"},
		{"x\b\by\x7f", "xy"},
		{"\u202eevil\u2066", "evil"},
		{"a\tb", "a b"},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			if got := CleanTitle(tc.raw); got != tc.want {
				t.Fatalf("CleanTitle(%q)=%q want %q", tc.raw, got, tc.want)
			}
		})
	}
}
