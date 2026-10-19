package pgstore

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Schayan-nk/docfind-documents/internal/fa"
)

// A sync reads and writes a whole folder's worth of rows, so it gets longer
// than a single-row operation but is still bounded.
const syncTimeout = 60 * time.Second

// The extracted text of the archive, so it is never extracted twice.
//
// SEE migrations/006_documents.sql FOR THE TRADE. This text is encrypted at
// rest in its current cache and is not encrypted here, so writing is off
// unless DOCFIND_PG_DOCTEXT=1 says otherwise. The store is built either way:
// the decision is the switch, not the code.
//
// NOT THE QUERY PATH. Text is read out of here once, at start-up, to build the
// in-process inverted index that already answers every search. Nothing here
// sits between a keystroke and a result.

// ExtractorVersion is bumped to invalidate every stored extraction.
//
// A CONSTANT IN THE BINARY, not a setting: it says "the code that produced
// this text has changed", and only a new binary can mean that. Bump it when
// extraction itself improves -- a better OCR language set, a higher page cap,
// a fixed parser -- and every document is re-read once. Forgetting to bump it
// means the improvement silently never reaches anything already indexed.
const ExtractorVersion = 1

// DocTextEnabled reports whether storing document text is switched on.
//
// OFF BY DEFAULT, and deliberately an explicit opt-in rather than "on when a
// database is configured". Every other table here holds data that was already
// unencrypted; this one holds text that is encrypted today. Turning that off
// should be somebody's decision, made once, visibly -- not a side effect of
// having configured PostgreSQL for the accounts.
func DocTextEnabled() bool {
	v := strings.TrimSpace(os.Getenv("DOCFIND_PG_DOCTEXT"))
	return v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "yes")
}

// Document is one archive file's stored text and how it was obtained.
type Document struct {
	Path       string    `json:"path"`
	Size       int64     `json:"size"`
	ModifiedAt time.Time `json:"modified_at"`
	SHA256     string    `json:"sha256,omitempty"`

	ExtractorVersion int       `json:"extractor_version"`
	ExtractedAt      time.Time `json:"extracted_at"`

	OCRUsed      bool `json:"ocr_used"`
	OCRTruncated bool `json:"ocr_truncated"`
	PagesRead    int  `json:"pages_read"`
	ToolsMissing bool `json:"tools_missing"`

	Body string `json:"-"` // never serialised: it is the document's contents
}

// Documents is the store.
type Documents struct{ db *DB }

// NewDocuments returns a store over db.
func NewDocuments(db *DB) *Documents { return &Documents{db: db} }

// Stored is what the archive looked like when each document was last read.
//
// Deliberately WITHOUT the body: this is called to decide what needs
// extracting, over thousands of rows, and pulling every document's full text
// across to answer "has this changed" would move tens of megabytes to compare
// three columns.
func (d *Documents) Stored() (map[string]Document, error) {
	ctx, cancel := context.WithTimeout(context.Background(), syncTimeout)
	defer cancel()

	rows, err := d.db.Pool().Query(ctx, `
		SELECT path, size_bytes, modified_at, coalesce(content_sha256,''),
		       extractor_version, extracted_at,
		       ocr_used, ocr_truncated, pages_read, tools_missing
		  FROM documents`)
	if err != nil {
		return nil, fmt.Errorf("pgstore: reading stored documents: %w", err)
	}
	defer rows.Close()

	out := map[string]Document{}
	for rows.Next() {
		var doc Document
		if err := rows.Scan(&doc.Path, &doc.Size, &doc.ModifiedAt, &doc.SHA256,
			&doc.ExtractorVersion, &doc.ExtractedAt,
			&doc.OCRUsed, &doc.OCRTruncated, &doc.PagesRead, &doc.ToolsMissing); err != nil {
			return nil, fmt.Errorf("pgstore: reading a stored document: %w", err)
		}
		out[doc.Path] = doc
	}
	return out, rows.Err()
}

