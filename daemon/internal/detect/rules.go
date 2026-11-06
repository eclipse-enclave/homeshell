// Derived from herdr src/detect/manifest.rs at v0.9.5 (Apache-2.0); modified.
// Source provenance is recorded in manifests/herdr/manifests.lock.

package detect

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/BurntSushi/toml"
)

// manifestFiles holds herdr's byte-for-byte TOML manifests.
//
//go:embed manifests/herdr/*.toml manifests/local/*.toml
var manifestFiles embed.FS

// engineVersion is herdr's MANIFEST_ENGINE_VERSION this engine implements. A
// manifest that needs a newer engine uses regions or semantics we lack.
const engineVersion = 3

// herdr's manifest limits (manifest.rs MAX_*), enforced at load like herdr does.
const (
	maxRulesPerManifest = 128
	maxGateDepth        = 8
	maxTotalGates       = 512
	maxMatchersPerGate  = 32
	maxTotalMatchers    = 1024
	maxMatcherChars     = 512
)

// topNonEmptyLinesEngineVersion is the engine that introduced top_non_empty_lines.
const topNonEmptyLinesEngineVersion = 3

// rawRuleSet / rawRule mirror herdr's TOML manifest schema.
type rawRuleSet struct {
	ID               string    `toml:"id"`
	Version          string    `toml:"version"`
	MinEngineVersion int       `toml:"min_engine_version"`
	UpdatedAt        string    `toml:"updated_at"`
	Aliases          []string  `toml:"aliases"`
	Rules            []rawRule `toml:"rules"`
}

type rawRule struct {
	ID              string `toml:"id"`
	State           string `toml:"state"`
	Priority        int    `toml:"priority"`
	Region          string `toml:"region"`
	VisibleIdle     bool   `toml:"visible_idle"`
	VisibleBlocker  bool   `toml:"visible_blocker"`
	VisibleWorking  bool   `toml:"visible_working"`
	SkipStateUpdate bool   `toml:"skip_state_update"`
	gate                   // contains/regex/line_regex/all/any/not, promoted
}

type ruleSet struct {
	id      string
	aliases []string
	rules   []compiledRule
}

type compiledRule struct {
	id              string
	state           string
	priority        int
	region          string
	visibleBlocker  bool
	visibleIdle     bool
	skipStateUpdate bool
	gate            compiledGate
}

// decodeRuleSet decodes herdr's TOML directly and rejects unknown keys like
// herdr's deny_unknown_fields instead of silently dropping a matcher.
func decodeRuleSet(data []byte) (rawRuleSet, error) {
	var raw rawRuleSet
	metadata, err := toml.Decode(string(data), &raw)
	if err != nil {
		return rawRuleSet{}, err
	}
	if undecoded := metadata.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, len(undecoded))
		for index, key := range undecoded {
			keys[index] = key.String()
		}
		return rawRuleSet{}, fmt.Errorf("unknown fields: %s", strings.Join(keys, ", "))
	}
	return raw, nil
}

func parseRuleSet(data []byte) (ruleSet, error) {
	raw, err := decodeRuleSet(data)
	if err != nil {
		return ruleSet{}, err
	}
	if err := validateManifest(raw); err != nil {
		return ruleSet{}, err
	}
	rs := ruleSet{id: raw.ID, aliases: raw.Aliases}
	rs.rules, err = compileRules(raw.Rules)
	if err != nil {
		return ruleSet{}, err
	}
	return rs, nil
}

func compileRules(rules []rawRule) ([]compiledRule, error) {
	var compiled []compiledRule
	for _, rule := range rules {
		matcher, err := rule.gate.compile()
		if err != nil {
			return nil, fmt.Errorf("rule %q: %w", rule.ID, err)
		}
		region := rule.Region
		if region == "" {
			region = "whole_recent" // herdr's default region
		}
		compiled = append(compiled, compiledRule{
			id: rule.ID, state: rule.State, priority: rule.Priority, region: region,
			visibleBlocker: rule.VisibleBlocker, visibleIdle: rule.VisibleIdle, skipStateUpdate: rule.SkipStateUpdate,
			gate: matcher,
		})
	}
	return compiled, nil
}

