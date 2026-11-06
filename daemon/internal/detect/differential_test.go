package detect

import (
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
)

// TestDifferentialAgainstHerdr runs a broad corpus of screen snapshots through
// both the real herdr binary (offline `agent explain --file`) and our Detect,
// and fails on ANY divergence in the winning rule, the skip flag, or the mapped
// state. It is the strict cross-check that our ported engine matches herdr.
//
// Scope: herdr's offline --file path hardcodes an empty OSC title/progress, so
// title-based rules (codex working/idle, claude spinner/✳) cannot fire on
// either side and are not exercised here. This harness covers the screen-region
// rules — the ported logic most at risk of drift. Skipped unless the herdr on
// PATH is the release the manifests are vendored from (manifests.lock), so CI
// without herdr still passes. herdr runs with empty config and state dirs, so a
// user's manifest overrides or remote updates cannot leak in.
type corpusCase struct {
	agent  string
	name   string
	screen string
}

func probeCases(t *testing.T) []corpusCase {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("testdata", "probes", "*.screen.txt"))
	if err != nil {
		t.Fatal(err)
	}
	var cases []corpusCase
	for _, file := range files {
		name := strings.TrimSuffix(filepath.Base(file), ".screen.txt")
		agent, name, ok := strings.Cut(name, "__")
		if !ok {
			t.Fatalf("invalid probe filename %s", file)
		}
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		cases = append(cases, corpusCase{agent, name, string(data)})
	}
	return cases
}

func TestDifferentialAgainstHerdr(t *testing.T) {
	if _, err := exec.LookPath("herdr"); err != nil {
		t.Skip("herdr not installed; skipping differential")
	}
	version, err := exec.Command("herdr", "--version").Output()
	if err != nil {
		t.Fatalf("herdr --version: %v", err)
	}
	if want := "herdr " + strings.TrimPrefix(readLock(t).Tag, "v"); strings.TrimSpace(string(version)) != want {
		t.Skipf("herdr on PATH is %q, manifests are vendored from %q", strings.TrimSpace(string(version)), want)
	}
	xdg := t.TempDir()
	var env []string
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "HERDR_") && !strings.HasPrefix(entry, "XDG_") {
			env = append(env, entry)
		}
	}
	env = append(env, "XDG_CONFIG_HOME="+filepath.Join(xdg, "config"), "XDG_STATE_HOME="+filepath.Join(xdg, "state"))

	// Compare only the vendored catalog. HomeShell-owned overlays intentionally
	// differ from upstream and have their own focused tests.
	files, err := fs.Glob(manifestFiles, "manifests/herdr/*.toml")
	if err != nil {
		t.Fatal(err)
	}
	upstream := map[string]ruleSet{}
	for _, file := range files {
		data, err := manifestFiles.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		rules, err := parseRuleSet(data)
		if err != nil {
			t.Fatal(err)
		}
		upstream[rules.id] = rules
	}

	cases := append([]corpusCase(nil), differentialCorpus...)
	for _, probe := range probeCases(t) {
		// herdr panics on CRLF screen text (manifest.rs line_start_offset).
		if !strings.Contains(probe.screen, "\r") {
			cases = append(cases, probe)
		}
	}
	for _, c := range cases {
		t.Run(c.agent+"/"+c.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "screen.txt")
			if err := os.WriteFile(path, []byte(c.screen), 0o644); err != nil {
				t.Fatal(err)
			}

			cmd := exec.Command("herdr", "agent", "explain", "--file", path, "--agent", c.agent, "--json")
			cmd.Env = env
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("herdr explain: %v", err)
			}
			var h struct {
				State       string `json:"state"`
				MatchedRule *struct {
					ID string `json:"id"`
				} `json:"matched_rule"`
				SkipStateUpdate bool `json:"skip_state_update"`
			}
			if err := json.Unmarshal(out, &h); err != nil {
				t.Fatalf("parse herdr json: %v\n%s", err, out)
			}
			herdrRule := ""
			if h.MatchedRule != nil {
				herdrRule = h.MatchedRule.ID
			}

			rules, ok := upstream[c.agent]
			if !ok {
				t.Fatalf("no vendored rules for %s", c.agent)
			}
			res := rules.evaluate(Snapshot{Agent: c.agent, Screen: c.screen})

			if res.MatchedRule != herdrRule {
				t.Errorf("matched-rule mismatch: herdr=%q ours=%q (herdr state=%q, ours=%v)\n--- screen ---\n%s---",
					herdrRule, res.MatchedRule, h.State, res.State, c.screen)
			}
			if res.SkipStateUpdate != h.SkipStateUpdate {
				t.Errorf("skip_state_update mismatch: herdr=%v ours=%v\n--- screen ---\n%s---",
					h.SkipStateUpdate, res.SkipStateUpdate, c.screen)
			}
			if want := herdrStateToDomain(h.State); res.State != want {
				t.Errorf("state mismatch: herdr=%q(%v) ours=%v\n--- screen ---\n%s---",
					h.State, want, res.State, c.screen)
			}
		})
	}
}

// herdrStateToDomain maps herdr's detection state string to our normalized
// vocabulary the same way mapState does (unknown -> idle).
func herdrStateToDomain(s string) domain.SessionState {
	switch s {
	case "working":
		return domain.SessionRunning
	case "blocked":
		return domain.SessionWaiting
	default: // "idle" or "unknown"
		return domain.SessionIdle
	}
}

