// Derived from herdr src/detect/manifest.rs at v0.9.5 (Apache-2.0); modified.
// Source provenance is recorded in manifests/herdr/manifests.lock.

package detect

import (
	"strconv"
	"strings"
)

// region returns the slice of the snapshot a rule inspects, and whether spec
// names a region this package understands. Ported from herdr's
// src/detect/manifest.rs region() and helpers, covering every region of herdr's
// engine version 3. The ok result is the single source of truth for which specs
// are valid (see validRegionSpec).
func region(s Snapshot, spec string) (string, bool) {
	spec = strings.TrimSpace(spec)
	switch spec {
	case "osc_title":
		return s.OSCTitle, true
	case "osc_progress":
		return s.OSCProgress, true
	}
	content := s.Screen
	switch spec {
	case "whole_recent":
		return content, true
	case "prompt_box_body":
		return promptBoxBody(content), true
	case "after_last_horizontal_rule":
		return afterLastHorizontalRule(content), true
	case "after_last_prompt_marker":
		return afterLastPromptMarker(content), true
	case "before_current_prompt_marker":
		return beforeCurrentPromptMarker(content), true
	case "whole_recent_without_current_prompt_marker":
		if _, ok := currentCodexPromptIndex(splitLines(content)); ok {
			return "", true
		}
		return content, true
	case "current_prompt_block_marker":
		return currentPromptBlockMarker(content), true
	case "after_current_prompt_block_marker":
		return afterCurrentPromptBlockMarker(content), true
	case "above_prompt_box":
		return abovePromptBox(content), true
	case "last_non_empty_above_prompt_box":
		return lastNonEmptyLine(abovePromptBox(content)), true
	}
	if count, ok := regionCount(spec, "bottom_non_empty_lines"); ok {
		return bottomNonEmptyLines(content, count), true
	}
	if count, ok := regionCount(spec, "bottom_lines"); ok {
		return bottomLines(content, count), true
	}
	if count, ok := topRegionCount(spec); ok {
		return topNonEmptyLines(content, count), true
	}
	return "", false
}

// validRegionSpec reports whether spec names a region this package can extract.
// It drives parse-time validation off region itself (with an empty snapshot, so
// only spec recognition matters), keeping the two in sync so a typo'd region in
// a manifest fails loudly when the manifests load instead of silently matching against "".
func validRegionSpec(spec string) bool {
	_, ok := region(Snapshot{}, spec)
	return ok
}

func regionCount(spec, name string) (int, bool) {
	rest, ok := strings.CutPrefix(spec, name)
	if !ok {
		return 0, false
	}
	rest, ok = strings.CutPrefix(rest, "(")
	if !ok {
		return 0, false
	}
	rest, ok = strings.CutSuffix(rest, ")")
	if !ok {
		return 0, false
	}
	// Rust's usize parse rejects a minus sign; Atoi would accept one.
	count, err := strconv.Atoi(rest)
	if err != nil || strings.HasPrefix(rest, "-") {
		return 0, false
	}
	return count, true
}

// maxTopRegionLineCount mirrors herdr's MAX_TOP_REGION_LINE_COUNT (u16::MAX).
const maxTopRegionLineCount = 65535

// topRegionCount mirrors herdr's top_region_count, which is stricter than
// regionCount: digits only, no leading zero, at most maxTopRegionLineCount.
func topRegionCount(spec string) (int, bool) {
	rest, ok := strings.CutPrefix(spec, "top_non_empty_lines(")
	if !ok {
		return 0, false
	}
	rest, ok = strings.CutSuffix(rest, ")")
	if !ok || rest == "" || rest[0] == '0' {
		return 0, false
	}
	for _, digit := range rest {
		if digit < '0' || digit > '9' {
			return 0, false
		}
	}
	count, err := strconv.Atoi(rest)
	if err != nil || count > maxTopRegionLineCount {
		return 0, false
	}
	return count, true
}

// splitLines mirrors Rust's str::lines(): split on '\n' and drop the single
// trailing empty element produced by a final newline. Interior blank lines are
// preserved. Rendered terminal text is '\n'-separated, so '\r' is not handled.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, "\n")
	if n := len(parts); n > 0 && parts[n-1] == "" {
		parts = parts[:n-1]
	}
	return parts
}

// lineStartOffset is the byte offset of line index within content, assuming
// each preceding line was followed by one '\n'.
func lineStartOffset(lines []string, index int) int {
	if index > len(lines) {
		index = len(lines)
	}
	off := 0
	for i := 0; i < index; i++ {
		off += len(lines[i]) + 1
	}
	return off
}

