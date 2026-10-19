package find

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// An empty folder is in the tree.
//
// The tree used to be derived from the documents alone, so a folder was only
// visible once it held a file the indexer could read. That was invisible
// while only docxgen made folders -- it creates one and writes into it in the
// same breath -- and became a bug the moment people could make their own:
// the folder appeared on disk, the page did not mention it, and creating one
// looked like it had silently failed.
func TestTheTreeShowsFoldersWithNoDocumentsInThem(t *testing.T) {
	root := t.TempDir()
	// One ordinary case folder with a document in it. The NAME matters: a
	// document's category comes from its own type, not from the folder it is
	// in, so this is named the way docxgen names one in order to land in the
	// Inquiry category rather than in "Other".
	must(t, os.MkdirAll(filepath.Join(root, "406", "300", "Inquiry"), 0o755))
	must(t, os.WriteFile(
		filepath.Join(root, "406", "300", "Inquiry", "inquiry_acme_persian_406-300.docx"),
		[]byte("x"), 0o644))
	// ...one category folder with nothing in it at all...
	must(t, os.MkdirAll(filepath.Join(root, "406", "300", "Proforma Invoice"), 0o755))
	// ...one reference folder with no categories...
	must(t, os.MkdirAll(filepath.Join(root, "406", "301"), 0o755))
	// ...and a whole group nobody has filed anything into yet.
	must(t, os.MkdirAll(filepath.Join(root, "407"), 0o755))

	ix := NewIndex(root)
	if _, err := ix.Scan(); err != nil {
		t.Fatalf("scan: %v", err)
	}

	groups := map[string]GroupNode{}
	for _, g := range ix.Tree() {
		groups[g.Name] = g
	}

	if _, ok := groups["407"]; !ok {
		t.Errorf("the empty group 407 is missing; the tree has %v", keysOf(groups))
	}
	g406, ok := groups["406"]
	if !ok {
		t.Fatalf("406 is missing; the tree has %v", keysOf(groups))
	}

	refs := map[string]ReferenceNode{}
	for _, r := range g406.References {
		refs[r.Name] = r
	}
	if _, ok := refs["301"]; !ok {
		t.Errorf("the empty reference 301 is missing; 406 has %v", keysOfRefs(refs))
	}
	r300, ok := refs["300"]
	if !ok {
		t.Fatalf("300 is missing; 406 has %v", keysOfRefs(refs))
	}

	cats := map[string]int{}
	for _, c := range r300.Categories {
		cats[c.Name] = c.Count
	}
	// The one with a document keeps its count...
	if cats["Inquiry"] != 1 {
		t.Errorf("Inquiry has count %d, want 1", cats["Inquiry"])
	}
	// ...and the empty one appears, with nothing in it, which is the truth
	// rather than a special case.
	if n, ok := cats["Proforma Invoice"]; !ok {
		t.Errorf("the empty category is missing; 406/300 has %v", cats)
	} else if n != 0 {
		t.Errorf("the empty category reports %d documents", n)
	}
}

// A folder holding a document is NOT also listed under its own name.
//
// A document's category comes from its type, so one called letter.docx in an
// Inquiry folder is categorised "Other". Seeding folder names
// unconditionally would show an empty "Inquiry" beside a populated "Other"
// for the same single folder on disk -- one folder appearing twice, once as a
// phantom. Only folders with nothing in them are seeded, and this is the test
// that says so.
func TestAFolderWithDocumentsIsNotListedTwice(t *testing.T) {
	root := t.TempDir()
	must(t, os.MkdirAll(filepath.Join(root, "406", "300", "Inquiry"), 0o755))
	// Deliberately a name that does NOT derive the Inquiry category.
	must(t, os.WriteFile(filepath.Join(root, "406", "300", "Inquiry", "letter.docx"), []byte("x"), 0o644))

	ix := NewIndex(root)
	if _, err := ix.Scan(); err != nil {
		t.Fatalf("scan: %v", err)
	}

	var cats []string
	total := 0
	for _, g := range ix.Tree() {
		for _, r := range g.References {
			for _, c := range r.Categories {
				cats = append(cats, c.Name)
				total += c.Count
			}
		}
	}
	if len(cats) != 1 {
		t.Errorf("one folder on disk produced %d categories (%v); it must produce one", len(cats), cats)
	}
	if total != 1 {
		t.Errorf("the document is counted %d times, want once", total)
	}
}

