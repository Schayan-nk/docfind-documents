package find

import (
	"net/http"
	"strings"
	"testing"
)

// The biggest bodies in the app are embedded assets: 205 KB of Chart.js, the
// 173 KB jszip/docx-preview pair, and 122 KB of dashboard markup. Their bytes
// cannot change until the binary does, which makes them the best possible 304
// candidates -- and until now most of them carried no validator at all, so
// every reload re-transferred them in full.

// Routes that must all answer with a validator. Every one of these is
// unauthenticated: they are shells and library code, and the data they load
// afterwards is what carries a permission check.
var cachedRoutes = []string{
	"/login",
	"/documents",
	"/settings",
	"/favicon.ico",
	"/vendor/jszip.min.js",
	"/vendor/docx-preview.min.js",
	"/app.css",
	"/nav.js",
}

func TestStaticRoutesCarryAnETagAndRevalidate(t *testing.T) {
	s, _, _ := newTestServer(t, "")

	for _, path := range cachedRoutes {
		t.Run(path, func(t *testing.T) {
			first := getAsset(t, s, path, "", "")
			if first.Code != http.StatusOK {
				t.Fatalf("status %d", first.Code)
			}
			etag := first.Header().Get("ETag")
			if etag == "" {
				t.Fatal("no ETag, so every reload re-transfers the whole body")
			}
			// no-cache means "revalidate", not "do not store" -- the pairing
			// with an ETag is what makes an upgrade visible immediately while
			// an unchanged body still costs nothing.
			if cc := first.Header().Get("Cache-Control"); !strings.Contains(cc, "no-cache") {
				t.Errorf("Cache-Control %q, want no-cache", cc)
			}

			second := getAsset(t, s, path, "", etag)
			if second.Code != http.StatusNotModified {
				t.Errorf("re-request with If-None-Match got %d, want 304", second.Code)
			}
			if second.Body.Len() != 0 {
				t.Errorf("a 304 carried %d bytes of body", second.Body.Len())
			}
		})
	}
}

// The regression: the gzip and identity forms are different bytes, so they may
// not share a tag. They did, which meant a cache holding the compressed body
// under the plain tag could hand it to a client that never asked for gzip.
func TestCompressedAndPlainFormsHaveDifferentETags(t *testing.T) {
	s, _, _ := newTestServer(t, "")

	// A text route large enough to be worth compressing.
	const path = "/vendor/docx-preview.min.js"

	plain := getAsset(t, s, path, "", "")
	gzipped := getAsset(t, s, path, "gzip", "")

	if enc := gzipped.Header().Get("Content-Encoding"); enc != "gzip" {
		t.Fatalf("Content-Encoding %q, want gzip -- the asset is not being precompressed", enc)
	}
	if plain.Header().Get("Content-Encoding") != "" {
		t.Fatal("a request that did not accept gzip got a compressed body")
	}

	pTag, gTag := plain.Header().Get("ETag"), gzipped.Header().Get("ETag")
	if pTag == "" || gTag == "" {
		t.Fatal("one of the two representations has no ETag")
	}
	if pTag == gTag {
		t.Errorf("both representations answer with %s, but they are different bytes", pTag)
	}

	// And each tag must still validate its own representation.
	if rec := getAsset(t, s, path, "gzip", gTag); rec.Code != http.StatusNotModified {
		t.Errorf("the gzip tag did not validate the gzip form: got %d", rec.Code)
	}
	if rec := getAsset(t, s, path, "", pTag); rec.Code != http.StatusNotModified {
		t.Errorf("the plain tag did not validate the plain form: got %d", rec.Code)
	}
	// Cross-validating must NOT produce a 304, or the wrong bytes get reused.
	if rec := getAsset(t, s, path, "", gTag); rec.Code == http.StatusNotModified {
		t.Error("the gzip tag validated a plain request, so a client could be sent gzip it cannot read")
	}
}

// The compression must be the once-per-process kind. If it were re-done per
// request, Chart.js would be gzipped on every cold load -- the exact cost the
// precompressed helper exists to remove.
func TestLargeAssetsAreCompressedAndSmaller(t *testing.T) {
	s, _, _ := newTestServer(t, "")

	for _, path := range []string{"/vendor/docx-preview.min.js", "/documents", "/nav.js"} {
		t.Run(path, func(t *testing.T) {
			plain := getAsset(t, s, path, "", "")
			gzipped := getAsset(t, s, path, "gzip", "")
			if gzipped.Header().Get("Content-Encoding") != "gzip" {
				t.Fatalf("%s was not compressed", path)
			}
			if gzipped.Body.Len() >= plain.Body.Len() {
				t.Errorf("compressed %d bytes vs plain %d", gzipped.Body.Len(), plain.Body.Len())
			}
		})
	}
}

// "GET /" is the catch-all pattern, so turning it into a cached asset must not
// make it answer for paths that do not exist.
func TestRootRouteStillRejectsOtherPaths(t *testing.T) {
	s, _, _ := newTestServer(t, "")
	for _, path := range []string{"/nope", "/does/not/exist"} {
		if rec := getAsset(t, s, path, "", ""); rec.Code != http.StatusNotFound {
			t.Errorf("%s returned %d, want 404", path, rec.Code)
		}
	}
}
