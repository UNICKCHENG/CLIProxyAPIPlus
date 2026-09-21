package cursor

import (
	"context"
	"sync"
)

// inFlightTracker tracks cancellable executor work so quiesce can drain it without stopping
// the bridges. The map is keyed by a process-local id, not the host stream id, because the
// non-streaming path has no stream.
type inFlightTracker struct {
	mu      sync.Mutex
	next    uint64
	cancels map[uint64]context.CancelFunc
}

func (t *inFlightTracker) register(cancel context.CancelFunc) uint64 {
	if cancel == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cancels == nil {
		t.cancels = make(map[uint64]context.CancelFunc)
	}
	t.next++
	id := t.next
	t.cancels[id] = cancel
	return id
}

func (t *inFlightTracker) unregister(id uint64) {
	if id == 0 {
		return
	}
	t.mu.Lock()
	delete(t.cancels, id)
	t.mu.Unlock()
}
