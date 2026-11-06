package detect

import (
	"regexp"
	"testing"
)

func TestTranslateRegex(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		want    string
	}{
		{"\\u four digits", "^\\" + "u28FF", `^\x{28FF}`},
		{"\\u braces", `\u{fe0f}?`, `\x{fe0f}?`},
		{"\\U eight digits", `\U0001F600`, `\x{0001F600}`},
		{"alphabetic outside class", `\p{Alphabetic}+`, `[\p{L}\p{Nl}]+`},
		{"alphabetic inside class", `[\p{Alphabetic}_]`, `[\p{L}\p{Nl}_]`},
		{"\\s outside class", `^\s*x`, `^` + unicodeSpaceClass + `*x`},
		{"\\s inside class", `[\s.]`, `[` + unicodeSpaceInClass + `.]`},
		{"\\s inside negated class", `[^\s]`, `[^` + unicodeSpaceInClass + `]`},
		{"\\S outside class", `\S+`, unicodeNonSpaceClass + `+`},
		{"\\S inside class remains ASCII", `[\S]`, `[\S]`},
		{"literal ] opens class", `[]\s]`, `[]` + unicodeSpaceInClass + `]`},
		{"escaped backslash is not an escape", `\\s`, `\\s`},
		{"\\d, \\w, \\b untouched", `\d\w\b`, `\d\w\b`},
		{"go escapes untouched", `\x{2800}-\x{28FF}`, `\x{2800}-\x{28FF}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := translateRegex(tt.pattern)
			if got != tt.want {
				t.Errorf("translateRegex(%q) = %q, want %q", tt.pattern, got, tt.want)
			}
			if _, err := regexp.Compile(got); err != nil {
				t.Errorf("translated pattern does not compile: %v", err)
			}
		})
	}
}

func TestTranslatedNonSpaceExcludesUnicodeWhitespace(t *testing.T) {
	translated := regexp.MustCompile("^" + translateRegex(`\S`) + "$")
	for _, space := range []string{" ", "\u00a0", "\u2003", "\v", "\u0085"} {
		if translated.MatchString(space) {
			t.Errorf("%q should not match \\S", space)
		}
	}
	if !translated.MatchString("x") {
		t.Error("a non-space should match \\S")
	}
}

// TestTranslatedSpaceMatchesUnicodeWhitespace is why \s is translated at all:
// Claude's prompt is the chevron followed by NBSP, which Go's \s does not match.
func TestTranslatedSpaceMatchesUnicodeWhitespace(t *testing.T) {
	const chevron = "❯"
	translated := regexp.MustCompile(translateRegex(`^` + chevron + `\s*$`))
	for _, space := range []string{" ", "\u00a0", "\u2003", "\v", "\u0085"} {
		if !translated.MatchString(chevron + space) {
			t.Errorf("%q should match", chevron+space)
		}
	}
	if regexp.MustCompile(`^` + chevron + `\s*$`).MatchString(chevron + "\u00a0") {
		t.Error("Go's own \\s matched NBSP; the translation may no longer be needed")
	}
}
