package find

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The three ways the archive still reached through a link after fbb8027, found
// by the review of the 5.37.0 candidate on 2026-10-03. Each test plants the
// link the way anyone able to write to the archive folder could -- a directory
// junction needs no privilege on Windows -- and checks nothing outside the
// archive is read, listed, written or deleted.

const outsideMarker = "OUTSIDE-THE-ARCHIVE-MARKER"

// outsideDir is a folder outside the archive holding one secret file.
func outsideDir(t *testing.T, name string) string {
	t.Helper()
	dir := tempDir(t)
	if err := os.WriteFile(filepath.Join(dir, name), []byte(outsideMarker), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// List, upload, new folder, rename and delete read the disk directly, so a
// junction in the path took each of them outside the archive -- delete
// included, as the service account, with no recycle bin.
func TestTheDocsOperationsDoNotFollowAJunction(t *testing.T) {
	root := buildArchive(t)
	outside := outsideDir(t, "secret.txt")
	if err := os.Mkdir(filepath.Join(root, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !mkJunction(t, filepath.Join(root, "a", "j"), outside) {
		t.Skip("no directory junctions on this system")
	}
	ix := NewIndex(root)
	if _, err := ix.Scan(); err != nil {
		t.Fatal(err)
	}
	s := NewServer(ix, "")

	entries, err := s.listFolder("a")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name == "j" {
			t.Error("the folder listing offered the junction")
		}
	}
	refused := func(what string, err error) {
		t.Helper()
		if !errors.Is(err, errNotFound) {
			t.Errorf("%s through the junction: got %v, want errNotFound", what, err)
		}
	}
	_, err = s.listFolder("a/j")
	refused("list", err)
	_, err = s.makeFolder("a/j", "made")
	refused("new folder", err)
	_, err = s.saveUpload("a/j", "up.txt", "tester", strings.NewReader("hello"))
	refused("upload", err)
	_, err = s.renameEntry("a/j/secret.txt", "renamed.txt")
	refused("rename", err)
	refused("delete", s.deleteEntry("a/j/secret.txt"))
	refused("delete the junction", s.deleteEntry("a/j"))

	des, err := os.ReadDir(outside)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, de := range des {
		names = append(names, de.Name())
	}
	sort.Strings(names)
	if strings.Join(names, ",") != "secret.txt" {
		t.Errorf("the folder outside the archive changed: %v", names)
	}
	if b, _ := os.ReadFile(filepath.Join(outside, "secret.txt")); string(b) != outsideMarker {
		t.Errorf("the file outside the archive changed: %q", b)
	}

	// A real folder is still worked with as before.
	if _, err := s.makeFolder("a", "real"); err != nil {
		t.Fatalf("a folder inside the archive: %v", err)
	}
	if _, err := s.makeFolder("a", "real"); !errors.Is(err, errExists) {
		t.Errorf("making it twice: got %v, want errExists", err)
	}
	if _, err := s.listFolder("a/real"); err != nil {
		t.Errorf("listing it: %v", err)
	}
	if err := s.deleteEntry("a/real"); err != nil {
		t.Errorf("deleting it: %v", err)
	}
}

// The scan checked a document, and extraction and download opened it again by
// its path. A junction swapped in between was followed: its target's text was
// indexed under the decoy's name. Opening through os.Root refuses it.
func TestADocumentIsOpenedThroughTheRoot(t *testing.T) {
	root := tempDir(t)
	zz := filepath.Join(root, "zz")
	if err := os.Mkdir(zz, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(zz, "notes.txt"), []byte("the decoy text"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := outsideDir(t, "notes.txt")
	ix := NewIndex(root)
	if _, err := ix.Scan(); err != nil {
		t.Fatal(err)
	}
	if _, ok := ix.Doc("zz/notes.txt"); !ok {
		t.Fatal("the decoy was not indexed")
	}
	extract := func() string {
		docs := []*Doc{{ID: "zz/notes.txt", Name: "notes.txt"}}
		extractAll(ix.Extractor(), root, docs, func(int) (int, string) {
			return 0, filepath.Join(root, "zz", "notes.txt")
		}, 1, nil)
		return docs[0].Text
	}
	if got := extract(); !strings.Contains(got, "the decoy text") {
		t.Fatalf("a plain document did not extract: %q", got)
	}

	// The swap: the checked folder is replaced by a junction.
	if err := os.Rename(zz, zz+"-old"); err != nil {
		t.Fatal(err)
	}
	if !mkJunction(t, zz, outside) {
		t.Skip("no directory junctions on this system")
	}
	if got := extract(); strings.Contains(got, outsideMarker) {
		t.Error("extraction followed a junction swapped in after the scan")
	}
	if f, err := ix.OpenDoc("zz/notes.txt"); err == nil {
		b, _ := io.ReadAll(f)
		f.Close()
		t.Errorf("OpenDoc opened %q through a junction", b)
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if f, err := openRegular(r, "zz/notes.txt"); err == nil {
		b, _ := io.ReadAll(f)
		f.Close()
		t.Errorf("os.Root followed a junction to %q", b)
	}

	// Swapped back, the document opens and extracts again.
	if err := os.Remove(zz); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(zz+"-old", zz); err != nil {
		t.Fatal(err)
	}
	f, err := ix.OpenDoc("zz/notes.txt")
	if err != nil {
		t.Fatalf("a plain document did not open: %v", err)
	}
	b, _ := io.ReadAll(f)
	f.Close()
	if string(b) != "the decoy text" {
		t.Errorf("opened %q", b)
	}
	if got := extract(); !strings.Contains(got, "the decoy text") {
		t.Errorf("a plain document did not extract after the swap back: %q", got)
	}
}
