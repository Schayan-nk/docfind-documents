package find

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// /static is the one route where a caller-supplied string reaches a filesystem
// path. Everywhere else an id is a key into the index, produced by the walk.
// So the path validation is the part tested hardest.

func TestStaticRelPathRefusesEscapes(t *testing.T) {
	hostile := []string{
		"/static/../server.go",
		"/static/../../go.mod",
		"/static/vendor/../../../go.mod",
		"/static/..",
		"/static/./x.svg",
		"/static//vendor/lucide/sprite.svg",
		`/static/..\server.go`,
		`/static/vendor\lucide\sprite.svg`,
		"/static/C:/Windows/win.ini",
		"/static/.env",
		"/static/vendor/.git/config",
		"/static/",
		"/staticx/foo.svg",
		"/notstatic/foo.svg",
	}
	for _, p := range hostile {
		if rel, ok := staticRelPath(p); ok {
			t.Errorf("staticRelPath(%q) accepted, returning %q", p, rel)
		}
	}

	good := map[string]string{
		"/static/vendor/lucide/sprite.svg":   "vendor/lucide/sprite.svg",
		"/static/vendor/lottie/robot.lottie": "vendor/lottie/robot.lottie",
		"/static/a.png":                      "a.png",
	}
	for p, want := range good {
		rel, ok := staticRelPath(p)
		if !ok || rel != want {
			t.Errorf("staticRelPath(%q) = (%q, %v), want (%q, true)", p, rel, ok, want)
		}
	}
}

func TestWithinDir(t *testing.T) {
	root := filepath.Clean("C:/app/static")
	inside := []string{"C:/app/static/a.svg", "C:/app/static/vendor/b.js"}
	outside := []string{"C:/app/other.txt", "C:/app/static/../x", "C:/"}
	for _, p := range inside {
		if !withinDir(root, filepath.Clean(p)) {
			t.Errorf("withinDir(%q) = false, want true", p)
		}
	}
	for _, p := range outside {
		if withinDir(root, filepath.Clean(p)) {
			t.Errorf("withinDir(%q) = true, want false", p)
		}
	}
}

// The embedded assets must actually be served, with the caching behaviour the
// rest of the asset routes have.
func TestEmbeddedStaticAssetsAreServed(t *testing.T) {
	s, _, _ := newTestServer(t, "")

	cases := []struct{ path, wantType string }{
		{"/static/vendor/lucide/sprite.svg", "image/svg+xml"},
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			rec := getAsset(t, s, c.path, "", "")
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d", rec.Code)
			}
			if got := rec.Header().Get("Content-Type"); got != c.wantType {
				t.Errorf("Content-Type %q, want %q", got, c.wantType)
			}
			etag := rec.Header().Get("ETag")
			if etag == "" {
				t.Fatal("no ETag; every load would re-transfer it")
			}
			if again := getAsset(t, s, c.path, "", etag); again.Code != http.StatusNotModified {
				t.Errorf("re-request got %d, want 304", again.Code)
			}
		})
	}
}

// A traversal attempt must not reach the filesystem even when the overlay is
// configured and the file it names exists.
func TestStaticOverlayCannotEscape(t *testing.T) {
	dir := tempDir(t)
	if err := os.MkdirAll(filepath.Join(dir, "vendor", "lottie"), 0o750); err != nil {
		t.Fatal(err)
	}
	// A file the request will try to reach by climbing out of the overlay.
	secret := filepath.Join(filepath.Dir(dir), "secret.png")
	if err := os.WriteFile(secret, []byte("PRIVATE"), 0o644); err != nil {
		t.Fatal(err)
	}
	// And a legitimate one inside it.
	if err := os.WriteFile(filepath.Join(dir, "vendor", "lottie", "robot-404.lottie"), []byte("PK\x03\x04stub"), 0o644); err != nil {
		t.Fatal(err)
	}

	old := staticOverlay
	staticOverlay = dir
	defer func() { staticOverlay = old }()

	s, _, _ := newTestServer(t, "")

	// The legitimate overlay file is served, which proves the mechanism works
	// and the refusals below are not simply everything failing.
	if rec := getAsset(t, s, "/static/vendor/lottie/robot-404.lottie", "", ""); rec.Code != http.StatusOK {
		t.Fatalf("overlay file not served: status %d", rec.Code)
	}

	for _, p := range []string{
		"/static/../secret.png",
		"/static/vendor/../../secret.png",
		"/static/vendor/lottie/../../../secret.png",
	} {
		rec := getAsset(t, s, p, "", "")
		if rec.Code == http.StatusOK {
			t.Errorf("%s was served: %q", p, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "PRIVATE") {
			t.Errorf("%s leaked the file contents", p)
		}
	}
}

// An extension not on the list is refused from the overlay, because nothing
// reviews what gets dropped into that directory.
func TestStaticOverlayRefusesUnknownTypes(t *testing.T) {
	dir := tempDir(t)
	for _, name := range []string{"evil.html", "evil.svgz", "notes.txt", "run.bat"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	old := staticOverlay
	staticOverlay = dir
	defer func() { staticOverlay = old }()

	s, _, _ := newTestServer(t, "")
	for _, name := range []string{"evil.html", "evil.svgz", "notes.txt", "run.bat"} {
		if rec := getAsset(t, s, "/static/"+name, "", ""); rec.Code == http.StatusOK {
			t.Errorf("/static/%s was served with type %q", name, rec.Header().Get("Content-Type"))
		}
	}
}
