package find

import (
	"bytes"
	"encoding/gob"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Schayan-nk/docfind-documents/internal/bidi"
	"github.com/Schayan-nk/docfind-documents/internal/pgstore"
	"github.com/Schayan-nk/docfind-documents/internal/secretbox"
)

// Doc is one indexed document.
type Doc struct {
	ID       string // archive-relative path; stable across rescans
	Ref      string // reference number derived from the folder tree
	Type     string // Contract, Anfrage, Pi_NO, …
	Category string // business category: Inquiry, Proforma Invoice, …
	Group    string // first three digits of Ref, matching the folder layout
	Name     string // file name
	Size     int64
	ModTime  time.Time
	Text     string // extracted content, kept for snippets
}

// Index holds every document plus an inverted index of their content.
//
// Everything lives in memory: a few thousand documents of a few kilobytes each
// is a handful of megabytes, and keeping it resident is what makes a query
// return in well under a millisecond. The on-disk copy exists only so a restart
// does not have to re-read every .docx.
type Index struct {
	mu sync.RWMutex
	// scanMu admits one scan at a time. It has to be separate from mu: mu is
	// deliberately not held across the walk so searches keep answering while a
	// scan runs, which is also what let two scans overlap.
	scanMu sync.Mutex
	// How the last walk of the archive ended. Guarded by mu with everything
	// else it sits beside. lastScanOK is deliberately separate from
	// lastScanAt: a failed attempt must not erase the fact that the documents
	// in memory were read successfully at some point.
	lastScanAt  time.Time
	lastScanOK  time.Time
	lastScanErr string
	// docText keeps extracted text between runs, so a rescan re-reads only
	// what changed instead of re-running tesseract over the whole archive.
	// nil leaves the previous behaviour untouched. See doctext.go.
	docText *pgstore.Documents
	// lastReuse is what the last scan did with that store, for the status
	// page. Held so a reader can see "reused 4,480, extracted 6" rather than
	// having to trust that the store is doing anything.
	lastReuse TextReuseStat
	root      string
	docs      []*Doc
	byID      map[string]int
	byRef     map[string][]int
	postings  map[string][]int32
	// vocab is every key of postings, sorted.
	//
	// It exists because a map cannot answer "which terms start with this", and
	// the two fallbacks that question drives -- prefix search and typo
	// correction -- were ranging over the whole map. That is invisible at a few
	// thousand documents and measurable at scale: a 200,000-document archive
	// took 44 ms to answer a term it did not have, because failing to find one
	// meant walking the entire vocabulary twice.
	//
	// Sorted, a prefix is a contiguous run that binary search finds in
	// O(log V), and the typo scan gets a deterministic order to walk instead of
	// a map's random one. Rebuilt with the index, from the same postings map,
	// so the two cannot disagree.
	vocab []string
	// vocabByLen groups the vocabulary by rune length, each group still in
	// sorted order.
	//
	// Typo correction allows one edit for a short word and two for a long one,
	// which means only words within that many characters of the query's length
	// can possibly be the answer. Checking that inside a loop over the whole
	// vocabulary still walks the whole vocabulary; grouping by length turns it
	// into a walk over three or five groups.
	//
	// Measured at 20,000 documents and 51,000 terms, correcting one typo was a
	// p50 of 23 ms and a p99 of 77 ms -- by a wide margin the slowest thing the
	// search box could be asked to do, and it is asked whenever somebody
	// mistypes. The strings are shared with vocab, so this costs one slice
	// header each, not another copy of the words.
	vocabByLen map[int][]string
	extractor  *Extractor
	// dirs is every folder in the tree, archive-relative and slash-separated,
	// to three levels.
	//
	// Held because the tree is a picture of the FOLDERS and used to be drawn
	// from the DOCUMENTS, so a folder with nothing readable in it did not
	// exist as far as the page was concerned. That was tolerable while only
	// docxgen made folders -- it creates one and writes into it at once -- and
	// became a bug as soon as people could make their own.
	dirs []string
	// idents maps a canonical order/invoice/reference/tender number (see
	// identindex.go) to the IDs of the documents whose text or file name
	// carries it. Rebuilt alongside everything else in rebuild(), from the
	// same docs slice, so it cannot fall out of step with the postings it
	// sits beside.
	idents map[string][]string
}

// NewIndex creates an empty index over an archive root.
func NewIndex(root string) *Index {
	return &Index{
		root:      root,
		byID:      map[string]int{},
		byRef:     map[string][]int{},
		postings:  map[string][]int32{},
		extractor: DefaultExtractor(),
	}
}

// Extractor returns the text extractor this index scans with, so the settings
// screen can report which formats are actually readable on this machine.
func (ix *Index) Extractor() *Extractor {
	if ix.extractor == nil {
		return DefaultExtractor()
	}
	return ix.extractor
}

// Root returns the archive directory being indexed.
func (ix *Index) Root() string { return ix.root }

