package find

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// minimalXlsx builds a workbook holding one shared string and one cell value,
// which is how Excel actually stores text: the string goes in the shared table
// and the sheet references it by index.
func minimalXlsx(t *testing.T, shared, cellValue string) []byte {
	t.Helper()
	files := map[string]string{
		"[Content_Types].xml": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
<Default Extension="xml" ContentType="application/xml"/>
<Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>
</Types>`,
		"_rels/.rels": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/>
</Relationships>`,
		"xl/workbook.xml": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheets><sheet name="Sheet1" sheetId="1" r:id="rId1"/></sheets></workbook>`,
		"xl/sharedStrings.xml": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" count="1" uniqueCount="1"><si><t>` + shared + `</t></si></sst>`,
		"xl/worksheets/sheet1.xml": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData><row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1"><v>` + cellValue + `</v></c></row></sheetData></worksheet>`,
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestIndexableExtensions(t *testing.T) {
	for _, in := range []string{"a.docx", "a.pdf", "a.xlsx", "a.txt", "a.png", "a.JPG", "a.tiff"} {
		if !Indexable(in) {
			t.Errorf("Indexable(%q) = false, want true", in)
		}
	}
	// Nothing can read these, so indexing them would only add dead rows.
	for _, in := range []string{"a.rar", "a.zip", "a.pmx", "a.msg", "a.exe", "a"} {
		if Indexable(in) {
			t.Errorf("Indexable(%q) = true, want false", in)
		}
	}
}

func TestXlsxTextReadsSharedStringsAndValues(t *testing.T) {
	dir := tempDir(t)
	path := filepath.Join(dir, "book.xlsx")
	if err := os.WriteFile(path, minimalXlsx(t, "شیر برقی VX-100", "125156"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := (&Extractor{}).Text(path)
	if err != nil {
		t.Fatalf("xlsx extraction failed: %v", err)
	}
	// The shared string is the cell's text; the bare number is a reference
	// someone will search for, so both have to reach the index.
	if !strings.Contains(got, "شیر برقی VX-100") {
		t.Errorf("shared string missing from extracted text: %q", got)
	}
	if !strings.Contains(got, "125156") {
		t.Errorf("cell value missing from extracted text: %q", got)
	}
}

// The reading path is the user's to point anywhere, and the tools that read
// PDFs and scans are optional. A machine without them must still index -- the
// document stays findable by name and reference, only its content is lost.
func TestExtractorWithoutToolsDegradesQuietly(t *testing.T) {
	dir := tempDir(t)
	pdf := filepath.Join(dir, "scan.pdf")
	if err := os.WriteFile(pdf, []byte("%PDF-1.4 not really a pdf"), 0o644); err != nil {
		t.Fatal(err)
	}
	img := filepath.Join(dir, "scan.png")
	if err := os.WriteFile(img, []byte("not really a png"), 0o644); err != nil {
		t.Fatal(err)
	}

	bare := &Extractor{} // no pdftotext, no pdftoppm, no tesseract
	if bare.OCRAvailable() {
		t.Fatal("an extractor with no tesseract path reported OCR available")
	}
	for _, p := range []string{pdf, img} {
		text, err := bare.Text(p)
		if err != nil {
			t.Errorf("Text(%s) errored with no tools configured: %v", filepath.Base(p), err)
		}
		if text != "" {
			t.Errorf("Text(%s) = %q, want empty", filepath.Base(p), text)
		}
	}
}

// A scan still has to be indexed, so a broken or unreadable file must come back
// as empty text rather than an error that would drop it from the archive.
func TestUnsupportedExtensionYieldsNothing(t *testing.T) {
	dir := tempDir(t)
	path := filepath.Join(dir, "bundle.rar")
	if err := os.WriteFile(path, []byte("rar payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	text, err := (&Extractor{}).Text(path)
	if err != nil || text != "" {
		t.Errorf("Text(.rar) = %q, %v; want empty and no error", text, err)
	}
}
