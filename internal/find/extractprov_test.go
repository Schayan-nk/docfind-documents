package find

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Schayan-nk/docfind-documents/internal/pgstore"
)

// Recording how a document's text was obtained.
//
// The three facts this captures cannot be worked out later -- doing so means
// extracting the whole archive again, which is the expensive operation storing
// the text exists to avoid repeating. So the tests are about the DECISIONS:
// which branch sets which flag, and above all that an empty result caused by
// missing tools is distinguishable from an empty document.

// TWO PATHS THROUGH THE SAME EXTRACTION MUST AGREE. TextWithProvenance is a
// near-copy of Text for PDFs rather than a refactor of it, because Text is on
// the hot path for every PDF in a 4,500-document scan. A near-copy drifts
// unless something checks, and the one that drifts would be the one writing
// the database.
func TestBothPDFPathsAgreeOnTheText(t *testing.T) {
	dir := tempDir(t)
	// A .txt, which both paths handle without any external tool, so this runs
	// on any machine.
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("نيروگاه نيكـان contract"), 0o600); err != nil {
		t.Fatal(err)
	}
	e := DefaultExtractor()

	plain, err1 := e.Text(path)
	withProv, prov, err2 := e.TextWithProvenance(path)
	if (err1 == nil) != (err2 == nil) {
		t.Errorf("the two paths disagree about failure: %v / %v", err1, err2)
	}
	if plain != withProv {
		t.Errorf("the two paths returned different text:\n  %q\n  %q", plain, withProv)
	}
	// A .txt involves no OCR, so there is nothing to record.
	if prov.OCRUsed || prov.Truncated || prov.ToolsMissing || prov.PagesRead != 0 {
		t.Errorf("a plain text file reported OCR provenance: %+v", prov)
	}
}

