package find

import (
	"testing"
	"time"
)

// TestChangeQueueDebounce checks that a burst of adds coalesces into one
// batch, and that nothing is applied until the caller ends the debounce
// window -- here, by calling flush directly instead of waiting out a real
// watchDebounce.
func TestChangeQueueDebounce(t *testing.T) {
	var got [][]string
	q := newChangeQueue(func(paths []string) {
		got = append(got, append([]string(nil), paths...))
	})
	// A timer that will not fire within the test, so only an explicit
	// flush() ends the window -- this is the "fake events" clock.
	q.after = func(d time.Duration, f func()) *time.Timer {
		return time.AfterFunc(time.Hour, f)
	}

	q.add("a.docx")
	q.add("b.docx")
	q.add("a.docx") // duplicate, coalesced into one entry

	if len(got) != 0 {
		t.Fatalf("apply must not run before the debounce window closes, got %v", got)
	}

	q.flush()

	if len(got) != 1 {
		t.Fatalf("want exactly one batch, got %d", len(got))
	}
	if len(got[0]) != 2 {
		t.Fatalf("want 2 de-duplicated paths, got %v", got[0])
	}

	// A second flush with nothing queued must not call apply again.
	q.flush()
	if len(got) != 1 {
		t.Fatalf("flush with nothing pending must not call apply, got %v", got)
	}
}

// TestChangeQueueRestartsTimer checks that each add restarts the window
// rather than letting an earlier one fire independently: stopping the first
// timer and only the second one being live is what "debounced" means here.
func TestChangeQueueRestartsTimer(t *testing.T) {
	starts := 0
	q := newChangeQueue(func([]string) {})
	q.after = func(d time.Duration, f func()) *time.Timer {
		starts++
		return time.AfterFunc(time.Hour, f)
	}

	q.add("a.docx")
	first := q.timer
	q.add("b.docx")
	if q.timer == first {
		t.Fatal("a second add should have replaced the timer")
	}
	if starts != 2 {
		t.Fatalf("want 2 timers started, got %d", starts)
	}
}
