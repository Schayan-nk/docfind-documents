//go:build windows

package find

import (
	"encoding/binary"
	"fmt"
	"path/filepath"
	"sync"
	"unicode/utf16"

	"golang.org/x/sys/windows"
)

// Watcher watches ix's archive root for filesystem changes using Windows'
// ReadDirectoryChangesW (via golang.org/x/sys/windows, which the module
// already requires; the standard syscall package's completion-port calls are
// deprecated for taking a 32-bit key), feeding a debounced changeQueue (see
// archivewatch.go) that re-indexes only the paths that changed rather than
// walking the whole tree.
//
// The periodic full Scan keeps running alongside this regardless: it is the
// reconciliation path for anything the watcher missed, and it is also what
// this falls back to on a buffer overflow or a watch error (see run).
type Watcher struct {
	ix     *Index
	handle windows.Handle
	port   windows.Handle
	queue  *changeQueue
	log    func(format string, args ...any)
	buf    []byte
	ov     windows.Overlapped

	stopOnce sync.Once
	stopCh   chan struct{}
	doneCh   chan struct{}
}

const (
	watchNotifyFilter = windows.FILE_NOTIFY_CHANGE_FILE_NAME |
		windows.FILE_NOTIFY_CHANGE_DIR_NAME |
		windows.FILE_NOTIFY_CHANGE_SIZE |
		windows.FILE_NOTIFY_CHANGE_LAST_WRITE
	watchBufSize = 64 * 1024
	// watchStopKey is the IOCP completion key used only for the packet
	// Stop() posts to wake a blocked GetQueuedCompletionStatus; a real
	// directory-change completion always carries watchIOKey instead.
	watchStopKey = ^uintptr(0)
	watchIOKey   = uintptr(1)
)

// NewWatcher opens a recursive watch on ix.Root() and starts the goroutine
// that feeds it. A returned error means watching could not be set up at all
// (an unreachable root, an exhausted handle table); the caller is expected
// to keep running its periodic full Scan regardless, which is the only
// indexing path this machine gets in that case.
func NewWatcher(ix *Index, log func(format string, args ...any)) (*Watcher, error) {
	root := ix.Root()
	if root == "" {
		return nil, fmt.Errorf("find: no archive directory configured")
	}
	if log == nil {
		log = func(string, ...any) {}
	}

	p, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(p,
		windows.FILE_LIST_DIRECTORY,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OVERLAPPED, 0)
	if err != nil {
		return nil, fmt.Errorf("find: opening %s for watching: %w", root, err)
	}
	port, err := windows.CreateIoCompletionPort(h, 0, watchIOKey, 1)
	if err != nil {
		windows.CloseHandle(h)
		return nil, fmt.Errorf("find: creating the watch completion port: %w", err)
	}

	w := &Watcher{
		ix: ix, handle: h, port: port, log: log, buf: make([]byte, watchBufSize),
		stopCh: make(chan struct{}), doneCh: make(chan struct{}),
	}
	// ARMED BEFORE RETURNING. Windows records changes only from the first
	// ReadDirectoryChangesW on: armed in the goroutine instead, a file written
	// between NewWatcher returning and that goroutine being scheduled was
	// never reported (seen as TestWatcherSeesFileEvent failing about one run
	// in ten), and waited for the periodic scan.
	if err := w.arm(); err != nil {
		windows.CloseHandle(port)
		windows.CloseHandle(h)
		return nil, fmt.Errorf("find: arming the watch on %s: %w", root, err)
	}
	w.queue = newChangeQueue(func(paths []string) {
		if _, err := ix.ApplyChanges(paths); err != nil {
			log("archive watch: re-indexing %d changed path(s): %v", len(paths), err)
		}
	})
	go w.run()
	return w, nil
}

// arm asks for the next batch of changes, delivered to the completion port.
// The buffer and the OVERLAPPED belong to the watcher, which lives on the
// heap, so they stay put while the read is outstanding.
func (w *Watcher) arm() error {
	var n uint32
	return windows.ReadDirectoryChanges(w.handle, &w.buf[0], uint32(len(w.buf)),
		true, watchNotifyFilter, &n, &w.ov, 0)
}

