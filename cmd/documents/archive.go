package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Schayan-nk/docfind-documents/internal/find"
	"github.com/Schayan-nk/docfind-documents/internal/obs"
)

// archive is the document folder the server serves: the index, where the index
// is cached between runs, and the watcher on the live root.
type archive struct {
	ix        *find.Index
	cachePath string
	// watcher belongs to the current root. A nil watcher (setup mode, or a
	// platform with no watcher) just means the periodic rescan is the only
	// thing keeping the index current.
	watcher *find.Watcher
}

func newArchive() *archive {
	return &archive{ix: find.NewIndex(""), cachePath: filepath.Join(cacheDir(), "docfind-index.gob")}
}

// resolveArchiveRoot is where the documents are: the flag, else DOCFIND_ARCHIVE.
func resolveArchiveRoot(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	return strings.TrimSpace(os.Getenv("DOCFIND_ARCHIVE"))
}

// bringOnline indexes dir and makes it the live root.
func (a *archive) bringOnline(dir string) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	if err := checkArchive(abs); err != nil {
		return err
	}
	if a.ix.Root() != abs {
		a.ix.SetRoot(abs)
		if err := a.ix.Load(a.cachePath); err == nil {
			logf("loaded %d documents from cache", a.ix.Count())
		}
	}
	logf("indexing %s", abs)
	started := time.Now()
	n, err := a.ix.Scan()
	if err != nil {
		return fmt.Errorf("reading the folder: %w", err)
	}
	logf("%d documents (%d updated) in %s", a.ix.Count(), n, time.Since(started).Round(time.Millisecond))
	if err := a.ix.Save(a.cachePath); err != nil {
		logf("warning: could not save the index cache: %v", err)
	}
	// A failure to watch is logged and not fatal: the periodic rescan still
	// keeps the index current, on a slower schedule.
	a.stopWatching()
	if w, werr := find.NewWatcher(a.ix, logf); werr != nil {
		logf("archive watch unavailable, relying on the periodic scan instead: %v", werr)
	} else {
		a.watcher = w
	}
	return nil
}

// open brings root online, and returns why not when it cannot be -- which is
// not fatal: the server starts and its pages say what is wrong.
func (a *archive) open(root string) (setupReason string) {
	if root == "" {
		return "No document folder is configured: start with -archive <folder> or set DOCFIND_ARCHIVE."
	}
	if err := a.bringOnline(root); err != nil {
		logf("starting in setup mode: %v", err)
		return err.Error()
	}
	return ""
}

func (a *archive) stopWatching() {
	if a.watcher != nil {
		a.watcher.Stop()
		a.watcher = nil
	}
}

// rescanEvery picks up documents added outside the server, until stop is
// closed.
func (a *archive) rescanEvery(every time.Duration, stop <-chan struct{}) {
	if every < 5*time.Second {
		every = 5 * time.Second
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			if a.ix.Root() == "" {
				continue // setup mode: nothing to rescan
			}
			done := obs.StartJob("archive-rescan")
			n, err := a.ix.Scan()
			if err == nil && n > 0 {
				_ = a.ix.Save(a.cachePath)
			}
			done(n, err)
		}
	}
}

// archiveError is a failure to reach the archive folder: the operating
// system's own error, and advice for whoever reads the log.
type archiveError struct {
	Path   string
	Cause  error
	Advice string
}

func (e *archiveError) Error() string {
	return fmt.Sprintf("archive folder not reachable:\n%s\n\n%s", e.Path, e.Advice)
}

func (e *archiveError) Unwrap() error { return e.Cause }

// checkArchive confirms the archive folder is readable, and says what to do
// when it is not.
func checkArchive(abs string) error {
	fi, err := os.Stat(abs)
	if err != nil {
		return &archiveError{Path: abs, Cause: err, Advice: hint(err)}
	}
	if !fi.IsDir() {
		return fmt.Errorf("that path is a file, not a folder:\n%s", abs)
	}
	// Stat can succeed on a share the account may not read, so open it and
	// take one entry.
	f, err := os.Open(abs)
	if err != nil {
		return &archiveError{Path: abs, Cause: err, Advice: hint(err)}
	}
	defer f.Close()
	if _, err := f.ReadDir(1); err != nil && !errors.Is(err, fs.ErrClosed) && err.Error() != "EOF" {
		return &archiveError{Path: abs, Cause: err, Advice: hint(err)}
	}
	return nil
}

// hint turns a filesystem error into something worth acting on.
func hint(err error) string {
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "user name or password is incorrect"),
		strings.Contains(msg, "logon failure"),
		strings.Contains(msg, "not allowed to log on"):
		return "The file server rejected the account this service runs as. Run the service as an\n" +
			"account the share accepts, or save credentials for it with cmdkey."
	case strings.Contains(msg, "access is denied"), errors.Is(err, fs.ErrPermission):
		return "The folder exists but this account may not read it. Grant it read access (and\n" +
			"write access, for uploads and the editor)."
	case strings.Contains(msg, "network path was not found"),
		strings.Contains(msg, "network name cannot be found"),
		strings.Contains(msg, "bad net"):
		return "The server was reached but has no share by that name."
	case strings.Contains(msg, "network is unreachable"),
		strings.Contains(msg, "did not properly respond"),
		strings.Contains(msg, "no such host"),
		strings.Contains(msg, "timed out"):
		return "The file server could not be reached from this machine."
	case errors.Is(err, fs.ErrNotExist):
		return "That folder does not exist. Check -archive or DOCFIND_ARCHIVE."
	}
	return "The system said: " + err.Error()
}
