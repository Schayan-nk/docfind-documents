package find

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// These tests treat the HTTP surface as hostile input. It binds loopback only,
// but "only local processes can reach it" is a weaker guarantee than it sounds
// on a shared desktop, and a path-traversal bug would turn a read-only document
// browser into a way to read anything the signed-in user can.

func newTestServer(t *testing.T, token string) (*Server, *Index, string) {
	t.Helper()
	root := buildArchive(t)
	ix := NewIndex(root)
	if _, err := ix.Scan(); err != nil {
		t.Fatal(err)
	}
	return NewServer(ix, token), ix, root
}

func do(t *testing.T, s *Server, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

// --- token ------------------------------------------------------------------

func TestEveryAPIRequiresTheToken(t *testing.T) {
	s, _, _ := newTestServer(t, "sekrit")

	endpoints := []struct{ method, path string }{
		{"GET", "/api/status"},
		{"GET", "/api/tree"},
		{"GET", "/api/search?q=a"},
		{"GET", "/api/doc?id=111/222/Contract_111222.docx"},
		{"POST", "/api/rescan"},
		{"POST", "/api/connect"},
	}
	for _, e := range endpoints {
		if got := do(t, s, e.method, e.path).Code; got != http.StatusForbidden {
			t.Errorf("%s %s without a token returned %d, want 403", e.method, e.path, got)
		}
		wrong := e.path + (map[bool]string{true: "&", false: "?"}[strings.Contains(e.path, "?")]) + "t=wrong"
		if got := do(t, s, e.method, wrong).Code; got != http.StatusForbidden {
			t.Errorf("%s %s with a wrong token returned %d, want 403", e.method, e.path, got)
		}
	}
}

func TestTokenAcceptedInHeaderOrQuery(t *testing.T) {
	s, _, _ := newTestServer(t, "sekrit")

	if got := do(t, s, "GET", "/api/status?t=sekrit").Code; got != http.StatusOK {
		t.Errorf("query token returned %d, want 200", got)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/status", nil)
	req.Header.Set("X-Token", "sekrit")
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("header token returned %d, want 200", rec.Code)
	}
}

// A prefix of the real token must not pass; this is the mistake a naive
// comparison makes.
func TestPartialTokenRejected(t *testing.T) {
	s, _, _ := newTestServer(t, "sekrit")
	for _, bad := range []string{"", "s", "sekri", "sekrit ", " sekrit", "SEKRIT", "sekritsekrit"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/api/status", nil)
		req.Header.Set("X-Token", bad)
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("token %q returned %d, want 403", bad, rec.Code)
		}
	}
}

// --- path traversal ---------------------------------------------------------

// The id is an index key the service produced itself, never a path from the
// page. This proves that stays true even when the client insists otherwise.
func TestDocIDCannotEscapeTheArchive(t *testing.T) {
	s, _, _ := newTestServer(t, "")

	hostile := []string{
		"../../../../windows/win.ini",
		"..%2f..%2f..%2fwindows%2fwin.ini",
		"....//....//windows/win.ini",
		"/windows/win.ini",
		"C:/windows/win.ini",
		"C:%5Cwindows%5Cwin.ini",
		"..\\..\\..\\windows\\win.ini",
		"111/222/../../../../windows/win.ini",
		"\\\\127.0.0.1\\C$\\windows\\win.ini",
		"111/222/Contract_111222.docx/../../../secret",
		"%00",
		"con", "nul", "PRN", // Windows device names
	}
	for _, id := range hostile {
		for _, mode := range []string{"", "text", "download"} {
			rec := do(t, s, "GET", "/api/doc?mode="+mode+"&id="+id)
			if rec.Code != http.StatusNotFound {
				t.Errorf("id %q mode %q returned %d, want 404", id, mode, rec.Code)
			}
			if body := rec.Body.String(); strings.Contains(strings.ToLower(body), "[fonts]") {
				t.Fatalf("id %q served the contents of a system file", id)
			}
		}
	}
}

func TestPathOfRejectsUnknownIDs(t *testing.T) {
	_, ix, _ := newTestServer(t, "")
	for _, id := range []string{"", "..", "../x", "does/not/exist.docx", "C:/windows/win.ini"} {
		if _, ok := ix.PathOf(id); ok {
			t.Errorf("PathOf(%q) resolved, want refusal", id)
		}
	}
}

