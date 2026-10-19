package find

import (
	"errors"
	"path/filepath"
	"strings"
	"unicode"
)

// Turning something a browser said into a path inside the archive.
//
// THIS FILE IS THE SECURITY BOUNDARY for every write to the document tree.
// Creating a folder, uploading a file, renaming and deleting all arrive as
// text from a signed-in person's browser and all end as an operation on a
// real directory that docxgen also writes into. Everything below exists to
// make sure that operation lands inside the archive and nowhere else.
//
// The rule is: REBUILD the path from validated segments rather than clean a
// string the caller supplied. A cleaner is a filter, and a filter is a list
// of the tricks somebody thought of. Splitting on separators, refusing any
// segment that is not a plain name, and joining the survivors onto the root
// admits only what was explicitly allowed.

var (
	errBadPath = errors.New("that location is not inside the archive")
	errBadName = errors.New("that name cannot be used for a folder or a file")
)

// maxSegment is a limit on one path component.
//
// Windows allows 255 per component. This is shorter on purpose: these are
// case folders and category folders in a business archive, and a 200-
// character folder name is a mistake or an attack rather than a filing
// decision. It also keeps the whole path clear of MAX_PATH on a deep tree
// reached over a UNC share, which is how the archive is reached from .13.
const maxSegment = 120

// reservedWindows are names Windows refuses to create as files or folders,
// with or without an extension.
//
// Worth handling explicitly rather than letting the operating system say no:
// the error it returns is unhelpful, and on some paths creating "CON" appears
// to succeed and then addresses a device instead of a directory.
var reservedWindows = map[string]bool{
	"con": true, "prn": true, "aux": true, "nul": true,
	"com1": true, "com2": true, "com3": true, "com4": true, "com5": true,
	"com6": true, "com7": true, "com8": true, "com9": true,
	"lpt1": true, "lpt2": true, "lpt3": true, "lpt4": true, "lpt5": true,
	"lpt6": true, "lpt7": true, "lpt8": true, "lpt9": true,
}

// safeSegment checks one path component -- a folder name, or a file name.
//
// Rejected, and why each matters:
//
//   - empty, "." and ".."      -- traversal, and the whole point of this file
//   - a separator of either kind -- a "name" carrying a path is not a name
//   - a colon                  -- "file.txt:hidden" is an NTFS alternate data
//     stream, which writes bytes nobody can see in a directory listing
//   - the characters Windows forbids anyway, said here so the message can
//   - a control character or a right-to-left override, which can make a
//     name render as something other than what it is
//   - a trailing dot or space  -- Windows silently strips them, so "evil ."
//     and "evil" are the same file while comparing as different strings
//   - a reserved device name
func safeSegment(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "." || s == ".." {
		return "", errBadName
	}
	if len([]rune(s)) > maxSegment {
		return "", errBadName
	}
	if strings.ContainsAny(s, `/\:*?"<>|`) {
		return "", errBadName
	}
	for _, r := range s {
		// Control characters, and the bidirectional overrides. A name holding
		// U+202E reads backwards on screen: "annex[U+202E]cod.exe" displays as
		// "annexexe.doc" and runs as a program.
		if r < 0x20 || r == 0x7f ||
			r == '‪' || r == '‫' || r == '‬' ||
			r == '‭' || r == '‮' ||
			r == '⁦' || r == '⁧' || r == '⁨' || r == '⁩' {
			return "", errBadName
		}
		if unicode.Is(unicode.Cf, r) && r != '‌' { // ZWNJ is ordinary Persian
			return "", errBadName
		}
	}
	// A trailing DOT only. A trailing space cannot survive the TrimSpace
	// above, and that is the fix rather than a gap: the hazard with a trailing
	// space is that WINDOWS strips it silently, so "evil " and "evil" become
	// one file while comparing as two different strings. Stripping it here,
	// deterministically and before anything else looks at the name, removes
	// the discrepancy instead of trying to detect it.
	//
	// A trailing dot is different: it survives TrimSpace and Windows strips it
	// too, so it has to be refused outright.
	if strings.HasSuffix(s, ".") {
		return "", errBadName
	}
	// The reserved check ignores any extension: CON.txt is CON.
	base := s
	if i := strings.IndexByte(base, '.'); i > 0 {
		base = base[:i]
	}
	if reservedWindows[strings.ToLower(base)] {
		return "", errBadName
	}
	return s, nil
}

