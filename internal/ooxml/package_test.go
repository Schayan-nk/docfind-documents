package ooxml

import (
	"archive/zip"
	"bytes"
	"testing"
)

func buildPkg(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	// [Content_Types].xml must exist for Open to accept the archive.
	all := map[string]string{
		"[Content_Types].xml": `<?xml version="1.0"?><Types/>`,
		"word/document.xml":   `<?xml version="1.0"?><w:document xmlns:w="ns"><w:body/></w:document>`,
	}
	for k, v := range entries {
		all[k] = v
	}
	for name, content := range all {
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

func TestRejectsNonDocx(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("readme.txt")
	w.Write([]byte("not a docx"))
	zw.Close()

	if _, err := Open(buf.Bytes()); err == nil {
		t.Fatal("expected an error for an archive that is not a WordprocessingML document")
	}
}