// Stop ends the watch and waits for its goroutine to exit, so shutdown never
// races a change that is still being applied.
func (w *Watcher) Stop() {
	if w == nil {
		return
	}
	w.stopOnce.Do(func() {
		close(w.stopCh)
		// Wake a GetQueuedCompletionStatus that is otherwise blocked
		// indefinitely: a synthetic packet, keyed so run tells it apart
		// from a real directory-change completion.
		_ = windows.PostQueuedCompletionStatus(w.port, 0, watchStopKey, nil)
	})
	<-w.doneCh
}

// watchOutcome classifies one completed (or failed) read, so the decision of
// what to do about it -- go on, or fall back to a full scan -- is a pure
// function that a test can drive without a real directory handle.
type watchOutcome int

const (
	watchOK watchOutcome = iota
	watchOverflow
	watchFailed
)

// classifyCompletion decides what a GetQueuedCompletionStatus result means.
//
// A ZERO-BYTE result with no error is the documented signal that the
// kernel's notification buffer overflowed: some changes were coalesced or
// lost entirely, and the only correct recovery is to stop trusting the delta
// and read the whole archive again.
func classifyCompletion(err error, qty uint32) watchOutcome {
	switch {
	case err != nil:
		return watchFailed
	case qty == 0:
		return watchOverflow
	default:
		return watchOK
	}
}

// run pumps directory-change completions until Stop is called. It owns the
// handle and port for its whole lifetime and closes both on the way out.
func (w *Watcher) run() {
	defer close(w.doneCh)
	defer w.queue.stop()
	defer windows.CloseHandle(w.handle)
	defer windows.CloseHandle(w.port)

	for {
		var qty uint32
		var key uintptr
		var rov *windows.Overlapped
		err := windows.GetQueuedCompletionStatus(w.port, &qty, &key, &rov, windows.INFINITE)
		if key == watchStopKey {
			return
		}

		switch classifyCompletion(err, qty) {
		case watchOverflow:
			w.log("archive watch: notification buffer overflowed -- falling back to a full scan")
			w.fullScan()
		case watchFailed:
			w.log("archive watch: %v -- falling back to a full scan", err)
			w.fullScan()
		default:
			w.notify(w.buf[:qty])
		}

		if err := w.arm(); err != nil {
			w.log("archive watch: arming the watch: %v -- falling back to a full scan", err)
			w.fullScan()
			return
		}
	}
}

// fullScan re-reads the whole archive, synchronously, as the recovery path
// for anything the watcher itself cannot trust: a lost or coalesced batch of
// changes, or a watch that has stopped working.
func (w *Watcher) fullScan() {
	if _, err := w.ix.Scan(); err != nil {
		w.log("archive watch: full-scan fallback failed: %v", err)
	}
}

// notify decodes one buffer of FILE_NOTIFY_INFORMATION records and queues
// the affected paths, archive-relative and slash-separated to match Doc.ID.
//
// Decoded by hand rather than cast through unsafe.Pointer: the buffer is a
// packed, variably-sized C structure with no fixed alignment guarantee once
// NextEntryOffset has walked partway into it, and encoding/binary reads it
// exactly as safely without depending on struct layout matching across Go
// versions.
func (w *Watcher) notify(b []byte) {
	for len(b) >= 12 {
		next := binary.LittleEndian.Uint32(b[0:4])
		nameLen := binary.LittleEndian.Uint32(b[8:12])
		end := 12 + int(nameLen)
		if end > len(b) {
			return // truncated record; nothing more to trust in this buffer
		}
		name := b[12:end]
		u16 := make([]uint16, len(name)/2)
		for i := range u16 {
			u16[i] = binary.LittleEndian.Uint16(name[i*2 : i*2+2])
		}
		if rel := filepath.ToSlash(string(utf16.Decode(u16))); rel != "" {
			w.queue.add(rel)
		}

		if next == 0 {
			return
		}
		if int(next) >= len(b) {
			return
		}
		b = b[next:]
	}
}
