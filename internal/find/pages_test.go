package find

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

var embeddedPages = []string{"ui.html", "settings.html", "login.html", "office.html"}

func readPage(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	return string(b)
}

// A native alert() is a modal OS dialog captioned with the server's address.
// On a Persian management page in front of a director that is the worst
// possible way to report "saved" -- and it blocks the page, including any
// automation driving it, until somebody clicks it.
func TestNoPageUsesNativeAlert(t *testing.T) {
	// Matches alert( but not window.alert-free things like role="alert" or a
	// property access such as foo.alert(.
	bare := regexp.MustCompile(`(^|[^\w.])alert\s*\(`)
	for _, p := range embeddedPages {
		src := readPage(t, p)
		for i, line := range strings.Split(src, "\n") {
			if bare.MatchString(line) {
				t.Errorf("%s:%d uses a native alert(): %s", p, i+1, strings.TrimSpace(line))
			}
		}
	}
}

// toast() is defined in nav.js. A page that calls it without loading nav.js
// first throws a ReferenceError at the moment it most needs to report
// something, which is exactly when nobody is watching the console.
func TestPagesThatToastLoadNavFirst(t *testing.T) {
	for _, p := range embeddedPages {
		src := readPage(t, p)
		call := strings.Index(src, "toast(")
		if call < 0 {
			continue
		}
		nav := strings.Index(src, "/nav.js")
		if nav < 0 {
			t.Errorf("%s calls toast() but never loads nav.js", p)
			continue
		}
		if nav > call {
			t.Errorf("%s loads nav.js after its first toast() call", p)
		}
	}
}

// The shared stylesheet has to be linked, or every shared component on the
// page -- .widget, .note, .err, .toast and the tokens they are built from --
// silently falls back to unstyled text.
func TestEveryPageLinksTheSharedStylesheet(t *testing.T) {
	for _, p := range embeddedPages {
		if !strings.Contains(readPage(t, p), "/app.css") {
			t.Errorf("%s does not link /app.css", p)
		}
	}
}

// The toast is defined once. A page carrying its own copy is how the two
// versions drifted apart in the first place.
func TestOnlyNavDefinesToast(t *testing.T) {
	for _, p := range embeddedPages {
		src := readPage(t, p)
		if strings.Contains(src, "function toast(") {
			t.Errorf("%s defines its own toast(); nav.js is the shared one", p)
		}
		if strings.Contains(src, ".toast {") {
			t.Errorf("%s carries its own .toast CSS; app.css is the shared one", p)
		}
	}
}
