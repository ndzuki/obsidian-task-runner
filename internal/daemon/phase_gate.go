package daemon

import "sync"

// phaseGate bounds concurrent execution sessions for one phase (refining, planning,
// merge, priority, pm). Unlike implementationGate it has no adoption concept —
// PID adoption exists only for implementing, whose gate is
// max_concurrent_tasks_per_project (+ optional max_concurrent_tasks global
// cap). A nil/absent gate means the phase is unlimited.
//
// Acquisition is non-blocking: the scheduler tries once per scan round and
// leaves the task pending for the next scan when the gate is full, matching
// the implementation-gate behavior. A later task completion triggers that
// follow-up scan, so waiting tasks resume without a timer round-trip.
type phaseGate struct {
	mu      sync.Mutex
	limit   int
	active  int
	changed chan struct{}
}

func newPhaseGate(limit int) *phaseGate {
	if limit < 1 {
		limit = 1
	}
	return &phaseGate{
		limit:   limit,
		changed: make(chan struct{}),
	}
}

// tryAcquire reserves one slot. Returns false when the gate is full; the
// returned channel is closed when capacity frees up (informational — the
// scheduler relies on scan re-dispatch, not on waiting for it).
func (g *phaseGate) tryAcquire() (bool, <-chan struct{}) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.active >= g.limit {
		return false, g.changed
	}
	g.active++
	return true, nil
}

func (g *phaseGate) release() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.active == 0 {
		return
	}
	g.active--
	close(g.changed)
	g.changed = make(chan struct{})
}
