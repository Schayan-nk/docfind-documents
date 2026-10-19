package find

import (
	"os"
	"path/filepath"
	"strings"
)

// Recording HOW a document's text was obtained, not just what it says.
//
// WHY THIS CANNOT BE ADDED LATER. Today OCR writes into the same text as
// direct extraction and nothing anywhere records which happened. The three
// facts below are not recoverable from the stored text afterwards -- working
// them out means extracting the archive again, which is the expensive
// operation storing the text exists to avoid repeating. So they are written
// with the first row or they are never written.
//
// THE ONE THAT CHANGES ANSWERS IS Truncated. OCR stops after MaxOCRPages,
// eight by default. A forty-page scanned contract is therefore indexed from
// its first eight pages, and a search for a term on page nine finds nothing --
// today, silently. Recording it is the difference between "this document does
// not mention it" and "we only looked at a fifth of it", and only one of those
// is an answer somebody can act on.

// Provenance says how a file's text was produced.
type Provenance struct {
	// OCRUsed is true when tesseract produced this text rather than an
	// embedded text layer. Embedded text is exact; OCR text contains errors,
	// so search ranking should not weigh them equally.
	OCRUsed bool
	// Truncated is true when OCR stopped before the end of the document.
	Truncated bool
	// PagesRead is how many pages OCR actually read, or 0 when OCR was not
	// used. Stored beside Truncated because "we read 8" and "we read 8 of 40"
	// are different facts and only the pair distinguishes them.
	PagesRead int
	// ToolsMissing is true when the text is empty because the external tools
	// were absent, not because the document is empty. Without it a later run
	// cannot tell a genuinely blank scan from one nobody could read -- and
	// would either re-OCR everything for ever or skip the readable ones.
	ToolsMissing bool
}

// TextWithProvenance extracts a file's text and says how it got it.
//
// Text() stays as it was and delegates here, so every existing caller is
// unchanged and there is only one extraction path -- two would drift, and the
// one that drifted would be the one writing the database.
func (e *Extractor) TextWithProvenance(path string) (string, Provenance, error) {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".pdf":
		return e.pdfTextWithProvenance(path)

	case ".png", ".jpg", ".jpeg", ".tif", ".tiff":
		// An image is nothing but a scan: any text at all came from OCR.
		if !e.OCRAvailable() {
			return "", Provenance{ToolsMissing: true}, nil
		}
		text := e.ocrImage(path)
		return text, Provenance{OCRUsed: true, PagesRead: 1}, nil

	case ".docx", ".xlsx", ".txt":
		// Read directly from the file format. No OCR is involved, so there is
		// no provenance to record beyond that.
		text, err := e.Text(path)
		return text, Provenance{}, err
	}
	// A format with no extractor. Not ToolsMissing: no tool would have helped.
	return "", Provenance{}, nil
}

// pdfTextWithProvenance is pdfText, saying which branch it took.
//
// Deliberately a near-copy of pdfText rather than a refactor of it: pdfText is
// on the hot path for every PDF in a 4,500-document scan and is called from
// code this change does not touch. The two are kept honest by
// TestBothPDFPathsAgreeOnTheText.
func (e *Extractor) pdfTextWithProvenance(path string) (string, Provenance, error) {
	var p Provenance
	var text string
	if e.PDFToText != "" {
		if out, err := e.run(e.PDFToText, "-layout", "-q", path, "-"); err == nil {
			text = out
		}
	}

	// Enough embedded text: this is a digitally generated PDF and OCR would
	// only make it worse.
	if len(strings.TrimSpace(text)) >= ocrTextThreshold {
		return text, p, nil
	}

	// Below the threshold, so it looks like a scan. Whether anything can be
	// done about that depends on the tools.
	if !e.OCRAvailable() || e.PDFToPPM == "" {
		// The text is short AND nothing could read it. If there was no
		// embedded text at all, the emptiness is the tools' absence rather
		// than the document's.
		if strings.TrimSpace(text) == "" {
			p.ToolsMissing = true
		}
		return text, p, nil
	}

	ocr, pages := e.ocrPDFCounted(path)
	if strings.TrimSpace(ocr) == "" {
		// OCR ran and found nothing. That is a genuinely blank scan, which is
		// a different and useful fact from "nobody could read it".
		return text, p, nil
	}
	p.OCRUsed = true
	p.PagesRead = pages
	// Truncated when OCR read exactly its cap: the page after the cap may or
	// may not exist, and this cannot tell. Reported as possibly-incomplete
	// rather than guessed at, because the failure being guarded against is a
	// search that quietly missed page nine.
	if e.MaxOCRPages > 0 && pages >= e.MaxOCRPages {
		p.Truncated = true
	}
	return ocr, p, nil
}

// ocrPDFCounted is ocrPDF, reporting how many pages it read.
func (e *Extractor) ocrPDFCounted(path string) (string, int) {
	dir, err := os.MkdirTemp("", "docfind-ocr-")
	if err != nil {
		return "", 0
	}
	defer os.RemoveAll(dir)

	max := e.MaxOCRPages
	if max <= 0 {
		max = 8
	}
	prefix := filepath.Join(dir, "p")
	// 200 dpi: enough for tesseract on a typed page, and four times fewer
	// pixels than 400 for no measurable loss on this archive.
	if _, err := e.run(e.PDFToPPM, "-r", "200", "-f", "1", "-l", itoa(max),
		"-png", path, prefix); err != nil {
		return "", 0
	}
	pages, err := filepath.Glob(prefix + "*.png")
	if err != nil || len(pages) == 0 {
		return "", 0
	}
	// Glob order is lexical, which for p-1..p-9 and p-10 puts 10 before 2.
	// It does not matter for a bag of words, and sorting it would imply an
	// ordering the caller does not get.
	var b strings.Builder
	read := 0
	for _, page := range pages {
		if t := strings.TrimSpace(e.ocrImage(page)); t != "" {
			b.WriteString(t)
			b.WriteByte('\n')
		}
		// Counted whether or not it yielded text: a blank page was still
		// looked at, and the cap is about pages read, not pages that helped.
		read++
	}
	return b.String(), read
}
