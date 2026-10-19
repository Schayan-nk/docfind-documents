package find

import (
	"testing"
)

// Persian folding on the document search path.
//
// THE EXISTING TEST PROVED LESS THAN IT LOOKED. TestTokenizeNormalisesPersian
// compares «كنتاكتور ۳۲ آمپر» against «کنتاکتور 32 آمپر», which does exercise
// the two kafs and both digit sets -- but «آمپر» is spelled IDENTICALLY on both
// sides, so it says nothing about the alef family, and nothing at all about
// kashida, hamza forms or harakat. Every case below differs genuinely between
// the document and the query, because a folding test where both sides already
// match is a test that passes on a broken fold.
//
// This is the same failure internal/fa was measured into existence for on the
// tender search: a reader looking for «نیکان» did not find the row spelling the
// town «نيكـان», and the failure was invisible because four other Nikan rows did
// match. A shorter list of real results reads as the whole answer.

// tokensEqual reports whether two spellings tokenise identically.
func tokensEqual(t *testing.T, document, query string) bool {
	t.Helper()
	a, b := Tokenize(document), Tokenize(query)
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// THE ONE THAT COST REAL RESULTS. Kashida is Unicode Lm -- a modifier letter,
// so unicode.IsLetter is true for it and it accumulates INSIDE the token
// rather than separating it. «نيكـان» therefore could never match «نیکان» however
// the reader spelled the query.
func TestAKashidaDoesNotHideADocument(t *testing.T) {
	if !tokensEqual(t, "نيروگاه نيكـان", "نیروگاه نیکان") {
		t.Errorf("a kashida still hides the document:\n doc   %v\n query %v",
			Tokenize("نيروگاه نيكـان"), Tokenize("نیروگاه نیکان"))
	}
	// A kashida in the QUERY must be tolerated too -- somebody pasting a name
	// out of a document brings its typesetting with it.
	if !tokensEqual(t, "نیروگاه نیکان", "نيروگاه نيكـان") {
		t.Error("a kashida typed in the query finds nothing")
	}
}

// The alef family. A reader searching «اب» expects «آب»: the madda is a
// pronunciation mark, not a different letter to go looking for. The old test
// spelled its example the same on both sides and so never checked this.
func TestTheAlefFamilyFolds(t *testing.T) {
	for _, c := range []struct{ doc, query string }{
		{"آمپر", "امپر"},
		{"امپر", "آمپر"},
		{"أحمد", "احمد"},
		{"إسلام", "اسلام"},
	} {
		if !tokensEqual(t, c.doc, c.query) {
			t.Errorf("%q does not match %q: %v vs %v",
				c.doc, c.query, Tokenize(c.doc), Tokenize(c.query))
		}
	}
}

// Hamza forms, which sit on letters people type either way.
func TestHamzaFormsFold(t *testing.T) {
	for _, c := range []struct{ doc, query string }{
		{"مسئول", "مسیول"},
		{"مؤسسه", "موسسه"},
	} {
		if !tokensEqual(t, c.doc, c.query) {
			t.Errorf("%q does not match %q: %v vs %v",
				c.doc, c.query, Tokenize(c.doc), Tokenize(c.query))
		}
	}
}

// HARAKAT SPLIT A WORD IN TWO. They are Unicode Mn, so unicode.IsLetter is
// false and the tokeniser treated them as separators -- a vowelled word became
// two fragments, neither of which is the word anybody searches for.
func TestHarakatDoNotSplitAWord(t *testing.T) {
	// "کُنتاکتور" with a damma on the first letter.
	vowelled := "کُنتاکتور"
	plain := "کنتاکتور"
	got := Tokenize(vowelled)
	if len(got) != 1 {
		t.Errorf("a vowelled word tokenised into %d pieces: %v", len(got), got)
	}
	if !tokensEqual(t, vowelled, plain) {
		t.Errorf("%q does not match %q: %v vs %v", vowelled, plain, got, Tokenize(plain))
	}
}

// ZWNJ. The tokeniser deliberately treated it as part of a word rather than a
// separator, and that judgement is right -- «می‌رود» must not become two
// tokens. Removing it satisfies that AND makes the two spellings match, which
// is strictly better than keeping it: a reader types whichever their keyboard
// produces.
func TestZWNJDoesNotSplitAWordAndBothSpellingsMatch(t *testing.T) {
	joined := "می‌رود"
	plain := "میرود"
	got := Tokenize(joined)
	if len(got) != 1 {
		t.Errorf("ZWNJ split the word into %d pieces: %v -- it is not a separator", len(got), got)
	}
	if !tokensEqual(t, joined, plain) {
		t.Errorf("%q does not match %q: %v vs %v", joined, plain, got, Tokenize(plain))
	}
}

// The behaviour the old test did check must not regress.
func TestTheKafsYehsAndDigitsStillFold(t *testing.T) {
	if !tokensEqual(t, "كنتاكتور ۳۲", "کنتاکتور 32") {
		t.Error("the two kafs or the digit sets stopped folding")
	}
	if !tokensEqual(t, "عربي", "عربی") {
		t.Error("the yehs stopped folding")
	}
	if !tokensEqual(t, "فاطمة", "فاطمه") {
		t.Error("teh marbuta stopped folding")
	}
}

// Latin text is unharmed. The archive mixes Persian with English part names,
// and a fold that damaged them would be a worse bug than the one being fixed.
func TestLatinTextIsUnharmed(t *testing.T) {
	got := Tokenize("MAGNESIUM ETHYLATE Lance Tube 32A")
	want := []string{"magnesium", "ethylate", "lance", "tube", "32a"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("token %d: %q, want %q", i, got[i], want[i])
		}
	}
}
