// Package bidi classifies text direction and splits mixed Persian/English
// strings into direction-homogeneous segments.
//
// Why this matters for DOCX: in WordprocessingML the *run* is the unit that
// carries direction. A run marked `<w:rtl/>` is an RTL embedding; one without it
// is LTR. If a single run holds both "شرکت نمونه" and "Example Co." Word has to
// guess, and the result is the classic mangled ordering users see when a value
// is dropped into a template verbatim. So when a value mixes scripts we emit one
// run per direction and let Word's own bidirectional algorithm lay them out.
//
// This is a pragmatic subset of UAX #9: it resolves strong characters, then
// assigns neutral runs (spaces, punctuation, digits) to the surrounding
// direction, falling back to the paragraph base direction when the neighbours
// disagree. Word applies the full algorithm at render time; our only job is to
// mark each run's embedding level correctly.
package bidi

import (
	"strings"
)

// Direction is the resolved embedding direction of a stretch of text.
type Direction uint8

const (
	Neutral Direction = iota
	LTR
	RTL
)

// IsFormattingRune reports whether r is a bidi formatting character, which
// should be ignored when classifying text.
func IsFormattingRune(r rune) bool {
	switch r {
	case 0x2066, 0x2067, 0x2068, 0x2069, 0x202A, 0x202B, 0x202C, 0x202D, 0x202E:
		return true
	}
	return false
}

// StripFormatting removes bidi formatting characters, leaving the text a reader
// actually sees. Used when comparing or measuring content.
func StripFormatting(s string) string {
	if !strings.ContainsAny(s, "⁦⁧⁨⁩‪‫‬‭‮") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if !IsFormattingRune(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ToLatinDigits normalises Persian and Arabic-Indic digits to Latin digits.
// ProcessMaker forms frequently receive Persian digits from users typing on a
// Persian keyboard; numeric formatting must run on the normalised form.
func ToLatinDigits(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 0x06F0 && r <= 0x06F9:
			b.WriteRune('0' + (r - 0x06F0))
		case r >= 0x0660 && r <= 0x0669:
			b.WriteRune('0' + (r - 0x0660))
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
