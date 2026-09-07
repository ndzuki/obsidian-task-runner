package daemon

import (
	"path/filepath"
	"testing"
	"time"
)

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

// TestNotifyKeyUnavailableDebounced verifies the API-key toast is emitted at
// most once per 5-minute window regardless of how many tasks fail — the
// anti-storm guard for batch key outages.
func TestNotifyKeyUnavailableDebounced(t *testing.T) {
	dir := t.TempDir()
	skillDir := writeVaultMap(t, dir, nil)
	runner := newTestRunner(skillDir, "dsh", filepath.Join(dir, "logs"), 1)
	runner.cfg.Notifications.Desktop = false // SendTaskAction becomes a no-op

	runner.notifyKeyUnavailable()
	first, ok := runner.keyNotifyAt.Load("key")
	if !ok {
		t.Fatal("first call did not record the debounce timestamp")
	}
	// Second call inside the window: timestamp must not advance (no new toast).
	runner.notifyKeyUnavailable()
	second, _ := runner.keyNotifyAt.Load("key")
	if !first.(time.Time).Equal(second.(time.Time)) {
		t.Fatal("debounce timestamp advanced inside the 5-minute window")
	}
	// Expire the window: next call refreshes the timestamp (new toast).
	runner.keyNotifyAt.Store("key", time.Now().Add(-6*time.Minute))
	runner.notifyKeyUnavailable()
	third, _ := runner.keyNotifyAt.Load("key")
	if time.Since(third.(time.Time)) > time.Minute {
		t.Fatal("debounce timestamp not refreshed after window expiry")
	}
}
