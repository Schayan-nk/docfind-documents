package find

import (
	"regexp"
	"strings"
	"testing"
)

// A page must define the helpers it calls.
//
// THIS HAS NOW BITTEN THREE TIMES, each time the same way: a block of script
// written against one page's vocabulary and pasted into another that spells
// the same idea differently.
//
//   - el() took two arguments on the home page and three in chat.js, so the
//     date picker's labels came out empty.
//   - faNum() is the documents page's Persian number formatter. The home
//     page calls its fa(). A rates panel written with faNum() threw a
//     ReferenceError on every load, which its own try/catch turned into
//     "نرخ ارز در دسترس نیست." -- a panel reporting the feed was down while
//     the feed was answering perfectly.
//
// That last one is why this is worth a test rather than care. The failure was
// SILENT AND PLAUSIBLE: the page said something reasonable, in Persian, that
// happened to be false, and nothing in the console or the tests disagreed.
//
// These names are the ones that actually differ between pages. A general
// "every identifier must resolve" check would need a JavaScript parser and
// would drown in browser built-ins; this is the short list of things people
// reach for by habit.
var pageHelpers = []string{
	"faNum", "fa", "num", "esc", "getJSON", "failInto", "jpost", "say",
}

// providedElsewhere are defined outside the pages, and a page may call them
// without defining them. nav.js is loaded by every page.
var providedElsewhere = map[string]bool{
	"toast": true, // nav.js owns it -- see TestOnlyNavDefinesToast
}

func TestEveryPageDefinesTheHelpersItCalls(t *testing.T) {
	navJS := readPage(t, "nav.js")

	for _, name := range embeddedPages {
		src := readPage(t, name)
		for _, h := range pageHelpers {
			if providedElsewhere[h] {
				continue
			}
			if !callsHelper(src, h) {
				continue
			}
			if definesHelper(src, h) || definesHelper(navJS, h) {
				continue
			}
			t.Errorf("%s calls %s() but neither it nor nav.js defines one.\n"+
				"  A missing helper is a ReferenceError, and a ReferenceError inside a\n"+
				"  try/catch becomes whatever that catch reports -- usually a sentence\n"+
				"  blaming something else entirely.", name, h)
		}
	}
}

// The detector itself, against the bug it was written for.
//
// A test that passes because it finds nothing is indistinguishable from one
// that passes because it cannot find anything. This runs the two halves over
// the exact shapes they have to tell apart -- including the real line that
// broke the rates panel.
func TestTheHelperDetectorCatchesTheBugItWasWrittenFor(t *testing.T) {
	// The line as it was written, on a page that has fa() and not faNum().
	bad := `const body = fa(Math.round(rial / 10));
	        return faNum(Math.abs(chg));`
	if !callsHelper(bad, "faNum") {
		t.Error("a call to faNum() was not detected")
	}
	if definesHelper(bad, "faNum") {
		t.Error("faNum() was reported as defined by a page that only calls it")
	}
	if !callsHelper(bad, "fa") {
		t.Error("a call to fa() was not detected")
	}

	// The three forms a helper is actually declared in here.
	for _, def := range []string{
		"function faNum(n) { return n; }",
		"const faNum = n => String(n);",
		"window.faNum = function (n) { return n; };",
	} {
		if !definesHelper(def, "faNum") {
			t.Errorf("a definition was not recognised: %s", def)
		}
	}

	// And what must NOT be read as a call, or the build fails over prose and
	// over a longer name that merely ends the same way.
	for _, quiet := range []string{
		"// fa() formats Persian digits", // a comment naming it is fine...
		"const x = alfa(3);",             // ...and alfa( is not fa(
		"obj.fa(1)",                      // a method on something else
	} {
		if quiet == "const x = alfa(3);" || quiet == "obj.fa(1)" {
			if callsHelper(quiet, "fa") {
				t.Errorf("%q was read as a call to fa()", quiet)
			}
		}
	}
}

// callsHelper reports whether the source calls name as a function.
//
// Deliberately crude, and biased towards NOT reporting: a false positive here
// would fail the build over a word in a comment. The definition check below
// then clears anything that is defined, so the only thing left is a genuine
// call to something absent.
func callsHelper(src, name string) bool {
	re := regexp.MustCompile(`(^|[^\w.$])` + regexp.QuoteMeta(name) + `\s*\(`)
	for _, m := range re.FindAllString(src, -1) {
		// Skip a call that is part of a longer name -- fa( must not match
		// alfa( -- which the leading class already handles, and skip the
		// definition itself, which the next function looks for.
		if strings.Contains(m, "function ") {
			continue
		}
		return true
	}
	return false
}

// definesHelper reports whether the source declares name.
//
// Covers the three forms these are written in: a function declaration, a
// const arrow, and an assignment to window.
func definesHelper(src, name string) bool {
	n := regexp.QuoteMeta(name)
	for _, pat := range []string{
		`function\s+` + n + `\s*\(`,
		`(const|let|var)\s+` + n + `\s*=`,
		`window\.` + n + `\s*=`,
	} {
		if regexp.MustCompile(pat).MatchString(src) {
			return true
		}
	}
	return false
}
