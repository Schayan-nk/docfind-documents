package find

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// More callers than slots must never run at once -- the whole point of
// bounding OCR, the mail sweep and the rest is that the peak concurrency
// actually stays at the limit, not just that things eventually finish.
func TestSemaphoreLimitsConcurrency(t *testing.T) {
	const limit = 2
	const callers = 10
	s := newSemaphore(limit)

	var current, peak atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.acquire()
			defer s.release()
			n := current.Add(1)
			for {
				p := peak.Load()
				if n <= p || peak.CompareAndSwap(p, n) {
					break
				}
			}
			time.Sleep(15 * time.Millisecond)
			current.Add(-1)
		}()
	}
	wg.Wait()

	if got := peak.Load(); got > limit {
		t.Errorf("peak concurrency was %d, want at most %d", got, limit)
	}
}
