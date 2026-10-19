package find

import (
	"archive/zip"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Schayan-nk/docfind-documents/internal/xmltree"
)

// Extractor turns a file on disk into plain text for the search index.
//
// Everything beyond .docx needs an external tool, and the reading path is the
// user's to point wherever they like -- at an archive of generated documents
// with a real text layer, or at a folder of old scans. So every tool here is
// optional and discovered at startup: a missing binary degrades that one format
// to "listed by name and reference, not searchable by content" rather than
// failing the scan. A file that cannot be read is never a reason to lose the
// rest of the index.
type Extractor struct {
	// PDFToText renders a PDF's embedded text layer. Empty disables PDF text.
	PDFToText string
	// PDFToPPM rasterises PDF pages so they can be OCR'd. Empty disables PDF
	// OCR, but PDFs with a text layer still index through PDFToText.
	PDFToPPM string
	// Tesseract reads text off an image. Empty disables OCR everywhere.
	Tesseract string
	// Languages is passed to tesseract -l. Persian documents here are
	// routinely bilingual, so the default trains on both scripts.
	Languages string
	// MaxOCRPages caps how many pages of one PDF are rasterised and read.
	// OCR is orders of magnitude slower than reading a text layer, and the
	// reference number and subject live on the first page or two.
	MaxOCRPages int
	// Timeout bounds any single external command.
	Timeout time.Duration
}

// ocrTextThreshold is how few characters a PDF's text layer may yield before
// it is treated as a scan. A digitally generated page carries far more than
// this; a scanned one typically returns nothing at all, or a few stray
// ligatures from an embedded logo.
const ocrTextThreshold = 96

// DefaultExtractor discovers the external tools on this machine.
func DefaultExtractor() *Extractor {
	e := &Extractor{
		Languages:   "fas+eng",
		MaxOCRPages: 8,
		Timeout:     90 * time.Second,
	}
	e.PDFToText = findTool("pdftotext")
	e.PDFToPPM = findTool("pdftoppm")
	e.Tesseract = findTool("tesseract")
	return e
}

// toolSearchDirs are non-PATH locations worth checking, so an operator who
// unzipped poppler next to the app does not also have to edit PATH.
var toolSearchDirs = []string{
	`C:\Program Files\poppler\Library\bin`,
	`C:\Program Files\Tesseract-OCR`,
	`C:\Program Files (x86)\Tesseract-OCR`,
	`C:\poppler\bin`,
}

// findTool locates a binary on PATH, falling back to the known install spots.
func findTool(name string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	exe := name
	if !strings.HasSuffix(exe, ".exe") {
		exe += ".exe"
	}
	dirs := append([]string{}, toolSearchDirs...)
	if self, err := os.Executable(); err == nil {
		dirs = append(dirs, filepath.Dir(self), filepath.Join(filepath.Dir(self), "tools"))
	}
	for _, d := range dirs {
		full := filepath.Join(d, exe)
		if st, err := os.Stat(full); err == nil && !st.IsDir() {
			return full
		}
	}
	return ""
}

// OCRAvailable reports whether scanned pages can be read at all.
func (e *Extractor) OCRAvailable() bool {
	return e != nil && e.Tesseract != ""
}

// indexableExts is every extension the scanner will pick up. Formats with no
// extractor are still worth indexing: the file name and reference number are
// often what someone actually searches for, and listing it beats hiding it.
var indexableExts = map[string]bool{
	".docx": true, ".pdf": true, ".xlsx": true, ".txt": true,
	".png": true, ".jpg": true, ".jpeg": true, ".tif": true, ".tiff": true,
}

// Indexable reports whether a path should be picked up by a scan.
func Indexable(path string) bool {
	return indexableExts[strings.ToLower(filepath.Ext(path))]
}

// Text extracts a file's searchable text, dispatching on extension.
//
// An error means the file could not be read at all. Returning empty text with
// no error is normal and expected -- an image with no OCR configured, or a
// format with no extractor -- and the caller still indexes the document by
// name and reference.
func (e *Extractor) Text(path string) (string, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".docx":
		return ExtractText(path)
	case ".pdf":
		return e.pdfText(path)
	case ".xlsx":
		return e.xlsxText(path)
	case ".txt":
		b, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		return string(b), nil
	case ".png", ".jpg", ".jpeg", ".tif", ".tiff":
		return e.ocrImage(path), nil
	}
	return "", nil
}

// pdfText reads a PDF's text layer, falling back to OCR when that layer is
// missing or nearly empty -- the signature of a scan.
func (e *Extractor) pdfText(path string) (string, error) {
	var text string
	if e.PDFToText != "" {
		// "-" writes to stdout; -layout keeps columns apart so a table does
		// not collapse into one unreadable line.
		if out, err := e.run(e.PDFToText, "-layout", "-q", path, "-"); err == nil {
			text = out
		}
	}
	if len(strings.TrimSpace(text)) >= ocrTextThreshold || !e.OCRAvailable() || e.PDFToPPM == "" {
		return text, nil
	}
	if ocr := e.ocrPDF(path); strings.TrimSpace(ocr) != "" {
		return ocr, nil
	}
	return text, nil
}

