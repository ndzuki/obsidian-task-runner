package daemon

import "testing"

func TestPhaseGateEnforcesLimit(t *testing.T) {
	g := newPhaseGate(2)
	for range 2 {
		if ok, _ := g.tryAcquire(); !ok {
			t.Fatalf("acquire failed, want success (limit 2)")
		}
	}
	if ok, _ := g.tryAcquire(); ok {
		t.Fatal("third acquire succeeded, want blocked at limit 2")
	}
	g.release()
	if ok, _ := g.tryAcquire(); !ok {
		t.Fatal("acquire after release failed, want success")
	}
	g.release()
	g.release() // extra release must be a no-op, not underflow
	if ok, _ := g.tryAcquire(); !ok {
		t.Fatal("acquire after over-release failed, want success")
	}
}

func TestPhaseGateChangedChannelClosesOnRelease(t *testing.T) {
	g := newPhaseGate(1)
	if ok, _ := g.tryAcquire(); !ok {
		t.Fatal("acquire failed")
	}
	if ok, _ := g.tryAcquire(); ok {
		t.Fatal("second acquire succeeded, want blocked")
	}
	changed := g.changed
	select {
	case <-changed:
		t.Fatal("changed channel closed before release")
	default:
	}
	g.release()
	select {
	case <-changed:
	default:
		t.Fatal("changed channel not closed after release")
	}
}

func TestPhaseGateZeroLimitCoercedToOne(t *testing.T) {
	g := newPhaseGate(0)
	if ok, _ := g.tryAcquire(); !ok {
		t.Fatal("gate with limit 0 coerced to 1 should accept one acquire")
	}
	if ok, _ := g.tryAcquire(); ok {
		t.Fatal("gate with limit 0 coerced to 1 should block a second acquire")
	}
}