func sliceFromLineIndex(content string, lines []string, index int) string {
	off := lineStartOffset(lines, index)
	if off > len(content) {
		off = len(content)
	}
	return content[off:]
}

// isHorizontalRule mirrors herdr's is_horizontal_rule: a trimmed line that
// starts with '─' chars and is either all rule chars or has >=3 leading ones.
func isHorizontalRule(line string) bool {
	t := strings.TrimSpace(line)
	if t == "" {
		return false
	}
	ruleChars := 0
	for _, r := range t {
		if r != '─' {
			break
		}
		ruleChars++
	}
	if ruleChars == 0 {
		return false
	}
	rest := strings.TrimLeft(t, "─")
	suffix := strings.TrimLeft(rest, " \t")
	return suffix == "" || ruleChars >= 3
}

// promptBoxTopBorderIndex returns the index of the prompt box's top border:
// the 2nd horizontal rule scanning from the bottom.
func promptBoxTopBorderIndex(lines []string) (int, bool) {
	count := 0
	for i := len(lines) - 1; i >= 0; i-- {
		if isHorizontalRule(lines[i]) {
			count++
			if count == 2 {
				return i, true
			}
		}
	}
	return 0, false
}

func promptBoxBody(content string) string {
	lines := splitLines(content)
	top, ok := promptBoxTopBorderIndex(lines)
	if !ok {
		return ""
	}
	start := lineStartOffset(lines, top+1)
	endIndex := len(lines)
	for rel, line := range lines[top+1:] {
		if isHorizontalRule(line) {
			endIndex = top + 1 + rel
			break
		}
	}
	end := lineStartOffset(lines, endIndex)
	if start > len(content) {
		start = len(content)
	}
	if end > len(content) {
		end = len(content)
	}
	if start > end {
		start = end
	}
	return content[start:end]
}

func afterLastHorizontalRule(content string) string {
	lastRuleEnd := 0
	offset := 0
	for _, line := range splitLines(content) {
		next := offset + len(line) + 1
		if isHorizontalRule(line) {
			if next > len(content) {
				lastRuleEnd = len(content)
			} else {
				lastRuleEnd = next
			}
		}
		offset = next
	}
	if lastRuleEnd > len(content) {
		lastRuleEnd = len(content)
	}
	return content[lastRuleEnd:]
}

// codexPromptLine reports whether a line is Codex's input prompt marker.
func codexPromptLine(line string) bool {
	return line == "›" || strings.HasPrefix(line, "› ")
}

func afterLastPromptMarker(content string) string {
	lines := splitLines(content)
	idx := -1
	for i, line := range lines {
		if codexPromptLine(line) {
			idx = i
		}
	}
	if idx < 0 {
		return content
	}
	return sliceFromLineIndex(content, lines, idx+1)
}

// lastHorizontalRuleIndex returns the index of the last horizontal rule in
// lines (scanning from the bottom). For Claude's layout this is the input box's
// bottom border; the status bar and footer sit below it.
func lastHorizontalRuleIndex(lines []string) (int, bool) {
	for i := len(lines) - 1; i >= 0; i-- {
		if isHorizontalRule(lines[i]) {
			return i, true
		}
	}
	return 0, false
}

// lastCodexPromptIndex returns the index of the last Codex '›' prompt line.
func lastCodexPromptIndex(lines []string) (int, bool) {
	idx := -1
	for i, line := range lines {
		if codexPromptLine(line) {
			idx = i
		}
	}
	if idx < 0 {
		return 0, false
	}
	return idx, true
}

// isEmptyPromptLine reports whether a line is just an input prompt marker with
// no text (Claude's "❯" or Codex's "›"), i.e. chrome, not content.
func isEmptyPromptLine(line string) bool {
	t := strings.TrimSpace(line)
	return t == "❯" || t == "›"
}

// isSpinnerLine reports whether a line is one of Claude's activity spinner /
// summary lines (e.g. "✻ Baked for 7s", "✽ Herding…"): they lead with a dingbat
// "star" glyph (U+2722–U+274F) that the spinner animates through, so they are
// ephemeral status, not content. Real assistant output leads with "●" (U+25CF),
// which is outside this range and so is kept.
func isSpinnerLine(line string) bool {
	t := strings.TrimSpace(line)
	if t == "" {
		return false
	}
	r := []rune(t)[0]
	return r >= 0x2722 && r <= 0x274F
}