// SetRoot points the index at a different archive folder and empties it, so the
// next Scan builds from scratch. Used by the setup screen once the user has
// supplied a folder that works.
func (ix *Index) SetRoot(root string) {
	ix.mu.Lock()
	ix.root = root
	ix.docs, ix.byID, ix.byRef, ix.postings = nil,
		map[string]int{}, map[string][]int{}, map[string][]int32{}
	ix.mu.Unlock()
}

// Count reports how many documents are indexed.
func (ix *Index) Count() int {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return len(ix.docs)
}

// Scan walks the archive and brings the index up to date.
//
// Only files whose size or modification time changed are re-read, so a rescan
// over an unchanged archive costs one stat per file and no parsing at all.
// Returns the number of documents added or refreshed.
func (ix *Index) Scan() (int, error) {
	if ix.root == "" {
		return 0, fmt.Errorf("find: no archive directory configured")
	}

	// One scan at a time. Nothing used to stop two -- a manager clicking
	// Rescan while the 30-second ticker was already walking, or two managers
	// clicking it together -- and each overlapping scan re-extracted every
	// changed file, which for a scanned PDF means rasterising pages and
	// running OCR with a 90-second budget per external command. Both then
	// called rebuild, so one of the two full passes was discarded wholesale.
	//
	// Serialising rather than joining an in-flight scan keeps the meaning of
	// the call: it returns once the index is up to date as of the caller's own
	// request, so someone who drops a file in and clicks Rescan still sees it.
	// The wait is the only cost, because the second walk then finds every file
	// unchanged -- the size and mtime check below re-reads nothing, leaving an
	// up-to-date rescan at one stat per file.
	ix.scanMu.Lock()
	defer ix.scanMu.Unlock()

	ix.mu.RLock()
	existing := make(map[string]*Doc, len(ix.docs))
	for _, d := range ix.docs {
		existing[d.ID] = d
	}
	ix.mu.RUnlock()

	var docs []*Doc
	// Every folder in the tree, whether or not it holds a readable document.
	// See the walk below for why.
	var dirs []string
	// The files whose text has to be read: an index into docs, and the path to
	// read. Collected during the walk and extracted afterwards, in parallel --
	// see below for why that is worth the extra pass.
	type pending struct {
		at   int
		path string
	}
	var todo []pending

	err := filepath.WalkDir(ix.root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // an unreadable folder should not abort the whole scan
		}
		if path != ix.root && (isLink(d.Type()) || renamedByWin32(d.Name())) {
			// A link is never followed or indexed (plainUnder). Nor is a name
			// Windows resolves to a different entry: WalkDir reopens a folder
			// by its path, and "dir." beside a junction "dir" opens the
			// junction. A link is not a directory here, so returning nil
			// already keeps the walk out of it; a real "dir." needs SkipDir.
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			// DIRECTORIES ARE RECORDED, not merely walked through.
			//
			// The tree used to be derived from the documents alone, which
			// meant a folder was only visible once it held a file the indexer
			// could read. That was invisible while folders were made by
			// docxgen -- it makes one and writes into it in the same breath --
			// and became a bug the moment people could make their own: the
			// folder appeared on disk and not on screen, so creating one
			// looked like it had silently failed.
			//
			// It also fixes a case that was always wrong: a folder whose only
			// contents are files the indexer skips is a real folder, and the
			// archive has several.
			if rel, err := filepath.Rel(ix.root, path); err == nil && rel != "." {
				slash := filepath.ToSlash(rel)
				// Three levels is the whole tree -- group, reference,
				// category -- and stopping there keeps this bounded on an
				// archive with thousands of case folders.
				if strings.Count(slash, "/") < 3 {
					dirs = append(dirs, slash)
				}
			}
			return nil
		}
		if !Indexable(path) {
			return nil
		}
		if strings.HasPrefix(d.Name(), "~$") {
			return nil // Word lock file
		}

		info, err := d.Info()
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(ix.root, path)
		if err != nil {
			return nil
		}
		id := filepath.ToSlash(rel)

		if prev, ok := existing[id]; ok &&
			prev.Size == info.Size() && prev.ModTime.Equal(info.ModTime()) {
			docs = append(docs, prev)
			return nil
		}

		// Everything but the text, which the pass below fills in.
		docs = append(docs, &Doc{
			ID:      id,
			Ref:     refFromPath(rel),
			Type:    typeFromName(d.Name()),
			Name:    d.Name(),
			Size:    info.Size(),
			ModTime: info.ModTime(),
		})
		todo = append(todo, pending{at: len(docs) - 1, path: path})
		return nil
	})
	if err != nil {
		ix.noteScan(err)
		return 0, err
	}

	// What is already stored, so an unchanged document is not read again.
	// A store that cannot be reached is not fatal: the scan extracts
	// everything, which is exactly what it did before this existed.
	reuse, rerr := newTextReuse(ix.docTextStore())
	if rerr != nil {
		reuse = nil
	}
	if reuse != nil {
		// Which paths will be reused, so their text can be fetched in one
		// query rather than one per document.
		want := make(map[string]bool, len(todo))
		for i := range todo {
			d := docs[todo[i].at]
			if st, ok := reuse.stored[d.ID]; ok && !st.NeedsExtraction(d.Size, d.ModTime) {
				want[d.ID] = true
			}
		}
		if err := reuse.loadBodies(want); err != nil {
			reuse = nil
		}
	}

	extractAll(ix.Extractor(), ix.root, docs, func(i int) (int, string) {
		return todo[i].at, todo[i].path
	}, len(todo), reuse)

	if reuse != nil {
		ix.noteTextReuse(reuse)
	}

	ix.rebuild(docs, dirs)
	ix.noteScan(nil)
	return len(todo), nil
}

