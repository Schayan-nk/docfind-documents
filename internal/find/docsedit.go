package find

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/Schayan-nk/docfind-documents/internal/obs"
)

// Changing the document tree: making folders, adding files, renaming and
// deleting.
//
// Every path here has already been through docspath.go, which is the security
// boundary; this file is about what happens to a real directory once the path
// is known to be inside the archive.
//
// TWO THINGS MAKE THIS DIFFERENT FROM AN ORDINARY FILE MANAGER.
//
// The tree is SHARED. docxgen writes into it whenever somebody generates a
// quotation, and the scanner walks it on a timer. So nothing here may assume
// it is the only writer: a folder can appear between the check and the
// create, and a file can be added to a folder between reading it and deleting
// it. Every operation below is written to lose that race safely rather than
// to win it.
//
// The contents are REAL BUSINESS DOCUMENTS. There is no undo and no recycle
// bin on a UNC share. Deleting is therefore the narrowest operation here --
// it refuses a folder that still has anything in it, so losing a case folder
// takes deliberate, repeated action rather than one mis-aimed click.

var (
	errExists    = errors.New("something with that name is already there")
	errNotFound  = errors.New("that folder or file is not there")
	errNotEmpty  = errors.New("that folder still has things in it")
	errNotInside = errors.New("that is not a folder in the archive")
)

// maxUploadBytes is the cap on one uploaded document.
//
// The archive's own largest file today is under 2 MB; 40 leaves room for a
// scanned contract at full resolution without turning a document tree into a
// place to put video.
const maxUploadBytes = 40 << 20

// docsRoot is the archive root, or an error if the index is not configured.
func (s *Server) docsRoot() (string, error) {
	if s.ix == nil {
		return "", errNotInside
	}
	root := strings.TrimSpace(s.ix.Root())
	if root == "" {
		return "", errNotInside
	}
	return root, nil
}

// plainPath resolves rel inside the archive and requires every step below the
// root to be a real folder or file, with no link anywhere on the way.
//
// archivePath checks the TEXT of the path, and that is not enough: a junction
// planted inside the archive (no privilege needed on Windows) is a folder name
// like any other, and os.Stat, ReadDir, Rename and Remove all go through it.
// Through one, listing showed folders outside the archive, upload and new
// folder wrote there, and delete removed files there -- as the service
// account, with no recycle bin. plainUnder Lstats each step instead. The root
// itself is the one path that may be anything, since the service was pointed
// at it. TestTheDocsOperationsDoNotFollowAJunction.
func plainPath(root, rel string) (string, os.FileInfo, error) {
	abs, err := archivePath(root, rel)
	if err != nil {
		return "", nil, err
	}
	if abs == root {
		st, err := os.Stat(root)
		if err != nil {
			return "", nil, errNotFound
		}
		return abs, st, nil
	}
	st, ok := plainUnder(root, abs)
	if !ok {
		return "", nil, errNotFound
	}
	return abs, st, nil
}

// taken reports whether anything at all has the name: a file, a folder, or a
// link, which Lstat sees as itself rather than following.
func taken(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// makeFolder creates one folder inside parent.
//
// The existence check is separate from the creation on purpose: Mkdir fails
// when the name is taken, and the page should say so rather than quietly do
// nothing when somebody re-submits. Mkdir rather than MkdirAll: the parent is
// known to be a real folder, so one level is all that is ever made, and
// MkdirAll would walk the path again, through whatever it now holds. If
// docxgen creates the same folder in between, that is reported as already
// there, which is true, and nothing is lost.
func (s *Server) makeFolder(parentRel, name string) (string, error) {
	root, err := s.docsRoot()
	if err != nil {
		return "", err
	}
	// The parent has to exist and be a directory. Creating a folder inside a
	// path that is not there would build the whole chain, which turns one
	// typo into a tree of empty folders.
	parent, st, err := plainPath(root, parentRel)
	if err != nil {
		return "", err
	}
	if !st.IsDir() {
		return "", errNotFound
	}
	clean, err := safeSegment(name)
	if err != nil {
		return "", err
	}
	target := filepath.Join(parent, clean)
	if taken(target) {
		return "", errExists
	}
	if err := os.Mkdir(target, 0o755); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return "", errExists
		}
		return "", fmt.Errorf("creating the folder: %w", err)
	}
	return archiveRel(root, target)
}

