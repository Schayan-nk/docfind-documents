package find

import (
	"os"
	"regexp"
	"strings"
	"testing"
	"unicode"
)

// The Persian pages print the server's own error sentence in preference to
// their local fallback, so an English string on any of these endpoints reaches
// a Persian-speaking user verbatim: a locked-out manager read "too many
// attempts, try again later" on an otherwise fully Persian login screen.
//
// This is a lint, not a behaviour test. It reads the source because that is
// where the strings are, and exercising every error branch through HTTP would
// cover fewer of them while proving less.
//
// /api/doc and the other document routes are deliberately absent: ui.html is
// English on purpose, and its errors should stay English.

var errorBody = regexp.MustCompile(`"error":"([^"]*)"`)

func hasPersian(s string) bool {
	for _, r := range s {
		if unicode.In(r, unicode.Arabic) {
			return true
		}
	}
	return false
}

func TestErrorSentencesOnPersianEndpointsArePersian(t *testing.T) {
	files := []string{
		"authroutes.go",
		"docsroutes.go",
		"../auth/middleware.go",
	}

	checked := 0
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("reading %s: %v", f, err)
		}
		for _, m := range errorBody.FindAllStringSubmatch(string(src), -1) {
			msg := m[1]
			// Interpolated bodies are built elsewhere; there is no literal
			// sentence here to judge.
			if strings.TrimSpace(msg) == "" || strings.Contains(msg, "%") {
				continue
			}
			checked++
			if !hasPersian(msg) {
				t.Errorf("%s: error sentence is not Persian: %q", f, msg)
			}
		}
	}
	// If the regexp stops matching, the test would pass by checking nothing.
	// The three files carry a little over twenty; a scan that finds far fewer
	// has stopped matching them.
	if checked < 15 {
		t.Fatalf("only %d error sentences found across %d files; the scan is no longer finding them", checked, len(files))
	}
}

// The specific regression: store sentinels must not be forwarded to the
// browser as Go error text.
func TestUserErrorsDoNotLeakGoErrorText(t *testing.T) {
	src, err := os.ReadFile("authroutes.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(src), `"error": err.Error()`) ||
		strings.Contains(string(src), `"error": err.Error(),`) {
		t.Error("an error body forwards err.Error(); store errors carry an 'auth: ' prefix and are written in English")
	}
}
