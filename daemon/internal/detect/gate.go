// Derived from herdr src/detect/manifest.rs at v0.9.5 (Apache-2.0); modified.
// Source provenance is recorded in manifests/herdr/manifests.lock.

package detect

import (
	"regexp"
	"strings"
)

// gate is the raw TOML matcher predicate. It mirrors herdr's manifest gate.
// contains needles are matched case-insensitively; regex matches the whole
// region text; line_regex matches if any single line matches.
type gate struct {
	Contains  []string `toml:"contains"`
	Regex     []string `toml:"regex"`
	LineRegex []string `toml:"line_regex"`
	All       []gate   `toml:"all"`
	Any       []gate   `toml:"any"`
	Not       []gate   `toml:"not"`
}

// compiledGate is a gate with regexes compiled and contains needles
// lowercased once, ready for repeated matching.
type compiledGate struct {
	contains  []string // already lowercased
	regex     []*regexp.Regexp
	lineRegex []*regexp.Regexp
	all       []compiledGate
	any       []compiledGate
	not       []compiledGate
}

func toLower(s string) string { return strings.ToLower(s) }

func (g gate) compile() (compiledGate, error) {
	var cg compiledGate
	for _, needle := range g.Contains {
		cg.contains = append(cg.contains, strings.ToLower(needle))
	}
	var err error
	if cg.regex, err = compileAll(g.Regex); err != nil {
		return compiledGate{}, err
	}
	if cg.lineRegex, err = compileAll(g.LineRegex); err != nil {
		return compiledGate{}, err
	}
	if cg.all, err = compileGates(g.All); err != nil {
		return compiledGate{}, err
	}
	if cg.any, err = compileGates(g.Any); err != nil {
		return compiledGate{}, err
	}
	if cg.not, err = compileGates(g.Not); err != nil {
		return compiledGate{}, err
	}
	return cg, nil
}

func compileAll(patterns []string) ([]*regexp.Regexp, error) {
	var out []*regexp.Regexp
	for _, p := range patterns {
		re, err := regexp.Compile(translateRegex(p))
		if err != nil {
			return nil, err
		}
		out = append(out, re)
	}
	return out, nil
}

func compileGates(gates []gate) ([]compiledGate, error) {
	var out []compiledGate
	for _, g := range gates {
		cg, err := g.compile()
		if err != nil {
			return nil, err
		}
		out = append(out, cg)
	}
	return out, nil
}

// matches reports whether the gate passes against text. lower must be the
// lowercased form of text (computed once by the caller and threaded down).
// Semantics: all contains present AND all regex match AND for each line_regex
// some line matches AND all all-gates pass AND (any empty OR some any-gate
// passes) AND no not-gate passes.
func (g compiledGate) matches(text, lower string) bool {
	for _, needle := range g.contains {
		if !strings.Contains(lower, needle) {
			return false
		}
	}
	for _, re := range g.regex {
		if !re.MatchString(text) {
			return false
		}
	}
	for _, re := range g.lineRegex {
		if !anyLineMatches(text, re) {
			return false
		}
	}
	for _, sub := range g.all {
		if !sub.matches(text, lower) {
			return false
		}
	}
	if len(g.any) > 0 {
		matched := false
		for _, sub := range g.any {
			if sub.matches(text, lower) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	for _, sub := range g.not {
		if sub.matches(text, lower) {
			return false
		}
	}
	return true
}

func anyLineMatches(text string, re *regexp.Regexp) bool {
	for _, line := range strings.Split(text, "\n") {
		if re.MatchString(line) {
			return true
		}
	}
	return false
}
