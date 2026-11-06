package detect

import (
	"strings"
	"unicode"
)

// CleanTitle strips a leading status glyph an agent prefixes to its
// terminal title, leaving the human-readable label. Claude sets the title to
// "<glyph> <label>". We drop a leading whitespace-separated token that contains
// no letters or digits, so a glyph prefix is removed while a normal title is
// left intact. A title with no letters/digits at all yields "" so the caller
// keeps the prior title rather than showing bare decoration.
func CleanTitle(raw string) string {
	title := strings.TrimSpace(stripControls(raw))
	if first, rest, found := strings.Cut(title, " "); found && !hasAlphanumeric(first) {
		title = strings.TrimSpace(rest)
	}
	if !hasAlphanumeric(title) {
		return ""
	}
	return title
}

func hasAlphanumeric(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			return true
		}
	}
	return false
}

// stripControls drops control and bidi-control runes, which a terminal or
// notification would act on rather than show; tabs and line breaks become spaces.
func stripControls(text string) string {
	return strings.Map(func(char rune) rune {
		switch {
		case char == '\t' || char == '\n' || char == '\r':
			return ' '
		case unicode.IsControl(char) || unicode.Is(unicode.Bidi_Control, char):
			return -1
		}
		return char
	}, text)
}
