package find

// Saving what the Word editor changed (office.go), and the versions each
// save leaves behind.
//
// THREE RULES, because these are the business's documents and the archive
// has no recycle bin:
//
//  1. The version a save replaces is kept first, in the versions folder
//     beside docfind's data -- outside the archive, so the search does not
//     find every old draft. Fifty per document.
//  2. A save never writes over a file that changed since the editor opened
//     it (docxgen made it again, or somebody restored an older version):
//     the edited one goes beside it as a new file instead, so both stay.
//  3. What comes back is checked like an upload -- that it is the kind of
//     file its name says, and, where the server has it, by the virus scan --
//     before it reaches the archive, and it reaches it in one rename.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Schayan-nk/docfind-documents/internal/audit"
	"github.com/Schayan-nk/docfind-documents/internal/auth"
	"github.com/Schayan-nk/docfind-documents/internal/obs"
	"github.com/Schayan-nk/docfind-documents/internal/onlyoffice"
)

// maxOfficeSave bounds an edited document fetched back.
const maxOfficeSave = 100 << 20

// keepVersions is how many replaced versions each document keeps.
const keepVersions = 50

// officeVersion is one replaced version of a document.
type officeVersion struct {
	V      string    `json:"v"` // its name in the versions folder
	ID     string    `json:"id"`
	At     time.Time `json:"at"`
	By     string    `json:"by"`     // whose change replaced it
	Reason string    `json:"reason"` // why it was replaced
	Size   int64     `json:"size"`
}

// officeWritten is what a save last wrote for an editing session, so the
// session's next save (the Save button, then closing) is not taken for a
// change by somebody else.
type officeWritten struct {
	Key  string    `json:"key"`
	Size int64     `json:"size"`
	Mod  time.Time `json:"mod"`
}

func (o *officeState) docDir(id string) string {
	sum := sha256.Sum256([]byte(id))
	return filepath.Join(o.versions, hex.EncodeToString(sum[:10]))
}

func (o *officeState) lock(id string) *sync.Mutex {
	o.mu.Lock()
	defer o.mu.Unlock()
	m := o.locks[id]
	if m == nil {
		m = &sync.Mutex{}
		o.locks[id] = m
	}
	return m
}

// handleOfficeCallback is POST /api/office/callback?t=: the document server
// saying a document was saved, or opened, or closed. It is answered
// {"error":0} once the saved file is safely in the archive; anything else and
// the document server keeps the edit and says so in the editor.
func (s *Server) handleOfficeCallback(w http.ResponseWriter, r *http.Request) {
	o := s.office
	if o == nil {
		http.NotFound(w, r)
		return
	}
	refuse := func(code int) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		io.WriteString(w, `{"error":1}`)
	}
	l, err := o.readLink(r.URL.Query().Get("t"))
	if err != nil {
		refuse(http.StatusForbidden)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		refuse(http.StatusBadRequest)
		return
	}
	cb, err := onlyoffice.ReadCallback(o.secret, body, r.Header.Get("Authorization"), time.Now())
	if err != nil || cb.Key != l.Key {
		obs.Default().Write("office", "warn", obs.Fields{"msg": "a callback that does not check out was refused", "id": l.ID})
		refuse(http.StatusForbidden)
		return
	}
	switch cb.Status {
	case onlyoffice.StatusMustSave, onlyoffice.StatusForceSaved:
		by := l.User
		if len(cb.Users) > 0 {
			by = cb.Users[0]
		}
		saved, err := s.officeSave(r.Context(), l, cb, by)
		if err != nil {
			obs.Default().Write("office", "error", obs.Fields{"msg": "an edited document was not saved", "id": l.ID, "error": err.Error()})
			refuse(http.StatusOK)
			return
		}
		s.auditChange(by, "docs.office_save", "doc:"+clipRunes(saved, 300), map[string]any{"opened": clipRunes(l.ID, 300), "users": cb.Users})
		s.rescanSoon("office save")
	case onlyoffice.StatusSaveError, onlyoffice.StatusForceError:
		obs.Default().Write("office", "error", obs.Fields{"msg": "the document server could not save", "id": l.ID, "status": cb.Status})
	}
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, `{"error":0}`)
}

