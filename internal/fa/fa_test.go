package fa

import (
	"strings"
	"testing"
	"unicode"
)

// The fold, and the mapping that makes it usable for highlighting.
//
// This package had no tests. It was extracted from internal/reports, where its
// behaviour was covered indirectly by the catalogue tests, and then acquired a
// second caller in the mail rules and a third in the document search -- at
// which point "covered somewhere else" stopped being good enough. The document
// search's snippet now depends on FoldIndexed's index mapping being exactly
// right, and a wrong snippet points a reader at the wrong sentence.

// THE INVARIANT. For every rune in the output, the rune it says it came from
// must actually fold to it. If this holds, a match found in folded text can
// always be located in the original.
func TestFoldIndexedMapsEveryRuneBackToItsSource(t *testing.T) {
	for _, in := range []string{
		"نيروگاه نيكـان",           // Arabic kaf and a kashida
		"کُنتاکتور ۳۲ آمپر",      // a harakat, Persian digits, alef madda
		"می‌رود و می‌آید",        // ZWNJ, twice
		"MAGNESIUM ETHYLATE 32A", // Latin, untouched
		"مسئول مؤسسه إسلام أحمد", // hamza forms
		"",      // empty
		"ـــ",   // nothing but kashida
		"a‌بَj", // mixed scripts with removals between them
	} {
		folded, idx := FoldIndexed(in)
		src := []rune(in)

		if len(folded) != len(idx) {
			t.Errorf("%q: %d folded runes but %d indices", in, len(folded), len(idx))
			continue
		}
		last := -1
		for i := range folded {
			if idx[i] < 0 || idx[i] >= len(src) {
				t.Errorf("%q: index %d is %d, outside 0..%d", in, i, idx[i], len(src)-1)
				continue
			}
			if idx[i] <= last {
				t.Errorf("%q: indices are not strictly increasing at %d (%d after %d)",
					in, i, idx[i], last)
			}
			last = idx[i]

			// The source rune must fold to the output rune.
			want := src[idx[i]]
			if to, ok := foldRunes[want]; ok {
				if to == -1 {
					t.Errorf("%q: output rune %d maps to %q, which the fold deletes",
						in, i, string(want))
					continue
				}
				want = to
			}
			if folded[i] != unicode.ToLower(want) {
				t.Errorf("%q: output rune %d is %q but its source %q folds to %q",
					in, i, string(folded[i]), string(src[idx[i]]), string(unicode.ToLower(want)))
			}
		}
	}
}

// A removal shortens the output, and the indices step over what was dropped.
// This is the case the old length-preserving fold could not express.
func TestFoldIndexedSkipsWhatItRemoves(t *testing.T) {
	// "نکـا": kaf, kashida, alef. The kashida is dropped.
	folded, idx := FoldIndexed("نکـا")
	if len(folded) != 3 {
		t.Fatalf("expected 3 runes after dropping the kashida, got %d (%q)", len(folded), string(folded))
	}
	// Source indices are 0 (nun), 1 (kaf), 2 (kashida, dropped), 3 (alef).
	if idx[0] != 0 || idx[1] != 1 || idx[2] != 3 {
		t.Errorf("indices %v do not step over the dropped kashida at 2", idx)
	}
}

// FoldIndexed and Fold must agree about which spellings are equal, or the
// search matches on one and highlights with the other.
func TestFoldIndexedAgreesWithFold(t *testing.T) {
	for _, pair := range [][2]string{
		{"نيروگاه نيكـان", "نیروگاه نیکان"},
		{"کُنتاکتور", "کنتاکتور"},
		{"می‌رود", "میرود"},
		{"آمپر", "امپر"},
		{"فاطمة", "فاطمه"},
		{"۳۲", "32"},
	} {
		a, _ := FoldIndexed(pair[0])
		b, _ := FoldIndexed(pair[1])
		if string(a) != string(b) {
			t.Errorf("FoldIndexed disagrees: %q -> %q, %q -> %q",
				pair[0], string(a), pair[1], string(b))
		}
		// Fold collapses whitespace, so compare on that footing.
		if fa, fb := Fold(pair[0]), Fold(pair[1]); fa != fb {
			t.Errorf("Fold disagrees: %q -> %q, %q -> %q", pair[0], fa, pair[1], fb)
		}
		// And the two functions must reach the same answer as each other.
		if strings.Join(strings.Fields(string(a)), " ") != Fold(pair[0]) {
			t.Errorf("FoldIndexed and Fold differ on %q: %q vs %q",
				pair[0], string(a), Fold(pair[0]))
		}
	}
}

// Whitespace is PRESERVED, unlike Fold. Collapsing it would break the mapping
// this function exists to provide.
func TestFoldIndexedKeepsWhitespaceWhereItIs(t *testing.T) {
	folded, idx := FoldIndexed("الف   ب")
	if !strings.Contains(string(folded), "   ") {
		t.Errorf("whitespace was collapsed: %q -- the index mapping depends on it not being",
			string(folded))
	}
	if len(folded) != len(idx) {
		t.Error("length disagreement")
	}
}

// Latin text passes through unchanged apart from case, because the archive
// mixes Persian with English part names and damaging those would be a worse
// bug than the one this fixes.
func TestLatinAndDigitsSurviveTheFold(t *testing.T) {
	folded, _ := FoldIndexed("Lance Tube 32A-B/C")
	if string(folded) != "lance tube 32a-b/c" {
		t.Errorf("Latin text was altered: %q", string(folded))
	}
}

// The rune table is built from Folds at init, and its own guard would panic on
// a malformed entry. Asserting the table is populated catches an empty Folds
// or a build that silently produced nothing.
func TestTheRuneTableCoversTheDocumentedRules(t *testing.T) {
	if len(foldRunes) < 40 {
		t.Fatalf("the rune table holds only %d entries; Folds has %d rules",
			len(foldRunes), len(Folds))
	}
	// Spot-check one of each kind: a removal, a letter swap, a digit.
	for r, want := range map[rune]rune{
		'ـ': -1,  // kashida, removed
		'ك': 'ک', // Arabic kaf
		'آ': 'ا', // alef madda
		'۵': '5', // Persian digit
	} {
		if got, ok := foldRunes[r]; !ok || got != want {
			t.Errorf("%q maps to %v (present %v), want %v", string(r), got, ok, want)
		}
	}
}
