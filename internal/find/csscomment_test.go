package find

import (
	"strings"
	"testing"
)

// styleBlocks returns the contents of each <style> element in a page.
func styleBlocks(page string) []string {
	var out []string
	rest := page
	for {
		i := strings.Index(rest, "<style")
		if i < 0 {
			return out
		}
		rest = rest[i:]
		open := strings.Index(rest, ">")
		if open < 0 {
			return out
		}
		end := strings.Index(rest, "</style>")
		if end < 0 {
			return out
		}
		out = append(out, rest[open+1:end])
		rest = rest[end+len("</style>"):]
	}
}

// The documents page's folder tree reads right-to-left, and says so in
// Persian.
//
// The page pins identifiers left-to-right on purpose -- reference numbers,
// file names, sizes and dates, so "111222-156.docx" does not come back with
// the extension on the wrong side. That rule was applied one selector too
// wide: a section HEADING is prose, not an identifier, and pinned it sat
// against the wrong edge of a right-to-left panel.
func TestTheFolderTreeReadsRightToLeft(t *testing.T) {
	page := string(uiHTML)

	// The headings are no longer pinned, and are no longer English.
	if strings.Contains(page, ".row-item, .nav-sect, #count,") {
		t.Error("the folder tree's section headings are still pinned left-to-right, " +
			"which puts them against the wrong edge of a right-to-left panel")
	}
	for _, want := range []string{"آرشیو", "بر اساس شمارهٔ پروندهٔ", "همهٔ اسناد"} {
		if !strings.Contains(page, strings.TrimSuffix(want, "ٔ")) {
			t.Errorf("the folder tree is missing the Persian string %q", want)
		}
	}
	if strings.Contains(page, `class="nav-sect">Archive<`) ||
		strings.Contains(page, `class="nav-sect">By reference<`) {
		t.Error("a folder tree heading is still in English")
	}

	// A folder name is sometimes an identifier ("515") and sometimes Persian
	// ("همهٔ اسناد"), so it takes its direction from its own content.
	if !strings.Contains(page, "unicode-bidi: plaintext;") {
		t.Error("folder names are pinned to one direction; a row whose name is " +
			"Persian and a row whose name is a reference number cannot both be " +
			"right that way")
	}

	// The arrows point the way the page reads. Back is rightward here, and
	// going deeper is leftward -- the same reasoning crumbBar carries.
	if !strings.Contains(page, `class="drill-up">`) {
		t.Fatal("the phone's folder panel has no way back up")
	}
	up := page[strings.Index(page, `class="drill-up">`):]
	up = up[:min(len(up), 220)]
	if !strings.Contains(up, "i-arrow-right") {
		t.Error("the \"up a level\" arrow does not point the way back in a " +
			"right-to-left page, which is rightward")
	}
	if !strings.Contains(page, `class="ic into" aria-hidden="true"><use href="#i-arrow-left"`) {
		t.Error("the \"into this folder\" arrow does not point the way forward in a " +
			"right-to-left page, which is leftward")
	}
}
