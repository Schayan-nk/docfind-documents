package docxhtml

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/Schayan-nk/docfind-documents/internal/ooxml"
)

// The renderer takes a file off disk and turns it into markup that goes
// straight into a page. Nothing in a .docx is signed or validated by Word, so
// every string and every style value out of it is treated as untrusted here.

const wns = `xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" ` +
	`xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" ` +
	`xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" ` +
	`xmlns:wp="http://schemas.openxmlformats.org/drawingml/2006/wordprocessingDrawing"`

// build assembles a package from part name to content.
func build(t *testing.T, parts map[string]string, binary map[string][]byte) *ooxml.Package {
	t.Helper()

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	write := func(name string, data []byte) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	write("[Content_Types].xml", []byte(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`+
		`<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">`+
		`<Default Extension="xml" ContentType="application/xml"/>`+
		`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>`+
		`</Types>`))
	for name, content := range parts {
		write(name, []byte(content))
	}
	for name, content := range binary {
		write(name, content)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	pkg, err := ooxml.Open(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	return pkg
}

// doc wraps body XML in a document part.
func doc(body string) string {
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
		`<w:document ` + wns + `><w:body>` + body + `</w:body></w:document>`
}

func render(t *testing.T, body string) string {
	t.Helper()
	out, err := Render(build(t, map[string]string{"word/document.xml": doc(body)}, nil), Options{})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// --- escaping ---------------------------------------------------------------

func TestTextIsEscaped(t *testing.T) {
	out := render(t, `<w:p><w:r><w:t>&lt;script&gt;alert(1)&lt;/script&gt; &amp; "quotes"</w:t></w:r></w:p>`)

	if strings.Contains(out, "<script>") {
		t.Error("a script tag came through as markup")
	}
	if !strings.Contains(out, "&lt;script&gt;") {
		t.Errorf("the text was not escaped: %s", out)
	}
	if strings.Contains(out, `alert(1)</script>`) {
		t.Error("a closing script tag came through verbatim")
	}
}

// A style value out of the document must never be able to add declarations of
// its own.
func TestStyleValuesAreFiltered(t *testing.T) {
	cases := []struct {
		name, rPr string
		banned    string
	}{
		{"colour with an injected declaration",
			`<w:color w:val="red;background:url(http://evil/x)"/>`, "url("},
		{"colour that is not hex",
			`<w:color w:val="expression(alert(1))"/>`, "expression"},
		{"font with a quote and a declaration",
			`<w:rFonts w:ascii="Arial&quot;;background:url(http://evil/x);&quot;"/>`, "url("},
		{"font with a semicolon",
			`<w:rFonts w:ascii="Arial;color:red"/>`, "color:red"},
		{"highlight that is not a known name",
			`<w:highlight w:val="url(http://evil/x)"/>`, "url("},
	}
	for _, tc := range cases {
		out := render(t, `<w:p><w:r><w:rPr>`+tc.rPr+`</w:rPr><w:t>x</w:t></w:r></w:p>`)
		if strings.Contains(out, tc.banned) {
			t.Errorf("%s: %q survived into the output:\n%s", tc.name, tc.banned, out)
		}
	}
}

func TestValidStyleValuesSurvive(t *testing.T) {
	out := render(t, `<w:p><w:r><w:rPr>`+
		`<w:b/><w:i/><w:u w:val="single"/>`+
		`<w:sz w:val="32"/><w:color w:val="C00000"/>`+
		`<w:rFonts w:ascii="B Nazanin"/><w:highlight w:val="yellow"/>`+
		`</w:rPr><w:t>styled</w:t></w:r></w:p>`)

	for _, want := range []string{
		"font-weight:700", "font-style:italic", "text-decoration:underline",
		"font-size:16.0pt", "color:#C00000", "B Nazanin", "background:#FFFF00",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

// --- links ------------------------------------------------------------------

func TestOnlySafeLinksBecomeAnchors(t *testing.T) {
	rels := func(target string) string {
		return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
			`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
			`<Relationship Id="rId9" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/hyperlink" ` +
			`Target="` + target + `" TargetMode="External"/></Relationships>`
	}
	body := `<w:p><w:hyperlink r:id="rId9"><w:r><w:t>click</w:t></w:r></w:hyperlink></w:p>`

	dangerous := []string{
		"javascript:alert(1)",
		"JavaScript:alert(1)",
		"data:text/html;base64,PHNjcmlwdD4=",
		"vbscript:msgbox",
		"file:///C:/windows/win.ini",
	}
	for _, target := range dangerous {
		pkg := build(t, map[string]string{
			"word/document.xml":            doc(body),
			"word/_rels/document.xml.rels": rels(target),
		}, nil)
		out, err := Render(pkg, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out, "<a ") {
			t.Errorf("%q became a link: %s", target, out)
		}
		if !strings.Contains(out, "click") {
			t.Errorf("%q: the link text was lost", target)
		}
	}

	for _, target := range []string{"https://example.com/x", "http://example.com", "mailto:a@b.c"} {
		pkg := build(t, map[string]string{
			"word/document.xml":            doc(body),
			"word/_rels/document.xml.rels": rels(target),
		}, nil)
		out, err := Render(pkg, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, `href="`+target+`"`) {
			t.Errorf("a safe link was dropped: %q\n%s", target, out)
		}
	}
}

// --- tables -----------------------------------------------------------------

func TestTableStructure(t *testing.T) {
	body := `<w:tbl><w:tblPr><w:bidiVisual/></w:tblPr>` +
		`<w:tr>` +
		`<w:tc><w:tcPr><w:gridSpan w:val="2"/><w:shd w:fill="D9D9D9"/></w:tcPr>` +
		`<w:p><w:r><w:t>merged header</w:t></w:r></w:p></w:tc>` +
		`<w:tc><w:p><w:r><w:t>third</w:t></w:r></w:p></w:tc>` +
		`</w:tr>` +
		`<w:tr>` +
		`<w:tc><w:tcPr><w:vMerge w:val="restart"/></w:tcPr><w:p><w:r><w:t>tall</w:t></w:r></w:p></w:tc>` +
		`<w:tc><w:p><w:r><w:t>b</w:t></w:r></w:p></w:tc>` +
		`<w:tc><w:p><w:r><w:t>c</w:t></w:r></w:p></w:tc>` +
		`</w:tr>` +
		`<w:tr>` +
		`<w:tc><w:tcPr><w:vMerge/></w:tcPr><w:p/></w:tc>` +
		`<w:tc><w:p><w:r><w:t>e</w:t></w:r></w:p></w:tc>` +
		`<w:tc><w:p><w:r><w:t>f</w:t></w:r></w:p></w:tc>` +
		`</w:tr></w:tbl>`

	out := render(t, body)

	if !strings.Contains(out, `<table class="dx-table" dir="rtl"`) {
		t.Errorf("a right-to-left table was not marked as such:\n%s", out)
	}
	if !strings.Contains(out, `colspan="2"`) {
		t.Error("gridSpan did not become colspan")
	}
	if !strings.Contains(out, "background:#D9D9D9") {
		t.Error("cell shading was lost")
	}
	if got := strings.Count(out, "<tr>"); got != 3 {
		t.Errorf("expected 3 rows, got %d", got)
	}
	// The continuation cell of a vertical merge is drawn by the cell above it.
	if got := strings.Count(out, "<td"); got != 7 {
		t.Errorf("expected 7 cells (the vMerge continuation omitted), got %d:\n%s", got, out)
	}
	for _, want := range []string{"merged header", "third", "tall", "e", "f"} {
		if !strings.Contains(out, want) {
			t.Errorf("cell text %q was lost", want)
		}
	}
}

func TestNestedTablesRender(t *testing.T) {
	inner := `<w:tbl><w:tr><w:tc><w:p><w:r><w:t>inner</w:t></w:r></w:p></w:tc></w:tr></w:tbl>`
	out := render(t, `<w:tbl><w:tr><w:tc>`+inner+`</w:tc></w:tr></w:tbl>`)

	if got := strings.Count(out, "<table"); got != 2 {
		t.Errorf("expected 2 tables, got %d", got)
	}
	if !strings.Contains(out, "inner") {
		t.Error("the nested table's text was lost")
	}
}

// --- direction --------------------------------------------------------------

func TestParagraphDirection(t *testing.T) {
	out := render(t, `<w:p><w:pPr><w:bidi/></w:pPr><w:r><w:rPr><w:rtl/></w:rPr>`+
		`<w:t>قرارداد</w:t></w:r></w:p>`)

	if !strings.Contains(out, `dir="rtl"`) {
		t.Errorf("a right-to-left paragraph was not marked:\n%s", out)
	}
	if !strings.Contains(out, "قرارداد") {
		t.Error("Persian text was lost")
	}
}

func TestDocumentDirectionFollowsTheMajority(t *testing.T) {
	rtl := strings.Repeat(`<w:p><w:pPr><w:bidi/></w:pPr><w:r><w:t>د</w:t></w:r></w:p>`, 5)
	if out := render(t, rtl+`<w:p><w:r><w:t>one</w:t></w:r></w:p>`); !strings.Contains(out, `class="dx-doc" dir="rtl"`) {
		t.Error("a mostly right-to-left document was laid out left to right")
	}

	ltr := strings.Repeat(`<w:p><w:r><w:t>x</w:t></w:r></w:p>`, 5)
	if out := render(t, ltr+`<w:p><w:pPr><w:bidi/></w:pPr><w:r><w:t>د</w:t></w:r></w:p>`); strings.Contains(out, `class="dx-doc" dir="rtl"`) {
		t.Error("a mostly left-to-right document was laid out right to left")
	}
}

// --- styles -----------------------------------------------------------------

func styled(t *testing.T, styles, body string) string {
	t.Helper()
	out, err := Render(build(t, map[string]string{
		"word/document.xml": doc(body),
		"word/styles.xml": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
			`<w:styles ` + wns + `>` + styles + `</w:styles>`,
	}, nil), Options{})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestHeadingStylesBecomeHeadings(t *testing.T) {
	out := styled(t,
		`<w:style w:styleId="Heading1"><w:name w:val="heading 1"/>`+
			`<w:rPr><w:b/><w:sz w:val="32"/></w:rPr></w:style>`,
		`<w:p><w:pPr><w:pStyle w:val="Heading1"/></w:pPr><w:r><w:t>Title</w:t></w:r></w:p>`)

	if !strings.Contains(out, "<h1") {
		t.Errorf("a heading style did not become a heading:\n%s", out)
	}
	// Formatting carried by the style applies even though the run says nothing.
	if !strings.Contains(out, "font-weight:700") {
		t.Errorf("formatting from the style was not applied:\n%s", out)
	}
}

func TestStyleInheritance(t *testing.T) {
	out := styled(t,
		`<w:style w:styleId="Base"><w:rPr><w:b/><w:color w:val="112233"/></w:rPr></w:style>`+
			`<w:style w:styleId="Derived"><w:basedOn w:val="Base"/>`+
			`<w:rPr><w:color w:val="AABBCC"/></w:rPr></w:style>`,
		`<w:p><w:pPr><w:pStyle w:val="Derived"/></w:pPr><w:r><w:t>x</w:t></w:r></w:p>`)

	if !strings.Contains(out, "font-weight:700") {
		t.Error("a property inherited from the base style was lost")
	}
	if !strings.Contains(out, "color:#AABBCC") {
		t.Error("the derived style did not override the base colour")
	}
	if strings.Contains(out, "color:#112233") {
		t.Error("the base colour survived when it should have been overridden")
	}
}

// A malformed file can point a style at itself. That must not hang the viewer.
func TestStyleCycleTerminates(t *testing.T) {
	done := make(chan string, 1)
	go func() {
		done <- styled(t,
			`<w:style w:styleId="A"><w:basedOn w:val="B"/><w:rPr><w:b/></w:rPr></w:style>`+
				`<w:style w:styleId="B"><w:basedOn w:val="A"/><w:rPr><w:i/></w:rPr></w:style>`,
			`<w:p><w:pPr><w:pStyle w:val="A"/></w:pPr><w:r><w:t>x</w:t></w:r></w:p>`)
	}()
	select {
	case out := <-done:
		if !strings.Contains(out, "x") {
			t.Error("the text was lost")
		}
	case <-timeout():
		t.Fatal("a cycle in basedOn did not terminate")
	}
}

func TestDirectFormattingBeatsTheStyle(t *testing.T) {
	out := styled(t,
		`<w:style w:styleId="S"><w:rPr><w:sz w:val="20"/></w:rPr></w:style>`,
		`<w:p><w:pPr><w:pStyle w:val="S"/></w:pPr>`+
			`<w:r><w:rPr><w:sz w:val="48"/></w:rPr><w:t>big</w:t></w:r></w:p>`)

	if !strings.Contains(out, "font-size:24.0pt") {
		t.Errorf("direct formatting did not win over the style:\n%s", out)
	}
}

// --- images -----------------------------------------------------------------

// A one-pixel PNG, so the test exercises real image bytes.
var onePixelPNG = []byte{
	0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A,
	0, 0, 0, 0x0D, 'I', 'H', 'D', 'R', 0, 0, 0, 1, 0, 0, 0, 1,
	8, 6, 0, 0, 0, 0x1F, 0x15, 0xC4, 0x89,
	0, 0, 0, 0x0A, 'I', 'D', 'A', 'T', 0x78, 0x9C, 0x63, 0, 1, 0, 0, 5, 0, 1,
	0x0D, 0x0A, 0x2D, 0xB4,
	0, 0, 0, 0, 'I', 'E', 'N', 'D', 0xAE, 0x42, 0x60, 0x82,
}

func imageDoc(t *testing.T, opt Options) string {
	t.Helper()
	body := `<w:p><w:r><w:drawing><wp:inline><wp:extent cx="1905000" cy="952500"/>` +
		`<a:graphic><a:graphicData><a:blip r:embed="rId5"/></a:graphicData></a:graphic>` +
		`</wp:inline></w:drawing></w:r></w:p>`

	pkg := build(t, map[string]string{
		"word/document.xml": doc(body),
		"word/_rels/document.xml.rels": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
			`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
			`<Relationship Id="rId5" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" ` +
			`Target="media/image1.png"/></Relationships>`,
	}, map[string][]byte{"word/media/image1.png": onePixelPNG})

	out, err := Render(pkg, opt)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestImageBecomesADataURI(t *testing.T) {
	out := imageDoc(t, Options{})

	if !strings.Contains(out, "src=\"data:image/png;base64,") {
		t.Errorf("the image was not embedded:\n%s", out)
	}
	// 1905000 EMU is 200 px at 96 DPI; 952500 is 100 px.
	if !strings.Contains(out, "width:200px") || !strings.Contains(out, "height:100px") {
		t.Errorf("the drawn size was not carried over:\n%s", out)
	}
}

// A scanned page at full resolution would otherwise be sent to the viewer whole.
func TestOversizedImagesAreOmitted(t *testing.T) {
	out := imageDoc(t, Options{MaxImageBytes: 10})
	if strings.Contains(out, "data:image") {
		t.Error("an image over the size cap was embedded anyway")
	}
	if !strings.Contains(out, "image omitted") {
		t.Errorf("no note was left where the image should be:\n%s", out)
	}
}

func TestMissingImagePartIsSkipped(t *testing.T) {
	body := `<w:p><w:r><w:drawing><a:blip r:embed="rIdMissing"/></w:drawing></w:r>` +
		`<w:r><w:t>after</w:t></w:r></w:p>`
	out := render(t, body)

	if !strings.Contains(out, "after") {
		t.Error("text after a broken image was lost")
	}
}

// --- lists ------------------------------------------------------------------

func TestNumberingProducesLists(t *testing.T) {
	numbering := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
		`<w:numbering ` + wns + `>` +
		`<w:abstractNum w:abstractNumId="0"><w:lvl w:ilvl="0"><w:numFmt w:val="bullet"/></w:lvl></w:abstractNum>` +
		`<w:abstractNum w:abstractNumId="1"><w:lvl w:ilvl="0"><w:numFmt w:val="decimal"/></w:lvl></w:abstractNum>` +
		`<w:num w:numId="1"><w:abstractNumId w:val="0"/></w:num>` +
		`<w:num w:numId="2"><w:abstractNumId w:val="1"/></w:num>` +
		`</w:numbering>`

	item := func(numID string) string {
		return `<w:p><w:pPr><w:numPr><w:ilvl w:val="0"/><w:numId w:val="` + numID + `"/></w:numPr></w:pPr>` +
			`<w:r><w:t>item</w:t></w:r></w:p>`
	}
	pkg := build(t, map[string]string{
		"word/document.xml":  doc(item("1") + item("1")),
		"word/numbering.xml": numbering,
	}, nil)
	out, err := Render(pkg, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "<ul") || strings.Count(out, "<li") != 2 {
		t.Errorf("a bulleted list did not render as one:\n%s", out)
	}

	pkg = build(t, map[string]string{
		"word/document.xml":  doc(item("2") + item("2")),
		"word/numbering.xml": numbering,
	}, nil)
	out, err = Render(pkg, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "<ol") {
		t.Errorf("a numbered list did not render as one:\n%s", out)
	}
}

// Every list opened has to be closed, or the rest of the page ends up inside it.
func TestListsAreClosed(t *testing.T) {
	numbering := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
		`<w:numbering ` + wns + `>` +
		`<w:abstractNum w:abstractNumId="0"><w:lvl w:ilvl="0"><w:numFmt w:val="bullet"/></w:lvl></w:abstractNum>` +
		`<w:num w:numId="1"><w:abstractNumId w:val="0"/></w:num></w:numbering>`

	body := `<w:p><w:pPr><w:numPr><w:ilvl w:val="0"/><w:numId w:val="1"/></w:numPr></w:pPr>` +
		`<w:r><w:t>item</w:t></w:r></w:p>` +
		`<w:p><w:r><w:t>after the list</w:t></w:r></w:p>`

	pkg := build(t, map[string]string{
		"word/document.xml":  doc(body),
		"word/numbering.xml": numbering,
	}, nil)
	out, err := Render(pkg, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(out, "<ul") != strings.Count(out, "</ul>") {
		t.Errorf("unbalanced list tags:\n%s", out)
	}
	if i, j := strings.Index(out, "</ul>"), strings.Index(out, "after the list"); i < 0 || i > j {
		t.Errorf("the list was not closed before the paragraph after it:\n%s", out)
	}
}

// --- damaged files ----------------------------------------------------------

// A damaged file must produce a message, not a panic and not a blank page.
// Two layers refuse it: the package reader rejects an archive that is not a
// Word document at all, and the renderer rejects one whose document part cannot
// be used. Both are checked, because only one of them runs per file.
func TestBrokenDocumentsAreReported(t *testing.T) {
	// No document part: the package reader is the gate.
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("word/other.xml")
	w.Write([]byte("<x/>"))
	zw.Close()
	if _, err := ooxml.Open(buf.Bytes()); err == nil {
		t.Error("an archive with no document part was accepted")
	}

	// A document part that cannot be used: the renderer is the gate.
	cases := map[string]string{
		"not xml":         "this is not xml at all",
		"no body":         `<?xml version="1.0"?><w:document ` + wns + `/>`,
		"truncated xml":   `<?xml version="1.0"?><w:document ` + wns + `><w:body>`,
		"unexpected root": `<?xml version="1.0"?><nonsense/>`,
	}
	for name, content := range cases {
		pkg := build(t, map[string]string{"word/document.xml": content}, nil)
		out, err := Render(pkg, Options{})
		if err == nil && !strings.Contains(out, "dx-doc") {
			t.Errorf("%s: produced neither a document nor an error", name)
		}
		if err != nil && !strings.Contains(err.Error(), "docxhtml") {
			t.Errorf("%s: the error does not say where it came from: %v", name, err)
		}
	}
}

func TestEmptyBodyRenders(t *testing.T) {
	out := render(t, "")
	if !strings.Contains(out, "dx-doc") {
		t.Errorf("an empty document produced nothing usable: %q", out)
	}
}

func TestDeletedTextIsNotShown(t *testing.T) {
	out := render(t, `<w:p><w:del><w:r><w:delText>removed</w:delText></w:r></w:del>`+
		`<w:ins><w:r><w:t>added</w:t></w:r></w:ins></w:p>`)

	if strings.Contains(out, "removed") {
		t.Error("text deleted with tracked changes is being shown")
	}
	if !strings.Contains(out, "added") {
		t.Error("inserted text was dropped")
	}
}

// The same document must always produce the same markup, or a viewer that
// re-reads a file appears to change it.
func TestOutputIsDeterministic(t *testing.T) {
	body := `<w:p><w:r><w:rPr><w:b/><w:i/><w:color w:val="112233"/>` +
		`<w:sz w:val="24"/><w:rFonts w:ascii="Arial"/></w:rPr><w:t>x</w:t></w:r></w:p>`

	first := render(t, body)
	for i := 0; i < 20; i++ {
		if got := render(t, body); got != first {
			t.Fatalf("output changed between runs:\n%s\n---\n%s", first, got)
		}
	}
}

func timeout() <-chan time.Time { return time.After(10 * time.Second) }