// officeSave fetches the edited document and puts it in the archive; it
// returns where it went -- the document itself, or a new file beside it
// when the document changed underneath the editor.
func (s *Server) officeSave(ctx context.Context, l officeLink, cb onlyoffice.Callback, by string) (string, error) {
	o := s.office
	root, err := s.docsRoot()
	if err != nil {
		return "", err
	}
	held, err := o.fetchEdited(ctx, cb.URL, s.quarantineDir())
	if err != nil {
		return "", err
	}
	defer os.Remove(held)
	if err := inspectUpload(held, strings.ToLower(path.Ext(l.ID))); err != nil {
		return "", fmt.Errorf("the edited file is not a %s: %w", path.Ext(l.ID), err)
	}
	if found, _ := scanUpload(held); found {
		s.keepRejected(held, path.Base(l.ID), by, "Windows Defender found a threat in an edited document")
		return "", errors.New("the virus scan found a threat in the edited document")
	}

	m := o.lock(l.ID)
	m.Lock()
	defer m.Unlock()
	ar, err := os.OpenRoot(root)
	if err != nil {
		return "", err
	}
	defer ar.Close()
	name := filepath.FromSlash(l.ID)
	st, err := ar.Stat(name)
	switch {
	case errors.Is(err, os.ErrNotExist):
		// Renamed or deleted while it was open: the edit comes back under
		// its old name if that name is still free.
		return l.ID, writeNew(ar, name, held)
	case err != nil:
		return "", err
	}
	if !o.unchanged(l, st) {
		side := beside(l.ID, by, time.Now())
		for n := 2; n < 100 && rootTaken(ar, filepath.FromSlash(side)); n++ {
			side = uniqueName(beside(l.ID, by, time.Now()), n)
		}
		obs.Default().Write("office", "warn", obs.Fields{"msg": "the document changed while it was open; the edit was saved beside it", "id": l.ID, "as": side})
		return side, writeNew(ar, filepath.FromSlash(side), held)
	}
	if err := o.keepVersion(ar, l.ID, by, "پیش از ویرایش در Word"); err != nil {
		return "", fmt.Errorf("keeping the version it replaces: %w", err)
	}
	if err := replaceIn(ar, name, held); err != nil {
		return "", err
	}
	if st, err := ar.Stat(name); err == nil {
		o.remember(l.ID, officeWritten{Key: l.Key, Size: st.Size(), Mod: st.ModTime()})
	}
	return l.ID, nil
}

// unchanged says whether the file on disk is the one the editor opened, or
// the one this session's last save wrote.
func (o *officeState) unchanged(l officeLink, st os.FileInfo) bool {
	if onlyoffice.Key(l.ID, st.Size(), st.ModTime()) == l.Key {
		return true
	}
	var w officeWritten
	raw, err := os.ReadFile(filepath.Join(o.docDir(l.ID), "written.json"))
	return err == nil && json.Unmarshal(raw, &w) == nil && w.Key == l.Key &&
		w.Size == st.Size() && w.Mod.Equal(st.ModTime())
}

func (o *officeState) remember(id string, w officeWritten) {
	dir := o.docDir(id)
	if os.MkdirAll(dir, 0o750) != nil {
		return
	}
	raw, _ := json.Marshal(w)
	_ = os.WriteFile(filepath.Join(dir, "written.json"), raw, 0o640)
}

