//go:build windows

package find

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestClassifyCompletion pins down the decision the watch loop makes about
// one completed read: a zero-byte, error-free result means the kernel's
// notification buffer overflowed, anything else that errored is a watch
// failure, and both mean the same thing to the caller -- fall back to a full
// scan -- which TestWatcherFullScanFallback checks separately.
func TestClassifyCompletion(t *testing.T) {
	cases := []struct {
		name string
		err  error
		qty  uint32
		want watchOutcome
	}{
		{"ok", nil, 128, watchOK},
		{"overflow", nil, 0, watchOverflow},
		{"error", errors.New("watch broke"), 0, watchFailed},
		{"error with bytes", errors.New("watch broke"), 64, watchFailed},
	}
	for _, c := range cases {
		if got := classifyCompletion(c.err, c.qty); got != c.want {
			t.Errorf("%s: classifyCompletion(%v, %d) = %v, want %v", c.name, c.err, c.qty, got, c.want)
		}
	}
}

// TestWatcherFullScanFallback checks the "-> full scan" half of "overflow or
// watch error -> full scan" directly, without needing a real overflow or a
// broken handle to provoke it: fullScan is just Watcher.ix.Scan(), called on
// a Watcher built without going through NewWatcher.
func TestWatcherFullScanFallback(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	ix := NewIndex(dir)
	w := &Watcher{ix: ix, log: func(string, ...any) {}}
	w.fullScan()

	if ix.Count() != 1 {
		t.Fatalf("fullScan should have run a real Scan, got %d document(s)", ix.Count())
	}
}

// TestWatcherSeesFileEvent is the end-to-end check: a real watch on a real
// temp directory notices a file written after the watch started, and the
// index reflects it once the debounce window closes.
func TestWatcherSeesFileEvent(t *testing.T) {
	dir := t.TempDir()
	ix := NewIndex(dir)
	if _, err := ix.Scan(); err != nil {
		t.Fatal(err)
	}

	w, err := NewWatcher(ix, t.Logf)
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	defer w.Stop()

	p := filepath.Join(dir, "new.txt")
	if err := os.WriteFile(p, []byte("hello from the watcher test"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Generous relative to watchDebounce (2s): this is a real OS notification
	// running alongside whatever else the test binary is doing, and a busy
	// machine can delay delivery well past the debounce window itself.
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if d, ok := ix.Doc("new.txt"); ok && d.Text != "" {
			return // event seen, debounced, and applied
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("watcher did not pick up the new file within the deadline")
}
