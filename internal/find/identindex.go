package find

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Schayan-nk/docfind-documents/internal/fa"
)

// An exact identifier index alongside the full-text one in index.go.
//
// A supplier's proforma invoice number, a purchase order, a tender number --
// the digits printed on the paper, not the archive's own reference number
// (that is Doc.Ref, built from the folder tree; see refFromPath). Someone who
// knows "INV-2024/017" wants the one document carrying that number, the way
// someone who knows the archive's own reference wants Query.Ref. Full-text
// search alone does not reliably find it: the query and the document can
// space or hyphenate the same number differently ("PI-2024/017" typed against
// a document that stored it as "PI2024017"), which tokenizes into different
// terms even though a person reads them as the same identifier.
//
// This file only extracts and normalises. The map itself is a field on Index
// (idents, in index.go), rebuilt in rebuild() from the same docs slice the
// postings index is built from -- so the two can never describe different
// scans -- and read under ix.mu like everything else there.

// identDigits matches one digit in any script a document in this archive
// might use it in: ASCII, Persian (۰-۹) and Arabic-Indic (٠-٩). The same
// three ranges fa.Folds folds to ASCII below.
const identDigits = `[0-9\x{06F0}-\x{06F9}\x{0660}-\x{0669}]`

// identPattern finds a candidate identifier: an optional short prefix this
// archive's documents actually carry, glued to a run of digits that may be
// split into groups by a dash or a slash -- "PI-2024/017", "INV12345",
// "111222-156", a bare "12345".
//
// No \b. Go's word boundary only recognises ASCII word characters, so it
// would refuse to sit next to a Persian digit at all -- neither side of that
// boundary is "word" by its rules, and \b never fires between two non-word
// positions. ExtractIdents checks the real, Unicode boundary itself, after
// matching, instead.
var identPattern = regexp.MustCompile(
	`(?i)(?:(?:PI|INV|PO|ORD|REF|TND|RFQ)[-_ ]?)?` + identDigits + `{2,}(?:[-/]` + identDigits + `{2,})*`,
)

// identFoldRunes reuses internal/fa's fold table -- the same one foldForSearch
// runs over document text for full-text search -- so an identifier and the
// text around it agree about which characters are the same character. Built
// once, the way fa's own internal table is.
var identFoldRunes = func() map[rune]rune {
	m := make(map[rune]rune, len(fa.Folds))
	for _, f := range fa.Folds {
		from := []rune(f[0])
		if len(from) != 1 {
			continue // multi-rune rules (the Arabic comma) don't apply here
		}
		to := []rune(f[1])
		switch len(to) {
		case 0:
			m[from[0]] = -1 // dropped: ZWNJ, ZWJ, tatweel, harakat, ...
		case 1:
			m[from[0]] = to[0]
		}
	}
	return m
}()

// IdentNormalize returns the canonical form of a candidate identifier:
// Arabic ي and ك folded to their Persian forms, ZWNJ/ZWJ/tatweel and harakat
// dropped, Persian and Arabic-Indic digits converted to ASCII, every letter
// upper-cased, and every separator -- space, dash, slash, underscore, and
// anything else that is not a letter or a digit once folded -- removed.
//
// Two spellings of the same identifier normalise to the same string, which is
// what the identifier index is keyed by and what a search query is compared
// against. The raw, as-typed or as-written form is kept alongside it
// (IdentMatch.Raw) wherever it needs to be shown rather than matched.
func IdentNormalize(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if to, ok := identFoldRunes[r]; ok {
			if to == -1 {
				continue
			}
			r = to
		}
		switch {
		case unicode.IsLetter(r):
			b.WriteRune(unicode.ToUpper(r))
		case unicode.IsDigit(r):
			b.WriteRune(r)
		}
		// Punctuation, whitespace and every other separator: dropped. A
		// canonical identifier is its letters and digits, nothing else.
	}
	return b.String()
}

// IdentMatch is one identifier found in a document or typed into a query:
// the exact text it appeared as, and the canonical form it is indexed and
// looked up under.
type IdentMatch struct {
	Raw       string
	Canonical string
}

// ExtractIdents finds candidate order, invoice, reference and tender numbers
// in s.
//
// A run of 4 or more digits -- ASCII, Persian or Arabic-Indic, optionally
// grouped with a dash or a slash -- optionally glued to a short prefix this
// archive's documents actually use. Fewer than 4 digits is treated as noise:
// a page number or a percentage looks exactly like the tail of a real
// identifier, and nothing this short survives being wrong.
//
// A match that starts or ends mid-word -- the "PI" inside "API2024017" -- is
// dropped: identBoundary checks, by hand and in Unicode, that neither side of
// the match is a letter or a digit that would make it part of something
// longer.
func ExtractIdents(s string) []IdentMatch {
	var out []IdentMatch
	for _, loc := range identPattern.FindAllStringIndex(s, -1) {
		start, end := loc[0], loc[1]
		if !identBoundary(s, start, false) || !identBoundary(s, end, true) {
			continue
		}
		raw := s[start:end]
		if identDigitCount(raw) < 4 {
			continue
		}
		canon := IdentNormalize(raw)
		if canon == "" {
			continue
		}
		out = append(out, IdentMatch{Raw: raw, Canonical: canon})
	}
	return out
}

