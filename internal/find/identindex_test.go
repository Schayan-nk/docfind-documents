package find

import (
	"testing"
)

// Normalisation: two spellings of the same identifier have to fold to the
// same canonical string, or the index that is keyed by it cannot find a
// document filed under one spelling from a query typed in the other -- the
// exact failure internal/fa was measured into existence for on full-text
// search, now for identifiers.
func TestIdentNormalize(t *testing.T) {
	cases := []struct {
		name string
		a, b string
	}{
		{"Arabic and Persian kaf", "PI-2024/كك", "PI-2024/کک"},
		{"Arabic and Persian yeh", "REF-يي-99", "REF-یی-99"},
		{"ZWNJ inside the run is dropped", "INV12‌345", "INV12345"},
		{"ZWJ inside the run is dropped", "INV12‍345", "INV12345"},
		{"tatweel is dropped", "PIـ10ـ2024", "PI102024"},
		{"Persian digits to ASCII", "۱۲۳۴۵", "12345"},
		{"Arabic-Indic digits to ASCII", "٩٨٧٦٥", "98765"},
		{"mixed digit scripts agree with ASCII", "PI-۲۰۲۴/٠١٧", "PI-2024/017"},
		{"dash and slash both fold away", "PI-2024/017", "PI2024017"},
		{"space folds away like any separator", "INV 2024 017", "INV2024017"},
		{"underscore folds away", "PO_2024_5566", "PO20245566"},
		{"lower-case Latin upper-cases", "pi-2024/017", "PI-2024/017"},
	}
	for _, c := range cases {
		got, want := IdentNormalize(c.a), IdentNormalize(c.b)
		if got != want {
			t.Errorf("%s: IdentNormalize(%q)=%q, IdentNormalize(%q)=%q, want equal",
				c.name, c.a, got, c.b, want)
		}
	}

	// And genuinely different identifiers must NOT collide -- a normaliser
	// that folded too much would be as useless as one that folded too little.
	if IdentNormalize("PI-2024/017") == IdentNormalize("INV-2024/017") {
		t.Error("a PI number and an INV number with the same digits normalised the same")
	}
	if IdentNormalize("12345") == IdentNormalize("123450") {
		t.Error("two different digit runs normalised the same")
	}
}

// Extraction: the patterns real documents and queries actually carry, per the
// task -- digit runs of 4 or more, possibly grouped with a dash or a slash,
// possibly prefixed ("PI-", "INV").
func TestExtractIdents(t *testing.T) {
	find := func(t *testing.T, s, wantCanonical string) {
		t.Helper()
		for _, m := range ExtractIdents(s) {
			if m.Canonical == wantCanonical {
				return
			}
		}
		t.Errorf("ExtractIdents(%q) did not contain canonical %q; got %+v", s, wantCanonical, ExtractIdents(s))
	}
	notFound := func(t *testing.T, s, canonical string) {
		t.Helper()
		for _, m := range ExtractIdents(s) {
			if m.Canonical == canonical {
				t.Errorf("ExtractIdents(%q) unexpectedly contained canonical %q", s, canonical)
			}
		}
	}

	find(t, "Please reference PI-2024/017 in your reply", "PI2024017")
	find(t, "Invoice INV12345 is attached", "INV12345")
	find(t, "111222-156.docx", "111222156")
	find(t, "invoice 12345", "12345")
	find(t, "فاکتور ۱۲۳۴۵", "12345") // Persian keyword, Persian digits
	find(t, "tender TND-445566 closes Friday", "TND445566")

	// Fewer than 4 digits is noise, not an identifier: a page number, a
	// percentage, a stray "no5".
	notFound(t, "see page 12 of the contract", "12")
	notFound(t, "a 20% discount", "20")

	// A prefix embedded inside a longer word is not a real prefix: "API" is
	// not "PI" with an "A" in front of it, and the whole match is dropped.
	notFound(t, "API2024017 is an internal system code", "PI2024017")

	// A run glued to more digits on the far side is one longer number, not a
	// 5-digit identifier hiding inside an 8-digit one.
	notFound(t, "the account number is 20240178", "20240")
}

// Two documents can print the same identifier with a different separator
// style ("PI-2024/017" vs "PI2024017"), which tokenizes into different
// full-text terms even though a person reads them as the same number. That
// is the whole reason this index exists, so canonicalisation has to agree
// across separator styles.
func TestExtractIdentsAgreesAcrossSeparatorStyle(t *testing.T) {
	a := ExtractIdents("Proforma invoice PI-2024/017 enclosed")
	b := ExtractIdents("see PI2024017 for the proforma")
	if len(a) == 0 || len(b) == 0 {
		t.Fatalf("expected both to extract an identifier, got %+v and %+v", a, b)
	}
	if a[0].Canonical != b[0].Canonical {
		t.Errorf("differently separated spellings did not canonicalise the same: %q vs %q",
			a[0].Canonical, b[0].Canonical)
	}
	if a[0].Raw == b[0].Raw {
		t.Error("Raw should keep the as-written form, not the canonical one")
	}
}

// docFor is a minimal Doc for rebuild()-driven tests: no file on disk, no
// extractor -- rebuild() (and buildIdentIndex inside it) only reads Text,
// Name, Ref and ID.
func docFor(id, name, text string) *Doc {
	return &Doc{ID: id, Name: name, Text: text}
}

// Exact identifier hits are scoped by the same filters (type, category,
// group, reference, date range) ordinary results are -- the same access
// every hit is held to, regardless of how it was found.
func TestExactIdentHitsRespectsScope(t *testing.T) {
	ix := NewIndex("")
	d := docFor("a.docx", "a.docx", "invoice INV12345 attached")
	ix.rebuild([]*Doc{d}, nil)
	ix.docs[0].Category = "Invoice" // rebuild() derives this from Type; set directly for the test

	inScope := Query{Limit: 10, Category: "Invoice"}
	if hits := ix.ExactIdentHits("INV12345", inScope); len(hits) != 1 {
		t.Fatalf("expected the document within its own category, got %d hits", len(hits))
	}

	outOfScope := Query{Limit: 10, Category: "Contract"}
	if hits := ix.ExactIdentHits("INV12345", outOfScope); len(hits) != 0 {
		t.Fatalf("expected no hits outside the category filter, got %d hits", len(hits))
	}
}