// ocrPDF rasterises the first pages of a PDF and reads each one.
func (e *Extractor) ocrPDF(path string) string {
	dir, err := os.MkdirTemp("", "docfind-ocr-")
	if err != nil {
		return ""
	}
	defer os.RemoveAll(dir)

	pages := e.MaxOCRPages
	if pages <= 0 {
		pages = 8
	}
	// 200 dpi is the usual floor for reliable OCR; higher mostly costs time.
	prefix := filepath.Join(dir, "p")
	if _, err := e.run(e.PDFToPPM, "-png", "-r", "200", "-f", "1", "-l", itoa(pages), path, prefix); err != nil {
		return ""
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var images []string
	for _, en := range entries {
		if !en.IsDir() && strings.HasSuffix(strings.ToLower(en.Name()), ".png") {
			images = append(images, filepath.Join(dir, en.Name()))
		}
	}
	// Page order matters for reading the result back.
	sort.Strings(images)

	var b strings.Builder
	for _, img := range images {
		if t := e.ocrImage(img); strings.TrimSpace(t) != "" {
			b.WriteString(t)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// ocrSlots bounds how many tesseract processes may run at once, across every
// caller -- a scan's own extraction workers (extractAll, in index.go, starts
// up to runtime.NumCPU() of them) and any other path that reaches ocrImage.
// OCR is the slowest, most CPU-heavy step this package runs, so without a
// cap a many-core machine mid-scan would happily run a tesseract process per
// core and leave little of the machine for the web requests docfind is
// otherwise answering. Two is enough to keep a scan moving without
// dominating it.
var ocrSlots = newSemaphore(2)

// ocrImage reads text off a single image, returning "" when OCR is
// unavailable or the image holds no recognisable text.
func (e *Extractor) ocrImage(path string) string {
	if !e.OCRAvailable() {
		return ""
	}
	ocrSlots.acquire()
	defer ocrSlots.release()
	langs := e.Languages
	if langs == "" {
		langs = "fas+eng"
	}
	out, err := e.run(e.Tesseract, path, "stdout", "-l", langs)
	if err != nil {
		return ""
	}
	return out
}

// xlsxText pulls the shared strings and inline cell text out of a workbook.
//
// Numbers are deliberately included: a spreadsheet's reference numbers and
// quantities are exactly what someone searches for.
func (e *Extractor) xlsxText(path string) (string, error) {
	// Read the zip directly rather than through internal/ooxml: that package
	// validates its input is a WordprocessingML document and rejects a
	// workbook outright.
	zr, err := zip.OpenReader(path)
	if err != nil {
		return "", err
	}
	defer zr.Close()

	var b strings.Builder
	for _, f := range zr.File {
		lower := strings.ToLower(f.Name)
		if !strings.HasPrefix(lower, "xl/") {
			continue
		}
		isStrings := strings.HasSuffix(lower, "sharedstrings.xml")
		isSheet := strings.Contains(lower, "worksheets/") && strings.HasSuffix(lower, ".xml")
		if !isStrings && !isSheet {
			continue
		}
		data, err := readZipEntry(f)
		if err != nil {
			continue // a damaged sheet should not lose the rest of the workbook
		}
		doc, err := xmltree.Parse(data)
		if err != nil || doc.Root() == nil {
			continue
		}
		for _, tag := range []string{"t", "v"} {
			for _, n := range doc.Root().FindAll(tag) {
				if s := strings.TrimSpace(n.TextContent()); s != "" {
					b.WriteString(s)
					b.WriteByte('\n')
				}
			}
		}
	}
	return b.String(), nil
}

// maxZipEntryBytes bounds how much one workbook part may decompress to, so a
// crafted or corrupt file in the reading path cannot exhaust memory during a
// scan. Real sheets are far smaller than this.
const maxZipEntryBytes = 64 << 20

func readZipEntry(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(io.LimitReader(rc, maxZipEntryBytes))
}

// run executes one external tool and returns its stdout.
//
// Arguments are passed as a slice rather than a command line, so no part of a
// file name can be taken as a flag or a shell metacharacter. The timeout is
// what keeps one corrupt scan from stalling an entire re-index.
func (e *Extractor) run(tool string, args ...string) (string, error) {
	timeout := e.Timeout
	if timeout <= 0 {
		timeout = 90 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, tool, args...)
	cmd.Stderr = nil
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// itoa avoids pulling strconv in for one call site.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
