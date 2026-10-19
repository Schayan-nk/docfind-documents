package find

import (
	"regexp"
	"strings"
	"testing"
)

// A reference to a symbol the sprite does not have renders as nothing at all:
// no error, no console warning, just a gap where the icon was. That makes it
// the exact class of mistake worth a test rather than an eye.
//
// It has already happened once. nav.js's svg() helper was changed to take a
// symbol name instead of path data, and two callers still passed path data --
// producing href="#i-<path d=" and two invisible icons in the mobile menu.
// Caught by querying getBBox() in a real browser; this catches it in CI.

var iconRef = regexp.MustCompile(`href="#i-([^"]*)"`)

// A page's own icon() helper builds the same reference at runtime.
//
// The scan above only sees icons written as literal markup, so an icon chosen
// in JavaScript went unchecked -- and three did: the inbox asked for "shield",
// "archive" and "edit", none of which is in the sprite, and every one of them
// rendered as a silent gap beside a filter nobody could see. Found by reading
// the sprite list, not by looking at the page, which is exactly why it wants a
// test rather than an eye.
var spriteName = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// The injected sprite must take up no space.
//
// It shipped once relying on the hidden attribute, which does not hide an
// <svg> here: it computed to display:inline and rendered at its intrinsic
// 150px, pushing the header of every page down by 160px. Measured in the
// browser, on the deployed build.
func TestInjectedSpriteIsNotRendered(t *testing.T) {
	page := injectAssets([]byte("<html><body>\n<header>x</header>\n</body></html>"))
	got := string(page)

	if !strings.Contains(got, `<svg style="display:none"`) {
		t.Error("the sprite is not hidden with an inline display:none")
	}
	// The attribute alone is what failed; an inline style does not depend on
	// the stylesheet having arrived either.
	if strings.Contains(got, "<svg hidden") {
		t.Error("the sprite relies on the hidden attribute, which does not hide an <svg>")
	}
	// The loader must be inert too: a <template> is not rendered, and the live
	// copy only appears where a page asks for it by marker.
	if !strings.Contains(got, `<template id="ldTpl">`) {
		t.Error("the loader was not injected as an inert template")
	}
	if strings.Count(got, `class="ld-w"`) != 1 {
		t.Errorf("expected exactly one loader copy (inside the template), found %d",
			strings.Count(got, `class="ld-w"`))
	}
}

// The sprite has to land inside <body> however that tag is written.
//
// This is a regression test for a real one. injectAfterBody matched the
// literal string "<body>", so the first page with an attribute on it --
// <body class="home-page"> -- got no sprite and no loader at all, and every
// icon on it rendered as a blank: a <use> pointing at a symbol that is not
// there produces no error and no console warning, only a gap.
func TestTheSpriteIsInjectedWhateverTheBodyTagLooksLike(t *testing.T) {
	for _, open := range []string{
		"<body>",
		`<body class="home-page">`,
		"<body\n  class=\"home-page\"\n  data-x=\"1\">",
		`<body id="x" class="y">`,
	} {
		page := injectAssets([]byte("<html>" + open + "\n<header>x</header>\n</body></html>"))
		got := string(page)
		if !strings.Contains(got, `<symbol id="i-search"`) {
			t.Errorf("no sprite injected for %q", open)
		}
		if !strings.Contains(got, `<template id="ldTpl">`) {
			t.Errorf("no loader injected for %q", open)
		}
		// And it must go INSIDE the tag, not before it -- markup between
		// </head> and <body> is moved by the parser and would land outside.
		if i, j := strings.Index(got, open[:len(open)-1]), strings.Index(got, "<svg"); i > j {
			t.Errorf("the sprite was injected before the body tag for %q", open)
		}
	}

	// Something that merely starts with the same letters is not a body tag.
	for _, open := range []string{"<bodyguard>", "<bodyish class='x'>"} {
		page := injectAssets([]byte("<html><head></head>" + open + "</html>"))
		if strings.Contains(string(page), "<symbol") {
			t.Errorf("injected into %q, which is not a body tag", open)
		}
	}
}
