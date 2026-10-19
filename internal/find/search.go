package find

import (
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Schayan-nk/docfind-documents/internal/bidi"
	"github.com/Schayan-nk/docfind-documents/internal/fa"
)

// Tokenize splits text into searchable terms.
//
// Persian normalisation happens here rather than at query time so both sides
// agree: the same word typed with an Arabic yeh (ي) on one machine and a
// Persian yeh (ی) on another produces the same token, and Persian digits match
// Latin ones. Without this, searching "کنتاکتور" would miss documents whose
// text came from a different keyboard.
func Tokenize(s string) []string {
	if s == "" {
		return nil
	}
	s = foldForSearch(s)

	var out []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for _, r := range s {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r):
			cur.WriteRune(r)
		default:
			// ZWNJ used to be accumulated here, so a compound word would not
			// split. foldForSearch now REMOVES it, which keeps that property
			// -- the runes join up into one token -- and additionally makes
			// «می‌رود» and «میرود» the same token, which keeping it did not.
			// Harakat used to land here too and SPLIT the word: they are
			// category Mn, so IsLetter is false and «کُنتاکتور» tokenised as
			// «ک» plus «نتاکتور», neither of which is the word. They are
			// removed by the fold as well.
			flush()
		}
	}
	flush()
	return out
}

// foldForSearch is the ONE folding used everywhere on the search path.
//
// THERE USED TO BE THREE, and they disagreed. Tokenize folded the two kafs,
// the yehs, teh marbuta and the digits; normalizeForMatch repeated the same
// four calls for phrases; foldRunes did a rune-for-rune subset for snippets.
// None of them touched kashida, the alef family, hamza forms or harakat --
// so a document spelling the town «نيكـان» could not be found by anybody typing
// «نیکان», which is the exact failure internal/fa was measured into existence
// for on the tender search. Three copies of a fold is the drift that package
// exists to prevent, and this is now the only one.
//
// StripFormatting stays in front of it: it removes the bidi isolate and
// embedding controls (U+2066..U+2069, U+202A..U+202E) that fa.Folds does not
// list, and those arrive in text extracted from Word documents.
func foldForSearch(s string) string {
	return fa.Fold(bidi.StripFormatting(s))
}

// Query is a parsed search request.
type Query struct {
	Terms    []string // all must match
	Phrases  []string // quoted, must appear verbatim (after normalisation)
	Type     string   // raw type filter, empty for any
	Category string   // business category filter, empty for any
	Group    string   // reference group filter, empty for any
	Ref      string   // exact reference filter (group + sub), empty for any
	// Since and Until bound the document's file time, half-open: a document
	// matches when Since <= ModTime < Until. Either may be zero, meaning
	// unbounded on that side.
	//
	// Half-open rather than inclusive on both ends because the range these
	// carry is almost always a whole Jalali month or year converted to
	// Gregorian instants. An inclusive end would need the last instant of the
	// last day, and "the last day at midnight" -- the obvious way to write it
	// -- silently drops everything filed after midnight on that day, which is
	// most of it.
	Since time.Time
	Until time.Time
	Limit int
}

// ParseQueryRef is ParseQueryFull with the exact-reference filter the browse
// tree uses for its middle level.
func ParseQueryRef(raw, typeFilter, category, group, ref string, limit int) Query {
	q := Query{Type: typeFilter, Category: category, Group: group, Ref: ref, Limit: limit}
	if q.Limit <= 0 {
		q.Limit = 100
	}

	var rest strings.Builder
	inQuote := false
	var phrase strings.Builder
	for _, r := range raw {
		switch {
		case r == '"' || r == '«' || r == '»':
			if inQuote {
				if p := normalizeForMatch(phrase.String()); p != "" {
					q.Phrases = append(q.Phrases, p)
				}
				phrase.Reset()
			}
			inQuote = !inQuote
		case inQuote:
			phrase.WriteRune(r)
		default:
			rest.WriteRune(r)
		}
	}
	if phrase.Len() > 0 {
		if p := normalizeForMatch(phrase.String()); p != "" {
			q.Phrases = append(q.Phrases, p)
		}
	}

	q.Terms = Tokenize(rest.String())
	return q
}

