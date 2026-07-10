// Package logutil provides a rotating log writer with compression and cleanup.
package logutil

import (
	"compress/gzip"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// RotatingWriter implements io.Writer with automatic rotation, compression,
// and age-based cleanup. Inspired by lumberjack but simpler.
type RotatingWriter struct {
	mu       sync.Mutex
	file     *os.File
	path     string
	maxSize  int64  // max bytes before rotation
	maxFiles int    // max rotated files to keep (0 = unlimited)
	maxAge   int    // max days to keep (0 = unlimited)
	written  int64  // bytes written to current file
}

// NewRotatingWriter creates a writer that writes to path, rotates when
// file exceeds maxSize bytes, keeps up to maxFiles rotated copies,
// and deletes files older than maxAge days.
func NewRotatingWriter(path string, maxSizeMB int, maxFiles int, maxAgeDays int) (*RotatingWriter, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create log dir %s: %w", dir, err)
	}

	w := &RotatingWriter{
		path:     path,
		maxSize:  int64(maxSizeMB) * 1024 * 1024,
		maxFiles: maxFiles,
		maxAge:   maxAgeDays,
	}

	if err := w.open(); err != nil {
		return nil, err
	}

	// Clean old logs on startup
	w.cleanOldLogs()
	return w, nil
}

func (w *RotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.file == nil {
		if err := w.open(); err != nil {
			return 0, err
		}
	}

	n, err := w.file.Write(p)
	w.written += int64(n)

	if w.maxSize > 0 && w.written >= w.maxSize {
		w.rotate()
	}

	return n, err
}

func (w *RotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file != nil {
		return w.file.Close()
	}
	return nil
}

func (w *RotatingWriter) open() error {
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	w.file = f
	w.written = info.Size()
	return nil
}

func (w *RotatingWriter) rotate() {
	if w.file != nil {
		w.file.Close()
		w.file = nil
	}

	// Shift existing rotated files
	baseFile := filepath.Base(w.path)
	dir := filepath.Dir(w.path)

	// Compress the current file
	compressedPath := w.path + ".1.gz"
	w.compressFile(w.path, compressedPath)

	// Shift older files: .1.gz → .2.gz, .2.gz → .3.gz, etc.
	for i := w.maxFiles; i > 1; i-- {
		old := fmt.Sprintf("%s.%d.gz", w.path, i-1)
		new := fmt.Sprintf("%s.%d.gz", w.path, i)
		if _, err := os.Stat(old); err == nil {
			os.Rename(old, new)
		}
	}

	// Clean up excess rotated files
	if w.maxFiles > 0 {
		for i := w.maxFiles; i < w.maxFiles+5; i++ {
			path := fmt.Sprintf("%s.%d.gz", w.path, i)
			os.Remove(path)
		}
	}

	// Open new file
	if err := w.open(); err != nil {
		fmt.Fprintf(os.Stderr, "logutil: failed to reopen after rotation: %v\n", err)
	}

	// Clean old files
	w.cleanOldLogs()

	_ = baseFile
	_ = dir
}

func (w *RotatingWriter) compressFile(src, dst string) {
	data, err := os.ReadFile(src)
	if err != nil {
		return
	}

	out, err := os.Create(dst)
	if err != nil {
		return
	}
	defer out.Close()

	gw := gzip.NewWriter(out)
	defer gw.Close()

	if _, err := gw.Write(data); err != nil {
		return
	}

	// Truncate source after successful compression
	os.Truncate(src, 0)
}

func (w *RotatingWriter) cleanOldLogs() {
	if w.maxAge <= 0 {
		return
	}

	dir := filepath.Dir(w.path)
	prefix := filepath.Base(w.path)

	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	cutoff := time.Now().AddDate(0, 0, -w.maxAge)

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		// Skip the active log file
		if name == prefix {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}

		// Delete by age
		if info.ModTime().Before(cutoff) {
			path := filepath.Join(dir, name)
			os.Remove(path)
		}
	}

	// Also enforce maxFiles
	if w.maxFiles > 0 {
		var rotated []os.DirEntry
		for _, entry := range entries {
			name := entry.Name()
			if !entry.IsDir() && strings.HasPrefix(name, prefix+".") {
				rotated = append(rotated, entry)
			}
		}
		// Sort by name (older first)
		sort.Slice(rotated, func(i, j int) bool {
			return rotated[i].Name() < rotated[j].Name()
		})
		// Delete excess
		for i := w.maxFiles; i < len(rotated); i++ {
			os.Remove(filepath.Join(dir, rotated[i].Name()))
		}
	}
}