// saveUpload writes one uploaded document into folderRel.
//
// Returns the name it was actually saved under, which may not be the name
// offered: see uniqueName.
func (s *Server) saveUpload(folderRel, name, user string, src io.Reader) (string, error) {
	root, err := s.docsRoot()
	if err != nil {
		return "", err
	}
	dir, st, err := plainPath(root, folderRel)
	if err != nil {
		return "", err
	}
	if !st.IsDir() {
		return "", errNotFound
	}
	clean, err := safeUploadName(name)
	if err != nil {
		return "", err
	}

	// QUARANTINE FIRST (uploadcheck.go). The bytes land outside the archive,
	// are checked for what they really are, and only then are written into
	// it -- so nothing the checks would refuse is ever, even briefly, a file
	// in a folder staff open.
	held, err := s.quarantineUpload(src)
	if err != nil {
		return "", err
	}
	defer os.Remove(held) // gone once copied in, or moved aside if refused
	if err := inspectUpload(held, strings.ToLower(filepath.Ext(clean))); err != nil {
		var r rejection
		if errors.As(err, &r) {
			s.keepRejected(held, clean, user, r.reason)
		}
		return "", err
	}
	if found, aerr := scanUpload(held); found {
		s.keepRejected(held, clean, user, "Windows Defender found a threat")
		return "", reject("ضدویروس در این پرونده تهدید پیدا کرد")
	} else if aerr != nil {
		obs.Default().Write("uploads", "warn", obs.Fields{"msg": "the virus scan could not run", "error": aerr.Error()})
	}
	src, err = os.Open(held)
	if err != nil {
		return "", err
	}
	defer src.(io.Closer).Close()

	// O_EXCL, so the file is created only if it does not exist and the
	// creation is the test. Checking first and writing second is the race
	// that overwrites a document somebody else added a moment ago -- and this
	// archive has already lost data once to two writers choosing the same
	// name, which is why the loop below renames rather than replaces.
	final := clean
	var f *os.File
	for attempt := 0; ; attempt++ {
		f, err = os.OpenFile(filepath.Join(dir, final), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err == nil {
			break
		}
		if !os.IsExist(err) {
			return "", fmt.Errorf("creating the file: %w", err)
		}
		if attempt >= 99 {
			return "", errExists
		}
		final = uniqueName(clean, attempt+1)
	}

	// A cap enforced while COPYING, not from a declared length. Content-Length
	// is whatever the client said; this is what it actually sent.
	written, err := io.Copy(f, io.LimitReader(src, maxUploadBytes+1))
	cerr := f.Close()
	if err == nil && written > maxUploadBytes {
		err = fmt.Errorf("the file is larger than the %d MB limit", maxUploadBytes>>20)
	}
	if err == nil {
		err = cerr
	}
	if err != nil {
		// A partial file is worse than no file: it indexes, it opens, and it
		// is wrong. Take it away.
		os.Remove(filepath.Join(dir, final))
		return "", err
	}
	if written == 0 {
		os.Remove(filepath.Join(dir, final))
		return "", errors.New("the file was empty")
	}
	return final, nil
}

// quarantineUpload writes an upload into the quarantine folder, capped at
// maxUploadBytes as it is copied, and returns where it is.
func (s *Server) quarantineUpload(src io.Reader) (string, error) {
	qdir := s.quarantineDir()
	if err := os.MkdirAll(qdir, 0o750); err != nil {
		return "", fmt.Errorf("the quarantine folder: %w", err)
	}
	f, err := os.CreateTemp(qdir, "upload-*.part")
	if err != nil {
		return "", fmt.Errorf("the quarantine folder: %w", err)
	}
	written, err := io.Copy(f, io.LimitReader(src, maxUploadBytes+1))
	cerr := f.Close()
	if err == nil {
		err = cerr
	}
	switch {
	case err != nil:
	case written > maxUploadBytes:
		err = fmt.Errorf("the file is larger than the %d MB limit", maxUploadBytes>>20)
	case written == 0:
		err = errors.New("the file was empty")
	}
	if err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// uniqueName turns "letter.pdf" into "letter (2).pdf".
//
// The counter goes before the extension so the file still opens in the right
// program, which "letter.pdf (2)" would not.
func uniqueName(name string, n int) string {
	ext := filepath.Ext(name)
	return fmt.Sprintf("%s (%d)%s", strings.TrimSuffix(name, ext), n+1, ext)
}

// renameEntry renames a folder or a file in place.
//
// In place: the new name is a NAME, not a path, so this cannot be used to
// move something to another folder. Moving is a different operation with
// different consequences -- it changes a document's reference number, which
// is how the whole archive is addressed -- and it is not offered.
func (s *Server) renameEntry(rel, newName string) (string, error) {
	root, err := s.docsRoot()
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(rel) == "" {
		return "", errNotInside // the archive root itself is not renameable
	}
	from, _, err := plainPath(root, rel)
	if err != nil {
		return "", err
	}
	clean, err := safeSegment(newName)
	if err != nil {
		return "", err
	}
	to := filepath.Join(filepath.Dir(from), clean)
	if strings.EqualFold(from, to) && from != to {
		// A case-only change on Windows: os.Rename refuses because the target
		// "exists" -- it is the same file. Two steps through a name nothing
		// else will have.
		via := filepath.Join(filepath.Dir(from), clean+".docfind-rename")
		if err := os.Rename(from, via); err != nil {
			return "", fmt.Errorf("renaming: %w", err)
		}
		if err := os.Rename(via, to); err != nil {
			os.Rename(via, from) // put it back rather than leave it renamed
			return "", fmt.Errorf("renaming: %w", err)
		}
		return archiveRel(root, to)
	}
	if taken(to) {
		return "", errExists
	}
	if err := os.Rename(from, to); err != nil {
		return "", fmt.Errorf("renaming: %w", err)
	}
	return archiveRel(root, to)
}

// deleteEntry removes one file, or one EMPTY folder.
//
// os.Remove, never os.RemoveAll, and that is the most important line in this
// file. RemoveAll on a path that resolved slightly wrong takes the archive
// with it, and there is no recycle bin on a UNC share. Requiring a folder to
// be empty means deleting a case folder is as many deliberate acts as it has
// documents, and every one of them is individually visible and refusable.
func (s *Server) deleteEntry(rel string) error {
	root, err := s.docsRoot()
	if err != nil {
		return err
	}
	if strings.TrimSpace(rel) == "" {
		return errNotInside // never the root
	}
	target, st, err := plainPath(root, rel)
	if err != nil {
		return err
	}
	if st.IsDir() {
		entries, err := os.ReadDir(target)
		if err != nil {
			return fmt.Errorf("reading the folder: %w", err)
		}
		if len(entries) > 0 {
			return errNotEmpty
		}
	}
	if err := os.Remove(target); err != nil {
		// The folder emptied between the read and the remove, or something
		// was added to it. Either way the caller should hear the truth rather
		// than a generic failure.
		if entries, rerr := os.ReadDir(target); rerr == nil && len(entries) > 0 {
			return errNotEmpty
		}
		return fmt.Errorf("deleting: %w", err)
	}
	return nil
}

// listFolder returns what is directly inside one folder.
//
// The tree the documents page draws comes from the INDEX, which only knows
// about files it could read. This reads the filesystem, so a folder that is
// empty -- or holds only files the indexer skips -- is still visible and can
// still be worked with. Without it a folder somebody just created would
// vanish until they put an indexable document in it.
func (s *Server) listFolder(rel string) ([]folderEntry, error) {
	root, err := s.docsRoot()
	if err != nil {
		return nil, err
	}
	dir, st, err := plainPath(root, rel)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		return nil, errNotFound
	}
	des, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading the folder: %w", err)
	}
	out := make([]folderEntry, 0, len(des))
	for _, de := range des {
		name := de.Name()
		if strings.HasPrefix(name, "~$") || strings.HasPrefix(name, ".") {
			continue // Word lock files, and anything hidden by convention
		}
		if isLink(de.Type()) || renamedByWin32(name) {
			continue // never shown, so never offered as a place to go (plainPath)
		}
		e := folderEntry{Name: name, Dir: de.IsDir()}
		if rel == "" {
			e.Path = name
		} else {
			e.Path = rel + "/" + name
		}
		if info, err := de.Info(); err == nil && !de.IsDir() {
			e.Size = info.Size()
		}
		out = append(out, e)
	}
	return out, nil
}

// folderEntry is one row of a folder listing.
type folderEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Dir  bool   `json:"dir"`
	Size int64  `json:"size,omitempty"`
}
