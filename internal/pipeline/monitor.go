// smoke-test: true
package pipeline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"gopkg.in/yaml.v3"
)

// MonitorResult captures the outcome of req_refine_count monitoring.
type MonitorResult struct {
	Tripped    bool   // true if count ≥ 3 triggered escalation
	FinalCount int    // count at monitor end
	Message    string // human-readable summary
}

// MonitorRefineCount watches a TASK markdown file for changes to the
// req_refine_count frontmatter field. If the count reaches 3 or more,
// it signals escalation (kill OMP → status=blocked → desktop notification).
//
// The process parameter is optional — in the smoke test it's nil.
// In production, a non-nil process would be killed on escalation.
func MonitorRefineCount(ctx context.Context, taskPath string, process *os.Process) (*MonitorResult, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("monitor: create watcher: %w", err)
	}
	defer watcher.Close()

	if err := watcher.Add(filepath.Dir(taskPath)); err != nil {
		return nil, fmt.Errorf("monitor: watch dir: %w", err)
	}

	// Read initial count.
	initialCount, err := readRefineCount(taskPath)
	if err != nil {
		return nil, fmt.Errorf("monitor: read initial count: %w", err)
	}

	result := &MonitorResult{FinalCount: initialCount}
	if initialCount >= 3 {
		result.Tripped = true
		result.Message = fmt.Sprintf("req_refine_count=%d 已达上限，触发升级", initialCount)
		return result, nil
	}

	// Watch for writes. Debounce rapid successive writes, then check threshold.
	var mu sync.Mutex
	var debounceTimer *time.Timer
	const debounceInterval = 200 * time.Millisecond

	// Channel to signal threshold trip from debounce callback.
	tripCh := make(chan int, 1)

	for {
		select {
		case <-ctx.Done():
			mu.Lock()
			final := result.FinalCount
			mu.Unlock()
			result.FinalCount = final
			return result, ctx.Err()
		case count := <-tripCh:
			mu.Lock()
			result.FinalCount = count
			mu.Unlock()
			if count >= 3 {
				result.Tripped = true
				result.Message = fmt.Sprintf("req_refine_count=%d 已达上限，触发升级", count)
				if process != nil {
					process.Kill()
				}
				return result, nil
			}
		case event, ok := <-watcher.Events:
			if !ok {
				return result, nil
			}
			if event.Op&fsnotify.Write == 0 {
				continue
			}
			// Filter only events for our target file (supports atomic-save editors).
			if filepath.Clean(event.Name) != filepath.Clean(taskPath) {
				continue
			}
			// Debounce: reset timer on each write.
			mu.Lock()
			if debounceTimer != nil {
				debounceTimer.Stop()
			}
			debounceTimer = time.AfterFunc(debounceInterval, func() {
				count, err := readRefineCount(taskPath)
				if err != nil {
					return
				}
				mu.Lock()
				result.FinalCount = count
				mu.Unlock()
				if count >= 3 {
					select {
					case tripCh <- count:
					default:
					}
				}
			})
			mu.Unlock()

		case err, ok := <-watcher.Errors:
			if !ok {
				return result, nil
			}
			return result, fmt.Errorf("monitor: watcher error: %w", err)
		}
	}
}

// readRefineCount parses the req_refine_count field from a TASK frontmatter.
// Uses retry logic to handle cloud-sync filesystems where WRITE events fire
// before the file is fully written.
func readRefineCount(taskPath string) (int, error) {
	const maxRetries = 5
	const retryDelay = 200 * time.Millisecond
	var data []byte
	var err error
	for i := range maxRetries {
		data, err = os.ReadFile(taskPath)
		if err == nil {
			break
		}
		if i < maxRetries-1 {
			time.Sleep(retryDelay)
		}
	}
	if err != nil {
		return 0, err
	}
	// Extract YAML frontmatter between --- delimiters.
	content := string(data)
	start := strings.Index(content, "---")
	if start != 0 {
		return 0, nil // no frontmatter
	}
	end := strings.Index(content[3:], "---")
	if end < 0 {
		return 0, nil
	}
	fmText := content[3 : 3+end]

	var fm map[string]interface{}
	if err := yaml.Unmarshal([]byte(fmText), &fm); err != nil {
		return 0, err
	}
	return extractInt(fm, "req_refine_count"), nil
}

// extractInt extracts an integer field from a frontmatter map, supporting both
// YAML int (interface{} -> int) and string representations (Obsidian quirk).
func extractInt(fm map[string]interface{}, key string) int {
	v, ok := fm[key]
	if !ok {
		return 0
	}
	switch val := v.(type) {
	case int:
		return val
	case float64:
		return int(val)
	case string:
		n, err := strconv.Atoi(val)
		if err != nil {
			return 0
		}
		return n
	default:
		return 0
	}
}
