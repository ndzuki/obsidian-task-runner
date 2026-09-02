package daemon

import "sync"

// implementationGate bounds concurrent implementing (Round 2) execution sessions.
// Capacity is granted per project (perLimit) with an optional global cap
// (global, 0 = unlimited) across all projects, so N projects run up to
// N*perLimit sessions in parallel — each project's work cannot starve the
// others' (previously a single daemon-wide limit let one project occupy every
// slot). It tracks both executor processes started by this daemon and surviving
// implementation processes adopted after a daemon restart.
type implementationGate struct {
	mu       sync.Mutex
	perLimit int            // per-project cap; <=0 = unlimited per project
	global   int            // total cap across all projects; <=0 = unlimited
	local    map[string]int // project → locally started active count
	active   map[string]int // project → active count (local + adopted)
	total    int            // active across all projects (global cap check)
	adopted  map[int]string // pid → project
	changed  chan struct{}
}

func newImplementationGate(global, perProject int) *implementationGate {
	return &implementationGate{
		perLimit: perProject,
		global:   global,
		local:    make(map[string]int),
		active:   make(map[string]int),
		adopted:  make(map[int]string),
		changed:  make(chan struct{}),
	}
}

// tryAcquireLocal reserves capacity for a locally started implementation of
// the given project. It succeeds when the project is below its per-project
// cap and the global total is below the optional global cap.
func (g *implementationGate) tryAcquireLocal(project string) (bool, <-chan struct{}) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.perLimit > 0 && g.active[project] >= g.perLimit {
		return false, g.changed
	}
	if g.global > 0 && g.total >= g.global {
		return false, g.changed
	}
	g.local[project]++
	g.active[project]++
	g.total++
	return true, nil
}

func (g *implementationGate) releaseLocal(project string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.local[project] == 0 {
		return
	}
	g.local[project]--
	g.active[project]--
	if g.active[project] == 0 {
		delete(g.active, project)
	}
	if g.total > 0 {
		g.total--
	}
	g.signalLocked()
}

// adopt accounts for a surviving process even when the configured limits were
// reduced below the current active counts. New work stays blocked until
// enough adopted processes exit.
func (g *implementationGate) adopt(pid int, project string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, exists := g.adopted[pid]; exists {
		return false
	}
	g.adopted[pid] = project
	g.active[project]++
	g.total++
	return true
}

func (g *implementationGate) releaseAdopted(pid int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	project, exists := g.adopted[pid]
	if !exists {
		return
	}
	delete(g.adopted, pid)
	g.active[project]--
	if g.active[project] == 0 {
		delete(g.active, project)
	}
	if g.total > 0 {
		g.total--
	}
	g.signalLocked()
}

// localActive returns the count of active slots held by locally started
// processes, summed across projects.
func (g *implementationGate) localActive() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	total := 0
	for _, n := range g.local {
		total += n
	}
	return total
}

// activeFor reports the active count for one project (local + adopted).
func (g *implementationGate) activeFor(project string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.active[project]
}

func (g *implementationGate) signalLocked() {
	close(g.changed)
	g.changed = make(chan struct{})
}
