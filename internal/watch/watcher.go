// Package watch provides fsnotify-based file watching for Obsidian vaults.
package watch

import (
	"context"
	"fmt"
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

	// Watch Projects/ and all subdirectories recursively.
	projectsPath := filepath.Join(vaultPath, "Projects")
	if err := fsn.Add(projectsPath); err != nil {
		_ = fsn.Close()
		return nil, fmt.Errorf("watch Projects directory: %w", err)
	}
	if err := filepath.Walk(projectsPath, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			if err := fsn.Add(p); err != nil {
				return fmt.Errorf("watch directory %s: %w", p, err)
			}
		}
		return nil
	}); err != nil {
		_ = fsn.Close()
		return nil, err
	}
	// Backward compatibility: old flat structure.
	for _, d := range []string{"Tasks", "Requirements"} {
		p := filepath.Join(vaultPath, d)
		if _, err := os.Stat(p); err == nil {
			if err := fsn.Add(p); err != nil {
				_ = fsn.Close()
				return nil, fmt.Errorf("watch legacy directory %s: %w", p, err)
			}
		}
	}

	return w, nil
}

func (w *Watcher) Events() <-chan Event      { return w.events }
func (w *Watcher) Start(ctx context.Context) { go w.loop(ctx) }

func (w *Watcher) loop(ctx context.Context) {
	defer close(w.events)
	defer func() {
		if err := w.fsn.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "watcher close error: %v\n", err)
		}
	}()
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
			if _, writeErr := os.Stderr.WriteString("watcher error: " + err.Error() + "\n"); writeErr != nil {
				return
			}
		}
	}
}

func (w *Watcher) handle(evt fsnotify.Event) {
	path := evt.Name

	// Auto-watch new directories under Projects/
	if evt.Op&fsnotify.Create != 0 {
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			if err := w.fsn.Add(path); err != nil {
				fmt.Fprintf(os.Stderr, "watcher add directory %s: %v\n", path, err)
			}
			return
		}
	}

	if evt.Op&(fsnotify.Create|fsnotify.Write) == 0 {
		return
	}
	base := filepath.Base(path)
	if strings.HasPrefix(base, ".") || strings.HasPrefix(base, "sed") ||
		strings.HasSuffix(base, ".tmp") || strings.HasSuffix(base, ".swp") ||
		strings.HasSuffix(base, "~") || filepath.Ext(base) != ".md" {
		return
	}

	parent := filepath.Base(filepath.Dir(path))
	var dir string
	switch parent {
	case "Tasks":
		dir = "Tasks"
	case "Requirements":
		dir = "Requirements"
	default:
		return
	}

	w.mu.Lock()
	last := w.debounce[path]
	now := time.Now()
	if now.Sub(last) < w.interval {
		w.mu.Unlock()
		return
	}
	w.debounce[path] = now
	w.mu.Unlock()

	op := "WRITE"
	if evt.Op&fsnotify.Create != 0 {
		op = "CREATE"
	}
	w.events <- Event{Path: path, Dir: dir, Operation: op}
}