// archivePath turns a slash-separated relative path from the browser into an
// absolute path inside root, or refuses.
//
// An empty rel is the root itself, which is legitimate: it is where a new
// top-level folder goes.
//
// The returned path is built by joining validated segments onto root. It is
// never the caller's string cleaned up, and the final containment check below
// is a belt as well as braces: if anything above ever lets a segment through
// that it should not, the path still has to resolve inside the archive.
func archivePath(root, rel string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", errBadPath
	}
	rel = strings.TrimSpace(rel)
	// Both separators, because the browser may send either and a Windows
	// client may send a backslash it thinks is ordinary.
	rel = strings.ReplaceAll(rel, `\`, "/")

	// RELATIVE ONLY. Anything beginning with a separator is refused rather
	// than reinterpreted.
	//
	// Reinterpreting is safe here and still wrong. "/etc/passwd" and
	// "\\evil-host\share\payload" both survive the segment rules and both
	// land inside the archive, at <root>/etc/passwd and
	// <root>/evil-host/share/payload -- contained, harmless, and a silent
	// answer to a question nobody asked. A caller sending either has
	// misunderstood what this parameter is, and a caller PROBING with either
	// should appear in the logs as a refusal rather than as a successful
	// request for a folder that happens not to exist.
	//
	// Nothing legitimate is lost: every path here is built by the page from
	// the tree it was given, and those are relative.
	if strings.HasPrefix(rel, "/") {
		return "", errBadPath
	}

	var parts []string
	for _, seg := range strings.Split(rel, "/") {
		if seg == "" {
			continue // a doubled slash, or a leading one
		}
		clean, err := safeSegment(seg)
		if err != nil {
			return "", err
		}
		parts = append(parts, clean)
	}

	abs := root
	if len(parts) > 0 {
		abs = filepath.Join(append([]string{root}, parts...)...)
	}

	// Belt and braces. filepath.Rel answers "how do I get from root to abs",
	// and any answer that starts by going up means abs is outside it. This
	// cannot trigger given the segment rules above, which is exactly why it is
	// worth keeping: it is the check that still holds if one of them is ever
	// relaxed by somebody who has not read this comment.
	within, err := filepath.Rel(root, abs)
	if err != nil {
		return "", errBadPath
	}
	within = filepath.ToSlash(within)
	if within == ".." || strings.HasPrefix(within, "../") {
		return "", errBadPath
	}
	return abs, nil
}

// archiveRel is the inverse: the slash-separated path a browser should be
// given for an absolute path inside root.
func archiveRel(root, abs string) (string, error) {
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return "", errBadPath
	}
	rel = filepath.ToSlash(rel)
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return "", errBadPath
	}
	if rel == "." {
		return "", nil
	}
	return rel, nil
}

// uploadExtensions is what may be added to the archive.
//
// An ALLOW-LIST, and a short one. This writes into a directory tree that is
// served back to staff and opened on their machines; it is not a general file
// store. Everything here is a document format the archive already holds, and
// nothing here is executable, a script, a shortcut or an installer.
//
// .lnk and .url are absent deliberately as well as the obvious ones: both are
// inert to look at and both run something when opened.
var uploadExtensions = map[string]bool{
	".pdf": true,
	".doc": true, ".docx": true, ".rtf": true, ".odt": true,
	".xls": true, ".xlsx": true, ".csv": true, ".ods": true,
	".ppt": true, ".pptx": true, ".odp": true,
	".txt": true,
	".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true,
	".tif": true, ".tiff": true,
	".zip": true,
	".msg": true, ".eml": true,
}

// safeUploadName validates a file name offered for upload.
func safeUploadName(name string) (string, error) {
	// Only the last component, whatever the browser sent. Some send a full
	// path when a directory is dropped.
	name = filepath.Base(strings.ReplaceAll(strings.TrimSpace(name), `\`, "/"))
	clean, err := safeSegment(name)
	if err != nil {
		return "", err
	}
	ext := strings.ToLower(filepath.Ext(clean))
	if ext == "" || !uploadExtensions[ext] {
		return "", errBadName
	}
	return clean, nil
}