// bottomContentLines returns up to count trailing lines of real content, joined
// by newlines. It always skips blank lines; skip, when non-nil, drops
// agent-specific chrome (borders, prompt, spinner) so a preview shows
// output/questions rather than furniture.
func bottomContentLines(content string, count int, skip func(string) bool) string {
	lines := splitLines(content)
	var kept []string
	for i := len(lines) - 1; i >= 0 && len(kept) < count; i-- {
		if strings.TrimSpace(lines[i]) == "" {
			continue
		}
		if skip != nil && skip(lines[i]) {
			continue
		}
		kept = append(kept, lines[i])
	}
	for i, j := 0, len(kept)-1; i < j; i, j = i+1, j-1 {
		kept[i], kept[j] = kept[j], kept[i]
	}
	return strings.Join(kept, "\n")
}

// isClaudeChrome reports whether a line is Claude Code input-box/status chrome:
// a box border, the empty "❯" prompt, or a "✻"/"✽" spinner/summary line. These
// shapes are specific to Claude Code's TUI, so this predicate is only applied to
// claude sessions (see contentTail).
func isClaudeChrome(line string) bool {
	return isHorizontalRule(line) || isEmptyPromptLine(line) || isSpinnerLine(line)
}

func bottomLines(content string, count int) string {
	lines := splitLines(content)
	start := len(lines) - count
	if start < 0 {
		start = 0
	}
	return sliceFromLineIndex(content, lines, start)
}

// bottomNonEmptyLines returns the content from the count-th non-empty line
// counting from the bottom (fewer if there are not that many).
func bottomNonEmptyLines(content string, count int) string {
	lines := splitLines(content)
	found := 0
	startIndex := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			found++
			startIndex = i
			if found == count {
				break
			}
		}
	}
	if startIndex < 0 {
		return ""
	}
	return sliceFromLineIndex(content, lines, startIndex)
}

// topNonEmptyLines returns the content up to and including the count-th
// non-empty line counting from the top.
func topNonEmptyLines(content string, count int) string {
	lines := splitLines(content)
	endIndex := -1
	found := 0
	for i, line := range lines {
		if found == count {
			break
		}
		if strings.TrimSpace(line) != "" {
			found++
			endIndex = i
		}
	}
	if endIndex < 0 {
		return ""
	}
	return content[:min(lineStartOffset(lines, endIndex+1), len(content))]
}

// codexBlockMarkerLine reports whether a line starts one of Codex's transcript
// blocks (bullet, stop square, failure cross or success tick).
func codexBlockMarkerLine(line string) bool {
	return strings.HasPrefix(line, "•") || strings.HasPrefix(line, "■") ||
		strings.HasPrefix(line, "✗") || strings.HasPrefix(line, "✓")
}

// currentCodexPromptIndex is the last '›' prompt line, unless a transcript
// block starts below it: then the marker is scrollback, not the live composer.
func currentCodexPromptIndex(lines []string) (int, bool) {
	promptIndex, ok := lastCodexPromptIndex(lines)
	if !ok {
		return 0, false
	}
	for _, line := range lines[promptIndex+1:] {
		if codexBlockMarkerLine(line) {
			return 0, false
		}
	}
	return promptIndex, true
}

func beforeCurrentPromptMarker(content string) string {
	lines := splitLines(content)
	promptIndex, ok := currentCodexPromptIndex(lines)
	if !ok {
		return content
	}
	return content[:min(lineStartOffset(lines, promptIndex), len(content))]
}

func currentPromptBlockMarker(content string) string {
	lines := splitLines(content)
	promptIndex, ok := currentCodexPromptIndex(lines)
	if !ok {
		return ""
	}
	for i := promptIndex - 1; i >= 0; i-- {
		if codexBlockMarkerLine(lines[i]) {
			return lines[i]
		}
	}
	return ""
}

func afterCurrentPromptBlockMarker(content string) string {
	lines := splitLines(content)
	promptIndex, ok := currentCodexPromptIndex(lines)
	if !ok {
		return ""
	}
	for i := promptIndex - 1; i >= 0; i-- {
		if codexBlockMarkerLine(lines[i]) {
			return sliceFromLineIndex(content, lines, i)
		}
	}
	return ""
}

// abovePromptBox is everything above the prompt box's top border, or the whole
// content when there is no box.
func abovePromptBox(content string) string {
	lines := splitLines(content)
	top, ok := promptBoxTopBorderIndex(lines)
	if !ok {
		return content
	}
	return content[:min(lineStartOffset(lines, top), len(content))]
}

func lastNonEmptyLine(content string) string {
	lines := splitLines(content)
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			return lines[i]
		}
	}
	return ""
}
