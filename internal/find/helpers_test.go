package find

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/Schayan-nk/docfind-documents/internal/auth"
	"github.com/Schayan-nk/docfind-documents/internal/pgstore"
)

// Helpers shared by this package's tests.

func asUser(t *testing.T, s *Server, method, target, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, target, nil)
	} else {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		r.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: token})
	}
	// The mutating routes check the origin of a state-changing request, the
	// same as every other POST in this application.
	r.Header.Set("Origin", "http://example.test")
	r.Host = "example.test"
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, r)
	return rec
}

func getAsset(t *testing.T, s *Server, path, acceptEncoding, ifNoneMatch string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("GET", path, nil)
	if acceptEncoding != "" {
		r.Header.Set("Accept-Encoding", acceptEncoding)
	}
	if ifNoneMatch != "" {
		r.Header.Set("If-None-Match", ifNoneMatch)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, r)
	return rec
}

// memSettings is the settings table without a database.
type memSettings struct {
	mu   sync.Mutex
	rows map[string][]byte
}

func (m *memSettings) Get(_ context.Context, key string, v any) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	raw, ok := m.rows[key]
	if !ok {
		return pgstore.ErrNoSetting
	}
	return json.Unmarshal(raw, v)
}

func (m *memSettings) Set(_ context.Context, key string, v any, _ string) error {
	raw, err := json.Marshal(v)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows[key] = raw
	return err
}

// signFor mints a token for an account.
//
// The version is read from the stored user rather than passed as zero: a
// token carrying the wrong version is refused, which is the revocation
// mechanism doing its job and would read here as a broken test.
func signFor(t *testing.T, signer *auth.Signer, users *auth.Store, name string) string {
	t.Helper()
	u, err := users.Get(name)
	if err != nil {
		t.Fatalf("no such account %s: %v", name, err)
	}
	tok, _, err := signer.SignSession(name, u.TokenVersion, "")
	if err != nil {
		t.Fatalf("signing for %s: %v", name, err)
	}
	return tok
}

// identityBox seals nothing; the mailbox tests are about hosts, not secrets.
type identityBox struct{}

// oauthEngine stands in for the engine's OAuth server and REST API: the
// shapes of ProcessMaker 3.5.8 (/<ws>/oauth2/token, /api/1.0/<ws>/...), a new
// refresh token on every renewal and the old one spent.
type oauthEngine struct {
	mu      sync.Mutex
	srv     *httptest.Server
	live    map[string]bool   // access tokens the API accepts
	refresh map[string]string // unspent refresh token -> its number
	next    int
	forms   []url.Values
	bearers []string
	revoked bool
	// badClient answers the token endpoint as for a wrong client secret.
	badClient bool
	// The designer API (pmdesign_test.go): canned replies and statuses by
	// "METHOD /project/...", and every call made with its body.
	design       map[string]string
	designStatus map[string]int
	designCalls  []string
	designBodies []string
}

func (e *oauthEngine) lastForm() url.Values {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.forms) == 0 {
		return nil
	}
	return e.forms[len(e.forms)-1]
}

func (e *oauthEngine) lastBearer() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.bearers) == 0 {
		return ""
	}
	return e.bearers[len(e.bearers)-1]
}

// writes counts the calls that reached the engine other than reads.
func (e *oauthEngine) writes() int {
	calls, _ := e.designLog()
	n := 0
	for _, c := range calls {
		if !strings.HasPrefix(c, "GET ") {
			n++
		}
	}
	return n
}

func (e *oauthEngine) designLog() ([]string, []string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.designCalls...), append([]string(nil), e.designBodies...)
}

func (e *oauthEngine) setDesign(key, reply string, status int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.design[key] = reply
	if status != 0 {
		e.designStatus[key] = status
	}
}
