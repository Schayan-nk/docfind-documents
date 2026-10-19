package find

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"

	"github.com/Schayan-nk/docfind-documents/internal/audit"
	"github.com/Schayan-nk/docfind-documents/internal/auth"
)

// The API behind the documents page's folder controls.
//
// TWO LEVELS OF PERMISSION, because the operations are not equally serious.
//
// Adding a document ADDS to the archive. The worst outcome is clutter, which
// somebody can tidy, so it is action:docs_upload, which the manager role
// gives.
//
// Making a folder, renaming and deleting change the tree itself, which has
// no recycle bin and holds documents people are obliged to keep; a rename
// also breaks every reference to the old name. Those are action:docs_edit
// and action:docs_delete, which are Explicit in the permission catalogue:
// held by named accounts, never by a role.
//
// Deleting is additionally narrowed in deleteEntry itself, which refuses any
// folder that is not empty. The permission says who may ask; that says what
// can be asked for.

// maxDocsUpload bounds one upload request. Per file the cap is
// maxUploadBytes; this bounds the whole multipart body so several files at
// once cannot get around it.
const maxDocsUpload = 200 << 20

// docsErrorText turns one of docsedit.go's errors into the sentence to show.
//
// Mapped rather than passed through: those errors are English because the
// code is, and this page is Persian. Anything unrecognised becomes a generic
// failure and the real reason is logged, because the unrecognised ones are
// the filesystem's and can name a UNC path.
func docsErrorText(err error) (string, int) {
	switch {
	case errors.Is(err, errBadName):
		return "این نام برای پوشه یا پرونده قابل استفاده نیست.", http.StatusBadRequest
	case errors.Is(err, errBadPath), errors.Is(err, errNotInside):
		return "این مسیر در آرشیو نیست.", http.StatusBadRequest
	case errors.Is(err, errExists):
		return "چیزی با این نام از قبل وجود دارد.", http.StatusConflict
	case errors.Is(err, errNotFound):
		return "این پوشه یا پرونده پیدا نشد.", http.StatusNotFound
	case errors.Is(err, errNotEmpty):
		return "این پوشه خالی نیست؛ ابتدا محتوای آن را حذف کنید.", http.StatusConflict
	}
	return "انجام نشد.", http.StatusBadGateway
}

func (s *Server) failDocs(w http.ResponseWriter, what string, err error) {
	msg, code := docsErrorText(err)
	if code == http.StatusBadGateway {
		logReportError("docs "+what, err)
	}
	b, _ := json.Marshal(map[string]string{"error": msg})
	writeJSONError(w, code, string(b))
}

// handleDocsList returns what is directly inside one folder.
//
// Reads the FILESYSTEM rather than the index, so a folder that was just made
// -- and therefore holds no indexable document -- is visible immediately. A
// folder that vanished the moment it was created would make the whole feature
// look broken.
func (s *Server) handleDocsList(w http.ResponseWriter, r *http.Request) {
	entries, err := s.listFolder(r.URL.Query().Get("path"))
	if err != nil {
		s.failDocs(w, "list", err)
		return
	}
	u := auth.UserFrom(r)
	writeJSON(w, map[string]any{
		"entries": entries,
		// What this reader may do, decided by the server. The page draws its
		// menu from this rather than guessing, so nobody is offered a control
		// that will refuse them.
		"can_write": u != nil,
		"can_admin": s.can(r, auth.ActionDocsDelete),
	})
}

// rescanSoon brings the index up to date after a change to the tree.
//
// In the background, because a scan walks the whole archive and the caller is
// a browser waiting on a button. The page reloads the tree straight after its
// request returns, so the usual case is that the walk finishes first on an
// archive this size; on a large one the folder appears a moment later rather
// than the request hanging.
//
// Needed for CREATING a folder as much as for uploading into one. The tree
// lists the folders the last scan saw, so without this a new folder is on
// disk and absent from the screen -- which looks exactly like the create
// having failed.
//
// Coalesced through a scanCoalescer rather than one goroutine per call: a
// burst of uploads used to queue one full ix.Scan() behind another, serialised
// by the index's own scanMu, so ten changes in a row paid for ten full walks
// of the archive when one, run after the last of them landed, would have
// reflected all ten. At most one scan is ever running plus one queued to catch
// whatever arrived while it ran; further calls while one is already queued are
// dropped, since that next run will see their changes too.
func (s *Server) rescanSoon(what string) {
	if s.ix == nil {
		return
	}
	s.rescanOnce.Do(func() {
		s.rescan = newScanCoalescer(func(reason string) error {
			_, err := s.ix.Scan()
			if err != nil {
				logReportError("rescan after "+reason, err)
			}
			return err
		})
	})
	s.rescan.trigger(what)
}

// scanCoalescer runs a background job so that many trigger calls in a row
// cost at most two runs: the one already in flight, which cannot be cancelled
// partway through, and a single queued run that starts right after and so
// covers everything that arrived while the first was busy. Triggers beyond
// that are dropped -- a third, fourth or fiftieth call while one is already
// queued needs nothing more than the run already waiting to happen.
type scanCoalescer struct {
	mu      sync.Mutex
	running bool
	queued  bool
	// reason is what the next run will be attributed to in the log. Whichever
	// trigger call is most recent when a run starts wins; a scan started for
	// several reasons at once cannot honestly be logged as any single one of
	// them, so it is logged as the last.
	reason string
	run    func(reason string) error
}

func newScanCoalescer(run func(reason string) error) *scanCoalescer {
	return &scanCoalescer{run: run}
}

// trigger asks for a run soon, coalescing with whatever is already in flight
// or queued. See the type doc for the rule.
func (c *scanCoalescer) trigger(reason string) {
	c.mu.Lock()
	c.reason = reason
	if c.running {
		c.queued = true
		c.mu.Unlock()
		return
	}
	c.running = true
	c.mu.Unlock()
	go c.loop()
}

