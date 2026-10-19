package find

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Nothing a browser can say may address a path outside the archive.
//
// The list is deliberately longer than "does it contain ..": every entry is a
// way somebody has actually escaped a document root somewhere, and the value
// of writing them down is that the next person to touch archivePath can see
// what it is holding back.
func TestArchivePathRefusesEverythingOutsideTheRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "archive")

	bad := []struct{ name, rel string }{
		{"parent", ".."},
		{"parent slash", "../"},
		{"climb out", "../../windows/system32"},
		{"climb from inside", "406/../../../etc/passwd"},
		{"backslash climb", `..\..\windows`},
		{"mixed separators", `406/..\..\..\secrets`},
		{"absolute windows", `C:\Windows\System32`},
		{"absolute unix", "/etc/passwd"},
		{"unc share", `\\evil-host\share\payload`},
		{"leading slash", "/406/300"},
		{"root as slash", "/"},
		{"drive relative", "C:file.txt"},
		{"alternate data stream", "406/notes.txt:hidden"},
		{"dot segment", "406/./../.."},
		{"trailing dot folder", "406/evil."},
		// A trailing SPACE is not here: TrimSpace removes it before anything
		// looks at the name, which is the fix rather than a gap. See
		// safeSegment -- the hazard is Windows stripping it silently so that
		// two strings address one file, and normalising first removes the
		// discrepancy instead of trying to detect it.
		{"reserved CON", "406/CON"},
		{"reserved con lower", "406/con"},
		{"reserved with extension", "406/CON.txt"},
		{"reserved COM1", "406/COM1"},
		{"null byte", "406/a\x00b"},
		{"newline", "406/a\nb"},
		{"tab", "406/a\tb"},
		{"rtl override", "406/annex\u202Ecod.exe"},
		{"left-to-right override", "406/a\u202Db"},
		{"wildcard star", "406/*"},
		{"wildcard question", "406/a?b"},
		{"pipe", "406/a|b"},
		{"quote", `406/a"b`},
		{"angle", "406/a<b>c"},
		{"too long", "406/" + strings.Repeat("a", maxSegment+1)},
	}
	for _, c := range bad {
		t.Run(c.name, func(t *testing.T) {
			got, err := archivePath(root, c.rel)
			if err == nil {
				t.Fatalf("accepted %q and returned %q; it must be refused", c.rel, got)
			}
		})
	}
}

// And the ordinary cases still work, or the feature does not exist.
func TestArchivePathAcceptsRealLocations(t *testing.T) {
	root := filepath.Join(t.TempDir(), "archive")

	ok := []struct{ name, rel, want string }{
		{"root itself", "", ""},
		{"one level", "406", "406"},
		{"two levels", "406/300", "406/300"},
		{"three levels", "406/300/Inquiry", "406/300/Inquiry"},
		{"persian folder", "406/300/پیش‌فاکتور", "406/300/پیش‌فاکتور"},
		{"spaces inside", "406/300/Proforma Invoice", "406/300/Proforma Invoice"},
		{"doubled slash", "406//300", "406/300"},
		{"trailing slash", "406/300/", "406/300"},
		{"backslash separator", `406\300`, "406/300"},
		{"hyphen and underscore", "406/300/a-b_c", "406/300/a-b_c"},
		{"dot inside a name", "406/300/v1.2 final", "406/300/v1.2 final"},
	}
	for _, c := range ok {
		t.Run(c.name, func(t *testing.T) {
			abs, err := archivePath(root, c.rel)
			if err != nil {
				t.Fatalf("refused %q: %v", c.rel, err)
			}
			rel, err := archiveRel(root, abs)
			if err != nil {
				t.Fatalf("archiveRel refused what archivePath built: %v", err)
			}
			if rel != c.want {
				t.Errorf("%q resolved to %q, want %q", c.rel, rel, c.want)
			}
			// Whatever it resolved to, it is inside the root.
			if !strings.HasPrefix(abs, root) {
				t.Errorf("%q resolved to %q, which is outside %q", c.rel, abs, root)
			}
		})
	}
}