// A format read directly from its own container records no OCR.
func TestDirectlyReadFormatsRecordNoOCR(t *testing.T) {
	dir := tempDir(t)
	e := DefaultExtractor()
	for _, name := range []string{"a.txt", "b.docx", "c.xlsx"} {
		path := filepath.Join(dir, name)
		// Deliberately not a valid docx/xlsx: the point is the provenance, and
		// a parse failure must still report "no OCR" rather than "tools
		// missing" -- the tools were never going to help.
		if err := os.WriteFile(path, []byte("not really that format"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, prov, _ := e.TextWithProvenance(path)
		if prov.OCRUsed {
			t.Errorf("%s reported OCR", name)
		}
		if prov.ToolsMissing {
			t.Errorf("%s reported the OCR tools as missing, but none was needed", name)
		}
	}
}

// AN UNKNOWN FORMAT IS NOT A MISSING TOOL. Nothing would have read it, so
// reporting tools_missing would put it on the retry list for ever.
func TestAnUnknownFormatIsNotReportedAsAToolingFailure(t *testing.T) {
	dir := tempDir(t)
	path := filepath.Join(dir, "a.zip")
	if err := os.WriteFile(path, []byte("PK\x03\x04"), 0o600); err != nil {
		t.Fatal(err)
	}
	text, prov, err := DefaultExtractor().TextWithProvenance(path)
	if err != nil {
		t.Fatalf("an unindexable format returned an error: %v", err)
	}
	if text != "" {
		t.Errorf("text came back as %q", text)
	}
	if prov.ToolsMissing {
		t.Error("an unindexable format was reported as a tooling failure, so it would be retried for ever")
	}
}

// An image with no OCR available is a TOOLING failure: an image is nothing but
// a scan, so empty text means nobody could read it rather than that it is
// blank. This is the distinction that decides whether a later run retries it.
func TestAnImageWithNoOCRReportsTheToolsAsMissing(t *testing.T) {
	dir := tempDir(t)
	path := filepath.Join(dir, "scan.png")
	if err := os.WriteFile(path, []byte("\x89PNG\r\n\x1a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Forced, so the test does not depend on what is installed here.
	e := &Extractor{MaxOCRPages: 8}
	if e.OCRAvailable() {
		t.Skip("this extractor unexpectedly has OCR")
	}
	text, prov, err := e.TextWithProvenance(path)
	if err != nil {
		t.Fatal(err)
	}
	if text != "" {
		t.Errorf("text came back as %q with no OCR configured", text)
	}
	if !prov.ToolsMissing {
		t.Error("an unreadable image was not recorded as a tooling failure, so a " +
			"later run with tesseract installed would skip it")
	}
	if prov.OCRUsed {
		t.Error("OCR was reported as used with no OCR available")
	}
}

// A PDF with no text layer and no tools: the emptiness is the tools' absence.
func TestAPDFThatCannotBeReadAtAllRecordsWhy(t *testing.T) {
	dir := tempDir(t)
	path := filepath.Join(dir, "scan.pdf")
	// Not a real PDF. pdftotext will fail or return nothing, which is exactly
	// the shape of a scan with no text layer.
	if err := os.WriteFile(path, []byte("%PDF-1.4\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// No tools at all.
	e := &Extractor{MaxOCRPages: 8}
	text, prov, err := e.TextWithProvenance(path)
	if err != nil {
		t.Fatal(err)
	}
	if text != "" {
		t.Errorf("text came back as %q", text)
	}
	if !prov.ToolsMissing {
		t.Error("a PDF nobody could read was not recorded as a tooling failure")
	}
	if prov.OCRUsed || prov.Truncated {
		t.Errorf("OCR provenance was invented: %+v", prov)
	}
}

// The reuse bookkeeping, which decides whether a rescan costs seconds or hours.
func TestReuseIsSkippedWhenNothingIsStored(t *testing.T) {
	// A nil store must behave exactly as before this existed: extract
	// everything, record nothing, and never panic.
	var t0 *textReuse
	if body, ok := t0.reuse(&Doc{ID: "x"}); ok || body != "" {
		t.Error("a nil store claimed to have text")
	}
	t0.save(&Doc{ID: "x"}, Provenance{})
	t0.countReused()
	t0.countExtracted()

	r, err := newTextReuse(nil)
	if err != nil || r != nil {
		t.Errorf("a nil store produced %v / %v", r, err)
	}
}

// THE COUNTERS ARE WRITTEN FROM THE PARALLEL PASS, one goroutine per core, so
// they are behind a mutex rather than being plain fields. Run under -race this
// is the test that would catch losing that.
func TestTheReuseCountersSurviveConcurrentUse(t *testing.T) {
	r := &textReuse{
		stored: map[string]pgstore.Document{},
		bodies: map[string]string{},
	}
	const workers, each = 8, 200
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < each; j++ {
				r.countReused()
				r.countExtracted()
			}
		}()
	}
	wg.Wait()
	if r.Reused != workers*each || r.Extracted != workers*each {
		t.Errorf("counters came out at %d/%d, want %d each -- an increment was lost",
			r.Reused, r.Extracted, workers*each)
	}
}

// A stored row that matches the file on disk is reused; one that does not is
// extracted again. This is the decision the whole feature turns on.
func TestReuseHappensOnlyForAnUnchangedDocument(t *testing.T) {
	mod := time.Now().Add(-time.Hour).Truncate(time.Second)
	r := &textReuse{
		stored: map[string]pgstore.Document{
			"a.pdf": {
				Path: "a.pdf", Size: 100, ModifiedAt: mod,
				ExtractorVersion: pgstore.ExtractorVersion,
			},
			"b.pdf": {
				Path: "b.pdf", Size: 100, ModifiedAt: mod,
				ExtractorVersion: pgstore.ExtractorVersion,
			},
		},
		bodies: map[string]string{"a.pdf": "stored text"},
	}

	// Unchanged and its body is loaded: reused.
	if body, ok := r.reuse(&Doc{ID: "a.pdf", Size: 100, ModTime: mod}); !ok || body != "stored text" {
		t.Errorf("an unchanged document was not reused: %q / %v", body, ok)
	}
	// Unchanged but its body was not loaded: must NOT claim to be reused, or
	// the document would be indexed with empty text and look searched.
	if _, ok := r.reuse(&Doc{ID: "b.pdf", Size: 100, ModTime: mod}); ok {
		t.Error("a document with no loaded body was reported as reusable")
	}
	// Changed size.
	if _, ok := r.reuse(&Doc{ID: "a.pdf", Size: 101, ModTime: mod}); ok {
		t.Error("a document whose size changed was reused")
	}
	// Not stored at all.
	if _, ok := r.reuse(&Doc{ID: "c.pdf", Size: 100, ModTime: mod}); ok {
		t.Error("a document that was never stored was reused")
	}
}
