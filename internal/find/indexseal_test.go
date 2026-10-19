package find

import (
	"os"
	"path/filepath"
	"testing"
)

// A cache that cannot be opened is not an error worth stopping for.
//
// This is what makes sealing it safe: every caller treats a failed load as "no
// cache" and scans the archive instead. A key lost, or a file copied from a
// machine whose DPAPI will not unwrap it, costs a slower start and nothing
// else -- there is no state in this file that is not derivable from the
// archive.
func TestAnUnreadableIndexCacheIsRecoverable(t *testing.T) {
	root := buildArchive(t)
	ix := NewIndex(root)
	if _, err := ix.Scan(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(tempDir(t), "index.dat")
	if err := ix.Save(path); err != nil {
		t.Fatal(err)
	}

	// Corrupt it the way a wrong key would: the bytes no longer authenticate.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)-1] ^= 0xFF
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	broken := NewIndex(root)
	if err := broken.Load(path); err == nil {
		t.Error("a tampered cache loaded without complaint, which means the seal is not " +
			"authenticating what it decrypts")
	}
	// The recovery path: scan, exactly as main.go does when Load fails.
	if _, err := broken.Scan(); err != nil {
		t.Fatalf("rescanning after an unreadable cache failed: %v", err)
	}
	if broken.Count() != ix.Count() {
		t.Errorf("after rescanning got %d documents, want %d", broken.Count(), ix.Count())
	}
}