// Uploads are an allow-list, and the list must not contain anything that runs.
func TestUploadNamesRefuseExecutables(t *testing.T) {
	bad := []string{
		"payload.exe", "payload.bat", "payload.cmd", "payload.com", "payload.scr",
		"payload.ps1", "payload.vbs", "payload.js", "payload.jse", "payload.wsf",
		"payload.msi", "payload.dll", "payload.hta", "payload.reg", "payload.jar",
		"payload.lnk", "payload.url", "payload.chm", "payload.cpl",
		// Double extensions: the LAST one decides, which is what Windows opens.
		"invoice.pdf.exe", "report.docx.js",
		// No extension at all.
		"README",
		// "/etc/passwd" reduces to "passwd", which has no extension and is
		// refused for that reason.
		"/etc/passwd",
	}
	for _, n := range bad {
		t.Run(n, func(t *testing.T) {
			if got, err := safeUploadName(n); err == nil {
				t.Errorf("accepted %q as %q; it must be refused", n, got)
			}
		})
	}

	good := map[string]string{
		"Fax.pdf":                        "Fax.pdf",
		"inquiry_acme_persian_1009.docx": "inquiry_acme_persian_1009.docx",
		"نامه.pdf":                       "نامه.pdf",
		"Sales Report.xlsx":              "Sales Report.xlsx",
		"scan.TIFF":                      "scan.TIFF", // extension folded only for the check
		// A browser that sends a path keeps only the last component.
		`C:\Users\sasan\Desktop\letter.pdf`: "letter.pdf",
		"folder/letter.pdf":                 "letter.pdf",
		// Traversal in a FILE name is stripped rather than refused, and that
		// is the intended behaviour: the name is reduced to its last
		// component and then joined to a folder that archivePath has already
		// validated, so "../escape.pdf" uploads "escape.pdf" into the folder
		// the caller chose and cannot reach the one above it. Refusing would
		// also be safe; stripping is friendlier to a browser that sends a
		// full path for a dropped file, which some do.
		"../escape.pdf": "escape.pdf",
		`..\escape.pdf`: "escape.pdf",
	}
	for in, want := range good {
		t.Run(in, func(t *testing.T) {
			got, err := safeUploadName(in)
			if err != nil {
				t.Fatalf("refused %q: %v", in, err)
			}
			if got != want {
				t.Errorf("%q became %q, want %q", in, got, want)
			}
		})
	}
}

// A separator inside what is meant to be ONE name is not a name.
func TestSafeSegmentRefusesPaths(t *testing.T) {
	for _, s := range []string{"a/b", `a\b`, "..", ".", "", "   ", "a:b"} {
		if _, err := safeSegment(s); err == nil {
			t.Errorf("safeSegment accepted %q", s)
		}
	}
	// Persian with a zero-width non-joiner is an ordinary folder name here --
	// "پیش‌فاکتور" is spelled with one -- and must not be swept up with the
	// other format characters.
	if _, err := safeSegment("پیش‌فاکتور"); err != nil {
		t.Errorf("safeSegment refused a normal Persian name: %v", err)
	}
}

// The containment check holds even for a root with a trailing separator or a
// relative root, because a deployment's configured path is whatever somebody
// typed into a .env file.
func TestArchivePathHoldsForAwkwardRoots(t *testing.T) {
	roots := []string{
		filepath.Join(t.TempDir(), "archive"),
		filepath.Join(t.TempDir(), "archive") + string(filepath.Separator),
	}
	if runtime.GOOS == "windows" {
		roots = append(roots, `D:\Archive\Documents`, `D:\Archive\Documents\`)
	}
	for _, root := range roots {
		for _, rel := range []string{"..", "../..", `..\..`, "406/../.."} {
			if _, err := archivePath(root, rel); err == nil {
				t.Errorf("root %q accepted %q", root, rel)
			}
		}
	}
}
