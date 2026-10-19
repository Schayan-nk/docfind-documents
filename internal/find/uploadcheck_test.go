package find

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeTemp(t *testing.T, name string, b []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func zipOf(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write(body)
	}
	zw.Close()
	return buf.Bytes()
}

var oleHeader = []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}

func TestAnUploadMustBeWhatItsNameSays(t *testing.T) {
	macroDocx := zipOf(t, map[string][]byte{"[Content_Types].xml": []byte("<x/>"), "word/vbaProject.bin": []byte("vba")})
	cleanDocx := zipOf(t, map[string][]byte{"[Content_Types].xml": []byte("<x/>"), "word/document.xml": []byte("<w/>")})
	exeInZip := zipOf(t, map[string][]byte{"invoice.pdf": []byte("%PDF-1.4"), "setup/invoice.exe": []byte("MZ")})
	vba := []byte{'_', 0, 'V', 0, 'B', 0, 'A', 0, '_', 0, 'P', 0, 'R', 0, 'O', 0, 'J', 0, 'E', 0, 'C', 0, 'T', 0}

	for _, c := range []struct {
		name, file string
		body       []byte
		ok         bool
	}{
		{"a real PDF", "a.pdf", []byte("%PDF-1.7\n..."), true},
		{"a program called .pdf", "a.pdf", []byte("MZ\x90\x00program"), false},
		{"text called .pdf", "a.pdf", []byte("hello"), false},
		{"a clean docx", "a.docx", cleanDocx, true},
		{"a docx carrying macros", "a.docx", macroDocx, false},
		{"a PDF called .docx", "a.docx", []byte("%PDF-1.4"), false},
		{"a zip with a program in it", "a.zip", exeInZip, false},
		{"an old Word file", "a.doc", append(append([]byte{}, oleHeader...), []byte("WordDocument")...), true},
		{"an old Word file with macros", "a.doc", append(append([]byte{}, oleHeader...), vba...), false},
		{"plain text", "a.txt", []byte("سلام\nhello"), true},
		{"UTF-16 text", "a.txt", []byte{0xFF, 0xFE, 'h', 0, 'i', 0}, true},
		{"binary called .txt", "a.txt", []byte("abc\x00\x01def"), false},
		{"a script called .txt", "a.txt", []byte("#!/bin/sh\nrm -rf /"), false},
		{"a PNG", "a.png", []byte("\x89PNG\r\n\x1a\nIHDR"), true},
		{"a JPEG called .png", "a.png", []byte{0xFF, 0xD8, 0xFF, 0xE0}, false},
	} {
		p := writeTemp(t, c.file, c.body)
		err := inspectUpload(p, filepath.Ext(c.file))
		if (err == nil) != c.ok {
			t.Errorf("%s: err = %v, want ok=%v", c.name, err, c.ok)
		}
	}
}

// A small ZIP that unpacks to something enormous.
func TestADecompressionBombIsRefused(t *testing.T) {
	bomb := zipOf(t, map[string][]byte{"zeros.txt": bytes.Repeat([]byte{0}, 20<<20)})
	if len(bomb) > 1<<20 {
		t.Fatalf("test setup: the bomb is %d bytes, not small", len(bomb))
	}
	p := writeTemp(t, "a.zip", bomb)
	err := inspectUpload(p, ".zip")
	if err == nil || !strings.Contains(err.Error(), "بزرگ") {
		t.Errorf("err = %v", err)
	}
}

// A refused upload never reaches the archive, and is kept in quarantine with
// a note saying who sent it and why it was refused.
func TestARefusedUploadIsQuarantinedNotFiled(t *testing.T) {
	s, root := editServer(t)
	qdir := t.TempDir()
	s.UseQuarantine(qdir)
	t.Setenv("DOCFIND_UPLOAD_AV", "off")

	_, err := s.saveUpload("406/300/Inquiry", "contract.pdf", "sadeghi", strings.NewReader("MZ\x90\x00program"))
	if err == nil {
		t.Fatal("a program called contract.pdf was filed")
	}
	if _, serr := os.Stat(filepath.Join(root, "406", "300", "Inquiry", "contract.pdf")); serr == nil {
		t.Fatal("the refused file reached the archive")
	}
	day := time.Now().Format("2006-01-02")
	notes, _ := filepath.Glob(filepath.Join(qdir, "rejected", day, "*.json"))
	held, _ := filepath.Glob(filepath.Join(qdir, "rejected", day, "*.quarantined"))
	if len(notes) != 1 || len(held) != 1 {
		t.Fatalf("quarantine holds %d notes and %d files", len(notes), len(held))
	}
	var note map[string]string
	raw, _ := os.ReadFile(notes[0])
	json.Unmarshal(raw, &note)
	if note["user"] != "sadeghi" || note["name"] != "contract.pdf" || note["reason"] == "" {
		t.Errorf("note = %v", note)
	}
	// Nothing left waiting: an accepted upload leaves no copy behind either.
	if _, err := s.saveUpload("406/300/Inquiry", "fine.pdf", "sadeghi", strings.NewReader("%PDF-1.4 ok")); err != nil {
		t.Fatal(err)
	}
	left, _ := filepath.Glob(filepath.Join(qdir, "upload-*.part"))
	if len(left) != 0 {
		t.Errorf("%d uploads were left in quarantine", len(left))
	}
}
