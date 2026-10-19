package find

// semaphore bounds how many callers may hold a slot at once, so background
// work -- OCR, a mail sweep, a customer sync -- cannot open unlimited
// concurrent work and starve web requests competing for the same CPU, disk or
// outbound connections.
//
// A buffered channel rather than golang.org/x/sync/semaphore: everything
// needed here is "block until a slot is free" and "fail instead of blocking",
// both of which a channel already does, and it keeps this out of go.mod. The
// interactive AI routes (adviceSlots in airoutes.go, askSlots in
// askroutes.go) already use the same channel pattern directly; this just
// gives the rest of the background work the same shape as a named type.
type semaphore chan struct{}

// newSemaphore returns a semaphore admitting n callers at once. n below one
// is treated as one, since a semaphore that admits nobody is a way to hang
// forever rather than a smaller limit.
func newSemaphore(n int) semaphore {
	if n < 1 {
		n = 1
	}
	return make(semaphore, n)
}

// acquire blocks until a slot is free. For background work that should wait
// its turn rather than fail outright -- OCR, the mail classifier sweep, the
// customer sync.
func (s semaphore) acquire() { s <- struct{}{} }

// release frees a slot taken by acquire or a successful tryAcquire.
func (s semaphore) release() { <-s }
