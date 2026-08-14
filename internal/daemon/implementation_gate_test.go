package daemon

import "testing"

// TestImplementationGatePerProjectCapacity guards the per-project Round 2
// concurrency semantics: each project gets its own budget, so N projects run
// up to N*perLimit sessions in parallel instead of sharing one daemon-wide
// pool (the old behavior starved later projects while one project held every
// slot).
func TestImplementationGatePerProjectCapacity(t *testing.T) {
	g := newImplementationGate(0, 2) // no global cap, 2 per project
	for _, p := range []string{"proj-a", "proj-a", "proj-b", "proj-b"} {
		if ok, _ := g.tryAcquireLocal(p); !ok {
			t.Fatalf("tryAcquireLocal(%s) denied, want granted (2 per project)", p)
		}
	}
	if g.localActive() != 4 {
		t.Fatalf("localActive = %d, want 4 (two projects x two slots)", g.localActive())
	}
	// Third slot of either project must be denied.
	if ok, _ := g.tryAcquireLocal("proj-a"); ok {
		t.Fatal("third proj-a slot granted, want denied")
	}
	if ok, _ := g.tryAcquireLocal("proj-b"); ok {
		t.Fatal("third proj-b slot granted, want denied")
	}
	// Releasing one slot of proj-a re-opens only proj-a.
	g.releaseLocal("proj-a")
	if ok, _ := g.tryAcquireLocal("proj-a"); !ok {
		t.Fatal("proj-a slot after release denied, want granted")
	}
	if ok, _ := g.tryAcquireLocal("proj-b"); ok {
		t.Fatal("proj-b third slot granted after proj-a release, want denied")
	}
	if g.localActive() != 4 {
		t.Fatalf("localActive = %d, want 4", g.localActive())
	}
}

// TestImplementationGateGlobalCap guards the optional global ceiling: with a
// global cap set, capacity across projects is bounded even when each project
// is below its own limit.
func TestImplementationGateGlobalCap(t *testing.T) {
	g := newImplementationGate(3, 2)
	for _, p := range []string{"proj-a", "proj-a", "proj-b"} {
		if ok, _ := g.tryAcquireLocal(p); !ok {
			t.Fatalf("tryAcquireLocal(%s) denied, want granted (under global 3)", p)
		}
	}
	if ok, _ := g.tryAcquireLocal("proj-c"); ok {
		t.Fatal("fourth slot granted, want denied by global cap")
	}
	g.releaseLocal("proj-a")
	if ok, _ := g.tryAcquireLocal("proj-c"); !ok {
		t.Fatal("proj-c slot denied after release, want granted")
	}
}

// TestImplementationGateAdoptTracksProject guards restart adoption: surviving
// implementation processes consume their project's per-project budget until
// they exit.
func TestImplementationGateAdoptTracksProject(t *testing.T) {
	g := newImplementationGate(0, 1)
	if !g.adopt(1001, "proj-a") {
		t.Fatal("adopt 1001 denied")
	}
	if g.activeFor("proj-a") != 1 {
		t.Fatalf("activeFor(proj-a) = %d, want 1", g.activeFor("proj-a"))
	}
	// Project budget exhausted by the adopted process.
	if ok, _ := g.tryAcquireLocal("proj-a"); ok {
		t.Fatal("local slot granted while adopted process holds the project budget")
	}
	// Unrelated project unaffected.
	if ok, _ := g.tryAcquireLocal("proj-b"); !ok {
		t.Fatal("proj-b slot denied, want granted")
	}
	g.releaseAdopted(1001)
	if ok, _ := g.tryAcquireLocal("proj-a"); !ok {
		t.Fatal("proj-a slot denied after adopted process exited")
	}
}

// TestImplementationGateReleaseBalances guards counters after mixed
// acquire/release cycles: releases never drive totals negative and the gate
// returns to a clean state.
func TestImplementationGateReleaseBalances(t *testing.T) {
	g := newImplementationGate(4, 2)
	g.adopt(2001, "proj-a")
	g.adopt(2002, "proj-b")
	g.releaseAdopted(2001)
	g.releaseAdopted(2002)
	if g.localActive() != 0 || g.activeFor("proj-a") != 0 || g.activeFor("proj-b") != 0 {
		t.Fatalf("post-release: local=%d active=%v, want clean", g.localActive(), g.active)
	}
	// Double release must be a no-op.
	g.releaseLocal("proj-a")
	if g.localActive() != 0 {
		t.Fatalf("double release drifted: local=%d, want 0", g.localActive())
	}
	g.releaseAdopted(2001)
	if g.localActive() != 0 {
		t.Fatalf("double adopted release drifted: local=%d, want 0", g.localActive())
	}
	// Fresh acquisition still works after the churn.
	if ok, _ := g.tryAcquireLocal("proj-a"); !ok {
		t.Fatal("fresh acquisition denied after churn")
	}
	g.releaseLocal("proj-a")
}