// noteScan records how the last walk ended.
//
// Kept so the health page can say when the archive was last read successfully
// rather than only how many documents are in memory. Those are different
// facts and the difference is the whole point of the page: an index holding
// 40,000 documents looks identical whether it was rebuilt four minutes ago or
// has been failing to reach the share since Tuesday.
//
// A failed scan does not clear lastScanOK. The previous success is still true
// -- the index is still serving those documents -- and showing "never" after
// one network blip would read as data loss rather than as a stale index.
func (ix *Index) noteScan(err error) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	ix.lastScanAt = time.Now()
	if err != nil {
		ix.lastScanErr = err.Error()
		return
	}
	ix.lastScanErr = ""
	ix.lastScanOK = ix.lastScanAt
}

// extractAll reads the text of every pending file, using every core.
//
// Why this is a separate pass rather than inline in the walk
// ----------------------------------------------------------
// Reading a .docx is this process unzipping a file: about four milliseconds.
// Reading a .pdf is spawning pdftotext and waiting for it: about a hundred and
// thirty, of which almost all is waiting rather than computing -- and a scanned
// PDF that falls through to OCR is far worse again, rasterising pages and
// running tesseract with a ninety-second budget.
//
// Done one at a time, that puts a hard ceiling on the archive. Measured on this
// machine: 232 documents a second when they are all Word, and 18 a second when
// two in five are PDFs. At the 200,000 documents this archive is heading for
// that is the difference between a cold index taking a quarter of an hour and
// taking the better part of three hours -- and a cold index is not a rare event,
// because it is what happens after any change to the cache format.
//
// Since the work is almost entirely waiting on another process, running several
// at once costs little and recovers nearly all of it. The concurrency is the
// core count: these are external processes, so the limit that matters is how
// many the machine will schedule, not how much Go work there is.
//
// Safe without a lock: each worker writes Text on a DIFFERENT element of a slice
// that was fully built before the pass started. Nothing appends, nothing is
// re-sliced, and rebuild sorts afterwards, so the order work finishes in cannot
// affect the result.
// reuse may be nil, in which case every pending file is extracted -- the
// behaviour before extracted text was kept anywhere durable.
//
// Each document is read through the archive root opened as an os.Root, never
// by its path: see extractThrough. The path from at is not used for reading.
func extractAll(e *Extractor, root string, docs []*Doc, at func(i int) (int, string), n int, reuse *textReuse) {
	if n == 0 {
		return
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		// The archive cannot be opened at all, so no document can be read;
		// leave every text empty rather than record that as what they say.
		return
	}
	defer r.Close()
	workers := runtime.NumCPU()
	if workers > n {
		workers = n
	}
	if workers < 1 {
		workers = 1
	}

	jobs := make(chan int, workers)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				idx, _ := at(i)

				// Already stored and unchanged: skip the expensive part.
				// This is the whole reason the text is kept -- a rescan after
				// a restart re-reads only what actually changed instead of
				// re-running tesseract over the archive.
				if body, ok := reuse.reuse(docs[idx]); ok {
					docs[idx].Text = body
					reuse.countReused()
					continue
				}

				text, prov, err := extractThrough(e, r, docs[idx].ID)
				if err != nil {
					// A file that cannot be parsed is still worth listing by
					// name and reference; only its content search is lost.
					text = ""
				}
				docs[idx].Text = text
				if reuse != nil {
					reuse.countExtracted()
					// Saved here, per document, so work already done survives
					// a scan that dies half way through.
					reuse.save(docs[idx], prov)
				}
			}
		}()
	}
	for i := 0; i < n; i++ {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
}