// identBoundary reports whether position i in s is a real edge of a token --
// the start or end of the string, or a position next to a rune that is not a
// letter or a digit in any script. forward selects which side of i to look at.
func identBoundary(s string, i int, forward bool) bool {
	if forward {
		if i >= len(s) {
			return true
		}
		r, _ := utf8.DecodeRuneInString(s[i:])
		return !identWordRune(r)
	}
	if i <= 0 {
		return true
	}
	r, _ := utf8.DecodeLastRuneInString(s[:i])
	return !identWordRune(r)
}

// identWordRune reports whether r would extend an identifier if it sat right
// against one: any letter or digit, in any script.
func identWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

// identDigitCount counts the digits (ASCII, Persian or Arabic-Indic) in s,
// ignoring everything else -- the prefix, the separators.
func identDigitCount(s string) int {
	n := 0
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9', r >= 0x06F0 && r <= 0x06F9, r >= 0x0660 && r <= 0x0669:
			n++
		}
	}
	return n
}

// buildIdentIndex scans every document's extracted text and file name for
// identifiers and returns canonical identifier -> document IDs, each list
// deduplicated per document and sorted for a deterministic result.
//
// Called from rebuild() with the same docs slice the postings index is built
// from, so the identifier index always describes the same scan.
func buildIdentIndex(docs []*Doc) map[string][]string {
	m := make(map[string][]string)
	for _, d := range docs {
		seen := make(map[string]bool)
		for _, im := range ExtractIdents(d.Text + " " + d.Name) {
			if seen[im.Canonical] {
				continue // the same identifier printed twice in one document
			}
			seen[im.Canonical] = true
			m[im.Canonical] = append(m[im.Canonical], d.ID)
		}
	}
	for k := range m {
		sort.Strings(m[k])
	}
	return m
}

// ExactIdentHits returns the documents whose content or file name carries an
// identifier the raw query text also normalises to -- filtered by the same
// type, category, group, reference and date-range scope q carries, which is
// the same access every ordinary search result is already held to.
//
// Ordering is by document ID rather than score: an exact identifier hit does
// not compete on term frequency, it either is the document or it is not.
func (ix *Index) ExactIdentHits(rawQuery string, q Query) []Hit {
	idents := ExtractIdents(rawQuery)
	if len(idents) == 0 {
		return nil
	}

	ix.mu.RLock()
	defer ix.mu.RUnlock()

	seen := make(map[string]bool)
	var hits []Hit
	for _, im := range idents {
		for _, id := range ix.idents[im.Canonical] {
			if seen[id] {
				continue
			}
			idx, ok := ix.byID[id]
			if !ok {
				continue
			}
			d := ix.docs[idx]
			if !ix.matchesScope(d, q) {
				continue
			}
			seen[id] = true
			hits = append(hits, Hit{Doc: d, Exact: true, Snippet: snippetFor(d, q)})
		}
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].Doc.ID < hits[j].Doc.ID })
	return hits
}

// SearchWithIdents runs the ordinary search and, when the raw query text
// normalises to an identifier some document carries, puts those documents
// first and marked exact -- deduplicated against the ordinary results below
// them, and cut to the same limit the ordinary search already respects.
func (ix *Index) SearchWithIdents(rawQuery string, q Query) ([]Hit, int, map[string]string) {
	hits, total, corrected := ix.SearchCount(q)

	exact := ix.ExactIdentHits(rawQuery, q)
	if len(exact) == 0 {
		return hits, total, corrected
	}

	exactIDs := make(map[string]bool, len(exact))
	for i := range exact {
		exactIDs[exact[i].Doc.ID] = true
	}

	merged := make([]Hit, 0, len(exact)+len(hits))
	merged = append(merged, exact...)
	combined := len(exact)
	for _, h := range hits {
		if exactIDs[h.Doc.ID] {
			continue // already listed above, as the exact hit
		}
		merged = append(merged, h)
		combined++
	}
	// The exact hits may include documents the ordinary search's own limit
	// had not surfaced; the header should count them too.
	if combined > total {
		total = combined
	}
	if len(merged) > q.Limit {
		merged = merged[:q.Limit]
	}
	return merged, total, corrected
}