// normalizeForMatch folds a phrase, and the document text it is matched
// against, identically to Tokenize.
//
// It used to repeat Tokenize's four calls by hand, with a comment claiming
// they were "the same folding". They were -- until Tokenize's changed, which
// is what two copies of a rule always do. Now there is one.
//
// fa.Fold collapses whitespace itself, which is what a phrase match needs: the
// query "نیروگاه  نیکان" and a document that wraps between those words have to
// compare equal.
func normalizeForMatch(s string) string {
	return foldForSearch(s)
}

// Hit is one search result.
type Hit struct {
	Doc     *Doc
	Score   int
	Snippet string
	// Exact marks a hit found through the identifier index (identindex.go)
	// rather than term score: the query normalised to an order, invoice,
	// reference or tender number this document's text or file name carries.
	Exact bool
}

// SearchCount is SearchExplain plus how many documents matched BEFORE the
// limit cut the list down.
//
// The page header needs that number rather than len(hits): the two are the
// same until a query matches more than the limit, and then len(hits) is just
// the limit staring back. With a 300-document archive that was invisible; at
// 4,467 every unfiltered search reported exactly "200 documents", which reads
// as a broken counter because it is one.
func (ix *Index) SearchCount(q Query) ([]Hit, int, map[string]string) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()

	// An empty query lists everything, which is what the CEO sees on opening
	// the app -- a browsable archive rather than a blank page.
	var candidates []int
	var corrected map[string]string
	if len(q.Terms) == 0 && len(q.Phrases) == 0 {
		candidates = make([]int, len(ix.docs))
		for i := range ix.docs {
			candidates[i] = i
		}
	} else {
		candidates, corrected = ix.candidatesFor(q)
	}

	hits := make([]Hit, 0, len(candidates))
	for _, i := range candidates {
		d := ix.docs[i]
		if !ix.matchesScope(d, q) {
			continue
		}

		normText := ""
		if len(q.Phrases) > 0 {
			normText = normalizeForMatch(d.Text)
			ok := true
			for _, p := range q.Phrases {
				if !strings.Contains(normText, p) {
					ok = false
					break
				}
			}
			if !ok {
				continue
			}
		}

		// No snippet yet. See below: it is the expensive half, and most of
		// these hits are about to be thrown away.
		hits = append(hits, Hit{
			Doc:   d,
			Score: scoreDoc(d, q),
		})
	}

	sort.SliceStable(hits, func(a, b int) bool {
		if hits[a].Score != hits[b].Score {
			return hits[a].Score > hits[b].Score
		}
		return hits[a].Doc.Ref > hits[b].Doc.Ref
	})

	total := len(hits)
	if len(hits) > q.Limit {
		hits = hits[:q.Limit]
	}

	// Snippets for the results that survived the limit, and no others.
	//
	// This used to be built inside the loop above, for every candidate. A
	// snippet is not cheap: snippetFor converts the whole document text to a
	// rune slice and folds a second copy of it to search case-insensitively,
	// so it is two allocations the length of the document, each time. A broad
	// query matches every document in the archive, and the page asks for two
	// hundred -- so on a twenty-thousand document archive that was nineteen
	// thousand eight hundred documents' worth of work computed and discarded.
	//
	// Measured before and after on the same synthetic corpus: a query matching
	// every document took 1.486s at twenty thousand documents, and the archive
	// on this installation is already four thousand and growing. The snippet
	// plays no part in scoring or ordering, so nothing about the results
	// changes -- only when the text is built.
	for i := range hits {
		hits[i].Snippet = snippetFor(hits[i].Doc, q)
	}
	return hits, total, corrected
}