// rebuild replaces the index contents and recomputes the lookups.
func (ix *Index) rebuild(docs []*Doc, dirs []string) {
	sort.Slice(docs, func(i, j int) bool {
		if docs[i].Ref != docs[j].Ref {
			return docs[i].Ref > docs[j].Ref // newest reference numbers first
		}
		return docs[i].Name < docs[j].Name
	})

	byID := make(map[string]int, len(docs))
	byRef := make(map[string][]int, len(docs))
	postings := make(map[string][]int32, 4096)

	for i, d := range docs {
		// Derived rather than stored, so a cache written before these existed
		// still gets them and a rule change takes effect on next start.
		d.Category = CategoryOf(d.Type)
		d.Group = GroupOf(d.Ref)
		byID[d.ID] = i
		if d.Ref != "" {
			byRef[d.Ref] = append(byRef[d.Ref], i)
		}
		seen := map[string]bool{}
		for _, tok := range Tokenize(d.Text + " " + d.Name + " " + d.Ref) {
			if seen[tok] {
				continue
			}
			seen[tok] = true
			postings[tok] = append(postings[tok], int32(i))
		}
	}

	// The sorted vocabulary, built here so it is always the postings map's own
	// keys and cannot drift from it. Sorting a few hundred thousand strings
	// costs a fraction of the scan that produced them.
	vocab := make([]string, 0, len(postings))
	for term := range postings {
		vocab = append(vocab, term)
	}
	sort.Strings(vocab)

	// Grouped by rune length, filled in sorted order so each group stays sorted
	// and the typo scan remains deterministic within it.
	byLen := make(map[int][]string, 32)
	for _, term := range vocab {
		n := utf8.RuneCountInString(term)
		byLen[n] = append(byLen[n], term)
	}

	// See identindex.go: the same per-document text and file name, scanned
	// once more for order/invoice/reference/tender numbers.
	idents := buildIdentIndex(docs)

	ix.mu.Lock()
	ix.docs, ix.byID, ix.byRef, ix.postings = docs, byID, byRef, postings
	ix.vocab, ix.vocabByLen = vocab, byLen
	ix.idents = idents
	// nil dirs means "this caller did not walk the filesystem" rather than
	// "there are no folders", so the cached set is kept. That is what loading
	// a saved index does: the snapshot holds documents and no folder list, and
	// clearing the folders there would empty the tree on every restart until
	// the first scan finished.
	if dirs != nil {
		ix.dirs = dirs
	}
	ix.mu.Unlock()
}

// ApplyChanges brings the index up to date for a batch of paths already
// known to have changed -- typically reported by the archive watcher (see
// watcher_windows.go) -- without walking the rest of the tree. A path may be
// archive-relative or absolute. One that still exists and is Indexable is
// (re-)extracted and merged in; one that no longer exists is dropped, along
// with its persisted metadata if a store is configured. A path that names a
// folder only updates the folder tree (NoteDir/ForgetDir); it is not a
// document. Returns how many documents were (re-)extracted.
//
// THIS IS THE ADD/UPDATE/REMOVE PATH for anything other than a full Scan,
// kept as one function so other code that needs to react to a document
// entering or leaving the index has a single, obvious place to hook into.
// Both this and Scan ultimately merge their result through rebuild, which is
// where a document actually becomes visible to lookups.
//
// Serialises through scanMu, exactly like Scan, so an overlapping call
// cannot race it to rebuild from a stale view of ix.docs.
func (ix *Index) ApplyChanges(paths []string) (int, error) {
	if ix.root == "" {
		return 0, fmt.Errorf("find: no archive directory configured")
	}
	ix.scanMu.Lock()
	defer ix.scanMu.Unlock()

	ix.mu.RLock()
	existing := make(map[string]*Doc, len(ix.docs))
	for _, d := range ix.docs {
		existing[d.ID] = d
	}
	ix.mu.RUnlock()

	store := ix.docTextStore()
	removed := map[string]bool{}
	changed := map[string]*Doc{}
	changedPath := map[string]string{}

	for _, raw := range paths {
		rel := raw
		if filepath.IsAbs(raw) {
			r, err := filepath.Rel(ix.root, raw)
			if err != nil {
				continue
			}
			rel = r
		}
		id := strings.Trim(filepath.ToSlash(rel), "/")
		if id == "" || strings.HasPrefix(id, "..") {
			continue // outside the archive root
		}
		full := filepath.Join(ix.root, filepath.FromSlash(id))

		// Never os.Stat: it follows a link. Gone, or reached through a link,
		// it is not a document of the archive's either way -- and one that
		// was indexed and is now behind a link leaves the index.
		info, plain := plainUnder(ix.root, full)
		if !plain {
			ix.ForgetDir(id)
			if _, ok := existing[id]; ok {
				removed[id] = true
			}
			delete(changed, id)
			delete(changedPath, id)
			continue
		}
		if info.IsDir() {
			ix.NoteDir(id)
			continue
		}
		if !Indexable(full) || strings.HasPrefix(info.Name(), "~$") {
			continue
		}
		if prev, ok := existing[id]; ok &&
			prev.Size == info.Size() && prev.ModTime.Equal(info.ModTime()) {
			continue // unchanged; nothing to do
		}

		changed[id] = &Doc{
			ID: id, Ref: refFromPath(id), Type: typeFromName(info.Name()),
			Name: info.Name(), Size: info.Size(), ModTime: info.ModTime(),
		}
		changedPath[id] = full
		delete(removed, id)
	}

	if len(removed) == 0 && len(changed) == 0 {
		return 0, nil
	}

	docs := make([]*Doc, 0, len(existing)+len(changed))
	for id, d := range existing {
		if removed[id] {
			continue
		}
		if nd, ok := changed[id]; ok {
			docs = append(docs, nd)
			continue
		}
		docs = append(docs, d)
	}
	for id, nd := range changed {
		if _, ok := existing[id]; !ok {
			docs = append(docs, nd)
		}
	}

	type pending struct {
		at   int
		path string
	}
	var todo []pending
	for i, d := range docs {
		if p, ok := changedPath[d.ID]; ok {
			todo = append(todo, pending{at: i, path: p})
		}
	}

	reuse, rerr := newTextReuse(store)
	if rerr != nil {
		reuse = nil
	}
	extractAll(ix.Extractor(), ix.root, docs, func(i int) (int, string) {
		return todo[i].at, todo[i].path
	}, len(todo), reuse)
	if reuse != nil {
		ix.noteTextReuse(reuse)
	}

	if store != nil {
		for id := range removed {
			_ = store.Delete(id) // best-effort: a stale row is harmless
		}
	}

	ix.rebuild(docs, nil)
	return len(todo), nil
}