// --- response headers -------------------------------------------------------

// --- setup endpoint ---------------------------------------------------------

// The password is the one piece of data here that must never come back out.
func TestConnectNeverEchoesThePassword(t *testing.T) {
	s, _, root := newTestServer(t, "")
	s.OnConnect(func(string) error { return nil })

	const secret = "hunter2-do-not-echo"
	body := `{"path":` + jsonString(root) + `,"user":"someone","password":"` + secret + `","remember":false}`

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/connect", strings.NewReader(body))
	s.Handler().ServeHTTP(rec, req)

	if strings.Contains(rec.Body.String(), secret) {
		t.Fatal("the response repeated the password")
	}
}

func TestConnectRejectsRubbish(t *testing.T) {
	s, _, _ := newTestServer(t, "")
	s.OnConnect(func(string) error { return nil })

	cases := []string{
		``,
		`{`,
		`not json at all`,
		`{"path":""}`,
		`{"path":"   "}`,
		`[]`,
		`{"path":123}`,
	}
	for _, body := range cases {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/connect", strings.NewReader(body))
		s.Handler().ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("body %q returned status %d; the handler should answer, not fail", body, rec.Code)
		}
		var out struct {
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Errorf("body %q produced unreadable JSON: %v", body, err)
			continue
		}
		if out.OK {
			t.Errorf("body %q was accepted, want refusal", body)
		}
		if out.Error == "" {
			t.Errorf("body %q was refused without saying why", body)
		}
	}
}

