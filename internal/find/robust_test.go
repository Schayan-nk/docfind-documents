package find

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A document archive is not a curated data set. It accumulates whatever the
// generator, Word and the people using it leave behind: half-written files,
// files someone still has open, names nobody planned for. None of that may stop
// the application from starting or from finding everything else.

func writeDoc(t *testing.T, root, rel string, body []byte) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, body, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScanWithoutRootIsAnError(t *testing.T) {
	ix := NewIndex("")
	if _, err := ix.Scan(); err == nil {
		t.Error("scanning with no folder configured should fail, not pretend to work")
	}
}

func TestScanOfMissingFolder(t *testing.T) {
	ix := NewIndex(filepath.Join(tempDir(t), "does-not-exist"))
	if _, err := ix.Scan(); err != nil {
		// WalkDir reports the missing root; either behaviour is acceptable so
		// long as it does not panic and the index stays empty.
		t.Logf("scan reported: %v", err)
	}
	if ix.Count() != 0 {
		t.Errorf("a missing folder produced %d documents", ix.Count())
	}
}

// --- query handling ---------------------------------------------------------

func TestSearchAPIClampsLimit(t *testing.T) {
	s, _, _ := newTestServer(t, "")
	for _, raw := range []string{"-1", "0", "999999", "abc", ""} {
		rec := do(t, s, "GET", "/api/search?limit="+raw)
		if rec.Code != http.StatusOK {
			t.Errorf("limit=%q returned %d", raw, rec.Code)
			continue
		}
		var out struct{ Count int }
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Errorf("limit=%q produced unreadable JSON: %v", raw, err)
		}
	}
}

func TestDocEndpointHandlesMissingParameters(t *testing.T) {
	s, _, _ := newTestServer(t, "")
	for _, target := range []string{
		"/api/doc",
		"/api/doc?id=",
		"/api/doc?mode=text",
		"/api/doc?id=&mode=download",
	} {
		method := "GET"
		if strings.Contains(target, "/open") {
			method = "POST"
		}
		rec := do(t, s, method, target)
		if rec.Code != http.StatusNotFound && rec.Code != http.StatusBadRequest {
			t.Errorf("%s %s returned %d, want 404 or 400", method, target, rec.Code)
		}
	}
}

// --- persistence ------------------------------------------------------------

func TestCacheFromADifferentFolderIsRejected(t *testing.T) {
	a := NewIndex(buildArchive(t))
	if _, err := a.Scan(); err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(tempDir(t), "idx.gob")
	if err := a.Save(cache); err != nil {
		t.Fatal(err)
	}

	b := NewIndex(buildArchive(t)) // a different folder
	if err := b.Load(cache); err == nil {
		t.Error("a cache belonging to another folder was accepted")
	}
}

func TestLoadOfRubbishCacheIsRefused(t *testing.T) {
	dir := tempDir(t)
	cache := filepath.Join(dir, "idx.gob")
	if err := os.WriteFile(cache, []byte("this is not gob data"), 0o644); err != nil {
		t.Fatal(err)
	}
	ix := NewIndex(dir)
	if err := ix.Load(cache); err == nil {
		t.Error("a corrupt cache file was accepted")
	}
	if ix.Count() != 0 {
		t.Error("a corrupt cache left documents behind")
	}
}

// --- consistency ------------------------------------------------------------

// --- setup mode -------------------------------------------------------------

func TestSetupModeIsReportedAndCleared(t *testing.T) {
	root := buildArchive(t)
	ix := NewIndex("")
	s := NewServer(ix, "")
	s.NeedsSetup("no folder yet")
	s.OnConnect(func(dir string) error {
		ix.SetRoot(dir)
		_, err := ix.Scan()
		return err
	})

	var st struct {
		NeedsSetup  bool   `json:"needs_setup"`
		SetupReason string `json:"setup_reason"`
	}
	if err := json.Unmarshal(do(t, s, "GET", "/api/status").Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if !st.NeedsSetup || st.SetupReason != "no folder yet" {
		t.Fatalf("setup state not reported: %+v", st)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/connect",
		strings.NewReader(`{"path":`+jsonString(root)+`}`))
	s.Handler().ServeHTTP(rec, req)

	var out struct {
		OK    bool
		Count int
		Error string
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.OK {
		t.Fatalf("connect failed: %s", out.Error)
	}
	if out.Count != 3 {
		t.Errorf("connected but indexed %d documents, want 3", out.Count)
	}

	if err := json.Unmarshal(do(t, s, "GET", "/api/status").Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if st.NeedsSetup {
		t.Error("still in setup mode after a successful connect")
	}
}

// Connecting to a folder that does not exist must report the problem and stay
// in setup mode rather than half-applying the change.
func TestConnectToMissingFolderStaysInSetup(t *testing.T) {
	ix := NewIndex("")
	s := NewServer(ix, "")
	s.NeedsSetup("no folder yet")
	s.OnConnect(func(dir string) error {
		if _, err := os.Stat(dir); err != nil {
			return err
		}
		ix.SetRoot(dir)
		_, err := ix.Scan()
		return err
	})

	missing := filepath.Join(tempDir(t), "nope")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/connect",
		strings.NewReader(`{"path":`+jsonString(missing)+`}`))
	s.Handler().ServeHTTP(rec, req)

	var out struct {
		OK    bool
		Error string
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.OK {
		t.Fatal("a missing folder was accepted")
	}

	var st struct {
		NeedsSetup bool `json:"needs_setup"`
	}
	if err := json.Unmarshal(do(t, s, "GET", "/api/status").Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if !st.NeedsSetup {
		t.Error("left setup mode despite the folder not existing")
	}
}

// Without a handler registered the endpoint must refuse rather than panic.
func TestConnectWithoutHandler(t *testing.T) {
	s := NewServer(NewIndex(""), "")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/connect", strings.NewReader(`{"path":"C:/x"}`))
	s.Handler().ServeHTTP(rec, req)

	var out struct {
		OK    bool
		Error string
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.OK || out.Error == "" {
		t.Error("expected a refusal explaining that nothing can be changed")
	}
}

// --- deep and awkward archives ---------------------------------------------