// NoteDir records a folder that has just been made, without a rescan.
//
// WHY THIS EXISTS. Making a folder used to schedule a background walk of the
// whole archive and then let the page ask for the tree again -- a race the
// page usually won, so a folder somebody had just created was not in the
// answer. That is indistinguishable from the create having failed, and it is
// what "add a hard refresh after adding a folder" was about.
//
// A rescan is the wrong instrument for it either way: the walk reads several
// thousand files to learn one fact that the caller already knows. Recording
// it costs nothing and is exact, so the very next Tree() is right.
//
// The background rescan still runs for UPLOADS, which genuinely need the
// document read and indexed. It is only the folder itself that is known
// without looking.
func (ix *Index) NoteDir(rel string) {
	rel = strings.Trim(filepath.ToSlash(rel), "/")
	// The tree is three levels -- group, reference, category -- and Scan
	// records no deeper, so this must not either or the two would disagree.
	if rel == "" || strings.Count(rel, "/") >= 3 {
		return
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if slices.Contains(ix.dirs, rel) {
		return
	}
	ix.dirs = append(ix.dirs, rel)
}

// ForgetDir drops a folder that has just been removed or renamed away.
//
// The counterpart of NoteDir, and needed for the same reason: without it a
// deleted folder stays on screen until the next full scan, which reads as the
// delete not having worked.
//
// Children go too. deleteEntry only removes empty folders, so in practice
// there are none -- but a rename moves a whole subtree at once, and leaving
// its children behind under a name that no longer exists would put phantom
// folders in the tree.
func (ix *Index) ForgetDir(rel string) {
	rel = strings.Trim(filepath.ToSlash(rel), "/")
	if rel == "" {
		return
	}
	prefix := rel + "/"
	ix.mu.Lock()
	defer ix.mu.Unlock()
	ix.dirs = slices.DeleteFunc(ix.dirs, func(d string) bool {
		return d == rel || strings.HasPrefix(d, prefix)
	})
}

// Doc returns one document by id.
func (ix *Index) Doc(id string) (*Doc, bool) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	i, ok := ix.byID[id]
	if !ok {
		return nil, false
	}
	return ix.docs[i], true
}

// PathOf resolves a document id to an absolute path.
//
// The id is only ever an index key the service produced itself, never a
// client-supplied path -- but the result is confirmed to sit inside the archive
// root regardless, so a future change cannot quietly open a traversal.
func (ix *Index) PathOf(id string) (string, bool) {
	doc, ok := ix.Doc(id)
	if !ok {
		return "", false
	}
	full := filepath.Join(ix.root, filepath.FromSlash(doc.ID))
	rel, err := filepath.Rel(ix.root, full)
	if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return "", false
	}
	// The text of the path being inside the root is not enough: a link on
	// the way would lead anywhere. Every step must be a real folder and the
	// document a regular file.
	if fi, ok := plainUnder(ix.root, full); !ok || !fi.Mode().IsRegular() {
		return "", false
	}
	return full, true
}

// OpenDoc opens a document for reading, for a download or the viewer.
//
// PathOf alone is a check on a path that is then opened again, and a link
// swapped in between the two would be followed. So the file is opened through
// the archive root as an os.Root, which resolves every step from the root's
// own handle and refuses any link that leads outside it -- a junction's target
// is always absolute, so a junction is refused outright. PathOf still runs
// first: it keeps refusing what the index never had, and links that stay
// inside the archive. TestADocumentIsOpenedThroughTheRoot.
func (ix *Index) OpenDoc(id string) (*os.File, error) {
	if _, ok := ix.PathOf(id); !ok {
		return nil, fs.ErrNotExist
	}
	r, err := os.OpenRoot(ix.root)
	if err != nil {
		return nil, err
	}
	defer r.Close() // a file opened through a Root outlives it
	return openRegular(r, id)
}

// openRegular opens id (slash-separated, relative to r) and requires a regular
// file: a folder or a device under a document's name is not a document.
func openRegular(r *os.Root, id string) (*os.File, error) {
	f, err := r.Open(filepath.FromSlash(id))
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		f.Close()
		return nil, fs.ErrNotExist
	}
	return f, nil
}