// validateManifest ports herdr's validate_manifest plus the engine-version gate
// herdr applies to remote manifests.
func validateManifest(raw rawRuleSet) error {
	if strings.TrimSpace(raw.ID) == "" {
		return errors.New("manifest id must not be empty")
	}
	if raw.MinEngineVersion > engineVersion {
		return fmt.Errorf("manifest %q requires engine %d, this engine is %d", raw.ID, raw.MinEngineVersion, engineVersion)
	}
	if len(raw.Rules) == 0 {
		return errors.New("manifest must contain at least one rule")
	}
	if len(raw.Rules) > maxRulesPerManifest {
		return fmt.Errorf("manifest contains %d rules, max is %d", len(raw.Rules), maxRulesPerManifest)
	}
	var totals gateTotals
	for _, rule := range raw.Rules {
		if err := validateRule(raw, rule, &totals); err != nil {
			return fmt.Errorf("rule %q: %w", rule.ID, err)
		}
	}
	return nil
}

func validateRule(raw rawRuleSet, rule rawRule, totals *gateTotals) error {
	if strings.TrimSpace(rule.ID) == "" {
		return errors.New("rule id must not be empty")
	}
	switch rule.State {
	case "", "idle", "working", "blocked", "unknown":
	default:
		return fmt.Errorf("unknown state %q", rule.State)
	}
	if rule.SkipStateUpdate {
		if rule.State != "unknown" {
			return errors.New(`skip_state_update requires state = "unknown"`)
		}
		if rule.VisibleIdle || rule.VisibleBlocker || rule.VisibleWorking {
			return errors.New("skip_state_update with visible state evidence")
		}
	}
	region := rule.Region
	if region == "" {
		region = "whole_recent"
	}
	if !validRegionSpec(region) {
		return fmt.Errorf("unknown region %q", region)
	}
	if strings.HasPrefix(strings.TrimSpace(region), "top_non_empty_lines(") &&
		raw.MinEngineVersion != 0 && raw.MinEngineVersion < topNonEmptyLinesEngineVersion {
		return fmt.Errorf("uses top_non_empty_lines but min_engine_version is below %d", topNonEmptyLinesEngineVersion)
	}
	return validateGate(rule.gate, "rule", 0, false, totals)
}

type gateTotals struct{ gates, matchers int }

// validateGate ports herdr's validate_gate and validate_not_gate: a not gate
// needs any matcher, every other gate a positive one.
func validateGate(spec gate, context string, depth int, negated bool, totals *gateTotals) error {
	if depth > maxGateDepth {
		return fmt.Errorf("%s exceeds max gate depth %d", context, maxGateDepth)
	}
	totals.gates++
	if totals.gates > maxTotalGates {
		return fmt.Errorf("manifest exceeds max gate count %d", maxTotalGates)
	}
	matchers := len(spec.Contains) + len(spec.Regex) + len(spec.LineRegex)
	if matchers > maxMatchersPerGate {
		return fmt.Errorf("%s has %d direct matchers, max is %d", context, matchers, maxMatchersPerGate)
	}
	totals.matchers += matchers
	if totals.matchers > maxTotalMatchers {
		return fmt.Errorf("manifest exceeds max matcher count %d", maxTotalMatchers)
	}
	for _, values := range [][]string{spec.Contains, spec.Regex, spec.LineRegex} {
		for _, value := range values {
			if utf8.RuneCountInString(value) > maxMatcherChars {
				return fmt.Errorf("%s matcher exceeds max length %d", context, maxMatcherChars)
			}
		}
	}
	hasPositive := matchers > 0 || len(spec.All) > 0 || len(spec.Any) > 0
	if negated && !hasPositive && len(spec.Not) == 0 {
		return errors.New("not gate must contain a matcher")
	}
	if !negated && !hasPositive {
		return fmt.Errorf("%s must contain a positive matcher", context)
	}
	for _, nested := range spec.All {
		if err := validateGate(nested, "all gate", depth+1, false, totals); err != nil {
			return err
		}
	}
	for _, nested := range spec.Any {
		if err := validateGate(nested, "any gate", depth+1, false, totals); err != nil {
			return err
		}
	}
	for _, nested := range spec.Not {
		if err := validateGate(nested, "not gate", depth+1, true, totals); err != nil {
			return err
		}
	}
	return nil
}

