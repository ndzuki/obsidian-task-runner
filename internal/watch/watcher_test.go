package watch

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

func newTestWatcher(t *testing.T) *Watcher {
	t.Helper()
	fsn, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatalf("fsnotify.NewWatcher: %v", err)
	}
	t.Cleanup(func() { _ = fsn.Close() })
	return &Watcher{
		fsn:      fsn,
		events:   make(chan Event, 10),
		debounce: make(map[string]time.Time),
		interval: 0,
	}
}

func drainEvent(t *testing.T, w *Watcher) (Event, bool) {
	t.Helper()
	select {
	case evt := <-w.events:
		return evt, true
	default:
		return Event{}, false
	}
}

// TestHandleRoutesReferencesFile guards knowledge-base intake: a write under
// References/ (any subdirectory depth) must surface as a watcher event so the
// daemon rebuilds INDEX and syncs the retrieval store automatically.
func TestHandleRoutesReferencesFile(t *testing.T) {
	w := newTestWatcher(t)
	ref := filepath.Join(t.TempDir(), "References", "core", "go", "probe-ref.md")

	w.handle(fsnotify.Event{Name: ref, Op: fsnotify.Write})

	evt, ok := drainEvent(t, w)
	if !ok {
		t.Fatal("expected event for References file")
	}
	if evt.Dir != "References" || evt.Path != ref || evt.Operation != "WRITE" {
		t.Fatalf("event = %+v, want dir=References path=%s op=WRITE", evt, ref)
	}
}

func TestHandleRoutesProjectRootReq(t *testing.T) {
	w := newTestWatcher(t)
	vault := t.TempDir()
	req := filepath.Join(vault, "Projects", "010-demo", "REQ-001-demo.md")

	w.handle(fsnotify.Event{Name: req, Op: fsnotify.Create})

	evt, ok := drainEvent(t, w)
	if !ok {
		t.Fatal("expected event for project-root REQ file")
	}
	if evt.Dir != "Requirements" || evt.Path != req || evt.Operation != "CREATE" {
		t.Fatalf("event = %+v, want dir=Requirements path=%s op=CREATE", evt, req)
	}
}

func TestHandleRoutesRequirementsDirReq(t *testing.T) {
	w := newTestWatcher(t)
	vault := t.TempDir()
	req := filepath.Join(vault, "Projects", "010-demo", "Requirements", "REQ-001-demo.md")

	w.handle(fsnotify.Event{Name: req, Op: fsnotify.Write})

	evt, ok := drainEvent(t, w)
	if !ok {
		t.Fatal("expected event for Requirements/ REQ file")
	}
	if evt.Dir != "Requirements" || evt.Operation != "WRITE" {
		t.Fatalf("event = %+v, want dir=Requirements op=WRITE", evt)
	}
}

func TestHandleDropsNonReqProjectFiles(t *testing.T) {
	w := newTestWatcher(t)
	vault := t.TempDir()

	for _, name := range []string{"README.md", "Notes.md", "TASK-001-x.md"} {
		w.handle(fsnotify.Event{Name: filepath.Join(vault, "Projects", "010-demo", name), Op: fsnotify.Create})
		if evt, ok := drainEvent(t, w); ok {
			t.Fatalf("unexpected event for %s: %+v", name, evt)
		}
	}
}

func TestHandleAddsNewDirectories(t *testing.T) {
	w := newTestWatcher(t)
	dir := filepath.Join(t.TempDir(), "brand-new-dir")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}

	// Directory creation is consumed by the auto-watch path, not emitted.
	w.handle(fsnotify.Event{Name: dir, Op: fsnotify.Create})
	if evt, ok := drainEvent(t, w); ok {
		t.Fatalf("directory create must not emit an event, got %+v", evt)
	}

	// The directory is now watched: WatchList contains it.
	if !slices.Contains(w.fsn.WatchList(), dir) {
		t.Fatalf("directory %s not in watch list: %v", dir, w.fsn.WatchList())
	}
}

// TestHandleNewDirThenFileEvent verifies the full discovery path: a REQ
// written into a directory created after daemon start still surfaces, as
// long as a Create/Write event reaches the handle (the inotify race where
// the file lands between mkdir and Add is covered by the daemon's
// periodic scan fallback, not by the watcher).
func TestHandleNewDirThenFileEvent(t *testing.T) {
	w := newTestWatcher(t)
	dir := filepath.Join(t.TempDir(), "010-demo")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	w.handle(fsnotify.Event{Name: dir, Op: fsnotify.Create})

	req := filepath.Join(dir, "REQ-001-demo.md")
	w.handle(fsnotify.Event{Name: req, Op: fsnotify.Write})
	evt, ok := drainEvent(t, w)
	if !ok || evt.Dir != "Requirements" || evt.Path != req {
		t.Fatalf("event = %+v, want dir=Requirements path=%s", evt, req)
	}
}
