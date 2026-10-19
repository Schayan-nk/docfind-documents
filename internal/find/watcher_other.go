//go:build !windows

package find

// Watcher is the non-Windows stand-in. Change notification is implemented
// with syscall.ReadDirectoryChanges, which only exists on Windows -- see
// watcher_windows.go -- so here there is nothing to run: the caller's
// periodic full Scan is the only indexing path.
type Watcher struct{}

// NewWatcher reports that watching is not available on this platform. A nil,
// nil return (not an error) is deliberate: this is expected and normal here,
// not a failure the caller should log as one.
func NewWatcher(ix *Index, log func(format string, args ...any)) (*Watcher, error) {
	return nil, nil
}

// Stop is a no-op: NewWatcher never returns a non-nil Watcher on this
// platform, but a nil-receiver call is safe regardless, the same as every
// other lifecycle method in this package that might be called on a watcher
// that was never started.
func (w *Watcher) Stop() {}
