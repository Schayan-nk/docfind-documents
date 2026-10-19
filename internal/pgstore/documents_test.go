package pgstore

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// The document text store.
//
// The behaviour worth testing is the RE-EXTRACTION DECISION: what counts as
// changed, what does not, and what must be retried. Getting it too eager
// re-reads 4,510 documents on every start -- hours of PDF extraction, which is
// the measured ceiling of the whole scan. Getting it too lax serves stale text
// for ever.

func freshDocuments(t *testing.T) *Documents {
	t.Helper()
	db := testDB(t)
	if _, err := db.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	clean := func() {
		db.Pool().Exec(context.Background(),
			`DELETE FROM documents WHERE path LIKE 'test/%'`)
	}
	clean()
	t.Cleanup(clean)
	return NewDocuments(db)
}

func TestDocumentTextAndProvenanceRoundTrip(t *testing.T) {
	d := freshDocuments(t)
	mod := time.Now().Add(-time.Hour).Truncate(time.Second)

	err := d.Save(Document{
		Path: "test/a.pdf", Size: 1234, ModifiedAt: mod,
		OCRUsed: true, OCRTruncated: true, PagesRead: 8,
		Body: "نيروگاه نيكـان contract terms",
	})
	if err != nil {
		t.Fatal(err)
	}

	stored, err := d.Stored()
	if err != nil {
		t.Fatal(err)
	}
	got, ok := stored["test/a.pdf"]
	if !ok {
		t.Fatal("the document was not stored")
	}
	if got.Size != 1234 {
		t.Errorf("size came back as %d", got.Size)
	}
	if !got.OCRUsed || !got.OCRTruncated || got.PagesRead != 8 {
		t.Errorf("the provenance was lost: %+v", got)
	}
	if got.ExtractorVersion != ExtractorVersion {
		t.Errorf("extractor version is %d, want %d", got.ExtractorVersion, ExtractorVersion)
	}
	// Stored() deliberately omits bodies -- pulling every document's text
	// across to answer "has this changed" would move tens of megabytes.
	if got.Body != "" {
		t.Error("Stored() carried a body; it is called over thousands of rows")
	}

	// The text comes back through Texts, and the FOLD went in beside it: the
	// fold is what makes Persian searchable, and PostgreSQL has no Persian
	// stemmer to do it later.
	bodies := map[string]string{}
	n, err := d.Texts(func(path, body string) { bodies[path] = body })
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 || bodies["test/a.pdf"] != "نيروگاه نيكـان contract terms" {
		t.Errorf("the body did not round-trip: %q", bodies["test/a.pdf"])
	}

	var folded string
	err = d.db.Pool().QueryRow(context.Background(),
		`SELECT body_folded FROM documents WHERE path = 'test/a.pdf'`).Scan(&folded)
	if err != nil {
		t.Fatal(err)
	}
	// The Arabic kaf and the kashida must be gone: that is the fold's job and
	// the reason the tender search was missing rows.
	if strings.Contains(folded, "ك") || strings.Contains(folded, "ـ") {
		t.Errorf("the stored fold still carries an Arabic kaf or a kashida: %q", folded)
	}
	if !strings.Contains(folded, "نیکان") {
		t.Errorf("the fold did not normalise the town name: %q", folded)
	}
}

// Delete is the archive watcher's incremental counterpart to Forget: one row
// gone from disk should not leave its old text behind.
func TestDeleteRemovesOneDocument(t *testing.T) {
	d := freshDocuments(t)
	if err := d.Save(Document{
		Path: "test/removeme.pdf", Size: 1, ModifiedAt: time.Now(), Body: "x",
	}); err != nil {
		t.Fatal(err)
	}
	if err := d.Save(Document{
		Path: "test/keep.pdf", Size: 1, ModifiedAt: time.Now(), Body: "y",
	}); err != nil {
		t.Fatal(err)
	}

	if err := d.Delete("test/removeme.pdf"); err != nil {
		t.Fatal(err)
	}

	stored, err := d.Stored()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := stored["test/removeme.pdf"]; ok {
		t.Error("the deleted document is still stored")
	}
	if _, ok := stored["test/keep.pdf"]; !ok {
		t.Error("Delete removed an unrelated document")
	}

	// Deleting something never stored is not an error: the watcher cannot
	// know in advance whether a removed path was ever extracted.
	if err := d.Delete("test/never-stored.pdf"); err != nil {
		t.Errorf("deleting an unknown path should be harmless, got %v", err)
	}
}

// THE RE-EXTRACTION DECISION, which is where the cost lives.
func TestWhatCountsAsNeedingReExtraction(t *testing.T) {
	mod := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	base := Document{
		Path: "test/a.pdf", Size: 100, ModifiedAt: mod,
		ExtractorVersion: ExtractorVersion,
	}

	if base.NeedsExtraction(100, mod) {
		t.Error("an unchanged document wants re-extracting; a rescan would re-read the archive")
	}
	if !base.NeedsExtraction(101, mod) {
		t.Error("a changed size was not noticed")
	}
	if !base.NeedsExtraction(100, mod.Add(time.Minute)) {
		t.Error("a changed modification time was not noticed")
	}

	// Sub-second differences must NOT trigger: PostgreSQL keeps microseconds
	// and a Windows filesystem does not agree to that precision, so comparing
	// exactly would re-extract everything on every single start.
	if base.NeedsExtraction(100, mod.Add(400*time.Millisecond)) {
		t.Error("a sub-second clock difference triggered a full re-extraction")
	}

	// Nothing stored at all.
	var absent Document
	if !absent.NeedsExtraction(100, mod) {
		t.Error("a document with no stored row was not marked for extraction")
	}

	// A bumped extractor version invalidates everything, which is the only way
	// an improvement to extraction reaches documents already indexed.
	old := base
	old.ExtractorVersion = ExtractorVersion - 1
	if !old.NeedsExtraction(100, mod) {
		t.Error("an older extractor version was treated as current")
	}

	// TOOLS MISSING IS RETRIED. The document was unreadable because tesseract
	// was not installed; if it has been installed since, this is exactly the
	// row to read again. A genuinely blank scan is left alone.
	missing := base
	missing.ToolsMissing = true
	if !missing.NeedsExtraction(100, mod) {
		t.Error("a document that failed for want of OCR tools is never retried")
	}
	blank := base
	blank.ToolsMissing = false
	if blank.NeedsExtraction(100, mod) {
		t.Error("a genuinely blank document is re-read for ever")
	}
}

// The switch is off unless somebody says so, because this text is encrypted
// where it lives today.
func TestStoringDocumentTextIsOffUnlessAskedFor(t *testing.T) {
	old, had := os.LookupEnv("DOCFIND_PG_DOCTEXT")
	t.Cleanup(func() {
		if had {
			os.Setenv("DOCFIND_PG_DOCTEXT", old)
		} else {
			os.Unsetenv("DOCFIND_PG_DOCTEXT")
		}
	})

	os.Unsetenv("DOCFIND_PG_DOCTEXT")
	if DocTextEnabled() {
		t.Error("storing document text is on by default; it trades an encryption key for database access control")
	}
	for _, v := range []string{"1", "true", "TRUE", "yes"} {
		os.Setenv("DOCFIND_PG_DOCTEXT", v)
		if !DocTextEnabled() {
			t.Errorf("%q did not switch it on", v)
		}
	}
	for _, v := range []string{"", "0", "false", "no", "maybe"} {
		os.Setenv("DOCFIND_PG_DOCTEXT", v)
		if DocTextEnabled() {
			t.Errorf("%q switched it on", v)
		}
	}
}
