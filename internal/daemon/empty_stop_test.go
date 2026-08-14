package daemon

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// TestWatchEmptyStopsTriggersOnTwoEmptyReplies guards the empty-response
// fallback trigger: two empty-stop replies inside the window must fire the
// callback exactly once (the session is then cancelled and re-enters the
// fallback path), while a single reply or replies outside the window must
// not fire.
func TestWatchEmptyStopsTriggersOnTwoEmptyReplies(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "omp.log")

	write := func(lines ...string) {
		t.Helper()
		f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = f.Close() }()
		for _, l := range lines {
			if _, err := f.WriteString(l + "\n"); err != nil {
				t.Fatal(err)
			}
		}
	}
	empty := `{"timestamp":"2026-08-13T15:41:05+08:00","level":"debug","message":"agent_end maintenance routing","route":"empty-stop-handled","stopReason":"stop","contentBlocks":0,"hasText":false,"successfulYield":false}`
	normal := `{"timestamp":"2026-08-13T15:42:44+08:00","level":"debug","message":"agent_end maintenance routing","route":"successful-yield-no-active-goal","stopReason":"toolUse","contentBlocks":2,"hasToolCalls":true,"successfulYield":true}`

	var fired atomic.Int32
	done := make(chan struct{})
	defer close(done)
	// Log file must exist before the watcher opens it (seek-to-end).
	write(normal)

	// Shrink the window for the test; restore afterwards.
	oldWindow := emptyStopWindow
	emptyStopWindow = 3 * time.Second
	defer func() { emptyStopWindow = oldWindow }()

	go watchEmptyStops(logPath, func() { fired.Add(1) }, done)

	// Let the watcher goroutine open the file and seek to end first —
	// otherwise a slow goroutine start seeks past the lines we are about
	// to write and never sees them (the second test's false-positive trap).
	time.Sleep(500 * time.Millisecond)

	// One empty reply: not enough to fire.
	write(empty)
	time.Sleep(300 * time.Millisecond)
	if got := fired.Load(); got != 0 {
		t.Fatalf("single empty reply fired callback %d times, want 0", got)
	}

	// Second empty reply inside the window: fires exactly once. The watcher
	// polls on a 2s ticker, so allow two full tick cycles before failing.
	write(empty)
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) && fired.Load() == 0 {
		time.Sleep(100 * time.Millisecond)
	}
	if got := fired.Load(); got != 1 {
		t.Fatalf("second empty reply fired %d times, want 1", got)
	}

	// Watcher returns after firing; further writes are ignored.
	write(empty)
	time.Sleep(200 * time.Millisecond)
	if got := fired.Load(); got != 1 {
		t.Fatalf("callback fired %d times after trigger, want 1", got)
	}
}

// TestWatchEmptyStopsResetsOutsideWindow guards the window boundary: an
// empty reply older than emptyStopWindow does not count toward a trigger.
func TestWatchEmptyStopsResetsOutsideWindow(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "omp.log")
	if err := os.WriteFile(logPath, []byte("start\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	empty := `{"route":"empty-stop-handled","contentBlocks":0}`

	var fired atomic.Int32
	done := make(chan struct{})
	defer close(done)

	// Shrink the window for the test; restore afterwards.
	oldWindow := emptyStopWindow
	emptyStopWindow = 3 * time.Second
	defer func() { emptyStopWindow = oldWindow }()

	go watchEmptyStops(logPath, func() { fired.Add(1) }, done)

	// Let the watcher goroutine open+seek first (see first test).
	time.Sleep(500 * time.Millisecond)

	appendLine := func(s string) {
		t.Helper()
		f, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = f.Close() }()
		if _, err := f.WriteString(s + "\n"); err != nil {
			t.Fatal(err)
		}
	}

	appendLine(empty)
	// Simulate the first reply aging out: wait past the (shrunk) window,
	// then a second empty reply must NOT fire (first is stale). The
	// watcher's ticker is 2s, so wait window + 2 ticks + margin.
	time.Sleep(emptyStopWindow + 6*time.Second)
	appendLine(empty)
	time.Sleep(500 * time.Millisecond)
	if got := fired.Load(); got != 0 {
		t.Fatalf("stale-then-fresh empty replies fired %d times, want 0", got)
	}
}