// extractThrough reads one document through r into a private copy and
// extracts the copy.
//
// The extractor works on paths -- pdftotext and tesseract are other programs,
// and Word and Excel files are opened by name -- so it cannot be handed the
// handle os.Root gives. Reading the archive by path instead reopened each file
// long after the walk had checked it, and a junction swapped in between was
// followed: its target's text was indexed and served under the decoy's name.
// The copy is made from the checked handle, so what is extracted is what was
// in the archive. It keeps the extension, which is how the extractor chooses
// its method, and is removed as soon as the text is out.
func extractThrough(e *Extractor, r *os.Root, id string) (string, Provenance, error) {
	src, err := openRegular(r, id)
	if err != nil {
		return "", Provenance{}, err
	}
	defer src.Close()
	dir, err := os.MkdirTemp("", "docfind-extract-")
	if err != nil {
		return "", Provenance{}, err
	}
	defer os.RemoveAll(dir)
	copyPath := filepath.Join(dir, "doc"+strings.ToLower(filepath.Ext(id)))
	dst, err := os.OpenFile(copyPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", Provenance{}, err
	}
	_, err = io.Copy(dst, src)
	if cerr := dst.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", Provenance{}, err
	}
	return e.TextWithProvenance(copyPath)
}

// isLink reports an entry that is not a plain file or folder: a symbolic link,
// or -- on Windows -- a directory junction or other reparse point, which Go
// reports as irregular (verified on go1.26.6: a junction is ModeIrregular, not
// a directory, and WalkDir does not descend into it).
func isLink(m fs.FileMode) bool { return m&(fs.ModeSymlink|fs.ModeIrregular) != 0 }

// renamedByWin32 reports a name Windows does not open as written. Every step of
// an ordinary path loses its trailing dots and spaces before it is resolved,
// so a folder made as "dir." (through a \\?\ path; no privilege needed) lists
// as itself but OPENS "dir" -- which may be a junction the listing rightly
// skipped. Such a name is treated like a link: not walked, indexed or served.
// Nothing legitimate has one: none among the 4,510 files on .13 on 2026-10-03,
// and safeSegment already refuses them for names made through the page.
// TestATrailingDotCannotReachAJunction.
func renamedByWin32(name string) bool {
	return strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ")
}

// plainUnder walks from root down to full one step at a time, never following
// anything, and reports whether every step is a real folder -- and what full
// itself is.
//
// THE ARCHIVE IS SERVED TO PEOPLE WHO CANNOT READ THE SERVER'S DISK. A link
// planted inside it -- a symbolic link, or a directory junction, which on
// Windows any account able to write to the folder may make without a special
// privilege -- would otherwise be indexed and served like any document,
// handing out whatever the service account can read elsewhere. Checking the
// text of the path (as PathOf did) does not see a link; checking only the last
// step (one Lstat) does not see a link in a folder above it.
// TestSymlinkCannotEscapeTheArchive, TestAJunctionCannotEscapeTheArchive,
// TestPlainUnderSeesEveryLinkOnTheWay.
func plainUnder(root, full string) (fs.FileInfo, bool) {
	rel, err := filepath.Rel(root, full)
	if err != nil || rel == "." || filepath.IsAbs(rel) ||
		rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, false
	}
	parts := strings.Split(rel, string(filepath.Separator))
	cur := root
	var fi fs.FileInfo
	for i, part := range parts {
		if renamedByWin32(part) {
			return nil, false // Lstat would look at a different entry
		}
		cur = filepath.Join(cur, part)
		if fi, err = os.Lstat(cur); err != nil || isLink(fi.Mode()) {
			return nil, false
		}
		if i < len(parts)-1 && !fi.IsDir() {
			return nil, false
		}
	}
	return fi, true
}

// Types lists the distinct document types present, for a filter dropdown.
func (ix *Index) Types() []string {
	ix.mu.RLock()
	defer ix.mu.RUnlock()

	seen := map[string]bool{}
	var out []string
	for _, d := range ix.docs {
		if d.Type != "" && !seen[d.Type] {
			seen[d.Type] = true
			out = append(out, d.Type)
		}
	}
	sort.Strings(out)
	return out
}

// --- persistence -----------------------------------------------------------

type snapshot struct {
	Root string
	Docs []*Doc
}

