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

type Event struct {
	Path      string
	Dir       string
	Operation string
}

type Watcher struct {
	fsn      *fsnotify.Watcher
	events   chan Event
	debounce map[string]time.Time
	mu       sync.Mutex
	interval time.Duration
}

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

	// Watch Projects/ and all subdirectories recursively
	projectsPath := filepath.Join(vaultPath, "Projects")
	// Watch Projects/ itself (catches new project dirs)
	fsn.Add(projectsPath)
	// Walk and add existing subdirs
	filepath.Walk(projectsPath, func(p string, info os.FileInfo, err error) error {
		if err != nil { return nil }
		if info.IsDir() { fsn.Add(p) }
		return nil
	})
	// Backward compat: old flat structure
	for _, d := range []string{"Tasks", "Requirements"} {
		p := filepath.Join(vaultPath, d)
		if _, err := os.Stat(p); err == nil { fsn.Add(p) }
	}

	return w, nil
}

func (w *Watcher) Events() <-chan Event   { return w.events }
func (w *Watcher) Start(ctx context.Context) { go w.loop(ctx) }

func (w *Watcher) loop(ctx context.Context) {
	defer close(w.events)
	defer w.fsn.Close()
	for {
		select {
		case <-ctx.Done(): return
		case evt, ok := <-w.fsn.Events:
			if !ok { return }
			w.handle(evt)
		case err, ok := <-w.fsn.Errors:
			if !ok { return }
			os.Stderr.WriteString("watcher error: " + err.Error() + "\n")
		}
	}
}

func (w *Watcher) handle(evt fsnotify.Event) {
	path := evt.Name

	// Auto-watch new directories under Projects/
	if evt.Op&fsnotify.Create != 0 {
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			w.fsn.Add(path)
			return
		}
	}

	if evt.Op&(fsnotify.Create|fsnotify.Write) == 0 { return }
	base := filepath.Base(path)
	if strings.HasPrefix(base, ".") || strings.HasPrefix(base, "sed") ||
		strings.HasSuffix(base, ".tmp") || strings.HasSuffix(base, ".swp") ||
		strings.HasSuffix(base, "~") || filepath.Ext(base) != ".md" { return }

	parent := filepath.Base(filepath.Dir(path))
	var dir string
	switch parent {
	case "Tasks": dir = "Tasks"
	case "Requirements": dir = "Requirements"
	default: return
	}

	w.mu.Lock()
	last := w.debounce[path]
	now := time.Now()
	if now.Sub(last) < w.interval { w.mu.Unlock(); return }
	w.debounce[path] = now
	w.mu.Unlock()

	op := "WRITE"
	if evt.Op&fsnotify.Create != 0 { op = "CREATE" }
	w.events <- Event{Path: path, Dir: dir, Operation: op}
}