// differentialCorpus is a broad, synthetic (no real project data) set of
// snapshots spanning every screen-region rule and the region-boundary edge
// cases most likely to reveal a port divergence.
var differentialCorpus = []corpusCase{
	// ---- claude: idle / prompt box ----
	{"claude", "idle-prompt-box", "● Done.\n\n────────────────────────────────────────\n❯\n────────────────────────────────────────\n  [Opus 4.8] │ ⏵⏵ accept edits\n"},
	{"claude", "idle-prompt-box-no-trailing-nl", "● Done.\n\n────────────────────────────────────────\n❯\n────────────────────────────────────────\n  [Opus 4.8] │ ⏵⏵ accept edits"},
	{"claude", "idle-prompt-with-typed-text", "output\n\n────────────────────────────────────────\n❯ some typed text\n────────────────────────────────────────\n  footer\n"},
	{"claude", "bare-prompt-no-box", "some scrollback\n❯\nmore text\n"},
	{"claude", "empty", ""},
	{"claude", "whitespace-only", "   \n  \n\t\n"},
	{"claude", "plain-output-no-chrome", "● Reading files.\n  Working through the change.\n"},

	// ---- claude: permission prompts ----
	{"claude", "bash-permission-strong", "● I'll run the tests.\n\n  Bash command\n    cargo test --all\n\n  Do you want to proceed?\n    1. Yes\n    2. Yes, and don't ask again\n    3. No, and tell Claude what to do differently (esc to cancel)\n"},
	{"claude", "bash-permission-arrow-numbered", "  Bash command\n    ls -la\n\n  Do you want to proceed?\n  ❯ 1. Yes\n    2. No\n"},
	{"claude", "proceed-no-options", "Do you want to proceed?\n"},
	{"claude", "proceed-esc-only-no-options", "Do you want to proceed?\n  esc to cancel\n"},
	{"claude", "generic-permission-after-rule", "● Edit file\n────────────────────────────────────────\n  Do you want to proceed?\n  ❯ 1. Yes\n    2. No, keep looking (esc to cancel)\n"},

	// ---- claude: selection form ----
	{"claude", "live-blocked-form", "● Pick one\n────────────────────────────────────────\n  Choose an option\n  ↑/↓ to navigate · enter to select · esc to cancel\n"},
	{"claude", "form-missing-nav-hint", "────────────────────────────────────────\n  enter to select · esc to cancel\n"},

	// ---- claude: viewers (skip_state_update) ----
	{"claude", "transcript-viewer", "some transcript body\nshowing detailed transcript · ctrl+o to toggle\n"},
	{"claude", "transcript-viewer-shortcuts", "showing detailed transcript\n? for shortcuts\n"},
	{"claude", "model-picker", "Select model\n  1. Opus\n  2. Sonnet\n  enter to set as default · esc to cancel\n"},

	// ---- claude: dynamic workflow ----
	{"claude", "dynamic-workflow", "Run a dynamic workflow?\n  esc to cancel\n"},

	// ---- claude: legacy blockers + not-gate ----
	{"claude", "legacy-waiting-permission", "waiting for permission to continue\n"},
	{"claude", "legacy-review-answers", "Please review your answers before continuing\n"},
	{"claude", "legacy-would-you-like-yes", "Would you like to continue?\n  yes\n  no\n"},
	{"claude", "legacy-not-gate-bare-prompt", "some output\n❯\n"},

	// ---- claude: region-boundary edge cases ----
	{"claude", "stacked-rules", "a\n────────\n────────\nb\n────────\nc\n"},
	{"claude", "multiline-prompt-body", "top\n────────\nline one\n❯\nline three\n────────\nfooter\n"},
	{"claude", "two-char-rule", "top\n──\n❯\n──\nfooter\n"},
	{"claude", "rule-with-label", "top\n─── section ───\n❯\n────────\nfooter\n"},
	{"claude", "box-drawing-permission", "╭──────────────────────╮\n│ Do you want to proceed? │\n│ ❯ 1. Yes               │\n│   2. No                │\n╰──────────────────────╯\n"},

	// ---- codex ----
	{"codex", "idle-prompt-marker", "• Ran build\n  ok\n\n›\n"},
	{"codex", "idle-prompt-marker-space", "• Summary\n\n› \n"},
	{"codex", "allow-command", "• Proposed command\n\n  Allow command?\n    cargo test\n\n  press enter to confirm or esc to cancel\n"},
	{"codex", "enter-to-submit-answer", "› \n  enter to submit answer\n"},
	{"codex", "weak-blocker-yn", "Continue building? [y/n]\n"},
	{"codex", "weak-blocker-do-you-want-yes", "Do you want to overwrite?\n  yes\n  no\n"},
	{"codex", "transcript-viewer", "› \n↑/↓ to scroll · pgup/pgdn to page · home/end to jump · q to quit · esc to edit prev\n"},
	{"codex", "prompt-then-block-marker", "› \n• new block started\ncontent\n"},
	{"codex", "empty", ""},
	{"codex", "plain-output", "• Working on the change.\n  Reading files.\n"},

	// ---- gemini ----
	{"gemini", "apply-change", "✔ ReadFile main.go\n\n│ Apply this change?\n│   ● Yes\n│   ○ No\n"},
	{"gemini", "allow-execution", "│ Allow execution of: go test\n│   ● Yes\n│   ○ No\n"},
	{"gemini", "working-esc-cancel", "✦ Thinking…\n  (esc to cancel)\n"},
	{"gemini", "prompt-yes-allow-line", "❯ Yes, allow\n"},
	{"gemini", "waiting-confirmation-yes", "waiting for user confirmation\n  yes / no\n"},
	{"gemini", "plain-output-idle", "✦ Here is the summary of the change.\n"},
	{"gemini", "empty", ""},
}
