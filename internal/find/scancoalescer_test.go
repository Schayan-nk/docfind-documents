package find

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A burst of triggers while a run is already in flight must coalesce into
// exactly one more run -- not one per trigger (that is the pile-up rescanSoon
// used to cause, ten uploads queuing ten full ix.Scan() calls) and not zero
// (which would silently drop changes that arrived mid-scan).
func TestScanCoalescerCoalescesBurstsIntoOneQueuedRun(t *testing.T) {
	var runs atomic.Int32
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var mu sync.Mutex
	first := true

	c := newScanCoalescer(func(reason string) error {
		runs.Add(1)
		mu.Lock()
		isFirst := first
		first = false
		mu.Unlock()
		if isFirst {
			started <- struct{}{}
			<-release // hold the first run open while triggers pile up behind it
		}
		return nil
	})

	c.trigger("a")
	<-started // the first run is now in flight

	for _, reason := range []string{"b", "c", "d", "e"} {
		c.trigger(reason)
	}

	close(release) // let the first run finish; the one queued run should follow

	deadline := time.Now().Add(2 * time.Second)
	for runs.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	// Give a wrongly-uncoalesced implementation time to run more than once.
	time.Sleep(50 * time.Millisecond)
	if got := runs.Load(); got != 2 {
		t.Errorf("got %d runs for one in-flight run plus five triggers behind it, want exactly 2", got)
	}
}

// Two triggers that do not overlap -- the second arriving only after the
// first has fully finished -- are two separate runs, not one.
func TestScanCoalescerRunsAgainAfterIdling(t *testing.T) {
	var runs atomic.Int32
	done := make(chan struct{}, 1)
	c := newScanCoalescer(func(reason string) error {
		runs.Add(1)
		done <- struct{}{}
		return nil
	})

	c.trigger("first")
	<-done
	time.Sleep(20 * time.Millisecond) // let the loop mark itself idle

	c.trigger("second")
	<-done
	time.Sleep(20 * time.Millisecond)

	if got := runs.Load(); got != 2 {
		t.Errorf("got %d runs for two non-overlapping triggers, want 2", got)
	}
}

// A single trigger with nothing behind it runs once and stops -- no queued
// run left spinning forever.
func TestScanCoalescerSingleTriggerRunsOnce(t *testing.T) {
	var runs atomic.Int32
	done := make(chan struct{}, 1)
	c := newScanCoalescer(func(reason string) error {
		runs.Add(1)
		done <- struct{}{}
		return nil
	})

	c.trigger("only")
	<-done
	time.Sleep(50 * time.Millisecond)

	if got := runs.Load(); got != 1 {
		t.Errorf("got %d runs for one trigger, want 1", got)
	}
	c.mu.Lock()
	running, queued := c.running, c.queued
	c.mu.Unlock()
	if running || queued {
		t.Errorf("coalescer left running=%v queued=%v after finishing; want both false", running, queued)
	}
}