// fetchEdited downloads the edited document from the document server into
// dir. Whatever host the link names, it is fetched from the document server
// docfind was configured with, and only from its cache of saved files.
func (o *officeState) fetchEdited(ctx context.Context, link, dir string) (string, error) {
	u, err := url.Parse(link)
	if err != nil {
		return "", err
	}
	p := strings.TrimPrefix(u.Path, officePrefix)
	if !strings.HasPrefix(p, "/cache/files/") || strings.Contains(p, "..") {
		return "", fmt.Errorf("the saved document's link is not in the document server's cache: %q", u.Path)
	}
	src := *o.docs
	src.Path, src.RawPath, src.RawQuery = strings.TrimRight(o.docs.Path, "/")+p, "", u.RawQuery
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src.String(), nil)
	if err != nil {
		return "", err
	}
	res, err := o.client.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("the document server answered %d", res.StatusCode)
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, "office-*.part")
	if err != nil {
		return "", err
	}
	n, err := io.Copy(f, io.LimitReader(res.Body, maxOfficeSave+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && (n == 0 || n > maxOfficeSave) {
		err = fmt.Errorf("the saved document is %d bytes", n)
	}
	if err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// beside is the name an edit is saved under when its document changed
// underneath it: "x.docx" becomes "x - ویرایش s.beigi 1405-07-14 1530.docx"
// -- the Gregorian date in the name, as the archive's own names are.
func beside(id, by string, at time.Time) string {
	ext := path.Ext(id)
	return strings.TrimSuffix(id, ext) + " - ویرایش " + safeName(by) + " " + at.Format("2006-01-02 1504") + ext
}

func safeName(s string) string {
	return strings.Map(func(r rune) rune {
		if strings.ContainsRune(`\/:*?"<>|`, r) || r < ' ' {
			return '_'
		}
		return r
	}, s)
}

func rootTaken(r *os.Root, name string) bool {
	_, err := r.Lstat(name)
	return err == nil
}

// writeNew puts held at name, which must not exist yet.
func writeNew(r *os.Root, name, held string) error {
	src, err := os.Open(held)
	if err != nil {
		return err
	}
	defer src.Close()
	f, err := r.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, src)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		r.Remove(name)
	}
	return err
}

