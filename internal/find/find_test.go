package find

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTokenizeNormalisesPersian(t *testing.T) {
	// Arabic yeh/kaf and Persian digits must fold to the same tokens as their
	// Persian equivalents, or a search misses documents typed on another
	// keyboard.
	a := Tokenize("كنتاكتور ۳۲ آمپر")
	b := Tokenize("کنتاکتور 32 آمپر")
	if len(a) != len(b) {
		t.Fatalf("token counts differ: %v vs %v", a, b)
	}
	for i := range a {
		if a[i] != b[i] {
			t.Errorf("token %d: %q vs %q", i, a[i], b[i])
		}
	}
}

func TestTokenizeSplitsOnPunctuation(t *testing.T) {
	got := Tokenize("Pi_NO_111222.docx — valve, 10%")
	want := map[string]bool{"pi": true, "no": true, "111222": true, "docx": true, "valve": true, "10": true}
	for _, tok := range got {
		delete(want, tok)
	}
	if len(want) != 0 {
		t.Errorf("missing tokens %v (got %v)", want, got)
	}
}

func TestRefFromPath(t *testing.T) {
	cases := map[string]string{
		filepath.Join("111", "222", "Contract_111222.docx"): "111222",
		filepath.Join("333", "444", "Pi_NO_333444.docx"):    "333444",
		filepath.Join("1", "Pi_NO_1.docx"):                  "1",
		"Loose_999.docx":                                    "999",
	}
	for in, want := range cases {
		if got := refFromPath(in); got != want {
			t.Errorf("refFromPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTypeFromName(t *testing.T) {
	cases := map[string]string{
		"Contract_111222.docx": "Contract",
		"Pi_NO_111222.docx":    "Pi_NO",
		"Anfrage_333444.docx":  "Anfrage",
		// Reference numbers are also written with a dash. The type has to come
		// out the same either way, or one document type shows up twice in the
		// browser's filter list as the numbering changes over.
		"inquiry_acme_persian_125-156.docx": "inquiry_acme_persian",
		"inquiry_acme_persian_154654.docx":  "inquiry_acme_persian",
		"bestellung_acme_125-156.docx":      "bestellung_acme",
	}
	for in, want := range cases {
		if got := typeFromName(in); got != want {
			t.Errorf("typeFromName(%q) = %q, want %q", in, got, want)
		}
	}
}

// buildArchive writes a small archive of real .docx files.
func buildArchive(t *testing.T) string {
	t.Helper()
	root := tempDir(t)

	docs := []struct {
		rel  string
		body string
	}{
		{filepath.Join("111", "222", "Contract_111222.docx"), "قرارداد شماره 111222 کنتاکتور سه فاز"},
		{filepath.Join("111", "222", "Pi_NO_111222.docx"), "Proforma invoice for valve VX-100"},
		{filepath.Join("333", "444", "Anfrage_333444.docx"), "Anfrage Float Level Switch Sample"},
	}
	for _, d := range docs {
		full := filepath.Join(root, d.rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, minimalDocx(t, d.body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// A rescan over an unchanged archive must re-read nothing.
func TestRescanIsIncremental(t *testing.T) {
	ix := NewIndex(buildArchive(t))
	if _, err := ix.Scan(); err != nil {
		t.Fatal(err)
	}
	n, err := ix.Scan()
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("second scan re-read %d documents, want 0", n)
	}
}

// A document id must never resolve outside the archive.
func TestPathOfStaysInsideRoot(t *testing.T) {
	ix := NewIndex(buildArchive(t))
	if _, err := ix.Scan(); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"../../etc/passwd", "..", "/etc/passwd", "nope.docx"} {
		if _, ok := ix.PathOf(bad); ok {
			t.Errorf("PathOf(%q) should have failed", bad)
		}
	}
}

func TestExtractTextReadsAllParts(t *testing.T) {
	root := buildArchive(t)
	text, err := ExtractText(filepath.Join(root, "111", "222", "Contract_111222.docx"))
	if err != nil {
		t.Fatal(err)
	}
	if !contains(text, "111222") || !contains(text, "کنتاکتور") {
		t.Errorf("extracted text incomplete: %q", text)
	}
}

func contains(s, sub string) bool { return len(s) > 0 && len(sub) > 0 && indexOf(s, sub) >= 0 }

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestCategoryOf(t *testing.T) {
	cases := map[string]string{
		"Anfrage": "Inquiry", "Inquiry": "Inquiry",
		"Pi_NO": "Proforma Invoice", "PI": "Proforma Invoice", "Proforma": "Proforma Invoice",
		"TechOffer": "Technical Offer", "PO": "Purchase Order",
		"PackingList": "Packing List", "Invoice": "Invoice",
		"Contract": "Contract", "OrderConfirm": "Order Confirmation",
		"Something": "Other", "": "Other",
		// The generic "order" alias (Purchase Order) must not swallow a longer,
		// more specific Order Confirmation alias just because Purchase Order is
		// listed first in categoryRules -- e.g. a second revision named
		// OrderConfirmation2_111222.docx.
		"OrderConfirmation2": "Order Confirmation",
		"Order":              "Purchase Order",
		// German document names file under the same business category as their
		// English counterparts, so all language versions of one job sit
		// together. "Anfrage" was already covered above; its purchase-order
		// counterpart was missing and fell through to "Other".
		"Bestellung":      "Purchase Order",
		"bestellung_acme": "Purchase Order",
	}
	for in, want := range cases {
		if got := CategoryOf(in); got != want {
			t.Errorf("CategoryOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGroupOf(t *testing.T) {
	for in, want := range map[string]string{"111222": "111", "333444": "333", "12": "12", "": "—"} {
		if got := GroupOf(in); got != want {
			t.Errorf("GroupOf(%q) = %q, want %q", in, got, want)
		}
	}
}

// The tree groups by the first three digits, then by business category.
func TestTree(t *testing.T) {
	ix := NewIndex(buildArchive(t))
	if _, err := ix.Scan(); err != nil {
		t.Fatal(err)
	}
	groups := ix.Tree()
	if len(groups) != 2 {
		t.Fatalf("expected 2 groups (111, 333), got %d: %+v", len(groups), groups)
	}
	if groups[0].Name != "333" || groups[1].Name != "111" {
		t.Errorf("groups should be newest first, got %s then %s", groups[0].Name, groups[1].Name)
	}

	var g111 *GroupNode
	for i := range groups {
		if groups[i].Name == "111" {
			g111 = &groups[i]
		}
	}
	// Count is folders-inside (one reference), Docs is documents-underneath
	// (two). The bracketed number in the sidebar is the former, because that
	// is what expanding the folder actually shows.
	if g111 == nil || g111.Count != 1 || g111.Docs != 2 {
		t.Fatalf("group 111 should show 1 reference holding 2 documents, got %+v", g111)
	}
	// The tree now mirrors the folders on disk: group, then reference, then
	// category. Both documents share reference 111222, so they sit under one
	// reference node, in two different categories.
	if len(g111.References) != 1 {
		t.Fatalf("expected 1 reference under 111, got %+v", g111.References)
	}
	ref := g111.References[0]
	if ref.Name != "222" || ref.Ref != "111222" {
		t.Errorf("reference node = %+v, want name 222 / ref 111222", ref)
	}
	// Contract_111222 and Pi_NO_111222 are different categories.
	if len(ref.Categories) != 2 {
		t.Errorf("expected 2 categories under 111/222, got %+v", ref.Categories)
	}
}
