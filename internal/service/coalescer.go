package service

import "sync"

// fetchResult holds the outcome of a coalesced backing fetch shared by all
// callers that joined the same in-flight call.
type fetchResult struct {
	val []byte
	err error
}

// call represents a single in-flight fetch for a particular key. The first
// caller to request a key creates the call and executes the work; subsequent
// callers attach to the same call and block on its done channel.
type call struct {
	wg     sync.WaitGroup
	res    fetchResult
	shared bool // true once at least one duplicate caller has joined
}

// Coalescer collapses concurrent fetches for the same key into a single
// execution of the supplied function, fanning the result out to every caller.
// This prevents a "cache stampede" / "thundering herd" where many goroutines
// independently miss the cache and hammer the backing store for the same key.
//
// It is a small in-house equivalent of golang.org/x/sync/singleflight,
// implemented with a mutex and per-key sync.WaitGroup so the dependency set
// stays minimal and the mechanics are explicit.
type Coalescer struct {
	mu    sync.Mutex
	calls map[string]*call
}

// NewCoalescer returns a ready-to-use Coalescer.
func NewCoalescer() *Coalescer {
	return &Coalescer{calls: make(map[string]*call)}
}

// Do executes fn for key, ensuring that concurrent calls with the same key
// share a single execution. It returns the value, the error from fn, and
// shared, which is true if the result was shared with at least one other caller
// (useful for metrics on how many requests were coalesced).
func (c *Coalescer) Do(key string, fn func() ([]byte, error)) (val []byte, err error, shared bool) {
	c.mu.Lock()
	if existing, ok := c.calls[key]; ok {
		existing.shared = true
		c.mu.Unlock()
		existing.wg.Wait()
		return existing.res.val, existing.res.err, true
	}

	cl := new(call)
	cl.wg.Add(1)
	c.calls[key] = cl
	c.mu.Unlock()

	// Execute the real work exactly once for this key.
	cl.res.val, cl.res.err = fn()
	cl.wg.Done()

	// Remove the call so future requests trigger a fresh fetch.
	c.mu.Lock()
	// Only delete if it is still the same call (defensive against races where
	// the map entry was already replaced).
	if c.calls[key] == cl {
		delete(c.calls, key)
	}
	shared = cl.shared
	c.mu.Unlock()

	return cl.res.val, cl.res.err, shared
}

// InFlight reports how many distinct keys currently have an in-flight call.
// Primarily useful for tests and introspection.
func (c *Coalescer) InFlight() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.calls)
}