// Save writes the index so the next start does not re-read every document.
func (ix *Index) Save(path string) error {
	ix.mu.RLock()
	snap := snapshot{Root: ix.root, Docs: ix.docs}
	ix.mu.RUnlock()

	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	// Encoded in memory first, so it can be sealed before anything reaches
	// the disk. The cache is a few tens of megabytes at the sizes this
	// archive reaches; holding it briefly is cheaper than the alternative,
	// which is writing the plain text out and encrypting it afterwards.
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(snap); err != nil {
		return err
	}

	// Sealed, because this file holds Doc.Text -- the extracted contents of
	// every document in the archive. Supplier names, prices, quantities,
	// contract terms, in one file, and it was written as plain gob.
	//
	// File permissions are not a substitute for this on the platform that
	// matters: Go's 0600 on Windows sets the read-only bit and nothing else,
	// so it never restricted who could read it.
	box, err := secretbox.OpenFileBox(path)
	if err != nil {
		return err
	}
	sealed, err := box.Seal(buf.Bytes())
	if err != nil {
		return err
	}

	tmp := path + ".part"
	// 0600 as well. It does little on Windows and everything on Linux, and
	// costs nothing on either.
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(sealed); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// Load restores a saved index. A missing or unreadable file is not an error:
// the caller simply rescans from scratch.
//
// That is what makes sealing the cache safe. Every caller already treats a
// failed load as "no cache" and scans -- see main.go, where Load's error is
// discarded and Scan runs regardless -- so a key that has been lost, or a file
// copied from another machine where DPAPI will not unwrap it, costs a slower
// start and nothing else. There is no state here that is not derivable from
// the archive itself.
func (ix *Index) Load(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	box, err := secretbox.OpenFileBox(path)
	if err != nil {
		return err
	}
	plain, err := box.Open(raw)
	if err != nil {
		return err
	}

	var snap snapshot
	if err := gob.NewDecoder(bytes.NewReader(plain)).Decode(&snap); err != nil {
		return err
	}
	if snap.Root != ix.root {
		return fmt.Errorf("find: saved index is for %q, not %q", snap.Root, ix.root)
	}
	// nil, not an empty slice: the snapshot carries documents and no folder
	// list, and saying "no folders" here would blank the tree on every
	// restart until the first scan finished. See rebuild.
	ix.rebuild(snap.Docs, nil)
	return nil
}

// --- derivation ------------------------------------------------------------

// refFromPath rebuilds the reference number from the folder segments.
// "220\100\Contract_111222.docx" -> "111222".
func refFromPath(rel string) string {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	var ref strings.Builder
	for _, p := range parts[:max(0, len(parts)-1)] {
		if isDigits(p) {
			ref.WriteString(p)
		}
	}
	if ref.Len() > 0 {
		return ref.String()
	}
	// Fall back to digits in the file name, for archives filed differently.
	return digitsOf(parts[len(parts)-1])
}

// typeFromName reads the document type from the leading part of the file name,
// which is how the generator names them: Contract_111222.docx.
func typeFromName(name string) string {
	base := strings.TrimSuffix(name, filepath.Ext(name))
	if i := strings.LastIndex(base, "_"); i > 0 {
		if isReference(base[i+1:]) {
			return base[:i]
		}
	}
	if i := strings.Index(base, "_"); i > 0 {
		return base[:i]
	}
	return base
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// isReference reports whether s looks like a trailing reference number on a
// file name: digits, optionally grouped with dashes ("154654", "125-156").
//
// Kept separate from isDigits because refFromPath uses that one on folder
// names, where a dash must not be accepted -- the archive splits a dashed
// reference into one folder per group, so a dashed folder name would mean the
// layout was built by something else and concatenating it would produce a
// reference that matches nothing.
func isReference(s string) bool {
	if s == "" {
		return false
	}
	sawDigit := false
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] >= '0' && s[i] <= '9':
			sawDigit = true
		case s[i] == '-':
		default:
			return false
		}
	}
	return sawDigit
}