// applyOverlay appends homeshell's local rules to the vendored rule set they
// name. Overlay rules are validated like vendored ones and must not reuse an id.
func applyOverlay(vendored ruleSet, data []byte) (ruleSet, error) {
	raw, err := decodeRuleSet(data)
	if err != nil {
		return ruleSet{}, err
	}
	if raw.ID != vendored.id {
		return ruleSet{}, fmt.Errorf("overlay for %q applied to %q", raw.ID, vendored.id)
	}
	if err := validateManifest(raw); err != nil {
		return ruleSet{}, err
	}
	known := map[string]bool{}
	for _, rule := range vendored.rules {
		known[rule.id] = true
	}
	for _, rule := range raw.Rules {
		if known[rule.ID] {
			return ruleSet{}, fmt.Errorf("overlay rule %q collides with another rule", rule.ID)
		}
		known[rule.ID] = true
	}
	added, err := compileRules(raw.Rules)
	if err != nil {
		return ruleSet{}, err
	}
	merged := vendored
	merged.rules = append(append([]compiledRule(nil), vendored.rules...), added...)
	return merged, nil
}

// enabledRuleSets is built on first use rather than at package init, because
// every `homeshell bridge` process links this package without detecting.
// registry maps agent label (and aliases) to its compiled rule set; sets holds
// each distinct rule set once, in filename order, for enumeration (see
// SupportedAgents).
var enabledRuleSets = sync.OnceValues(func() (registry map[string]ruleSet, sets []ruleSet) {
	all, err := loadRuleSets(manifestFiles)
	if err != nil {
		panic(fmt.Sprintf("detect: %v", err))
	}
	registry = map[string]ruleSet{}
	for _, set := range all {
		if !runtimeScreenAgent(set.id) {
			continue
		}
		for _, label := range append([]string{set.id}, set.aliases...) {
			if _, taken := registry[label]; taken {
				panic(fmt.Sprintf("detect: agent label %q is claimed by two manifests", label))
			}
			registry[label] = set
		}
		sets = append(sets, set)
	}
	return registry, sets
})

// loadRuleSets parses every manifest in herdr's published catalog, then appends
// the local overlays. Loading the complete catalog is deliberate: a manifest
// that needs a newer engine must fail during review rather than leaving the
// vendored release only partly validated. init separately selects the small set
// HomeShell has explicitly enabled at runtime.
func loadRuleSets(files fs.FS) ([]ruleSet, error) {
	vendored, err := fs.Glob(files, "manifests/herdr/*.toml")
	if err != nil {
		return nil, err
	}
	var sets []ruleSet
	index := map[string]int{}
	for _, name := range vendored {
		data, err := fs.ReadFile(files, name)
		if err != nil {
			return nil, err
		}
		parsed, err := parseRuleSet(data)
		if err != nil {
			return nil, fmt.Errorf("parsing %s: %w", name, err)
		}
		index[parsed.id] = len(sets)
		sets = append(sets, parsed)
	}
	overlays, err := fs.Glob(files, "manifests/local/*.toml")
	if err != nil {
		return nil, err
	}
	for _, name := range overlays {
		data, err := fs.ReadFile(files, name)
		if err != nil {
			return nil, err
		}
		id := strings.TrimSuffix(path.Base(name), ".toml")
		position, ok := index[id]
		if !ok {
			return nil, fmt.Errorf("overlay %s: no vendored manifest %q", name, id)
		}
		if sets[position], err = applyOverlay(sets[position], data); err != nil {
			return nil, fmt.Errorf("overlay %s: %w", name, err)
		}
	}
	return sets, nil
}
