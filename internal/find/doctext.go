package find

import (
	"sync"

	"github.com/Schayan-nk/docfind-documents/internal/pgstore"
)

// Keeping extracted text between runs, so no page is ever OCR'd twice.
//
// WHAT THIS IS ACTUALLY FOR, which is not what it sounds like. The search
// already works: it is an in-process inverted index and it stays the query
// path. What does not work is KEEPING the text. Today it lives only in a
// DPAPI-wrapped, machine-bound cache, so it cannot be usefully backed up and
// is rebuilt from nothing if the box is -- and rebuilding means re-extracting
// every PDF, which is the measured throughput ceiling of the whole scan: 18.2
// documents a second at 40% PDFs against 232 for an all-Word archive.
//
// So this is a durable store for expensive work, and the searchability that
// comes with it is a consequence rather than the point.
//
// OFF UNLESS ASKED FOR. See pgstore.DocTextEnabled and
// migrations/006_documents.sql: this text is encrypted at rest in the existing
// cache and is not encrypted in PostgreSQL, so switching it on is a decision
// with a prerequisite -- disk encryption on .13, which is not yet in place.

// TextReuseStat is what one scan did with the store.
//
// Reported on the status page because the value of this feature is invisible
// otherwise: "reused 4,480, extracted 6" is the difference between a rescan
// that took seconds and one that took hours, and without the numbers nobody
// can tell whether the store is being used at all.
type TextReuseStat struct {
	Reused     int `json:"reused"`
	Extracted  int `json:"extracted"`
	Saved      int `json:"saved"`
	SaveFailed int `json:"save_failed"`
	// Active says a store was in use for the last scan. Without it, all-zero
	// counters would read the same whether the feature is off or the archive
	// simply had nothing to do.
	Active bool `json:"active"`
}

// noteTextReuse records what the scan did, for the status page.
func (ix *Index) noteTextReuse(t *textReuse) {
	if t == nil {
		return
	}
	t.mu.Lock()
	stat := TextReuseStat{
		Reused: t.Reused, Extracted: t.Extracted,
		Saved: t.Saved, SaveFailed: t.SaveFailed, Active: true,
	}
	t.mu.Unlock()

	ix.mu.Lock()
	ix.lastReuse = stat
	ix.mu.Unlock()
}

// UseDocText makes extracted text durable across restarts.
//
// nil leaves everything exactly as it was: extract on scan, keep it in the
// encrypted cache, lose it if the cache is rebuilt.
func (ix *Index) UseDocText(d *pgstore.Documents) {
	ix.mu.Lock()
	ix.docText = d
	ix.mu.Unlock()
}

// docTextStore returns the store, or nil.
func (ix *Index) docTextStore() *pgstore.Documents {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return ix.docText
}

// textReuse decides, for one scan, which documents can skip extraction.
//
// Built once before the parallel pass and then only READ by the workers, which
// is what makes it safe to share: nothing mutates it after construction.
type textReuse struct {
	// stored is what the database already knows about each path.
	stored map[string]pgstore.Document
	// bodies is the text for the paths that need no re-extraction. Held
	// separately because Stored deliberately does not carry bodies -- pulling
	// every document's full text across to answer "has this changed" would
	// move tens of megabytes to compare three columns.
	bodies map[string]string

	// saves is serialised, because the workers all write through it.
	mu    sync.Mutex
	store *pgstore.Documents
	// Counters, for one honest log line at the end.
	Reused, Extracted, Saved, SaveFailed int
}

// newTextReuse reads what is already stored. A failure is not fatal: the scan
// falls back to extracting everything, which is exactly what it did before.
func newTextReuse(store *pgstore.Documents) (*textReuse, error) {
	if store == nil {
		return nil, nil
	}
	stored, err := store.Stored()
	if err != nil {
		return nil, err
	}
	return &textReuse{stored: stored, bodies: map[string]string{}, store: store}, nil
}

// loadBodies fetches the text for every path that will be reused.
//
// One query for all of them rather than one per document: at 4,500 documents
// the round trips cost more than the bytes.
func (t *textReuse) loadBodies(needed map[string]bool) error {
	if t == nil || len(needed) == 0 {
		return nil
	}
	_, err := t.store.Texts(func(path, body string) {
		if needed[path] {
			t.bodies[path] = body
		}
	})
	return err
}

// reuse returns the stored text for a document, and whether it may be used.
func (t *textReuse) reuse(d *Doc) (string, bool) {
	if t == nil {
		return "", false
	}
	st, ok := t.stored[d.ID]
	if !ok || st.NeedsExtraction(d.Size, d.ModTime) {
		return "", false
	}
	body, ok := t.bodies[d.ID]
	return body, ok
}

// save records a freshly extracted document.
//
// SAVED PER DOCUMENT, INSIDE THE WORKER, not batched at the end. The whole
// point is not to repeat expensive extraction, and a scan that OCR'd four
// hundred scanned pages and then died before a final batch write would have
// repeated exactly the work this exists to keep.
//
// A failed write is counted and swallowed: the text is already in the index
// and the scan must not fail because a database went away.
func (t *textReuse) save(d *Doc, p Provenance) {
	if t == nil || t.store == nil {
		return
	}
	doc := pgstore.Document{
		Path: d.ID, Size: d.Size, ModifiedAt: d.ModTime,
		OCRUsed: p.OCRUsed, OCRTruncated: p.Truncated,
		PagesRead: p.PagesRead, ToolsMissing: p.ToolsMissing,
		Body: d.Text,
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.store.Save(doc); err != nil {
		t.SaveFailed++
		return
	}
	t.Saved++
}

// THE NIL CHECK HAS TO BE HERE, not in bump. Passing &t.Reused evaluates the
// field address before bump is entered, so a nil receiver panics on the way in
// and never reaches the check inside. Caught by the nil-store test, which is
// exactly what that test is for: every method here takes a nil receiver,
// because nil is the ordinary state of a deployment with no database.
func (t *textReuse) countReused() {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.Reused++
	t.mu.Unlock()
}

func (t *textReuse) countExtracted() {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.Extracted++
	t.mu.Unlock()
}
