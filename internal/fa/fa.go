// Package fa folds Persian text so two spellings of one word compare equal:
// Arabic and Persian letters, the kashida, diacritics and the digit sets.
package fa

import (
	"strings"
	"unicode"
)

// Folding Persian text so two spellings of one word compare equal.
//
// EXTRACTED FROM internal/reports, where it was measured into existence: a
// reader searching the tender subjects for «نیکان» did not find the row that
// spells the town «نيكـان» -- an Arabic kaf with a kashida through it -- and the
// failure was invisible, because four other Nikan rows did match. Counted over
// the same column, «خرید» matches 289 rows as typed and 343 folded.
//
// It lives here because a SECOND caller appeared: the mail rules are searched
// too, and a rule written with an Arabic kaf has to be findable by somebody
// typing a Persian one. A copy in each package would have been the drift this
// exists to prevent -- the reports code says as much about its own two sides.
//
// The database is no help. Measured on the live server, 'ک' = 'ك' is FALSE
// under utf8mb4_0900_ai_ci, and so are the two forms of yeh, heh and alef.

var Folds = [][2]string{
	// Decoration first, so the letter rules below see plain letters.
	{"ـ", ""}, // kashida / tatweel: a typesetting stretch, never meaning
	{"‌", ""}, // ZWNJ: optional inside a compound word
	{"‍", ""}, // ZWJ
	{"‎", ""}, // left-to-right mark
	{"‏", ""}, // right-to-left mark

	// Harakat. Nobody types these consistently and they never distinguish
	// two words that would be searched for separately.
	{"ً", ""}, {"ٌ", ""}, {"ٍ", ""}, {"َ", ""},
	{"ُ", ""}, {"ِ", ""}, {"ّ", ""}, {"ْ", ""},
	{"ٔ", ""}, {"ٰ", ""},

	// The letters that have an Arabic twin on a standard keyboard. These are
	// the two that actually cost results in this database.
	{"ك", "ک"}, // ARABIC KAF -> PERSIAN KEHEH
	{"ي", "ی"}, // ARABIC YEH -> PERSIAN YEH
	{"ى", "ی"}, // ALEF MAKSURA -> PERSIAN YEH
	{"ئ", "ی"}, // YEH WITH HAMZA -> PERSIAN YEH

	// Alef and its hamza forms. A reader searching «اب» expects «آب»: the
	// madda is a pronunciation mark, not a different letter to look for.
	{"آ", "ا"}, // ALEF WITH MADDA
	{"أ", "ا"}, // ALEF WITH HAMZA ABOVE
	{"إ", "ا"}, // ALEF WITH HAMZA BELOW
	{"ٱ", "ا"}, // ALEF WASLA

	{"ة", "ه"}, // TEH MARBUTA -> HEH
	{"ؤ", "و"}, // WAW WITH HAMZA -> WAW

	// Digits. A customer number read off a screen arrives in whichever digits
	// the reader's keyboard produces; the column holds ASCII.
	{"۰", "0"}, {"۱", "1"}, {"۲", "2"}, {"۳", "3"},
	{"۴", "4"}, {"۵", "5"}, {"۶", "6"}, {"۷", "7"},
	{"۸", "8"}, {"۹", "9"},
	{"٠", "0"}, {"١", "1"}, {"٢", "2"}, {"٣", "3"},
	{"٤", "4"}, {"٥", "5"}, {"٦", "6"}, {"٧", "7"},
	{"٨", "8"}, {"٩", "9"},

	// Arabic comma and semicolon, so they do not survive as word glue.
	{"،", " "}, {"؛", " "},
}

// foldRunes is Folds as a rune map, built once.
//
// Every key in Folds is a single rune and every value is a single rune or
// nothing, which is what makes the indexed fold below possible: one rune in
// becomes at most one rune out, so a position in the folded text can be traced
// back to the character it came from.
var foldRunes = func() map[rune]rune {
	m := make(map[rune]rune, len(Folds))
	for _, f := range Folds {
		from := []rune(f[0])
		if len(from) != 1 {
			// A multi-rune rule would break FoldIndexed's promise. Nothing in
			// Folds is one today; this keeps that true if somebody adds one.
			panic("fa: Folds keys must be single runes, got " + f[0])
		}
		to := []rune(f[1])
		switch len(to) {
		case 0:
			m[from[0]] = -1 // deleted
		case 1:
			m[from[0]] = to[0]
		default:
			panic("fa: Folds values must be one rune or empty, got " + f[1])
		}
	}
	return m
}()

// FoldIndexed folds s and reports where each folded rune came from.
//
// WHY THIS EXISTS. Folding REMOVES characters -- a kashida, a ZWNJ, a harakat
// -- so a position in folded text is not a position in the original. Anything
// that finds a match in folded text and then wants to show the ORIGINAL
// around it needs the mapping back, or the highlight lands in the wrong place
// or off the end. That is exactly what the document search's snippet does.
//
// Returns the folded runes and, for each of them, the index of the rune in the
// input it came from. The two slices are always the same length.
//
// UNLIKE Fold, whitespace is NOT collapsed. Collapsing would break the very
// mapping this exists to provide. It is otherwise the same folding, so the two
// agree about which spellings are equal.
func FoldIndexed(s string) ([]rune, []int) {
	src := []rune(s)
	out := make([]rune, 0, len(src))
	idx := make([]int, 0, len(src))
	for i, r := range src {
		if to, ok := foldRunes[r]; ok {
			if to == -1 {
				continue // dropped: a kashida, a ZWNJ, a harakat
			}
			r = to
		}
		out = append(out, unicode.ToLower(r))
		idx = append(idx, i)
	}
	return out, idx
}

// Fold folds one string the way FoldSQL folds a column.
//
// Also lower-cased and whitespace-collapsed: the subjects in this database
// mix Persian with English part names ("Lance Tube", "MAGNESIUM ETHYLATE"),
// and a reader typing "lance tube" means the same thing. MySQL's own
// comparison is already case-insensitive under this collation, so the SQL
// side does not repeat the lower-casing -- but doing it here keeps faFold
// usable on its own, in Go, for the tests and for any later in-memory match.
func Fold(s string) string {
	for _, f := range Folds {
		s = strings.ReplaceAll(s, f[0], f[1])
	}
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}