// Loading a saved index must not blank the tree.
//
// The snapshot carries documents and no folder list. Treating that as "there
// are no folders" would empty the tree on every restart until the first scan
// finished, which is the whole window somebody is looking at the page.
func TestLoadingASnapshotKeepsTheFolders(t *testing.T) {
	root := t.TempDir()
	must(t, os.MkdirAll(filepath.Join(root, "406", "300", "Inquiry"), 0o755))
	must(t, os.WriteFile(filepath.Join(root, "406", "300", "Inquiry", "a.docx"), []byte("x"), 0o644))
	must(t, os.MkdirAll(filepath.Join(root, "406", "300", "Empty"), 0o755))

	ix := NewIndex(root)
	if _, err := ix.Scan(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	before := countCategories(ix.Tree())

	// rebuild with nil folders is what loading a snapshot does.
	ix.mu.RLock()
	docs := ix.docs
	ix.mu.RUnlock()
	ix.rebuild(docs, nil)

	if after := countCategories(ix.Tree()); after != before {
		t.Errorf("the tree went from %d categories to %d after a snapshot load", before, after)
	}
}

// No row in the tree is nameless.
//
// The reference level is normally named from the reference's own digits, but
// refFromPath builds a reference out of digit segments only -- so a folder
// with a NAME contributes nothing to it, and the level came out blank. The
// page drew a row with no text in it, which is the one thing a row in a
// browser cannot be. Reachable before, ordinary now that people can make
// folders and upload into them, which is how it was found.
func TestNoRowInTheTreeIsNameless(t *testing.T) {
	root := t.TempDir()
	// A folder named rather than numbered, with a document in it.
	must(t, os.MkdirAll(filepath.Join(root, "507", "Proforma Invoice"), 0o755))
	must(t, os.WriteFile(
		filepath.Join(root, "507", "Proforma Invoice", "good.docx"), []byte("x"), 0o644))
	// And a document lying loose in the group folder, with no level to name
	// at all -- the other way to reach the same blank.
	must(t, os.WriteFile(filepath.Join(root, "507", "loose.docx"), []byte("x"), 0o644))

	ix := NewIndex(root)
	if _, err := ix.Scan(); err != nil {
		t.Fatalf("scan: %v", err)
	}

	seen := map[string]bool{}
	for _, g := range ix.Tree() {
		if g.Name == "" {
			t.Errorf("a group has no name")
		}
		for _, r := range g.References {
			if r.Name == "" {
				t.Errorf("a reference under %q has no name", g.Name)
			}
			seen[r.Name] = true
			for _, c := range r.Categories {
				if c.Name == "" {
					t.Errorf("a category under %q/%q has no name", g.Name, r.Name)
				}
			}
		}
	}
	// The named folder is named after itself, not after its digits.
	if !seen["Proforma Invoice"] {
		t.Errorf("the named folder is missing; the tree has %v", keysOfSet(seen))
	}
	// The loose document falls back to the group it is actually sitting in.
	if !seen["507"] {
		t.Errorf("the loose document has nowhere to sit; the tree has %v", keysOfSet(seen))
	}
}

// A new folder is in the tree on the VERY NEXT read, with no rescan.
//
// This is the "add a hard refresh after adding a folder" report, and the
// refresh was the symptom rather than the fix. Creating a folder used to
// schedule a background walk of the whole archive and then let the page ask
// for the tree again -- a race the page usually won, so the folder somebody
// had just made was missing from the answer. Which looks exactly like the
// create having failed.
//
// Nothing here scans. If Tree() needs a walk to know about a folder this
// process just created, this test fails.
func TestANewFolderIsInTheTreeWithoutARescan(t *testing.T) {
	root := t.TempDir()
	must(t, os.MkdirAll(filepath.Join(root, "406", "300", "Inquiry"), 0o755))
	must(t, os.WriteFile(
		filepath.Join(root, "406", "300", "Inquiry", "inquiry_acme_persian_406-300.docx"),
		[]byte("x"), 0o644))

	ix := NewIndex(root)
	if _, err := ix.Scan(); err != nil {
		t.Fatalf("scan: %v", err)
	}

	// Made on disk and recorded, the way handleDocsFolder does it.
	must(t, os.MkdirAll(filepath.Join(root, "406", "301"), 0o755))
	ix.NoteDir("406/301")

	if !treeHasRef(ix, "406", "301") {
		t.Error("the new folder is not in the tree until something rescans")
	}

	// And it survives an actual scan, rather than being a phantom that the
	// next walk contradicts.
	if _, err := ix.Scan(); err != nil {
		t.Fatalf("rescan: %v", err)
	}
	if !treeHasRef(ix, "406", "301") {
		t.Error("the folder vanished when the archive was scanned properly")
	}
}

// A deleted folder leaves the tree immediately, and its children with it.
func TestForgettingAFolderTakesItsChildrenToo(t *testing.T) {
	root := t.TempDir()
	must(t, os.MkdirAll(filepath.Join(root, "406", "300", "Inquiry"), 0o755))

	ix := NewIndex(root)
	if _, err := ix.Scan(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !treeHasRef(ix, "406", "300") {
		t.Fatal("the folder was not there to begin with")
	}

	ix.ForgetDir("406/300")

	if treeHasRef(ix, "406", "300") {
		t.Error("the folder is still in the tree after being forgotten")
	}
	// The category beneath it must have gone as well, or the tree keeps a
	// child of something that no longer exists.
	ix.mu.RLock()
	left := append([]string(nil), ix.dirs...)
	ix.mu.RUnlock()
	for _, d := range left {
		if strings.HasPrefix(d, "406/300") {
			t.Errorf("%q survived its parent being forgotten", d)
		}
	}
}

// NoteDir records nothing deeper than the tree shows.
//
// Scan stops at three levels, so this must too -- otherwise the two disagree
// about what the folder set contains and a scan would silently drop entries
// that creating a folder had added.
func TestNoteDirStopsWhereTheScanStops(t *testing.T) {
	ix := NewIndex(t.TempDir())
	ix.NoteDir("406/300/Inquiry")        // three levels: kept
	ix.NoteDir("406/300/Inquiry/deeper") // four: not the tree's business
	ix.NoteDir("")                       // the root is not a folder in the tree

	ix.mu.RLock()
	got := append([]string(nil), ix.dirs...)
	ix.mu.RUnlock()

	if !slices.Contains(got, "406/300/Inquiry") {
		t.Errorf("the category level was not recorded; dirs are %v", got)
	}
	for _, d := range got {
		if strings.Count(d, "/") >= 3 {
			t.Errorf("%q is deeper than the tree goes", d)
		}
		if d == "" {
			t.Error("an empty path was recorded")
		}
	}
}

// treeHasRef reports whether group/ref is in the tree.
func treeHasRef(ix *Index, group, ref string) bool {
	for _, g := range ix.Tree() {
		if g.Name != group {
			continue
		}
		for _, r := range g.References {
			if r.Name == ref {
				return true
			}
		}
	}
	return false
}

func keysOfSet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func countCategories(gs []GroupNode) int {
	n := 0
	for _, g := range gs {
		for _, r := range g.References {
			n += len(r.Categories)
		}
	}
	return n
}

func keysOf(m map[string]GroupNode) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func keysOfRefs(m map[string]ReferenceNode) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