// A huge body must not be read into memory in full.
func TestConnectBodyIsCapped(t *testing.T) {
	s, _, _ := newTestServer(t, "")
	s.OnConnect(func(string) error { return nil })

	huge := `{"path":"` + strings.Repeat("A", 1<<20) + `"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/connect", strings.NewReader(huge))
	s.Handler().ServeHTTP(rec, req)

	var out struct{ OK bool }
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.OK {
		t.Error("a one-megabyte path was accepted")
	}
}

// --- serving ----------------------------------------------------------------

// The UI is the only thing served from the root; there is no static file tree
// to walk into.
func TestOnlyRootServesHTML(t *testing.T) {
	s, _, _ := newTestServer(t, "")
	for _, p := range []string{"/index.html", "/ui.html", "/static/x", "/vendor/", "/vendor/x.js"} {
		if got := do(t, s, "GET", p).Code; got != http.StatusNotFound {
			t.Errorf("GET %s returned %d, want 404", p, got)
		}
	}

	// The document pane's client-side renderer and the app's own icon are the
	// only other things served unauthenticated: static library code and a
	// picture, neither of which is archive data the token is there to guard.
	for _, p := range []string{"/favicon.ico", "/vendor/docx-preview.min.js", "/vendor/jszip.min.js"} {
		if got := do(t, s, "GET", p).Code; got != http.StatusOK {
			t.Errorf("GET %s returned %d, want 200", p, got)
		}
	}

	// A dot-segment is normalised by the router and redirected, never served.
	// What matters is that the redirect target is not the UI and carries no
	// content of its own.
	rec := do(t, s, "GET", "/../server.go")
	if rec.Code == http.StatusOK {
		t.Errorf("GET /../server.go was served with status 200")
	}
	if loc := rec.Header().Get("Location"); loc != "" && loc == "/" {
		t.Errorf("a dot-segment path redirected to the UI: %q", loc)
	}
	// The root is the archive's address, by redirect.
	if rec := do(t, s, "GET", "/"); rec.Code != http.StatusFound || rec.Header().Get("Location") != "/documents" {
		t.Errorf("GET / answered %d to %q, want a redirect to /documents", rec.Code, rec.Header().Get("Location"))
	}
	if got := do(t, s, "GET", "/documents").Code; got != http.StatusOK {
		t.Errorf("GET /documents returned %d, want 200", got)
	}
}

// Document text ends up in JSON; anything that could break out of it would end
// up as markup in the page.
func TestDocumentTextIsSafelyEncoded(t *testing.T) {
	root := tempDir(t)
	dir := filepath.Join(root, "111", "222")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	payload := `</script><script>alert(1)</script> & "quotes" 'and' <b>tags</b>`
	if err := os.WriteFile(filepath.Join(dir, "Contract_111222.docx"),
		minimalDocx(t, payload), 0o644); err != nil {
		t.Fatal(err)
	}

	ix := NewIndex(root)
	if _, err := ix.Scan(); err != nil {
		t.Fatal(err)
	}
	s := NewServer(ix, "")

	rec := do(t, s, "GET", "/api/doc?mode=text&id=111/222/Contract_111222.docx")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var out struct{ Text string }
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("response was not valid JSON: %v", err)
	}
	if !strings.Contains(out.Text, "alert(1)") {
		t.Error("the document text was lost")
	}
	// The raw response must not contain an unescaped closing script tag.
	if strings.Contains(rec.Body.String(), "</script>") {
		t.Error("a closing script tag survived into the response body verbatim")
	}
}

// --- concurrency ------------------------------------------------------------

// Run with -race: searching while the archive is rescanned is the normal state
// of this program, since a timer rescans every thirty seconds.
func TestConcurrentSearchAndRescan(t *testing.T) {
	s, ix, _ := newTestServer(t, "")

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 25; j++ {
				do(t, s, "GET", "/api/search?q=111222")
				do(t, s, "GET", "/api/tree")
				do(t, s, "GET", "/api/status")
			}
		}()
	}
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				if _, err := ix.Scan(); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// mkJunction makes a directory junction at link pointing at target -- what any
// account that can write to a folder may do on Windows, with no special
// privilege (unlike a symbolic link).
func mkJunction(t *testing.T, link, target string) bool {
	t.Helper()
	if runtime.GOOS != "windows" {
		return false
	}
	out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput()
	if err != nil {
		t.Logf("cannot make a junction here: %v %s", err, out)
		return false
	}
	return true
}

// A link that appears after the scan, reported by the change watcher, is not
// indexed either.
func TestTheWatcherDoesNotFollowALink(t *testing.T) {
	root := buildArchive(t)
	secret := filepath.Join(tempDir(t), "secret.docx")
	if err := os.WriteFile(secret, []byte("OUTSIDE-THE-ARCHIVE-MARKER"), 0o600); err != nil {
		t.Fatal(err)
	}
	ix := NewIndex(root)
	if _, err := ix.Scan(); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "late-link.docx")
	if err := os.Symlink(secret, link); err != nil {
		t.Skipf("cannot create a symlink here (%v)", err)
	}
	if _, err := ix.ApplyChanges([]string{link}); err != nil {
		t.Fatal(err)
	}
	if _, ok := ix.Doc("late-link.docx"); ok {
		t.Error("the watcher indexed a symbolic link")
	}
}

// plainUnder sees a link at any level below the root, not only the last one.
func TestPlainUnderSeesEveryLinkOnTheWay(t *testing.T) {
	root := tempDir(t)
	if err := os.MkdirAll(filepath.Join(root, "a", "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(root, "a", "b", "doc.docx")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if fi, ok := plainUnder(root, f); !ok || !fi.Mode().IsRegular() {
		t.Errorf("a plain file: %v %v", fi, ok)
	}
	for name, p := range map[string]string{
		"the root itself": root,
		"outside":         filepath.Join(root, "..", "x.docx"),
		"missing":         filepath.Join(root, "a", "missing.docx"),
		"a file as a dir": filepath.Join(f, "x.docx"),
	} {
		if _, ok := plainUnder(root, p); ok {
			t.Errorf("%s: %q passed", name, p)
		}
	}
	outside := tempDir(t)
	if err := os.WriteFile(filepath.Join(outside, "doc.docx"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if mkJunction(t, filepath.Join(root, "a", "j"), outside) {
		if _, ok := plainUnder(root, filepath.Join(root, "a", "j", "doc.docx")); ok {
			t.Error("a file behind a junction in a middle folder passed")
		}
	}
	if err := os.Symlink(outside, filepath.Join(root, "a", "s")); err == nil {
		if _, ok := plainUnder(root, filepath.Join(root, "a", "s", "doc.docx")); ok {
			t.Error("a file behind a directory symlink in a middle folder passed")
		}
	}
}
