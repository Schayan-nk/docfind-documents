package find

// The Word editor (5.55.0): a document in the archive opened in ONLYOFFICE
// Docs, the real thing -- pages, the letterheads and their header boxes, page
// numbers, right-to-left Persian -- rather than a form standing in for it.
// docxgen still makes every Inquiry, TO and PI; this is where they are read
// and changed afterwards.
//
// THE DOCUMENT SERVER IS NEVER EXPOSED. It listens on loopback beside docfind
// and is reached three ways, all of them through here:
//
//   - the browser loads the editor through /office/ds/, a relay that only a
//     signed-in person gets through, so the editor works wherever docfind
//     does -- through IIS, over HTTPS, from outside -- with nothing new opened;
//   - the document server fetches the file from /api/office/file, with a
//     link docfind signed for that one file;
//   - it posts the saved document back to /api/office/callback, with the
//     same link and its own token, signed with the secret both share.
//
// Saving (officesave.go) keeps the version it replaces, every time, and never
// writes over a file that changed since the editor opened it.
//
// Configured by DOCFIND_OFFICE_URL (the document server, e.g.
// http://127.0.0.1:8088) and the shared secret in office.key beside the data
// (DOCFIND_OFFICE_KEY_FILE moves it) -- the same secret as in the document
// server's local.json. Without both the button is not offered.

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/Schayan-nk/docfind-documents/internal/auth"
	"github.com/Schayan-nk/docfind-documents/internal/obs"
	"github.com/Schayan-nk/docfind-documents/internal/onlyoffice"
)

// officePrefix is where the document server appears inside docfind.
const officePrefix = "/office/ds"

type officeState struct {
	docs     *url.URL // the document server, as docfind reaches it
	self     string   // docfind, as the document server reaches it
	secret   []byte   // shared with the document server
	linkKey  []byte   // signs docfind's own file and callback links
	versions string   // where every replaced version is kept
	proxy    *httputil.ReverseProxy
	client   *http.Client

	mu    sync.Mutex
	locks map[string]*sync.Mutex // one save at a time per document
}

// UseOffice turns the editor on. docsURL is the document server as docfind
// reaches it; selfURL is docfind as the document server reaches it; keyFile
// holds the shared secret; versionsDir keeps what each save replaced.
func (s *Server) UseOffice(docsURL, selfURL, keyFile, versionsDir string) error {
	docsURL = strings.TrimRight(strings.TrimSpace(docsURL), "/")
	if docsURL == "" {
		return nil
	}
	u, err := url.Parse(docsURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("DOCFIND_OFFICE_URL is not an http(s) address")
	}
	raw, err := os.ReadFile(keyFile)
	if err != nil {
		return errors.New("the office key file cannot be read: " + keyFile)
	}
	secret := []byte(strings.TrimSpace(string(raw)))
	if len(secret) < 32 {
		return errors.New("the office key is shorter than 32 characters")
	}
	m := hmac.New(sha256.New, secret)
	m.Write([]byte("docfind office links"))
	o := &officeState{docs: u, self: strings.TrimRight(selfURL, "/"), secret: secret, linkKey: m.Sum(nil),
		versions: versionsDir, client: &http.Client{Timeout: 2 * time.Minute}, locks: map[string]*sync.Mutex{}}
	o.proxy = &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(u)
			pr.Out.URL.Path = strings.TrimPrefix(pr.In.URL.Path, officePrefix)
			pr.Out.URL.RawPath = ""
			if pr.Out.URL.Path == "" {
				pr.Out.URL.Path = "/"
			}
			pr.Out.Host = u.Host
			// docfind's own session never reaches the document server.
			pr.Out.Header.Del("Cookie")
			pr.Out.Header.Del("Authorization")
			// So the links it writes into its pages come back through here.
			pr.Out.Header.Set("X-Forwarded-Host", pr.In.Host)
			pr.Out.Header.Set("X-Forwarded-Proto", requestScheme(pr.In))
			pr.Out.Header.Set("X-Forwarded-Prefix", officePrefix)
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			obs.Default().Write("office", "error", obs.Fields{"msg": "the document server did not answer", "error": err.Error()})
			writeJSONError(w, http.StatusBadGateway, `{"error":"ویرایشگر Word پاسخ نداد."}`)
		},
	}
	s.office = o
	return nil
}

// requestScheme is how the browser reached docfind: IIS says so in
// X-Forwarded-Proto, which docfind already trusts when told to.
func requestScheme(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	if p := strings.ToLower(r.Header.Get("X-Forwarded-Proto")); p == "https" || p == "http" {
		return p
	}
	return "http"
}

// officeRelay passes the editor's own requests -- its scripts, its frames,
// its websocket -- to the document server, for somebody signed in.
func (s *Server) officeRelay() http.Handler {
	relay := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.office == nil {
			http.NotFound(w, r)
			return
		}
		// docfind's own policy is for docfind's pages; the editor brings its
		// own, and lives in a frame of the /office page.
		h := w.Header()
		h.Del("Content-Security-Policy")
		h.Set("X-Frame-Options", "SAMEORIGIN")
		if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			rc := http.NewResponseController(w)
			_ = rc.SetReadDeadline(time.Time{})
			_ = rc.SetWriteDeadline(time.Time{})
		}
		s.office.proxy.ServeHTTP(w, r)
	})
	if !s.AuthEnabled() {
		return relay
	}
	return s.guard.RequireAuth(relay)
}

