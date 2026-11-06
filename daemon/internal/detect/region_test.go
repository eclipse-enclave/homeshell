package detect

import "testing"

func TestRegion(t *testing.T) {
	claude := "line above\n────────\n❯\n────────\n  footer status\n"
	codex := "• ran build\n› \nallow command?\n"
	snap := Snapshot{Screen: claude, OSCTitle: "✳ title", OSCProgress: "4;0"}

	tests := []struct {
		name   string
		spec   string
		snap   Snapshot
		want   string
		wantOK bool
	}{
		{"osc_title", "osc_title", snap, "✳ title", true},
		{"osc_progress", "osc_progress", snap, "4;0", true},
		{"whole_recent", "whole_recent", snap, claude, true},
		{"prompt_box_body is between last two rules", "prompt_box_body", snap, "❯\n", true},
		{"after_last_horizontal_rule is the footer", "after_last_horizontal_rule", snap, "  footer status\n", true},
		{"after_last_prompt_marker (codex)", "after_last_prompt_marker", Snapshot{Screen: codex}, "allow command?\n", true},
		{"bottom_non_empty_lines(2)", "bottom_non_empty_lines(2)", Snapshot{Screen: "a\n\nb\nc\n"}, "b\nc\n", true},
		{"unknown region is not recognized", "no_such_region", snap, "", false},
		{"bottom_lines rejects a negative count", "bottom_lines(-1)", snap, "", false},

		// Regions added with herdr engine 3; cases mirror manifest.rs tests.
		{"top_non_empty_lines(2)", "top_non_empty_lines(2)", Snapshot{Screen: "\na\n\nb\nc\n"}, "\na\n\nb\n", true},
		{"top_non_empty_lines rejects a leading zero", "top_non_empty_lines(02)", snap, "", false},
		{"top_non_empty_lines rejects zero", "top_non_empty_lines(0)", snap, "", false},
		{"top_non_empty_lines rejects above u16", "top_non_empty_lines(65536)", snap, "", false},
		{"above_prompt_box", "above_prompt_box", snap, "line above\n", true},
		{"above_prompt_box without a box is everything", "above_prompt_box", Snapshot{Screen: "a\nb\n"}, "a\nb\n", true},
		{"last_non_empty_above_prompt_box", "last_non_empty_above_prompt_box", Snapshot{Screen: "a\nb\n\n────\n❯\n────\n"}, "b", true},
		{"before_current_prompt_marker", "before_current_prompt_marker", Snapshot{Screen: codex}, "• ran build\n", true},
		{"before_current_prompt_marker with a block after the prompt", "before_current_prompt_marker", Snapshot{Screen: "› old\n• new block\n"}, "› old\n• new block\n", true},
		{"whole_recent_without_current_prompt_marker with a live prompt", "whole_recent_without_current_prompt_marker", Snapshot{Screen: codex}, "", true},
		{"whole_recent_without_current_prompt_marker without one", "whole_recent_without_current_prompt_marker", Snapshot{Screen: "• output\n"}, "• output\n", true},
		{"current_prompt_block_marker", "current_prompt_block_marker", Snapshot{Screen: "• first\n• Working (3s)\n  detail\n› \n"}, "• Working (3s)", true},
		{"after_current_prompt_block_marker", "after_current_prompt_block_marker", Snapshot{Screen: "• first\n• Working (3s)\n  detail\n› \n"}, "• Working (3s)\n  detail\n› \n", true},
		{"after_current_prompt_block_marker without a block", "after_current_prompt_block_marker", Snapshot{Screen: "› \n"}, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, ok := region(tt.snap, tt.spec); got != tt.want || ok != tt.wantOK {
				t.Errorf("region(%q) = (%q, %v), want (%q, %v)", tt.spec, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestIsHorizontalRule(t *testing.T) {
	// Mirrors herdr's is_horizontal_rule: all-rule-chars OR >=3 leading rule chars.
	cases := map[string]bool{
		"────────":    true,
		"───":         true,
		"──":          true,  // all rule chars (no non-rule suffix)
		"  ─── label": true,  // >=3 rule chars then a label
		"─ label":     false, // 1 rule char, non-empty suffix
		"❯":           false,
		"":            false,
	}
	for line, want := range cases {
		if got := isHorizontalRule(line); got != want {
			t.Errorf("isHorizontalRule(%q) = %v, want %v", line, got, want)
		}
	}
}
