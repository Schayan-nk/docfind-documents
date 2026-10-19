package find

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The app bar computes its gutter from --page-max (app.css .appbar). A page
// that sets it to anything but a width makes that padding invalid, and the
// browser drops it to zero without a word: inbox.html's "none" put the
// header's items on the window's edges.
func TestEveryPageSetsARealPageWidth(t *testing.T) {
	pages, _ := filepath.Glob("*.html")
	decl := regexp.MustCompile(`--page-max\s*:\s*([^;}]+)`)
	for _, p := range pages {
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range decl.FindAllStringSubmatch(string(raw), -1) {
			v := strings.TrimSpace(m[1])
			if v == "none" || v == "auto" || v == "" || v == "initial" || v == "unset" {
				t.Errorf("%s sets --page-max: %s; the app bar needs a width there", p, v)
			}
		}
	}
}