func digitsOf(s string) string {
	var b strings.Builder
	for _, r := range bidi.ToLatinDigits(s) {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// GroupNode is one reference group in the browse tree.
//
// Count is how many references sit under it, not how many documents. A folder
// tree is read as a folder tree: "300 (2)" answers "what will I find if I
// open this", which is two sub-folders. Summing the documents underneath
// instead made a group of two references holding nine files read as "300 (9)",
// which matches nothing the person sees when they expand it.
type GroupNode struct {
	Name       string          `json:"name"`  // first three digits, e.g. "406"
	Count      int             `json:"count"` // number of references
	Docs       int             `json:"docs"`  // documents in the whole subtree
	References []ReferenceNode `json:"references"`
}

// ReferenceNode is one reference within a group: the second three digits.
//
// This level exists because the archive on disk has it -- a document lives at
// 406\300\Inquiry -- and a tree that jumped straight from 406 to Inquiry did
// not match the folders people were looking at, and merged every reference
// beginning 406 into one undifferentiated list.
type ReferenceNode struct {
	Name       string         `json:"name"`  // second three digits, e.g. "300"
	Ref        string         `json:"ref"`   // whole reference, e.g. "406300"
	Count      int            `json:"count"` // number of categories beneath
	Docs       int            `json:"docs"`  // documents across those categories
	Categories []CategoryNode `json:"categories"`
}

// CategoryNode is one document category within a reference.
type CategoryNode struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// SubGroupOf returns the second part of a reference, which is the folder level
// below the group: "406300" gives "300".
//
// Anything too short to have a second part gives "", and the caller hangs its
// categories directly off the group rather than inventing a level.
func SubGroupOf(ref string) string {
	if len(ref) <= 3 {
		return ""
	}
	if len(ref) >= 6 {
		return ref[3:6]
	}
	return ref[3:]
}

// subNameOf names the tree's middle level for one document.
//
// Normally that is the second half of the reference, because the folder there
// is named with digits and refFromPath built the reference out of exactly
// those digits. But refFromPath keeps ONLY digit segments, so a folder with a
// name -- "Proforma Invoice", anything somebody types into the new-folder box
// -- contributes nothing, the reference comes out as the group alone, and
// SubGroupOf gives "". That drew a row with no name on it at all.
//
// It was always possible (the archive has a few such folders) and it became
// ordinary the moment people could make folders and upload into them. So when
// the digits do not name this level, use the folder that is actually there,
// read off the document's own path -- which is both the truth and what the
// person who made the folder called it.
//
// A document lying loose in the group folder has no middle level to name. It
// takes the group's name, so it reads as sitting in 507 itself, rather than
// hanging under a blank.
func subNameOf(d *Doc) string {
	if sub := SubGroupOf(d.Ref); sub != "" {
		return sub
	}
	parts := strings.Split(d.ID, "/")
	if len(parts) >= 3 && parts[1] != "" {
		return parts[1]
	}
	return d.Group
}

// Tree returns the browse hierarchy: reference group, reference, then
// category -- matching the folders on disk.
//
// This is what the sidebar shows, so someone who does not know a reference
// number can still find a document by walking down to it.
func (ix *Index) Tree() []GroupNode {
	ix.mu.RLock()
	defer ix.mu.RUnlock()

	// group -> reference -> category -> count
	groups := map[string]map[string]map[string]int{}
	refOf := map[string]string{} // "406/300" -> full ref, for filtering

	// Which folders on disk actually hold a document. Filled by the loop
	// below and used by the one after it -- see there for why the order
	// matters.
	filled := map[string]bool{}

	for _, d := range ix.docs {
		if i := strings.LastIndex(d.ID, "/"); i > 0 {
			filled[d.ID[:i]] = true
		}
		sub := subNameOf(d)
		if groups[d.Group] == nil {
			groups[d.Group] = map[string]map[string]int{}
		}
		if groups[d.Group][sub] == nil {
			groups[d.Group][sub] = map[string]int{}
		}
		groups[d.Group][sub][d.Category]++
		refOf[d.Group+"/"+sub] = d.Ref
	}

	// THEN the folders that hold no document at all.
	//
	// After the documents, and only for folders the loop above did not
	// account for. The order and the condition are both deliberate, because
	// a folder's NAME and the CATEGORY of what is inside it are two different
	// things: the category comes from the document's own type, so a file
	// called letter.docx sitting in an Inquiry folder is categorised "Other".
	// Seeding every folder name unconditionally would therefore show an empty
	// "Inquiry" beside a populated "Other" for the same one folder on disk --
	// a folder that appears twice, once as a phantom.
	//
	// Restricting this to folders with nothing in them avoids that entirely:
	// a folder with documents keeps exactly the behaviour it has always had,
	// and one without appears with a count of zero, which is the truth and is
	// the whole point -- otherwise a folder somebody just made is invisible
	// until they put something in it, and creating one looks like it failed.
	for _, dir := range ix.dirs {
		if filled[dir] {
			continue
		}
		parts := strings.Split(dir, "/")
		g := parts[0]
		if groups[g] == nil {
			groups[g] = map[string]map[string]int{}
		}
		if len(parts) < 2 {
			continue
		}
		sub := parts[1]
		if groups[g][sub] == nil {
			groups[g][sub] = map[string]int{}
		}
		// A reference with no documents has no Ref of its own to filter by.
		// The group and the folder name joined is what docxgen would have
		// produced for it, and is what the page shows.
		if _, ok := refOf[g+"/"+sub]; !ok {
			refOf[g+"/"+sub] = g + sub
		}
		if len(parts) < 3 {
			continue
		}
		if _, ok := groups[g][sub][parts[2]]; !ok {
			groups[g][sub][parts[2]] = 0
		}
	}

	out := make([]GroupNode, 0, len(groups))
	for name, refs := range groups {
		node := GroupNode{Name: name}
		for sub, cats := range refs {
			ref := ReferenceNode{Name: sub, Ref: refOf[name+"/"+sub]}
			for cat, n := range cats {
				ref.Categories = append(ref.Categories, CategoryNode{Name: cat, Count: n})
				ref.Docs += n
			}
			// The count shown beside a reference is how many category folders
			// it holds, matching what expanding it reveals.
			ref.Count = len(ref.Categories)
			sort.Slice(ref.Categories, func(i, j int) bool {
				ri, rj := categoryRank(ref.Categories[i].Name), categoryRank(ref.Categories[j].Name)
				if ri != rj {
					return ri < rj
				}
				return ref.Categories[i].Name < ref.Categories[j].Name
			})
			node.References = append(node.References, ref)
			node.Docs += ref.Docs
		}
		node.Count = len(node.References)
		sort.Slice(node.References, func(i, j int) bool { return node.References[i].Name > node.References[j].Name })
		out = append(out, node)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name > out[j].Name })
	return out
}

// Categories lists the distinct categories present, in workflow order.
func (ix *Index) Categories() []string {
	ix.mu.RLock()
	defer ix.mu.RUnlock()

	seen := map[string]bool{}
	var out []string
	for _, d := range ix.docs {
		if !seen[d.Category] {
			seen[d.Category] = true
			out = append(out, d.Category)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		ri, rj := categoryRank(out[i]), categoryRank(out[j])
		if ri != rj {
			return ri < rj
		}
		return out[i] < out[j]
	})
	return out
}