// Texts streams every stored body to fn, for building the in-memory index.
//
// STREAMED RATHER THAN RETURNED AS A MAP. The archive is 4,510 documents of
// extracted text; collecting all of it into one map before the caller touches
// any of it doubles the peak memory for no gain, since the caller is feeding
// each one straight into an index.
func (d *Documents) Texts(fn func(path, body string)) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), syncTimeout)
	defer cancel()

	rows, err := d.db.Pool().Query(ctx,
		`SELECT path, body FROM documents WHERE body <> ''`)
	if err != nil {
		return 0, fmt.Errorf("pgstore: reading document text: %w", err)
	}
	defer rows.Close()

	n := 0
	for rows.Next() {
		var path, body string
		if err := rows.Scan(&path, &body); err != nil {
			return n, fmt.Errorf("pgstore: reading a document's text: %w", err)
		}
		fn(path, body)
		n++
	}
	return n, rows.Err()
}

// NeedsExtraction reports whether a file on disk differs from what is stored.
//
// SIZE, TIME OR VERSION -- never a blind rescan. Re-reading 4,510 documents to
// find the six that changed is hours of PDF extraction, which is the measured
// throughput ceiling of the whole scan.
//
// A stored row whose extractor version is older is stale by definition: the
// code that produced its text has been replaced.
func (d Document) NeedsExtraction(size int64, mod time.Time) bool {
	if d.Path == "" {
		return true // nothing stored
	}
	if d.ExtractorVersion != ExtractorVersion {
		return true
	}
	if d.Size != size {
		return true
	}
	// Truncated to the second: PostgreSQL keeps microseconds and a Windows
	// filesystem does not agree with it to that precision, so comparing
	// exactly would re-extract the whole archive on every start.
	if !d.ModifiedAt.Truncate(time.Second).Equal(mod.UTC().Truncate(time.Second)) {
		return true
	}
	// TOOLS MISSING IS WORTH RETRYING, and this is the reason that column
	// exists. The document was unreadable because tesseract was not installed;
	// if it has been installed since, this is the row that should be read
	// again. A genuinely blank scan is left alone.
	return d.ToolsMissing
}

// Save stores one document's text and provenance.
func (d *Documents) Save(doc Document) error {
	doc.Path = strings.TrimSpace(doc.Path)
	if doc.Path == "" {
		return fmt.Errorf("pgstore: a document needs a path")
	}
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()

	var sha any
	if doc.SHA256 != "" {
		sha = doc.SHA256
	}
	_, err := d.db.Pool().Exec(ctx, `
		INSERT INTO documents (path, size_bytes, modified_at, content_sha256,
		                       extractor_version, extracted_at,
		                       ocr_used, ocr_truncated, pages_read, tools_missing,
		                       body, body_folded)
		VALUES ($1,$2,$3,$4,$5,now(),$6,$7,$8,$9,$10,$11)
		ON CONFLICT (path) DO UPDATE SET
		    size_bytes        = EXCLUDED.size_bytes,
		    modified_at       = EXCLUDED.modified_at,
		    content_sha256    = EXCLUDED.content_sha256,
		    extractor_version = EXCLUDED.extractor_version,
		    extracted_at      = EXCLUDED.extracted_at,
		    ocr_used          = EXCLUDED.ocr_used,
		    ocr_truncated     = EXCLUDED.ocr_truncated,
		    pages_read        = EXCLUDED.pages_read,
		    tools_missing     = EXCLUDED.tools_missing,
		    body              = EXCLUDED.body,
		    body_folded       = EXCLUDED.body_folded`,
		doc.Path, doc.Size, doc.ModifiedAt.UTC(), sha,
		ExtractorVersion,
		doc.OCRUsed, doc.OCRTruncated, doc.PagesRead, doc.ToolsMissing,
		doc.Body, fa.Fold(doc.Body))
	if err != nil {
		return fmt.Errorf("pgstore: saving document %q: %w", doc.Path, err)
	}
	return nil
}

// Delete removes one document, for the archive watcher's incremental path: a
// file that is gone should not leave its extracted text and provenance
// behind for some future, unrelated file at the same path to inherit.
// Deleting a path that was never stored is not an error.
func (d *Documents) Delete(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("pgstore: a document needs a path")
	}
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	if _, err := d.db.Pool().Exec(ctx, `DELETE FROM documents WHERE path = $1`, path); err != nil {
		return fmt.Errorf("pgstore: deleting document %q: %w", path, err)
	}
	return nil
}
