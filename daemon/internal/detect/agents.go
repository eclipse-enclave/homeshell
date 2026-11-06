package detect

import "sort"

// AgentInfo describes an agent this package can classify: the rule set we ship
// for it, where it came from, and any caveat.
type AgentInfo struct {
	// Label is the primary agent label (the rule-set id) accepted by Detect.
	Label string
	// Aliases are additional labels that resolve to the same rule set.
	Aliases []string
	// HookAuthoritative marks agents that herdr treats as reporting their own
	// lifecycle via hooks, so terminal-screen detection is a secondary, less
	// complete signal for them. HomeShell currently excludes such agents from
	// SupportedAgents; retaining the metadata makes an accidental opt-in visible
	// to the tests. See hookAuthorityLabels.
	HookAuthoritative bool
}

// hookAuthorityLabels mirrors herdr's full_lifecycle_hook_authority set
// (herdr src/detect/mod.rs). For these agents herdr trusts lifecycle hooks over
// the screen, so screen-only detection (all this package does) is a secondary
// signal. Listed here for reference even for agents we do not ship a rule set
// for.
//
// Note: two of these — "omp" and "mastracode" — have NO herdr screen manifest
// at all (they are hook-only in herdr). They therefore cannot be supported by
// screen detection: there are no rules to port.
var hookAuthorityLabels = map[string]bool{
	"pi":         true,
	"omp":        true,
	"mastracode": true,
	"hermes":     true,
	"opencode":   true,
	"kilo":       true,
	"kimi":       true,
}

// runtimeScreenAgent reports whether HomeShell intentionally enables a
// vendored manifest for live session classification. The complete herdr
// catalog is embedded and validated so syncing catches engine drift, but a
// newly published manifest must not silently turn into HomeShell behavior.
// In particular, hook-authoritative agents need lifecycle integration that
// HomeShell does not implement; their screen rules are only a fallback inside
// herdr's source-arbitration model.
func runtimeScreenAgent(label string) bool {
	if hookAuthorityLabels[label] {
		return false
	}
	switch label {
	case "amp", "agy", "claude", "cline", "codex", "copilot", "cursor",
		"devin", "droid", "gemini", "grok", "kiro", "letta",
		"maki", "muse", "qodercli", "qwen":
		return true
	default:
		return false
	}
}

// SupportedAgents lists the vendored rule sets HomeShell has explicitly enabled
// for runtime screen detection, sorted by label. Pass any returned Label (or
// Alias) to Detect.
func SupportedAgents() []AgentInfo {
	_, sets := enabledRuleSets()
	infos := make([]AgentInfo, 0, len(sets))
	for _, rs := range sets {
		infos = append(infos, AgentInfo{
			Label:             rs.id,
			Aliases:           append([]string(nil), rs.aliases...),
			HookAuthoritative: hookAuthorityLabels[rs.id],
		})
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].Label < infos[j].Label })
	return infos
}

// Supported reports whether Detect has a rule set for the given agent label or
// alias.
func Supported(agent string) bool {
	registry, _ := enabledRuleSets()
	_, ok := registry[agent]
	return ok
}