// replaceIn puts held in place of name in one rename: a copy beside it
// first, then the rename, so a reader never meets half a document. Windows
// refuses the rename while somebody has the file open; it is tried again
// for a few seconds.
func replaceIn(r *os.Root, name, held string) error {
	tmp := filepath.Join(filepath.Dir(name), ".~office-"+strconv.FormatInt(time.Now().UnixNano(), 36)+".tmp")
	if err := writeNew(r, tmp, held); err != nil {
		return err
	}
	var err error
	for i := 0; i < 20; i++ {
		if err = r.Rename(tmp, name); err == nil {
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	r.Remove(tmp)
	return fmt.Errorf("replacing the document: %w", err)
}

// keepVersion copies the document as it is now into its versions, with who
// is about to replace it and why, and lets the oldest go past fifty.
func (o *officeState) keepVersion(ar *os.Root, id, by, reason string) error {
	src, err := ar.Open(filepath.FromSlash(id))
	if err != nil {
		return err
	}
	defer src.Close()
	dir := o.docDir(id)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	now := time.Now()
	v := strconv.FormatInt(now.UnixNano(), 10)
	f, err := os.OpenFile(filepath.Join(dir, v+path.Ext(id)), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, src)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(f.Name())
		return err
	}
	meta, _ := json.Marshal(officeVersion{V: v, ID: id, At: now, By: by, Reason: reason, Size: n})
	if err := os.WriteFile(filepath.Join(dir, v+".json"), meta, 0o640); err != nil {
		os.Remove(f.Name())
		return err
	}
	vs := o.listVersions(id)
	for _, old := range vs[min(len(vs), keepVersions):] {
		os.Remove(filepath.Join(dir, old.V+".json"))
		os.Remove(filepath.Join(dir, old.V+path.Ext(id)))
	}
	return nil
}

// listVersions are a document's kept versions, newest first.
func (o *officeState) listVersions(id string) []officeVersion {
	dir := o.docDir(id)
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []officeVersion
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".json") || e.Name() == "written.json" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		var v officeVersion
		if err == nil && json.Unmarshal(raw, &v) == nil && v.ID == id {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out
}

func (o *officeState) version(id, v string) (officeVersion, bool) {
	for _, x := range o.listVersions(id) {
		if x.V == v {
			return x, true
		}
	}
	return officeVersion{}, false
}

// handleOfficeVersions is GET /api/office/versions?id=: what a document was
// before each change, for somebody who may read it.
func (s *Server) handleOfficeVersions(w http.ResponseWriter, r *http.Request) {
	if s.office == nil {
		writeJSONError(w, http.StatusServiceUnavailable, officeOff)
		return
	}
	doc, _, ok := s.officeDoc(r, r.URL.Query().Get("id"))
	if !ok {
		writeJSONError(w, http.StatusNotFound, `{"error":"این سند پیدا نشد."}`)
		return
	}
	vs := s.office.listVersions(doc.ID)
	if vs == nil {
		vs = []officeVersion{}
	}
	writeJSON(w, map[string]any{"id": doc.ID, "versions": vs, "can_restore": s.can(r, auth.ActionDocsWrite)})
}

// handleOfficeVersionFile is GET /api/office/version?id=&v=: one kept
// version, to download.
func (s *Server) handleOfficeVersionFile(w http.ResponseWriter, r *http.Request) {
	if s.office == nil {
		http.NotFound(w, r)
		return
	}
	doc, _, ok := s.officeDoc(r, r.URL.Query().Get("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	v, ok := s.office.version(doc.ID, r.URL.Query().Get("v"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(filepath.Join(s.office.docDir(doc.ID), v.V+path.Ext(doc.ID)))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	ext := path.Ext(doc.Name)
	name := strings.TrimSuffix(doc.Name, ext) + " (" + v.At.Format("2006-01-02 1504") + ")" + ext
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(name))
	http.ServeContent(w, r, "", v.At, f)
}

// handleOfficeRestore is POST /api/office/restore {id, v}: a kept version
// put back, the current one kept first like any other change.
func (s *Server) handleOfficeRestore(w http.ResponseWriter, r *http.Request) {
	o := s.office
	if o == nil {
		writeJSONError(w, http.StatusServiceUnavailable, officeOff)
		return
	}
	var req struct {
		ID string `json:"id"`
		V  string `json:"v"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, `{"error":"درخواست نامعتبر است."}`)
		return
	}
	doc, _, ok := s.officeDoc(r, req.ID)
	if !ok {
		writeJSONError(w, http.StatusNotFound, `{"error":"این سند پیدا نشد."}`)
		return
	}
	v, ok := o.version(doc.ID, req.V)
	if !ok {
		writeJSONError(w, http.StatusNotFound, `{"error":"این نسخه پیدا نشد."}`)
		return
	}
	root, err := s.docsRoot()
	if err != nil {
		s.failDocs(w, "restore", err)
		return
	}
	user := ""
	if u := auth.UserFrom(r); u != nil {
		user = u.Username
	}
	m := o.lock(doc.ID)
	m.Lock()
	defer m.Unlock()
	ar, err := os.OpenRoot(root)
	if err != nil {
		s.failDocs(w, "restore", err)
		return
	}
	defer ar.Close()
	if err := o.keepVersion(ar, doc.ID, user, "پیش از بازگرداندن نسخهٔ "+v.At.Format("2006-01-02 15:04")); err != nil {
		s.failDocs(w, "restore", err)
		return
	}
	if err := replaceIn(ar, filepath.FromSlash(doc.ID), filepath.Join(o.docDir(doc.ID), v.V+path.Ext(doc.ID))); err != nil {
		s.failDocs(w, "restore", err)
		return
	}
	audit.Note(r.Context(), "doc:"+clipRunes(doc.ID, 300), map[string]any{"restored": v.V})
	s.rescanSoon("office restore")
	writeJSON(w, map[string]any{"ok": true})
}
