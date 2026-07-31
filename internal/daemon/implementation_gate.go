package daemon

import "sync"

// implementationGate tracks both OMP processes started by this daemon and
// surviving implementation processes adopted after a daemon restart.
type implementationGate struct {
	mu      sync.Mutex
	limit   int
	active  int
	local   int
	adopted map[int]struct{}
	changed chan struct{}
}

func newImplementationGate(limit int) *implementationGate {
	if limit < 1 {
		limit = 1
	}
	return &implementationGate{
		limit:   limit,
		adopted: make(map[int]struct{}),
		changed: make(chan struct{}),
	}
}

// tryAcquireLocal reserves capacity for a locally started implementation.
func (g *implementationGate) tryAcquireLocal() (bool, <-chan struct{}) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.active >= g.limit {
		return false, g.changed
	}
	g.active++
	g.local++
	return true, nil
}

func (g *implementationGate) releaseLocal() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.local == 0 {
		return
	}
	g.local--
	g.active--
	g.signalLocked()
}

// adopt accounts for a surviving process even when the configured limit was
// reduced below the current active count. New work stays blocked until enough
// adopted processes exit.
func (g *implementationGate) adopt(pid int) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, exists := g.adopted[pid]; exists {
		return false
	}
	g.adopted[pid] = struct{}{}
	g.active++
	return true
}

func (g *implementationGate) releaseAdopted(pid int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, exists := g.adopted[pid]; !exists {
		return
	}
	delete(g.adopted, pid)
	if g.active > 0 {
		g.active--
	}
	g.signalLocked()
}

// adoptedActive returns the count of active slots held by adopted processes.
func (g *implementationGate) adoptedActive() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.active - g.local
}

// localActive returns the count of active slots held by locally started processes.
func (g *implementationGate) localActive() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.local
}

func (g *implementationGate) signalLocked() {
	close(g.changed)
	g.changed = make(chan struct{})
}
