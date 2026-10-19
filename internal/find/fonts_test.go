package find

import (
	"regexp"
	"strings"
	"testing"
)

// A webfont that fails to load does not break anything visibly: the browser
// falls through the stack and the page renders in Tahoma. That is exactly what
// the stylesheet did for years while naming Archivo as its Latin face without
// the font being installed anywhere -- so the one failure mode worth a test is
// a @font-face pointing at a file this binary does not carry.
func TestEveryFontFaceURLIsEmbedded(t *testing.T) {
	css := string(appCSS)
	urls := regexp.MustCompile(`url\("(/static/fonts/[^"]+)"\)`).FindAllStringSubmatch(css, -1)
	if len(urls) == 0 {
		t.Fatal("no @font-face urls found in app.css; the webfonts have been removed " +
			"or the url() form changed, and this test no longer guards anything")
	}
	seen := map[string]bool{}
	for _, m := range urls {
		p := strings.TrimPrefix(m[1], "/")
		if seen[p] {
			continue
		}
		seen[p] = true
		b, err := staticFS.ReadFile(p)
		if err != nil {
			t.Errorf("app.css asks for %s, which is not embedded: %v", m[1], err)
			continue
		}
		// woff2 files begin with the ASCII signature "wOF2". A truncated or
		// HTML-error-page download would be served with a font MIME type and
		// silently render as nothing.
		if len(b) < 4 || string(b[:4]) != "wOF2" {
			t.Errorf("%s is not a WOFF2 file (first bytes %q); a download that "+
				"returned an error page would look exactly like this", m[1], safePrefix(b))
		}
		if len(b) < 5000 {
			t.Errorf("%s is only %d bytes, which is too small to be a real font", m[1], len(b))
		}
	}
}

// Both families have to be named, and Archivo has to come first in each stack:
// its faces carry a unicode-range covering only Latin, so it is the ordering
// that makes a mixed Persian-and-Latin sentence take Latin from Archivo and
// Persian from Vazirmatn. Reversed, Vazirmatn claims everything and the Latin
// silently stops being Archivo.
func TestFontStacksPutArchivoBeforeVazirmatn(t *testing.T) {
	css := string(appCSS)
	for _, v := range []string{"--font:", "--font-display:"} {
		i := strings.Index(css, v)
		if i < 0 {
			t.Errorf("%s is gone from app.css", v)
			continue
		}
		decl := css[i:]
		if end := strings.Index(decl, ";"); end > 0 {
			decl = decl[:end]
		}
		a, vz := strings.Index(decl, "Archivo"), strings.Index(decl, "Vazirmatn")
		if a < 0 {
			t.Errorf("%s does not name Archivo", v)
		}
		if vz < 0 {
			t.Errorf("%s does not name Vazirmatn", v)
		}
		if a >= 0 && vz >= 0 && a > vz {
			t.Errorf("%s lists Vazirmatn before Archivo, so Latin text will be "+
				"rendered by Vazirmatn rather than Archivo: %s", v, decl)
		}
	}
	// Persian titles are Vazirmatn Bold, so the heading rule must ask for 700.
	if !regexp.MustCompile(`h1, h2, h3, h4 \{[^}]*font-weight: 700`).MatchString(css) {
		t.Error("headings no longer ask for weight 700, so titles render at text weight " +
			"and the page loses its hierarchy")
	}
}

func safePrefix(b []byte) string {
	if len(b) > 12 {
		b = b[:12]
	}
	return string(b)
}