// matchesScope reports whether a document passes the type, category, group,
// reference and date-range filters a Query carries -- the scoping every
// result has to respect regardless of how it was found. SearchCount applies
// it to a term match; identindex.go's ExactIdentHits applies the same
// function to an identifier match, so an exact hit is never shown to someone
// who would not otherwise have been allowed to see it.
//
// Caller holds ix.mu; this touches only the Doc it is given.
func (ix *Index) matchesScope(d *Doc, q Query) bool {
	if q.Type != "" && !strings.EqualFold(d.Type, q.Type) {
		return false
	}
	if q.Category != "" && !strings.EqualFold(d.Category, q.Category) {
		return false
	}
	if q.Group != "" && d.Group != q.Group {
		return false
	}
	if q.Ref != "" && d.Ref != q.Ref {
		return false
	}
	// Half-open on purpose: Since <= ModTime < Until. See the field comments
	// on Query -- an inclusive end written as "the last day at midnight"
	// drops almost the whole of that day.
	if !q.Since.IsZero() && d.ModTime.Before(q.Since) {
		return false
	}
	if !q.Until.IsZero() && !d.ModTime.Before(q.Until) {
		return false
	}
	return true
}

// candidatesFor intersects the posting lists, starting from the rarest term so
// the intersection shrinks as fast as possible. The second return value maps
// a mistyped term to the real word it was matched against, for whichever
// terms needed that fallback.
func (ix *Index) candidatesFor(q Query) ([]int, map[string]string) {
	if len(q.Terms) == 0 {
		// Phrase-only query: every document has to be checked, but phrase
		// matching is a substring scan and the corpus is small.
		all := make([]int, len(ix.docs))
		for i := range ix.docs {
			all[i] = i
		}
		return all, nil
	}

	type termList struct {
		term string
		docs []int32
	}
	var corrected map[string]string
	lists := make([]termList, 0, len(q.Terms))
	for _, t := range q.Terms {
		p, ok := ix.postings[t]
		if !ok {
			// A term nobody has: try it as a prefix, so typing "2201" finds
			// 111222 and half-remembered words still match.
			if p = ix.prefixPostings(t); len(p) == 0 {
				// Still nothing -- try the nearest real word by edit distance,
				// so a typo like "vlave" still finds what "valve" would have.
				// This is the last resort, tried only once everything cheaper
				// has already failed.
				fixed, fp := ix.fuzzyPostings(t)
				if fixed == "" {
					return nil, nil // AND semantics: one dead term means no results
				}
				p = fp
				if corrected == nil {
					corrected = map[string]string{}
				}
				corrected[t] = fixed
			}
		}
		lists = append(lists, termList{t, p})
	}
	sort.Slice(lists, func(i, j int) bool { return len(lists[i].docs) < len(lists[j].docs) })

	set := make(map[int32]int, len(lists[0].docs))
	for _, id := range lists[0].docs {
		set[id] = 1
	}
	for _, l := range lists[1:] {
		for _, id := range l.docs {
			if _, ok := set[id]; ok {
				set[id]++
			}
		}
	}

	out := make([]int, 0, len(set))
	for id, n := range set {
		if n == len(lists) {
			out = append(out, int(id))
		}
	}
	sort.Ints(out)
	return out, corrected
}

