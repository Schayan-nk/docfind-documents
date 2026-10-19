package find

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestApplyChangesSkipsUnchanged is the start-up guarantee: a file whose
// size and modification time have not moved since it was last indexed is not
// re-read or re-extracted when the watcher reports it anyway (a benign
// notification, or a reconciliation pass).
func TestApplyChangesSkipsUnchanged(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(p, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	ix := NewIndex(dir)
	if _, err := ix.Scan(); err != nil {
		t.Fatal(err)
	}
	before, ok := ix.Doc("note.txt")
	if !ok {
		t.Fatal("note.txt was not indexed by the initial scan")
	}

	n, err := ix.ApplyChanges([]string{"note.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("want 0 re-extracted for an unchanged file, got %d", n)
	}
	after, ok := ix.Doc("note.txt")
	if !ok {
		t.Fatal("note.txt disappeared from the index")
	}
	if after != before {
		t.Fatal("an unchanged file's Doc was replaced instead of left alone")
	}
}

// TestApplyChangesAddsAndUpdates checks the two live paths through the same
// function: a brand new file becomes a new Doc, and a modified one is
// re-extracted in place.
func TestApplyChangesAddsAndUpdates(t *testing.T) {
	dir := t.TempDir()
	ix := NewIndex(dir)
	if _, err := ix.Scan(); err != nil {
		t.Fatal(err)
	}
	if ix.Count() != 0 {
		t.Fatalf("want an empty archive to start, got %d documents", ix.Count())
	}

	p := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(p, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if n, err := ix.ApplyChanges([]string{"note.txt"}); err != nil || n != 1 {
		t.Fatalf("ApplyChanges(add) = %d, %v; want 1, nil", n, err)
	}
	d, ok := ix.Doc("note.txt")
	if !ok || d.Text != "v1" {
		t.Fatalf("note.txt not indexed with its text, got %+v", d)
	}

	// Force the modification time forward: some filesystems have a coarser
	// resolution than the write above, and a false "unchanged" would defeat
	// the point of this test.
	newTime := d.ModTime.Add(time.Second)
	if err := os.WriteFile(p, []byte("v2, which is longer"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, newTime, newTime); err != nil {
		t.Fatal(err)
	}
	if n, err := ix.ApplyChanges([]string{"note.txt"}); err != nil || n != 1 {
		t.Fatalf("ApplyChanges(update) = %d, %v; want 1, nil", n, err)
	}
	d, ok = ix.Doc("note.txt")
	if !ok || d.Text != "v2, which is longer" {
		t.Fatalf("note.txt not re-extracted after a change, got %+v", d)
	}
}

// TestApplyChangesRemoves checks the other half of the "add/update/remove"
// path: a path that no longer exists drops out of the index.
func TestApplyChangesRemoves(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "gone.txt")
	if err := os.WriteFile(p, []byte("bye"), 0o644); err != nil {
		t.Fatal(err)
	}

	ix := NewIndex(dir)
	if _, err := ix.Scan(); err != nil {
		t.Fatal(err)
	}
	if _, ok := ix.Doc("gone.txt"); !ok {
		t.Fatal("gone.txt was not indexed by the initial scan")
	}

	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if n, err := ix.ApplyChanges([]string{"gone.txt"}); err != nil || n != 0 {
		t.Fatalf("ApplyChanges(remove) = %d, %v; want 0, nil", n, err)
	}
	if _, ok := ix.Doc("gone.txt"); ok {
		t.Fatal("gone.txt is still in the index after being deleted")
	}
	if ix.Count() != 0 {
		t.Fatalf("want an empty index after the only document was removed, got %d", ix.Count())
	}
}
