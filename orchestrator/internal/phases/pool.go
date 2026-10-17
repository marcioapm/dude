package phases

import (
	"context"
	"sync"
	"time"
)

// pool is a loop's slots, held across its passes: work a pass stopped
// waiting for keeps its slot until it finishes, so the next pass has only
// the rest. busy is the ids in its slots, for work that must never run
// twice at once.
type pool struct {
	slots chan struct{}
	mu    sync.Mutex
	busy  map[string]bool
}

func newPool(n int) *pool {
	return &pool{slots: make(chan struct{}, n), busy: map[string]bool{}}
}

// free is how many slots no work holds.
func (p *pool) free() int { return cap(p.slots) - len(p.slots) }

// claim marks id as being worked on; false when it already is.
func (p *pool) claim(id string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.busy[id] {
		return false
	}
	p.busy[id] = true
	return true
}

func (p *pool) unclaim(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.busy, id)
}

// take waits for a slot until ctx ends or, when not nil, until expired is
// closed; false when it got none.
func (p *pool) take(ctx context.Context, expired <-chan struct{}) bool {
	// A free slot first: a pass with a cancelled ctx still starts its work,
	// as it always has, and that work sees the ctx.
	select {
	case p.slots <- struct{}{}:
		return true
	default:
	}
	select {
	case p.slots <- struct{}{}:
		return true
	case <-ctx.Done():
	case <-expired:
	}
	return false
}

func (p *pool) give() { <-p.slots }

// hold takes a slot, waiting for one however long it takes.
func (p *pool) hold() { p.slots <- struct{}{} }

// passWait is how long a pass waits for its own work: until expired is
// closed when SweepWait is set, else (nil, never) for all of it.
type passWait struct {
	expired <-chan struct{}
	stop    func() bool
}

func (s *Syncer) passWait() passWait {
	if s.SweepWait <= 0 {
		return passWait{stop: func() bool { return false }}
	}
	expired := make(chan struct{})
	t := time.AfterFunc(s.SweepWait, func() { close(expired) })
	return passWait{expired: expired, stop: t.Stop}
}

// collect receives up to n results of a pass's work until w expires, and
// counts those that acted.
func (w passWait) collect(results <-chan bool, n int) int {
	acted := 0
	for range n {
		select {
		case did := <-results:
			if did {
				acted++
			}
		case <-w.expired:
			return acted
		}
	}
	return acted
}

// Slots of the phase sweep and of the publish worker, made on first use:
// a Syncer is built as a struct literal in many places.
func (s *Syncer) pools() (sweep, publish *pool) {
	s.poolsOnce.Do(func() {
		s.sweepPool, s.publishPool = newPool(sweepSlots), newPool(publishSlots)
	})
	return s.sweepPool, s.publishPool
}
