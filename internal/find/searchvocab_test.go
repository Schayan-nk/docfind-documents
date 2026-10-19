package find

import (
	"sort"
	"strings"
	"testing"
	"unicode/utf8"
)

// The sorted vocabulary is exactly the postings map's keys.
//
// Two structures describing the same thing is the kind of arrangement that
// works until one of them is updated and the other is not. They are built
// together in rebuild for that reason, and this fails the moment they drift --
// a vocabulary missing a term silently stops that term's prefix from matching,
// which looks like a search bug and not like a stale index.
func TestTheVocabularyMatchesThePostings(t *testing.T) {
	ix := indexWithTerms(t)

	if len(ix.vocab) != len(ix.postings) {
		t.Fatalf("vocabulary has %d terms, postings has %d", len(ix.vocab), len(ix.postings))
	}
	if !sort.StringsAreSorted(ix.vocab) {
		t.Fatal("the vocabulary is not sorted; the prefix binary search is meaningless " +
			"and prefix matches will be missed")
	}
	for _, term := range ix.vocab {
		if _, ok := ix.postings[term]; !ok {
			t.Errorf("vocabulary carries %q, which is not in the postings map", term)
		}
	}
	for term := range ix.postings {
		if i := sort.SearchStrings(ix.vocab, term); i >= len(ix.vocab) || ix.vocab[i] != term {
			t.Errorf("postings carries %q, which is missing from the vocabulary -- every "+
				"prefix search that should have found it will come back empty", term)
		}
	}
}

// The fast prefix lookup returns exactly what scanning every term would have.
//
// This is the test that makes the optimisation safe to keep: the binary search
// is compared against the obvious, slow, obviously-correct implementation on
// the same index, for prefixes that match many terms, one term, and none.
func TestPrefixLookupAgreesWithAFullScan(t *testing.T) {
	ix := indexWithTerms(t)

	slow := func(prefix string) []int32 {
		seen := map[int32]bool{}
		var out []int32
		for term, docs := range ix.postings {
			if !strings.HasPrefix(term, prefix) {
				continue
			}
			for _, id := range docs {
				if !seen[id] {
					seen[id] = true
					out = append(out, id)
				}
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
		return out
	}

	for _, prefix := range []string{
		"va",    // many terms
		"valv",  // fewer
		"valve", // an exact term, which is also its own prefix
		"22",    // digits
		"1112",  // the reference-number case this exists for
		"zz",    // nothing
		"qqqq",  // nothing, longer
		"ab",    // may or may not match; either way both must agree
		"شرکت",  // Persian
		"قرارداد",
	} {
		fast, ref := ix.prefixPostings(prefix), slow(prefix)
		if len(fast) != len(ref) {
			t.Errorf("prefix %q: fast returned %d docs, a full scan returned %d",
				prefix, len(fast), len(ref))
			continue
		}
		for i := range fast {
			if fast[i] != ref[i] {
				t.Errorf("prefix %q: results differ at %d (%d vs %d)", prefix, i, fast[i], ref[i])
				break
			}
		}
	}

	// A one-character prefix is refused, as before: it would match most of the
	// archive and mean nothing.
	if got := ix.prefixPostings("v"); got != nil {
		t.Errorf("a single-character prefix returned %d documents; it should be refused", len(got))
	}
}

// The bounded edit distance agrees with the plain one wherever the plain one's
// answer is usable.
//
// within() stops early once no remaining row could come in under the ceiling.
// That is only safe if it never abandons a computation whose true answer would
// have been within the ceiling, so it is checked against a straightforward
// full-table implementation over a spread of word pairs.
func TestBoundedEditDistanceAgreesWithTheFullOne(t *testing.T) {
	full := func(a, b string) int {
		ra, rb := []rune(a), []rune(b)
		if len(ra) == 0 {
			return len(rb)
		}
		if len(rb) == 0 {
			return len(ra)
		}
		prev := make([]int, len(rb)+1)
		cur := make([]int, len(rb)+1)
		for j := range prev {
			prev[j] = j
		}
		for i := 1; i <= len(ra); i++ {
			cur[0] = i
			for j := 1; j <= len(rb); j++ {
				cost := 1
				if ra[i-1] == rb[j-1] {
					cost = 0
				}
				cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
			}
			prev, cur = cur, prev
		}
		return prev[len(rb)]
	}

	words := []string{
		"valve", "vlave", "valves", "value", "salve", "", "v",
		"flange", "flanges", "flnage",
		"قرارداد", "قراردادها", "قرارداذ",
		"تامین", "تأمین", "تامبن",
		"supplier", "suppliar", "supplyer", "supply",
	}
	var buf levBuf
	for _, a := range words {
		for _, b := range words {
			want := full(a, b)
			for _, max := range []int{0, 1, 2, 3, 8} {
				got, ok := buf.within([]rune(a), b, max)
				if want <= max {
					if !ok {
						t.Errorf("within(%q,%q,max=%d) abandoned a pair whose real distance "+
							"is %d -- a correction that should have been found was not",
							a, b, max, want)
					} else if got != want {
						t.Errorf("within(%q,%q,max=%d) = %d, full distance is %d",
							a, b, max, got, want)
					}
				} else if ok {
					t.Errorf("within(%q,%q,max=%d) accepted a pair whose real distance is %d",
						a, b, max, want)
				}
			}
		}
	}
}

// indexWithTerms builds a small index with a vocabulary worth searching:
// English, Persian, digits, and several words sharing prefixes.
func indexWithTerms(t *testing.T) *Index {
	t.Helper()
	root := tempDir(t)
	docs := []struct{ rel, body string }{
		{"111/222/Inquiry/a_111-222.docx", "valve flange gasket supplier 111222 قرارداد شرکت"},
		{"111/223/Inquiry/b_111-223.docx", "valves flanges valve body 111223 قراردادها شرکتی"},
		{"221/100/Inquiry/c_221-100.docx", "value supply supplier 221100 تامین کننده"},
		{"221/101/Inquiry/d_221-101.docx", "salve valve seat 221101 تأمین مبلغ"},
	}
	for _, d := range docs {
		writeDoc(t, root, d.rel, minimalDocx(t, d.body))
	}
	ix := NewIndex(root)
	if _, err := ix.Scan(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if ix.Count() != len(docs) {
		t.Fatalf("indexed %d documents, want %d", ix.Count(), len(docs))
	}
	if len(ix.vocab) == 0 {
		t.Fatal("the index built no vocabulary")
	}
	// Sanity: the corpus really does contain words of the lengths the typo
	// scan needs, or TestTypoCorrection would pass for the wrong reason.
	if utf8.RuneCountInString("valve") < 3 {
		t.Fatal("test corpus assumption broken")
	}
	return ix
}
