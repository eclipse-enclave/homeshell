package detect

import "strings"

// Rust's \s is Unicode White_Space; Go's is [\t\n\f\r ] only. Claude draws its
// prompt as ❯ plus NBSP, so a ported \s must also match \v, NBSP and friends.
const (
	unicodeSpaceClass    = `[\s\v\p{Z}\x{85}]`
	unicodeSpaceInClass  = `\s\v\p{Z}\x{85}`
	unicodeNonSpaceClass = `[^\s\v\p{Z}\x{85}]`
)

// translateRegex rewrites the Rust regex syntax herdr manifests use into Go RE2:
// \uXXXX, \u{X} and \UXXXXXXXX become \x{...}, \p{Alphabetic} becomes letters plus
// letter numbers (Go has no Other_Alphabetic), and \s/\S become Unicode-aware
// whitespace classes. \d, \w and \b stay ASCII-only in Go; no herdr manifest
// currently relies on their Unicode behavior.
func translateRegex(pattern string) string {
	var out strings.Builder
	inClass := false
	classStart := false // just after '[' or '[^', where ']' is a literal
	for i := 0; i < len(pattern); i++ {
		char := pattern[i]
		if char != '\\' || i+1 == len(pattern) {
			switch {
			case char == '[' && !inClass:
				inClass, classStart = true, true
				out.WriteByte(char)
				if i+1 < len(pattern) && pattern[i+1] == '^' {
					out.WriteByte('^')
					i++
				}
				continue
			case char == ']' && inClass && !classStart:
				inClass = false
			}
			classStart = false
			out.WriteByte(char)
			continue
		}
		classStart = false
		rest := pattern[i:]
		if replacement, consumed, ok := translateEscape(rest, inClass); ok {
			out.WriteString(replacement)
			i += consumed - 1
			continue
		}
		// Any other escape, including \\, passes through as a pair.
		out.WriteString(rest[:2])
		i++
	}
	return out.String()
}

// translateEscape rewrites one escape at the start of rest (which begins with a
// backslash) and reports how many bytes it consumed.
func translateEscape(rest string, inClass bool) (string, int, bool) {
	switch rest[1] {
	case 's':
		if inClass {
			return unicodeSpaceInClass, 2, true
		}
		return unicodeSpaceClass, 2, true
	case 'S':
		if !inClass {
			return unicodeNonSpaceClass, 2, true
		}
	case 'u', 'U':
		if strings.HasPrefix(rest[2:], "{") {
			end := strings.IndexByte(rest, '}')
			if end > 3 && isHex(rest[3:end]) {
				return `\x{` + rest[3:end] + `}`, end + 1, true
			}
			return "", 0, false
		}
		digits := 4
		if rest[1] == 'U' {
			digits = 8
		}
		if len(rest) >= 2+digits && isHex(rest[2:2+digits]) {
			return `\x{` + rest[2:2+digits] + `}`, 2 + digits, true
		}
	case 'p':
		const alphabetic = `\p{Alphabetic}`
		if strings.HasPrefix(rest, alphabetic) {
			if inClass {
				return `\p{L}\p{Nl}`, len(alphabetic), true
			}
			return `[\p{L}\p{Nl}]`, len(alphabetic), true
		}
	}
	return "", 0, false
}

func isHex(digits string) bool {
	for _, digit := range digits {
		if !strings.ContainsRune("0123456789abcdefABCDEF", digit) {
			return false
		}
	}
	return digits != ""
}
