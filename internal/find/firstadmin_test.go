package find

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Schayan-nk/docfind-documents/internal/auth"
)

// /api/first-admin is the one route where an unauthenticated caller can create
// an account, so its "from the server itself" gate has to mean the connection
// and not a header.
//
// Guard.ClientIP deliberately honours the left-most X-Forwarded-For entry when
// -trust-proxy-headers is on, which is right for logging and throttling and
// wrong for a trust decision: judging the gate by it let a remote caller take
// ownership of an empty archive by sending one header.

func emptyAccountServer(t *testing.T, trustProxyHeaders bool) *Server {
	t.Helper()
	s, _, _ := newTestServer(t, "")
	// A path that does not exist yields an empty store, which is the state
	// that opens this route at all.
	store, err := auth.OpenStore(filepath.Join(tempDir(t), "users.json"))
	if err != nil {
		t.Fatal(err)
	}
	s.UseAccounts(store, auth.NewSigner([]byte("test-signing-key-32-bytes-long!!"), time.Hour), trustProxyHeaders)
	if !s.NeedsFirstAdmin() {
		t.Fatal("the store should start empty, so the route should be open")
	}
	return s
}

const firstAdminBody = `{"username":"attacker","display_name":"A","password":"correct-horse-battery"}`

func postFirstAdmin(t *testing.T, s *Server, remoteAddr string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("POST", "/api/first-admin", strings.NewReader(firstAdminBody))
	r.RemoteAddr = remoteAddr
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, r)
	return rec
}

// The regression. Trusting the proxy header is the configuration in which the
// old code believed the forgery, so the test runs with it switched on.
func TestFirstAdminCannotBeClaimedWithAForgedForwardedHeader(t *testing.T) {
	for _, forged := range []string{"127.0.0.1", "::1", "localhost", "127.0.0.1, 203.0.113.7"} {
		t.Run(forged, func(t *testing.T) {
			s := emptyAccountServer(t, true)
			rec := postFirstAdmin(t, s, "203.0.113.7:44321", map[string]string{"X-Forwarded-For": forged})

			if rec.Code != http.StatusForbidden {
				t.Errorf("status %d, want 403: a forged %q claimed the first administrator (%s)",
					rec.Code, forged, strings.TrimSpace(rec.Body.String()))
			}
			// The status code is the symptom; no account existing is the point.
			if !s.NeedsFirstAdmin() {
				t.Error("an administrator was created from a remote connection")
			}
		})
	}
}

// X-Real-IP is the other header a proxy commonly sets.
func TestFirstAdminIgnoresOtherClientAddressHeaders(t *testing.T) {
	s := emptyAccountServer(t, true)
	rec := postFirstAdmin(t, s, "203.0.113.7:44321", map[string]string{
		"X-Real-IP":        "127.0.0.1",
		"X-Forwarded-Host": "localhost",
		"Forwarded":        "for=127.0.0.1",
	})
	if rec.Code != http.StatusForbidden {
		t.Errorf("status %d, want 403 (%s)", rec.Code, strings.TrimSpace(rec.Body.String()))
	}
	if !s.NeedsFirstAdmin() {
		t.Error("an administrator was created from a remote connection")
	}
}

// The gate must still admit the case it exists for, or the app cannot be set
// up at all -- a fix that locks everyone out is not a fix.
func TestFirstAdminStillWorksOverLoopback(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:51000", "[::1]:51000"} {
		t.Run(addr, func(t *testing.T) {
			s := emptyAccountServer(t, true)
			rec := postFirstAdmin(t, s, addr, nil)
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d, want 200: loopback setup was refused (%s)",
					rec.Code, strings.TrimSpace(rec.Body.String()))
			}
			if s.NeedsFirstAdmin() {
				t.Error("the request succeeded but no administrator was created")
			}
		})
	}
}

// Once an administrator exists the route must close, whatever the caller
// claims to be: otherwise a second caller could overwrite ownership.
func TestFirstAdminClosesAfterTheFirstOne(t *testing.T) {
	s := emptyAccountServer(t, true)
	if rec := postFirstAdmin(t, s, "127.0.0.1:51000", nil); rec.Code != http.StatusOK {
		t.Fatalf("setup failed: %d %s", rec.Code, strings.TrimSpace(rec.Body.String()))
	}
	rec := postFirstAdmin(t, s, "127.0.0.1:51000", nil)
	if rec.Code != http.StatusConflict {
		t.Errorf("status %d, want 409 once an administrator exists", rec.Code)
	}
}
