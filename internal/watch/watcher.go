// Package watch provides fsnotify-based file watching for Obsidian vaults.
package watch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Event represents a file change event.
type Event struct {
	Path      string
	Dir       string // "Tasks" or "Requirements"
	Operation string // "CREATE", "WRITE", "REMOVE"
}

// Watcher watches an Obsidian vault's Tasks/ and Requirements/ directories.
type Watcher struct {
	fsn     *fsnotify.Watcher
	events  chan Event
	debounce map[string]time.Time // per-directory last event time
	mu       sync.Mutex
	interval time.Duration
}

// New creates a new Watcher for the given vault path.
func New(vaultPath string, debounceInterval time.Duration) (*Watcher, error) {
	fsn, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	w := &Watcher{
		fsn:      fsn,
		events:   make(chan Event, 100),
		debounce: make(map[string]time.Time),
		interval: debounceInterval,
	}

	// Watch Tasks/ and Requirements/
	for _, dir := range []string{"Tasks", "Requirements"} {
		fullPath := filepath.Join(vaultPath, dir)
		if info, err := os.Stat(fullPath); err == nil && info.IsDir() {
			if err := fsn.Add(fullPath); err != nil {
				fsn.Close()
				return nil, err
			}
		}
	}

	return w, nil
}

// Events returns the channel of debounced file events.
func (w *Watcher) Events() <-chan Event {
	return w.events
}

// Start begins watching for file changes. Runs until ctx is cancelled.
func (w *Watcher) Start(ctx context.Context) {
	go w.loop(ctx)
}

func (w *Watcher) loop(ctx context.Context) {
	defer close(w.events)
	defer w.fsn.Close()

	for {
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-w.fsn.Events:
			if !ok {
				return
			}
			w.handle(evt)
		case err, ok := <-w.fsn.Errors:
			if !ok {
				return
			}
			// Log errors but keep running
			os.Stderr.WriteString("watcher error: " + err.Error() + "\n")
		}
	}
}

func (w *Watcher) handle(evt fsnotify.Event) {
	// Filter: only close_write and create events
	if evt.Op&(fsnotify.Create|fsnotify.Write) == 0 {
		return
	}

	path := evt.Name
	base := filepath.Base(path)

	// Skip temp files and hidden files
	if strings.HasPrefix(base, ".") || strings.HasPrefix(base, "sed") ||
		strings.HasSuffix(base, ".tmp") || strings.HasSuffix(base, ".swp") ||
		strings.HasSuffix(base, "~") {
		return
	}
	// Skip non-markdown
	if filepath.Ext(base) != ".md" {
		return
	}

	// Determine which directory
	var dir string
	parent := filepath.Base(filepath.Dir(path))
	switch parent {
	case "Tasks":
		dir = "Tasks"
	case "Requirements":
		dir = "Requirements"
	default:
		return // not in watched dir
	}

	// Per-directory debounce
	w.mu.Lock()
	last := w.debounce[dir]
	now := time.Now()
	if now.Sub(last) < w.interval {
		w.mu.Unlock()
		return
	}
	w.debounce[dir] = now
	w.mu.Unlock()

	op := "WRITE"
	if evt.Op&fsnotify.Create != 0 {
		op = "CREATE"
	}

	w.events <- Event{Path: path, Dir: dir, Operation: op}
}
