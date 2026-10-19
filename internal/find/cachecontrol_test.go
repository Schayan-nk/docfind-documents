package find

import (
	"net/http"
	"strings"
	"testing"
)

// A page names its assets with the build version baked into the query
// string -- handlePage did the stamping when the route was built -- and that
// is what lets the asset itself be cached for a year instead of revalidated
// on every load. See versionAssetLinks and isVersionedRequest.
func TestPageLinksCarryTheBuildVersion(t *testing.T) {
	s, _, _ := newTestServer(t, "")

	rec := getAsset(t, s, "/settings", "", "")
	body := rec.Body.String()
	want := `href="/app.css?v=` + assetStamp() + `"`
	if !strings.Contains(body, want) {
		t.Errorf("settings page does not link app.css with the build version: want %q in body", want)
	}
	want = `src="/nav.js?v=` + assetStamp() + `"`
	if !strings.Contains(body, want) {
		t.Errorf("settings page does not link nav.js with the build version: want %q in body", want)
	}
}

// A request for the plain, unversioned path is what every reload before this
// feature sent, and what an old cached page or a hand-typed address still
// sends -- it must keep revalidating rather than being told to cache
// forever, or an upgrade would go unnoticed indefinitely.
func TestUnversionedAssetRequestGetsNoCache(t *testing.T) {
	s, _, _ := newTestServer(t, "")

	rec := getAsset(t, s, "/app.css", "", "")
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-cache") {
		t.Errorf("Cache-Control %q, want no-cache", cc)
	}
	if strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
		t.Error("an unversioned request must not be told the response is immutable")
	}
}

// The versioned URL a page now hands the browser must be answered with a
// year-long, immutable Cache-Control -- that is the entire point of naming
// the build in the query string -- while still carrying an ETag, so a
// request that does show up (a hard reload, a proxy that stripped the
// header) still costs a 304 rather than a full body.
func TestVersionedAssetRequestGetsAYearOfImmutableCache(t *testing.T) {
	s, _, _ := newTestServer(t, "")

	path := "/app.css?v=" + assetStamp()
	first := getAsset(t, s, path, "", "")
	if first.Code != http.StatusOK {
		t.Fatalf("status %d", first.Code)
	}
	cc := first.Header().Get("Cache-Control")
	if !strings.Contains(cc, "immutable") || !strings.Contains(cc, "max-age=31536000") {
		t.Errorf("Cache-Control %q, want public, max-age=31536000, immutable", cc)
	}
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("a versioned asset carried no ETag")
	}

	second := getAsset(t, s, path, "", etag)
	if second.Code != http.StatusNotModified {
		t.Errorf("re-request with If-None-Match got %d, want 304", second.Code)
	}
}

// A version left over from a build this server no longer is must not be
// trusted as immutable: the content behind that path may no longer be what
// the stale link implies, so it falls back to the ordinary revalidated path.
func TestStaleVersionQueryDoesNotGetImmutableCache(t *testing.T) {
	s, _, _ := newTestServer(t, "")

	rec := getAsset(t, s, "/app.css?v=not-a-real-build", "", "")
	if cc := rec.Header().Get("Cache-Control"); strings.Contains(cc, "immutable") {
		t.Errorf("Cache-Control %q, a stale version query must not be treated as immutable", cc)
	}
}

// sw.js is the one file a browser must always be able to notice changed --
// see stampWorker's reasoning -- so it must never be handed a long-lived
// cache lifetime, versioned query or not.
func TestServiceWorkerNeverGetsImmutableCache(t *testing.T) {
	s, _, _ := newTestServer(t, "")

	for _, path := range []string{"/sw.js", "/sw.js?v=" + assetStamp()} {
		rec := getAsset(t, s, path, "", "")
		if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-cache") ||
			strings.Contains(cc, "immutable") {
			t.Errorf("%s: Cache-Control %q, want no-cache and never immutable", path, cc)
		}
	}
}

// The API's own no-store policy, set once in securityHeaders for every
// /api/ path, must survive this change untouched -- this package does not
// duplicate it.
func TestAPIResponsesStayNoStore(t *testing.T) {
	s, _, _ := newTestServer(t, "")

	rec := getAsset(t, s, "/api/status", "", "")
	cc := rec.Header().Get("Cache-Control")
	if !strings.Contains(cc, "no-store") {
		t.Errorf("Cache-Control %q, want no-store", cc)
	}
}
