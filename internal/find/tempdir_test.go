package find

import (
	"os"
	"strings"
	"testing"
	"time"
)

// A temporary directory whose cleanup does not fail the test on Windows.
//
// t.TempDir() removes its directory when the test ends and FAILS the test if
// it cannot. On Windows a file written moments earlier can still be held
// briefly by something outside this process -- a scanner, the search indexer --
// and RemoveAll then reports "The directory is not empty".
//
// Measured on this machine before changing anything: four full-suite runs out
// of eight failed that way, in four DIFFERENT tests across three packages,
// with every assertion in every one of them passing. The message is always
// "testing.go:1369: TempDir RemoveAll cleanup", never an assertion.
//
// Worth fixing rather than re-running, because a suite that fails half its
// runs for a reason unrelated to the code teaches everybody to re-run it and
// stop reading the output -- and then the failure that IS real gets the same
// shrug. This package's tests are the ones that verify the report builder's
// routes, so it is the package where that mattered most.
//
// Retried, and a residue is LOGGED rather than failed. Logged rather than
// ignored on purpose: a genuine leaked file handle would keep the directory
// undeletable on every retry of every run, and that log line is what would
// say so. A scanner holding a file appears once and is gone on the retry.
//
// The same helper exists in internal/reports. Not shared, because a test
// helper cannot cross a package boundary without becoming exported API that
// ships in the binary, and forty lines of duplication is the cheaper of the
// two.
func tempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "find-"+safeTestName(t.Name())+"-")
	if err != nil {
		t.Fatalf("temporary directory: %v", err)
	}
	t.Cleanup(func() {
		for i := 0; ; i++ {
			if err := os.RemoveAll(dir); err == nil {
				return
			} else if i == 3 {
				t.Logf("could not remove %s: %v -- harmless if this is the only "+
					"run it happens on; a file handle this package failed to "+
					"close would do it every time", dir, err)
				return
			}
			time.Sleep(80 * time.Millisecond)
		}
	})
	return dir
}

// safeTestName makes a test's name usable in a filename.
//
// A subtest's name carries its parent and its case:
// "TestFirstAdminStillWorksOverLoopback/[::1]:51000". Both the slash and the
// colon are illegal in a Windows path, and MkdirTemp refuses a pattern
// containing a separator on every platform -- which turned a helper meant to
// stop spurious failures into eight real ones the first time it ran under a
// table-driven test. The name is kept rather than dropped because it is what
// identifies the directory in the log line above.
func safeTestName(name string) string {
	var b strings.Builder
	b.Grow(len(name))
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}