// fuzzyPostings finds the vocabulary term closest to t by edit distance and
// returns its postings, for a query term that matched nothing exactly or as a
// prefix -- the common shape of an ordinary typo.
//
// The tolerance grows with word length (one slip in a short word changes it
// more than one slip in a long one), and both ends are bounded: too short a
// term matches almost anything, and a query built to be expensive -- a
// ten-thousand-character token, say -- must not turn into a scan of the whole
// vocabulary computing edit distance against it.
func (ix *Index) fuzzyPostings(t string) (string, []int32) {
	n := utf8.RuneCountInString(t)
	if n < 3 || n > 24 {
		return "", nil
	}
	maxDist := 1
	if n >= 7 {
		maxDist = 2
	}

	// Walked over the sorted vocabulary rather than ranging the map, for two
	// reasons. It is deterministic, so the same typo always produces the same
	// correction instead of whichever equally-distant word the map happened to
	// yield first. And the buffers below are allocated once for the whole scan
	// instead of twice per candidate word -- at a 200,000-document vocabulary
	// that was hundreds of thousands of throwaway slices per mistyped query.
	// Only words whose length is within maxDist of the query's can be that many
	// edits away, so only those groups are looked at. The same candidates as a
	// full vocabulary walk, reached without touching the rest of it: at 51,000
	// terms this is three groups instead of all of them.
	//
	// Ascending length, and each group already sorted, so the walk order is
	// fixed -- two words at equal distance always resolve the same way.
	ra := []rune(t)
	var buf levBuf
	best, bestDist := "", maxDist+1
	for ln := n - maxDist; ln <= n+maxDist; ln++ {
		for _, vocab := range ix.vocabByLen[ln] {
			// bestDist-1 because only a strictly better match replaces the
			// current one; anything at or above it cannot win, so the
			// computation can stop the moment it passes that.
			if d, ok := buf.within(ra, vocab, bestDist-1); ok {
				best, bestDist = vocab, d
			}
		}
	}
	if best == "" {
		return "", nil
	}
	return best, ix.postings[best]
}

// levBuf is a reusable pair of rows for the edit-distance table.
//
// The distance is computed for every length-compatible word in the vocabulary,
// which for one mistyped term is tens of thousands of calls. Allocating the two
// rows inside each call made that tens of thousands of allocations; here they
// are grown once and reused.
type levBuf struct{ prev, cur []int }

// within is edit distance with a ceiling: it returns the distance and true only
// when that distance is at most max, and abandons the computation as soon as
// every value in the current row already exceeds max -- because no later row
// can bring it back down.
//
// Exactly equivalent to computing the full distance and comparing, just without
// finishing computations whose answer cannot be used.
func (b *levBuf) within(ra []rune, s string, max int) (int, bool) {
	if max < 0 {
		return 0, false
	}
	rb := []rune(s)
	if d := len(ra) - len(rb); d > max || -d > max {
		return 0, false // the length difference alone exceeds the ceiling
	}
	if len(ra) == 0 {
		return len(rb), len(rb) <= max
	}
	if len(rb) == 0 {
		return len(ra), len(ra) <= max
	}

	if cap(b.prev) < len(rb)+1 {
		b.prev = make([]int, len(rb)+1)
		b.cur = make([]int, len(rb)+1)
	}
	prev, cur := b.prev[:len(rb)+1], b.cur[:len(rb)+1]
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		rowMin := cur[0]
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
			if cur[j] < rowMin {
				rowMin = cur[j]
			}
		}
		if rowMin > max {
			return 0, false // nothing below can be smaller than this row's floor
		}
		prev, cur = cur, prev
	}
	d := prev[len(rb)]
	return d, d <= max
}

