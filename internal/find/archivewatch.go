package find

import (
	"sync"
	"time"
)

// watchDebounce is how long the change queue waits after the last reported
// path before re-indexing, so a burst that touches one file several times in
// a row -- an application's own save-then-rewrite, a folder of forty files
// copied in at once -- costs one reindex instead of one per event.
const watchDebounce = 2 * time.Second

// changeQueue collects paths reported by a filesystem watcher and, once they
// go quiet for watchDebounce, hands the whole de-duplicated batch to apply in
// one call.
//
// EVERYTHING SERIALISED THROUGH ONE TIMER, so a batch is never handed off
// while it is still growing: each new path restarts the same timer rather
// than racing a second one that was already counting down.
type changeQueue struct {
	apply func(paths []string)
	// after stands in for time.AfterFunc, swappable so a test can drive the
	// queue with a fake clock instead of waiting out a real debounce window.
	after func(d time.Duration, f func()) *time.Timer

	mu      sync.Mutex
	pending map[string]bool
	timer   *time.Timer
}

// newChangeQueue returns a queue that calls apply with the accumulated paths
// once they have gone quiet for watchDebounce.
func newChangeQueue(apply func(paths []string)) *changeQueue {
	return &changeQueue{
		apply:   apply,
		after:   time.AfterFunc,
		pending: map[string]bool{},
	}
}

// add queues one changed path and restarts the debounce window.
func (q *changeQueue) add(path string) {
	if path == "" {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.pending[path] = true
	if q.timer != nil {
		q.timer.Stop()
	}
	q.timer = q.after(watchDebounce, q.flush)
}

// flush hands every pending path to apply and empties the queue.
//
// A METHOD IN ITS OWN RIGHT, not inlined into the timer callback, so a test
// can call it directly to end a debounce window instead of waiting out
// watchDebounce for real.
func (q *changeQueue) flush() {
	q.mu.Lock()
	if len(q.pending) == 0 {
		q.mu.Unlock()
		return
	}
	paths := make([]string, 0, len(q.pending))
	for p := range q.pending {
		paths = append(paths, p)
	}
	q.pending = map[string]bool{}
	q.mu.Unlock()
	q.apply(paths)
}

// stop cancels a pending timer without flushing it, for shutdown: anything
// still in the queue is picked up by the next full Scan instead.
func (q *changeQueue) stop() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.timer != nil {
		q.timer.Stop()
	}
}
