package find

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// buildWideArchive writes n real .docx files sharing a searchable word, spread
// over enough reference folders to look like the live tree.
func buildWideArchive(t *testing.T, n int) string {
	t.Helper()
	root := tempDir(t)
	for i := 0; i < n; i++ {
		ref := fmt.Sprintf("%03d-%03d", 100+i/50, i%50)
		rel := filepath.Join(ref[:3], ref[4:], "Inquiry", "inquiry_acme_english_"+ref+".docx")
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatal(err)
		}
		body := fmt.Sprintf("valve body assembly for order %s", ref)
		if err := os.WriteFile(full, minimalDocx(t, body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}
