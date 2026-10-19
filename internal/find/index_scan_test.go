package find

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// Scan used to hold no lock across the walk, so a Rescan click overlapping the
// 30-second ticker ran two full walks and two full extraction passes -- and
// extraction shells out to OCR with a 90-second budget per command -- after
// which one of the two results was thrown away by the other's rebuild.
//
// The count of documents actually re-read is what shows this: a scan reports
// how many it added or refreshed, and a scan that starts after another has
// finished finds every file unchanged and re-reads none of them. Overlapping
// scans each snapshot the index before anyone has rebuilt it, so each one
// believes every file is new and re-reads the whole archive.
func TestConcurrentScansRunOneAtATime(t *testing.T) {
	ix := NewIndex(buildArchive(t)) // three documents
	const callers = 6

	var wg sync.WaitGroup
	changed := make([]int, callers)
	errs := make([]error, callers)
	for i := range changed {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			changed[i], errs[i] = ix.Scan()
		}(i)
	}
	wg.Wait()

	total := 0
	for i := range changed {
		if errs[i] != nil {
			t.Fatalf("caller %d: %v", i, errs[i])
		}
		total += changed[i]
	}
	if total != 3 {
		t.Errorf("%d concurrent scans re-read %d documents in total, want 3: the archive was extracted more than once", callers, total)
	}
	if ix.Count() != 3 {
		t.Errorf("index holds %d documents after concurrent scans, want 3", ix.Count())
	}
}

// The property the serialisation relies on to stay cheap: once the index is
// current, a further scan costs one stat per file and re-reads nothing. If
// this regresses, waiting on the mutex stops being nearly free.
func TestRescanOfAnUnchangedArchiveReReadsNothing(t *testing.T) {
	ix := NewIndex(buildArchive(t))

	first, err := ix.Scan()
	if err != nil {
		t.Fatal(err)
	}
	if first != 3 {
		t.Fatalf("first scan read %d documents, want 3", first)
	}

	again, err := ix.Scan()
	if err != nil {
		t.Fatal(err)
	}
	if again != 0 {
		t.Errorf("rescan of an unchanged archive re-read %d documents, want 0", again)
	}
	if ix.Count() != 3 {
		t.Errorf("index holds %d documents, want 3", ix.Count())
	}
}

// Serialising must not mean a changed file is missed: someone who copies a
// document in and rescans has to see it, which is why scans queue rather than
// joining whichever scan happens to be in flight.
func TestRescanPicksUpAFileAddedAfterTheFirstScan(t *testing.T) {
	root := buildArchive(t)
	ix := NewIndex(root)
	if _, err := ix.Scan(); err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(root, "111", "222")
	if err := os.WriteFile(filepath.Join(dir, "Extra_111222.docx"), minimalDocx(t, "سند تازه"), 0o644); err != nil {
		t.Fatal(err)
	}

	changed, err := ix.Scan()
	if err != nil {
		t.Fatal(err)
	}
	if changed != 1 {
		t.Errorf("rescan read %d documents, want 1 (only the new file)", changed)
	}
	if ix.Count() != 4 {
		t.Errorf("index holds %d documents, want 4", ix.Count())
	}
	if _, ok := ix.PathOf("111/222/Extra_111222.docx"); !ok {
		t.Error("the file added between scans is not in the index")
	}
}
