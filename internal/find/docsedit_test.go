package find

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A server whose archive is a throwaway directory.
func editServer(t *testing.T) (*Server, string) {
	t.Helper()
	root := t.TempDir()
	// A little of the real shape: group / reference / category.
	if err := os.MkdirAll(filepath.Join(root, "406", "300", "Inquiry"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(root, "406", "300", "Inquiry", "letter.docx"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	return &Server{ix: NewIndex(root)}, root
}

func TestMakeFolder(t *testing.T) {
	s, root := editServer(t)

	rel, err := s.makeFolder("406/300", "Proforma Invoice")
	if err != nil {
		t.Fatalf("creating a folder: %v", err)
	}
	if rel != "406/300/Proforma Invoice" {
		t.Errorf("returned %q", rel)
	}
	if st, err := os.Stat(filepath.Join(root, "406", "300", "Proforma Invoice")); err != nil || !st.IsDir() {
		t.Error("the folder is not on disk")
	}

	// Again is a refusal, not a silent success -- somebody pressing the
	// button twice should be told the name is taken.
	if _, err := s.makeFolder("406/300", "Proforma Invoice"); !errors.Is(err, errExists) {
		t.Errorf("creating it twice gave %v, want errExists", err)
	}

	// At the top level, which is how a new case group starts.
	if _, err := s.makeFolder("", "407"); err != nil {
		t.Errorf("creating a top-level folder: %v", err)
	}

	// Into somewhere that is not there: one typo must not build a chain of
	// empty folders.
	if _, err := s.makeFolder("406/999", "Inquiry"); !errors.Is(err, errNotFound) {
		t.Errorf("creating inside a missing parent gave %v, want errNotFound", err)
	}
	if _, err := os.Stat(filepath.Join(root, "406", "999")); err == nil {
		t.Error("the missing parent was created anyway")
	}

	// And the boundary still holds through this door.
	for _, bad := range []string{"..", "../../elsewhere", `..\..`} {
		if _, err := s.makeFolder(bad, "x"); err == nil {
			t.Errorf("makeFolder accepted parent %q", bad)
		}
	}
	for _, bad := range []string{"..", "a/b", "CON", "x."} {
		if _, err := s.makeFolder("406", bad); err == nil {
			t.Errorf("makeFolder accepted name %q", bad)
		}
	}
}

func TestSaveUploadNeverOverwrites(t *testing.T) {
	s, root := editServer(t)
	dir := "406/300/Inquiry"

	first, err := s.saveUpload(dir, "note.pdf", "tester", strings.NewReader("%PDF-1.4 one"))
	if err != nil {
		t.Fatalf("first upload: %v", err)
	}
	if first != "note.pdf" {
		t.Errorf("saved as %q, want note.pdf", first)
	}

	// The same name again must NOT replace the first. This archive has
	// already lost documents to two writers choosing one name.
	second, err := s.saveUpload(dir, "note.pdf", "tester", strings.NewReader("%PDF-1.4 two"))
	if err != nil {
		t.Fatalf("second upload: %v", err)
	}
	if second == first {
		t.Fatal("the second upload took the same name and overwrote the first")
	}
	if second != "note (2).pdf" {
		t.Errorf("second saved as %q, want \"note (2).pdf\"", second)
	}
	// The counter goes BEFORE the extension, or the file stops opening in the
	// right program.
	if filepath.Ext(second) != ".pdf" {
		t.Errorf("%q lost its extension", second)
	}
	got, _ := os.ReadFile(filepath.Join(root, "406", "300", "Inquiry", "note.pdf"))
	if string(got) != "%PDF-1.4 one" {
		t.Errorf("the first file now holds %q", got)
	}

	// An empty upload leaves nothing behind.
	if _, err := s.saveUpload(dir, "empty.pdf", "tester", strings.NewReader("")); err == nil {
		t.Error("an empty file was accepted")
	}
	if _, err := os.Stat(filepath.Join(root, "406", "300", "Inquiry", "empty.pdf")); err == nil {
		t.Error("the empty file was left on disk")
	}

	// Extensions that run are refused at this door too.
	if _, err := s.saveUpload(dir, "payload.exe", "tester", strings.NewReader("MZ")); err == nil {
		t.Error("an executable was accepted")
	}
	// And a traversing folder.
	if _, err := s.saveUpload("../../elsewhere", "note.pdf", "tester", strings.NewReader("x")); err == nil {
		t.Error("upload accepted a folder outside the archive")
	}
}

func TestSaveUploadRefusesOversize(t *testing.T) {
	s, root := editServer(t)
	big := strings.NewReader(strings.Repeat("a", maxUploadBytes+10))
	if _, err := s.saveUpload("406/300/Inquiry", "big.pdf", "tester", big); err == nil {
		t.Fatal("an oversize upload was accepted")
	}
	// A partial file is worse than none: it indexes, it opens, and it is
	// wrong.
	if _, err := os.Stat(filepath.Join(root, "406", "300", "Inquiry", "big.pdf")); err == nil {
		t.Error("the partial file was left behind")
	}
}

func TestRenameStaysInItsFolder(t *testing.T) {
	s, root := editServer(t)

	rel, err := s.renameEntry("406/300/Inquiry/letter.docx", "final letter.docx")
	if err != nil {
		t.Fatalf("renaming: %v", err)
	}
	if rel != "406/300/Inquiry/final letter.docx" {
		t.Errorf("renamed to %q", rel)
	}
	if _, err := os.Stat(filepath.Join(root, "406", "300", "Inquiry", "final letter.docx")); err != nil {
		t.Error("the renamed file is not there")
	}

	// A new NAME, never a path: renaming must not be a way to move something
	// to another folder, or out of the archive.
	for _, bad := range []string{"../escaped.docx", `..\escaped.docx`, "other/letter.docx", ".."} {
		if _, err := s.renameEntry("406/300/Inquiry/final letter.docx", bad); err == nil {
			t.Errorf("rename accepted the new name %q", bad)
		}
	}
	// The root itself is not renameable.
	if _, err := s.renameEntry("", "anything"); err == nil {
		t.Error("the archive root was renamed")
	}
	// Onto a name that is taken.
	if _, err := s.saveUpload("406/300/Inquiry", "taken.pdf", "tester", strings.NewReader("%PDF-1.4 x")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.renameEntry("406/300/Inquiry/final letter.docx", "taken.pdf"); !errors.Is(err, errExists) {
		t.Errorf("renaming onto an existing name gave %v, want errExists", err)
	}
}

// Deleting is the narrowest operation here, and these are the guarantees.
func TestDeleteRefusesAnythingButAnEmptyFolderOrOneFile(t *testing.T) {
	s, root := editServer(t)

	// A folder with something in it is refused, and NOTHING is removed.
	if err := s.deleteEntry("406/300/Inquiry"); !errors.Is(err, errNotEmpty) {
		t.Errorf("deleting a full folder gave %v, want errNotEmpty", err)
	}
	if _, err := os.Stat(filepath.Join(root, "406", "300", "Inquiry", "letter.docx")); err != nil {
		t.Fatal("the document inside was removed anyway")
	}
	// So is a whole branch, which is the case that would cost the archive.
	if err := s.deleteEntry("406"); !errors.Is(err, errNotEmpty) {
		t.Errorf("deleting a branch gave %v, want errNotEmpty", err)
	}
	if _, err := os.Stat(filepath.Join(root, "406", "300", "Inquiry", "letter.docx")); err != nil {
		t.Fatal("deleting the branch took the documents with it")
	}

	// The root, whatever it is called.
	for _, bad := range []string{"", ".", "..", "../.."} {
		if err := s.deleteEntry(bad); err == nil {
			t.Errorf("delete accepted %q", bad)
		}
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatal("the archive root was removed")
	}

	// One file: allowed.
	if err := s.deleteEntry("406/300/Inquiry/letter.docx"); err != nil {
		t.Fatalf("deleting one file: %v", err)
	}
	// And now the folder is empty, so it can go too.
	if err := s.deleteEntry("406/300/Inquiry"); err != nil {
		t.Fatalf("deleting the emptied folder: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "406", "300", "Inquiry")); err == nil {
		t.Error("the folder is still there")
	}
	// Something that is not there.
	if err := s.deleteEntry("406/300/Inquiry"); !errors.Is(err, errNotFound) {
		t.Errorf("deleting a missing folder gave %v, want errNotFound", err)
	}
}

func TestListFolderSeesEmptyFolders(t *testing.T) {
	s, _ := editServer(t)
	if _, err := s.makeFolder("406/300", "Empty"); err != nil {
		t.Fatal(err)
	}

	got, err := s.listFolder("406/300")
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	names := map[string]bool{}
	for _, e := range got {
		names[e.Name] = e.Dir
	}
	// The whole reason this reads the filesystem rather than the index: a
	// folder somebody just made holds no indexable document, so the index
	// does not know it exists.
	if dir, ok := names["Empty"]; !ok || !dir {
		t.Errorf("the empty folder is missing from %v", names)
	}
	if dir, ok := names["Inquiry"]; !ok || !dir {
		t.Errorf("Inquiry is missing from %v", names)
	}

	// Word lock files are not documents.
	if err := os.WriteFile(filepath.Join(s.ix.Root(), "406", "300", "~$hidden.docx"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, _ = s.listFolder("406/300")
	for _, e := range got {
		if strings.HasPrefix(e.Name, "~$") {
			t.Errorf("a Word lock file was listed: %q", e.Name)
		}
	}

	// And listing outside the archive is refused.
	if _, err := s.listFolder("../.."); err == nil {
		t.Error("listing accepted a folder outside the archive")
	}
}
