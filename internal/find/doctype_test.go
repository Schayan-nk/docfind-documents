package find

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every document used to be served as a .docx attachment whatever it actually
// was: a PDF arrived as a file the browser refused to open, under a name that
// lied about its format, and an image could not be viewed at all.

func TestDocContentType(t *testing.T) {
	cases := []struct {
		name       string
		wantMIME   string
		wantInline bool
	}{
		{"Contract_111222.docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", false},
		{"scan.pdf", "application/pdf", true},
		{"prices.xlsx", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", false},
		{"notes.txt", "text/plain; charset=utf-8", true},
		{"photo.png", "image/png", true},
		{"photo.jpg", "image/jpeg", true},
		{"photo.jpeg", "image/jpeg", true},
		// An image format no browser renders: correctly typed, still downloaded.
		{"fax.tif", "image/tiff", false},
		{"fax.tiff", "image/tiff", false},
		// Case must not matter; Windows archives are full of upper-case names.
		{"SCAN.PDF", "application/pdf", true},
		{"CONTRACT.DocX", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", false},
		// Anything else is handed over as bytes rather than guessed at.
		{"archive.zip", "application/octet-stream", false},
		{"no-extension", "application/octet-stream", false},
		// A double extension must be read from the last one only.
		{"report.docx.pdf", "application/pdf", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mime, inline := docContentType(c.name)
			if mime != c.wantMIME {
				t.Errorf("type %q, want %q", mime, c.wantMIME)
			}
			if inline != c.wantInline {
				t.Errorf("inline %v, want %v", inline, c.wantInline)
			}
		})
	}
}

// No format may ever be served as HTML: the archive holds files this
// application did not create, and an HTML type on the app's own origin would
// let a planted file run script against the signed-in session.
func TestNoDocumentIsServedAsHTML(t *testing.T) {
	for ext, typ := range docTypes {
		if strings.Contains(typ.mime, "html") || strings.Contains(typ.mime, "xml+xhtml") {
			t.Errorf("%s declares %q", ext, typ.mime)
		}
	}
	if mime, _ := docContentType("evil.html"); mime != "application/octet-stream" {
		t.Errorf("an .html file would be served as %q", mime)
	}
	if mime, _ := docContentType("evil.svg"); mime != "application/octet-stream" {
		t.Errorf("an .svg file would be served as %q -- SVG executes script", mime)
	}
}

// End to end through the real route, because the header plumbing is where the
// bug actually lived.
func TestServedDocumentsDeclareTheirRealType(t *testing.T) {
	root := tempDir(t)
	dir := filepath.Join(root, "900", "100")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	write := func(name string, body []byte) {
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("Contract_900100.docx", minimalDocx(t, "قرارداد"))
	write("scan_900100.pdf", []byte("%PDF-1.4\nstub\n"))
	write("photo_900100.png", []byte("\x89PNG\r\n\x1a\nstub"))
	write("notes_900100.txt", []byte("plain text"))
	write("fax_900100.tif", []byte("II*\x00stub"))

	ix := NewIndex(root)
	if _, err := ix.Scan(); err != nil {
		t.Fatal(err)
	}
	// Empty token and no accounts: /api/doc is open, as in desktop mode.
	s := NewServer(ix, "")

	cases := []struct {
		id              string
		wantType        string
		wantDisposition string
	}{
		{"900/100/scan_900100.pdf", "application/pdf", "inline"},
		{"900/100/photo_900100.png", "image/png", "inline"},
		{"900/100/notes_900100.txt", "text/plain; charset=utf-8", "inline"},
		{"900/100/fax_900100.tif", "image/tiff", "attachment"},
		{"900/100/Contract_900100.docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", "attachment"},
	}
	for _, c := range cases {
		t.Run(filepath.Ext(c.id), func(t *testing.T) {
			rec := do(t, s, "GET", "/api/doc?id="+c.id)
			if rec.Code != 200 {
				t.Fatalf("status %d: %s", rec.Code, strings.TrimSpace(rec.Body.String()))
			}
			if got := rec.Header().Get("Content-Type"); got != c.wantType {
				t.Errorf("Content-Type %q, want %q", got, c.wantType)
			}
			cd := rec.Header().Get("Content-Disposition")
			if !strings.HasPrefix(cd, c.wantDisposition+";") {
				t.Errorf("Content-Disposition %q, want %s", cd, c.wantDisposition)
			}
			// The filename must still be there in both forms, or a download
			// lands as "doc" with no extension.
			if !strings.Contains(cd, `filename="`) || !strings.Contains(cd, "filename*=UTF-8''") {
				t.Errorf("Content-Disposition lost the filename: %q", cd)
			}
		})
	}
}

// ServeContent sniffs a type only when none is set, and Range support is what
// lets a browser's PDF viewer page through a large scan without downloading
// all of it. Both must survive the change.
func TestPDFKeepsRangeSupportAndDeclaredType(t *testing.T) {
	root := tempDir(t)
	dir := filepath.Join(root, "900", "100")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	body := []byte("%PDF-1.4\n" + strings.Repeat("x", 4096))
	if err := os.WriteFile(filepath.Join(dir, "big_900100.pdf"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	ix := NewIndex(root)
	if _, err := ix.Scan(); err != nil {
		t.Fatal(err)
	}
	s := NewServer(ix, "")

	r := httptest.NewRequest("GET", "/api/doc?id=900/100/big_900100.pdf", nil)
	r.Header.Set("Range", "bytes=0-99")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, r)

	if rec.Code != 206 {
		t.Fatalf("status %d, want 206 Partial Content", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/pdf" {
		t.Errorf("Content-Type %q on a range response, want application/pdf", got)
	}
	if rec.Body.Len() != 100 {
		t.Errorf("returned %d bytes, want 100", rec.Body.Len())
	}
}
