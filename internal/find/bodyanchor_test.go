package find

import (
	"strings"
	"testing"
)

// The sprite must land in the real <body>, not in text that merely says
// "<body>".
//
// THIS IS THE BUG, WRITTEN DOWN. home.html explains, in a CSS comment inside
// its <style> block, that "an element appended to <body> renders underneath a
// modal's backdrop". A plain search for "<body" finds that sentence several
// thousand bytes before the real element, so the sprite and the loader
// template were injected into a stylesheet in the head -- where a browser
// reads them as CSS text, not markup.
//
// The result: no <symbol> anywhere in the document, every <use href="#i-...">
// resolving to nothing, and every icon on the CEO's landing page blank. Plus
// a stylesheet cut short, because the injected markup ended that comment
// early. None of it produces an error; a <use> pointing at nothing simply
// takes up no space.
//
// It survived every existing icon test, because those check that a reference
// has a matching symbol -- which was true. What was false is that the two
// ended up in the same document.
func TestTheSpriteIsNotInjectedIntoAStyleOrScript(t *testing.T) {
	cases := []struct {
		name string
		page string
	}{{
		name: "a CSS comment mentioning the body element",
		page: `<html><head><style>
/* an element appended to <body> renders underneath a modal's backdrop */
.x { color: red }
</style></head><body class="home-page"><main id="home"></main></body></html>`,
	}, {
		name: "a script with a body tag in a string",
		page: `<html><head><script>var t = "<body>";</script></head>` +
			`<body><main id="m"></main></body></html>`,
	}, {
		name: "an HTML comment mentioning the body element",
		page: `<html><head><!-- put this after <body> --></head>` +
			`<body><main id="m"></main></body></html>`,
	}, {
		name: "a plain page with nothing to trip over",
		page: `<html><head></head><body><main id="m"></main></body></html>`,
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := string(injectAssets([]byte(tc.page)))

			// The head must not have grown a sprite.
			head := out[:strings.Index(out, "</head>")]
			if strings.Contains(head, "<symbol") {
				t.Errorf("the sprite was injected into the head:\n%s", head)
			}
			if strings.Contains(head, "ldTpl") {
				t.Errorf("the loader template was injected into the head:\n%s", head)
			}

			// And the body must have one.
			body := out[strings.Index(out, "</head>"):]
			if !strings.Contains(body, "<symbol id=\"i-") {
				t.Errorf("the sprite is not in the body:\n%s", trim(body))
			}
			if !strings.Contains(body, `id="ldTpl"`) {
				t.Errorf("the loader template is not in the body")
			}
		})
	}
}

// Every page this binary serves must end up with a usable sprite.
//
// The check that matters is not "the sprite is somewhere in the bytes" -- it
// was, throughout the bug above -- but that it is after the head, where a
// browser will parse it as markup and getElementById will find the symbols.
func TestEveryPageGetsItsSpriteInsideTheBody(t *testing.T) {
	for _, name := range embeddedPages {
		src := readPage(t, name)
		if !strings.Contains(src, "<body") {
			continue // the sign-in card and anything else without one
		}
		out := string(injectAssets([]byte(src)))

		i := strings.Index(out, "</head>")
		if i < 0 {
			continue
		}
		if strings.Contains(out[:i], "<symbol") {
			t.Errorf("%s: the sprite landed in the head", name)
		}
		if !strings.Contains(out[i:], `<symbol id="i-`) {
			t.Errorf("%s: no sprite in the body", name)
		}
	}
}

// bodyTagIndex itself, on the shapes that broke it.
func TestBodyTagIndexIgnoresTextThatLooksLikeATag(t *testing.T) {
	cases := []struct {
		name string
		page string
		want string // the markup expected to start at the returned index
	}{
		{"plain", `<body>x`, `<body>x`},
		{"with attributes", `<body class="a">x`, `<body class="a">x`},
		{"self-closing-ish", `<body/>`, `<body/>`},
		{"after a style comment", `<style>/* <body> */</style><body id="r">`, `<body id="r">`},
		{"after a script string", `<script>"<body>"</script><body id="r">`, `<body id="r">`},
		{"after an html comment", `<!-- <body> --><body id="r">`, `<body id="r">`},
		{"uppercase style", `<STYLE>/* <body> */</STYLE><body id="r">`, `<body id="r">`},
		{"not a body element", `<bodyish>`, ""},
		{"nothing at all", `<html></html>`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			i := bodyTagIndex([]byte(tc.page))
			if tc.want == "" {
				if i >= 0 {
					t.Errorf("found a body at %d in %q, want none", i, tc.page)
				}
				return
			}
			if i < 0 {
				t.Fatalf("no body found in %q", tc.page)
			}
			if got := tc.page[i:]; !strings.HasPrefix(got, tc.want) {
				t.Errorf("index %d points at %q, want it to start %q", i, got, tc.want)
			}
		})
	}
}

// An unclosed <script> swallows the rest of the document, which is what a
// browser does too -- so there is no body to find, and injecting anywhere
// would put markup inside that script.
func TestAnUnclosedScriptLeavesThePageAlone(t *testing.T) {
	page := []byte(`<html><head><script>var x = 1;</head><body><main></main></body>`)
	if i := bodyTagIndex(page); i >= 0 {
		t.Errorf("found a body at %d inside an unclosed script", i)
	}
	out := injectAssets(page)
	if strings.Contains(string(out), "<symbol") {
		t.Error("markup was injected into a page whose script never closes")
	}
}

func trim(s string) string {
	if len(s) > 600 {
		return s[:600] + "..."
	}
	return s
}