// prefixPostings unions every term starting with the given prefix.
//
// In a sorted vocabulary every term sharing a prefix is one contiguous run, so
// this is a binary search for where that run starts and a walk to where it
// ends -- O(log V + matches) rather than the O(V) map range it used to be.
//
// That mattered more than it looks, because this is not only the error path: it
// is what makes typing "2201" find 111222, which is how people actually search
// for a case. Measured on a 200,000-document archive, ranging the whole map
// cost tens of milliseconds on every such search.
func (ix *Index) prefixPostings(prefix string) []int32 {
	if len(prefix) < 2 {
		return nil
	}
	start := sort.SearchStrings(ix.vocab, prefix)
	seen := map[int32]bool{}
	var out []int32
	for i := start; i < len(ix.vocab); i++ {
		term := ix.vocab[i]
		// Sorted order means the first term that does not carry the prefix ends
		// the run: everything after it sorts higher still.
		if !strings.HasPrefix(term, prefix) {
			break
		}
		for _, id := range ix.postings[term] {
			if !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// scoreDoc ranks a match. An exact reference-number hit outranks everything:
// when someone types 111222 they want that case, not a document that merely
// mentions the number in passing.
func scoreDoc(d *Doc, q Query) int {
	score := 1
	for _, t := range q.Terms {
		switch {
		case d.Ref == t:
			score += 1000
		case strings.HasPrefix(d.Ref, t):
			score += 200
		}
		if strings.Contains(strings.ToLower(d.Name), t) {
			score += 50
		}
	}
	score += 25 * len(q.Phrases)
	return score
}

// foldRunes is gone; fa.FoldIndexed replaced it.
//
// It preserved length by folding one rune for one rune, which is what made an
// index into the folded text map onto the original -- and also what limited it
// to the handful of substitutions that happen to be length-preserving. It
// could never remove a kashida, a ZWNJ or a harakat, which is precisely what
// the search needed. fa.FoldIndexed does the full fold and returns the
// mapping alongside it, so nothing has to choose between folding properly and
// knowing where the match was.

// snippetFor returns a short window of text around the first match, so a
// content hit can be judged without opening the file.
//
// The match is located in folded text but the window is cut from the original,
// so the reader sees the document's own capitalisation and characters rather
// than the search engine's internal form.
func snippetFor(d *Doc, q Query) string {
	if d.Text == "" {
		return ""
	}

	needle := ""
	if len(q.Phrases) > 0 {
		needle = q.Phrases[0]
	} else {
		for _, t := range q.Terms {
			if t != d.Ref {
				needle = t
				break
			}
		}
	}

	orig := []rune(d.Text)
	if needle == "" {
		return firstRunes(collapse(d.Text), 140)
	}

	// THE FOLD CHANGES LENGTH, so a position in the folded text is not a
	// position in the original: removing a kashida, a ZWNJ or a harakat shifts
	// everything after it. fa.FoldIndexed hands back the mapping, and `at` is
	// translated through it before being used to cut the ORIGINAL text.
	//
	// Before the fold learned to remove characters this worked by accident --
	// every rule was one rune for one rune, so the indices happened to line
	// up. That is exactly the kind of accident that turns into a snippet
	// pointing at the wrong sentence.
	folded, srcIndex := fa.FoldIndexed(d.Text)
	at := indexRunes(folded, []rune(needle))
	if at >= 0 {
		at = srcIndex[at]
	} else {
		// A phrase has its whitespace collapsed, so retry against the
		// collapsed form -- and take that form's own mapping with it.
		collapsed := collapse(d.Text)
		cFolded, cIndex := fa.FoldIndexed(collapsed)
		at = indexRunes(cFolded, []rune(needle))
		if at < 0 {
			return firstRunes(collapsed, 140)
		}
		at = cIndex[at]
		orig = []rune(collapsed)
	}

	// The needle is measured in FOLDED runes and `at` is now an ORIGINAL
	// index, so the end has to allow for what the fold removed. Overshooting
	// is harmless -- minInt clamps it -- and undershooting would cut the match
	// in half.
	needleLen := len([]rune(needle)) + 8
	start := maxInt(0, at-60)
	end := minInt(len(orig), at+needleLen+60)

	out := collapse(string(orig[start:end]))
	if start > 0 {
		out = "…" + out
	}
	if end < len(orig) {
		out += "…"
	}
	return out
}

// indexRunes finds needle within haystack, in rune space.
func indexRunes(haystack, needle []rune) int {
	if len(needle) == 0 || len(needle) > len(haystack) {
		return -1
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		match := true
		for j := range needle {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

func collapse(s string) string { return strings.Join(strings.Fields(s), " ") }

func firstRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