func (c *scanCoalescer) loop() {
	for {
		c.mu.Lock()
		reason := c.reason
		c.mu.Unlock()

		c.run(reason)

		c.mu.Lock()
		if c.queued {
			// Something triggered while this run was in progress; run once
			// more rather than lose it, and only once no matter how many
			// triggers arrived.
			c.queued = false
			c.mu.Unlock()
			continue
		}
		c.running = false
		c.mu.Unlock()
		return
	}
}

// handleDocsFolder creates a folder.
func (s *Server) handleDocsFolder(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Parent string `json:"parent"`
		Name   string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, `{"error":"درخواست نامعتبر است."}`)
		return
	}
	audit.Note(r.Context(), "doc:"+clipRunes(req.Parent+"/"+req.Name, 300), nil)
	rel, err := s.makeFolder(req.Parent, req.Name)
	if err != nil {
		s.failDocs(w, "folder", err)
		return
	}
	// RECORDED, NOT RESCANNED. The tree lists the folders the last scan saw,
	// so without this the new folder is on disk and absent from the screen --
	// indistinguishable from the create having failed, and exactly what it
	// looked like the first time this was tried.
	//
	// Scheduling a background walk instead was the first fix and it was a
	// race the page usually won: the answer came back before the walk
	// finished, so the folder still was not in it. Recording the one fact the
	// caller already knows is exact and immediate, and it does not read
	// several thousand files to learn it.
	if s.ix != nil {
		s.ix.NoteDir(rel)
	}
	writeJSON(w, map[string]any{"path": rel})
}

// handleDocsUpload writes documents into a folder.
func (s *Server) handleDocsUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxDocsUpload)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		writeJSONError(w, http.StatusBadRequest,
			`{"error":"فرم خوانده نشد؛ ممکن است پرونده‌ها بزرگ‌تر از حد مجاز باشند."}`)
		return
	}
	defer r.MultipartForm.RemoveAll()

	folder := r.FormValue("path")
	files := r.MultipartForm.File["file"]
	if len(files) == 0 {
		writeJSONError(w, http.StatusBadRequest, `{"error":"پرونده‌ای انتخاب نشده است."}`)
		return
	}

	// Each file is attempted and reported on its own. One refused extension
	// in a drop of six should not discard the other five -- and the page can
	// say exactly which one it was.
	var saved []string
	var failed []map[string]string
	// Named on anything refused and kept in quarantine.
	uploader := ""
	if u := auth.UserFrom(r); u != nil {
		uploader = u.Username
	}
	for _, fh := range files {
		f, err := fh.Open()
		if err != nil {
			failed = append(failed, map[string]string{"name": fh.Filename, "error": "خوانده نشد"})
			continue
		}
		name, err := s.saveUpload(folder, fh.Filename, uploader, f)
		f.Close()
		if err != nil {
			msg, _ := docsErrorText(err)
			var refused rejection
			switch {
			case errors.Is(err, errBadName):
				msg = "این نوع پرونده پذیرفته نمی‌شود."
			case errors.As(err, &refused):
				// Said plainly: "this file carries macros" is something the
				// person can act on; "refused" is not.
				msg = refused.reason
			}
			failed = append(failed, map[string]string{"name": fh.Filename, "error": msg})
			continue
		}
		saved = append(saved, name)
	}

	// A document nobody can find is not filed. Rescanning here means it is
	// searchable by the time the page redraws, rather than at the next timer.
	if len(saved) > 0 {
		s.rescanSoon("upload")
	}
	// The folder and the names that landed -- names, never contents.
	audit.Note(r.Context(), "doc:"+clipRunes(folder, 300), map[string]any{
		"saved": clipList(saved, 20), "failed": len(failed)})
	writeJSON(w, map[string]any{"saved": saved, "failed": failed})
}

// handleDocsRename renames a folder or a document.
func (s *Server) handleDocsRename(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, `{"error":"درخواست نامعتبر است."}`)
		return
	}
	audit.Note(r.Context(), "doc:"+clipRunes(req.Path, 300), map[string]any{"to": clipRunes(req.Name, 200)})
	rel, err := s.renameEntry(req.Path, req.Name)
	if err != nil {
		s.failDocs(w, "rename", err)
		return
	}
	// The folder set is corrected here so the tree is right immediately; the
	// rescan follows because renaming a FOLDER moves every document under it
	// and each one's id changes with its path.
	if s.ix != nil {
		s.ix.ForgetDir(req.Path)
		s.ix.NoteDir(rel)
	}
	s.rescanSoon("rename")
	writeJSON(w, map[string]any{"path": rel})
}

// handleDocsDelete removes a document, or an empty folder.
func (s *Server) handleDocsDelete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, `{"error":"درخواست نامعتبر است."}`)
		return
	}
	if strings.TrimSpace(req.Path) == "" {
		writeJSONError(w, http.StatusBadRequest, `{"error":"این مسیر در آرشیو نیست."}`)
		return
	}
	audit.Note(r.Context(), "doc:"+clipRunes(req.Path, 300), nil)
	if err := s.deleteEntry(req.Path); err != nil {
		s.failDocs(w, "delete", err)
		return
	}
	// Dropped from the folder set immediately, so it leaves the tree on this
	// request rather than at the next scan -- a folder that is still there
	// after being deleted reads as the delete not having worked. Harmless
	// when the deleted thing was a document and not a folder: nothing matches.
	if s.ix != nil {
		s.ix.ForgetDir(req.Path)
	}
	s.rescanSoon("delete")
	writeJSON(w, map[string]any{"ok": true})
}