// officeLink is what docfind's own links to the document server carry.
type officeLink struct {
	ID   string `json:"id"`  // the document, archive-relative
	Key  string `json:"key"` // the version the editor opened
	User string `json:"u"`   // who opened it
	Exp  int64  `json:"exp"`
}

func (o *officeState) link(l officeLink) string {
	t, _ := onlyoffice.Sign(o.linkKey, l)
	return t
}

func (o *officeState) readLink(t string) (officeLink, error) {
	claims, err := onlyoffice.Verify(o.linkKey, t, time.Now())
	if err != nil {
		return officeLink{}, err
	}
	raw, _ := json.Marshal(claims)
	var l officeLink
	if json.Unmarshal(raw, &l) != nil || l.ID == "" || l.Key == "" {
		return officeLink{}, onlyoffice.ErrToken
	}
	return l, nil
}

// officeDoc is a document a reader may open, and its state on disk now.
func (s *Server) officeDoc(r *http.Request, id string) (*Doc, os.FileInfo, bool) {
	if s.ix == nil {
		return nil, nil, false
	}
	doc, ok := s.ix.Doc(id)
	if !ok {
		return nil, nil, false
	}
	f, err := s.ix.OpenDoc(doc.ID)
	if err != nil {
		return nil, nil, false
	}
	st, err := f.Stat()
	f.Close()
	if err != nil {
		return nil, nil, false
	}
	return doc, st, true
}

const officeOff = `{"error":"ویرایشگر Word روی این سرور راه‌اندازی نشده است."}`

// handleOfficeConfig is GET /api/office/config?id=: the editor's
// configuration for one document, signed. Somebody who may change documents
// gets it to edit (a .docx, .xlsx or .pptx); anybody else, to read.
func (s *Server) handleOfficeConfig(w http.ResponseWriter, r *http.Request) {
	o := s.office
	if o == nil {
		writeJSONError(w, http.StatusServiceUnavailable, officeOff)
		return
	}
	doc, st, ok := s.officeDoc(r, r.URL.Query().Get("id"))
	if !ok {
		writeJSONError(w, http.StatusNotFound, `{"error":"این سند پیدا نشد."}`)
		return
	}
	kind, editable := onlyoffice.Kind(doc.Name)
	if kind == "" {
		writeJSONError(w, http.StatusBadRequest, `{"error":"این نوع پرونده در ویرایشگر باز نمی‌شود."}`)
		return
	}
	me := auth.UserFrom(r)
	user, name := "", ""
	if me != nil {
		user, name = me.Username, me.DisplayName
	}
	if name == "" {
		name = user
	}
	edit := editable && s.can(r, auth.ActionDocsWrite)
	key := onlyoffice.Key(doc.ID, st.Size(), st.ModTime())
	now := time.Now()
	fileLink := o.link(officeLink{ID: doc.ID, Key: key, User: user, Exp: now.Add(24 * time.Hour).Unix()})
	cfg := onlyoffice.Config{
		DocumentType: kind,
		Type:         map[bool]string{true: "mobile", false: "desktop"}[r.URL.Query().Get("mobile") == "1"],
		Width:        "100%", Height: "100%",
		Document: onlyoffice.Document{
			FileType: strings.TrimPrefix(strings.ToLower(path.Ext(doc.Name)), "."), Key: key, Title: doc.Name,
			URL:         o.self + "/api/office/file?t=" + url.QueryEscape(fileLink),
			Permissions: onlyoffice.Permissions{Edit: edit, Download: true, Print: true, Comment: edit, Review: edit},
		},
		EditorConfig: onlyoffice.EditorConfig{
			Lang: "fa", Region: "fa-IR", Mode: map[bool]string{true: "edit", false: "view"}[edit],
			User:          onlyoffice.User{ID: user, Name: name},
			CoEditing:     &onlyoffice.CoEditing{Mode: "fast", Change: true},
			Customization: onlyoffice.Customization{Forcesave: true, Autosave: true, UITheme: "theme-dark"},
		},
	}
	if edit {
		// A session can stay open for days; the callback also needs the
		// document server's own token, so a long life here opens nothing.
		cb := o.link(officeLink{ID: doc.ID, Key: key, User: user, Exp: now.Add(30 * 24 * time.Hour).Unix()})
		cfg.EditorConfig.CallbackURL = o.self + "/api/office/callback?t=" + url.QueryEscape(cb)
	}
	signed, err := cfg.Signed(o.secret)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, `{"error":"انجام نشد."}`)
		return
	}
	writeJSON(w, map[string]any{"config": signed, "api": officePrefix + "/web-apps/apps/api/documents/api.js",
		"id": doc.ID, "name": doc.Name, "ref": doc.Ref, "edit": edit, "versions": len(o.listVersions(doc.ID))})
}

// handleOfficeFile is GET /api/office/file?t=: the document, for the
// document server, which holds a link docfind signed for this one file.
func (s *Server) handleOfficeFile(w http.ResponseWriter, r *http.Request) {
	o := s.office
	if o == nil || s.ix == nil {
		http.NotFound(w, r)
		return
	}
	l, err := o.readLink(r.URL.Query().Get("t"))
	if err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	f, err := s.ix.OpenDoc(l.ID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, r, "", st.ModTime(), f)
}
