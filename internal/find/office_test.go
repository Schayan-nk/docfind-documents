package find

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Schayan-nk/docfind-documents/internal/onlyoffice"
)

// fakeDocs stands in for ONLYOFFICE Docs: it serves an "edited" document
// from its cache, and its editor script, and says what it was asked.
type fakeDocs struct {
	mu     sync.Mutex
	edited []byte
	asked  []*http.Request
}

func (f *fakeDocs) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.asked = append(f.asked, r.Clone(r.Context()))
	edited := f.edited
	f.mu.Unlock()
	switch {
	case strings.HasPrefix(r.URL.Path, "/cache/files/"):
		w.Write(edited)
	case r.URL.Path == "/web-apps/apps/api/documents/api.js":
		w.Header().Set("Content-Security-Policy", "theirs")
		io.WriteString(w, "window.DocsAPI = {};")
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeDocs) last() *http.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.asked[len(f.asked)-1]
}

const officeSecret = "an ONLYOFFICE secret that is long enough"

// The document server is asked by docfind's own handler, as it would be.
func officeGet(s *Server, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", target, nil))
	return rec
}

func officeCallback(t *testing.T, s *Server, link string, cb onlyoffice.Callback, secret string) map[string]int {
	t.Helper()
	tok, _ := onlyoffice.Sign([]byte(secret), cb)
	cb.Token = tok
	body, _ := json.Marshal(cb)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", link, bytes.NewReader(body)))
	var out map[string]int
	json.Unmarshal(rec.Body.Bytes(), &out)
	return out
}
