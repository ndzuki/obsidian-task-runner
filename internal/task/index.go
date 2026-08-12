package task

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

// frontmatterHeadSize is the read budget for the frontmatter-only fast path.
// Task frontmatter is a few KB; 8 KiB covers it comfortably. Files whose
// frontmatter does not close within the head are re-read in full.
const frontmatterHeadSize = 8 * 1024

type taskEntry struct {
	mtime time.Time
	size  int64
	fm    *yamlfrontmatter.Frontmatter
}

// Index caches parsed TASK frontmatter keyed by absolute path, validated by
// mtime+size on every scan. Watcher events invalidate entries eagerly so a
// write-back is visible on the very next scan; a lost event is repaired by
// the stat check. Scans therefore avoid re-reading unchanged documents, and
// dependency checks inside IsReady reuse the same cache instead of touching
// disk again.
//
// Not safe for concurrent Scan calls; Invalidate may run concurrently.
type Index struct {
	mu    sync.Mutex
	tasks map[string]*taskEntry

	// GatedPaths lists task files that are schedulable by status but held
	// back by unresolved blocked_by dependencies (filled by the last Scan).
	// The daemon logs them so a "ready but not dispatched" task is explainable.
	GatedPaths []string
}

// NewIndex returns an empty index.
func NewIndex() *Index { return &Index{tasks: make(map[string]*taskEntry)} }

// Invalidate drops the cached entry for path (e.g. on a watcher event),
// forcing the next scan to re-read the file.
func (idx *Index) Invalidate(path string) {
	idx.mu.Lock()
	delete(idx.tasks, path)
	idx.mu.Unlock()
}

// lookup returns the frontmatter for path, stat-validating the cache like a
// scan would. Dependency checks run ahead of the main scan loop (a task may
// reference a file the scan has not visited yet), so a bare cache read could
// serve a stale entry; the mtime+size check keeps lookups fresh.
func (idx *Index) lookup(path string) (*yamlfrontmatter.Frontmatter, bool) {
	return idx.frontmatter(path)
}

// Scan walks every Projects/*/Tasks/ directory and returns ready tasks.
// Unchanged files reuse the cached frontmatter; changed or new files are
// re-read (frontmatter head only) and the cache is updated. Dependency
// checks inside IsReady use the cache via lookup, avoiding repeated reads.
func (idx *Index) Scan(vaultPath string) ([]ReadyTask, error) {
	projectsDir := filepath.Join(vaultPath, "Projects")
	projEntries, err := os.ReadDir(projectsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read Projects dir: %w", err)
	}

	idx.GatedPaths = idx.GatedPaths[:0]
	var ready []ReadyTask
	for _, proj := range projEntries {
		if !proj.IsDir() {
			continue
		}
		tasksDir := filepath.Join(projectsDir, proj.Name(), "Tasks")
		entries, err := os.ReadDir(tasksDir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" {
				continue
			}
			filePath := filepath.Join(tasksDir, entry.Name())
			fm, ok := idx.frontmatter(filePath)
			if !ok {
				continue
			}
			if !isReadyWith(fm, vaultPath, idx.lookup) {
				// Schedulable status but unresolved dependencies: record so the
				// daemon can explain why a ready-looking task is not dispatched.
				if dependencyGated(fm, vaultPath, idx.lookup) {
					idx.GatedPaths = append(idx.GatedPaths, filePath)
				}
				continue
			}
			if fm.Project == "" {
				continue
			}
			ready = append(ready, buildReadyTask(fm, filePath, entry.Name()))
		}
	}
	sort.Slice(ready, func(i, j int) bool {
		// Stage ordering is project-scoped: "P2" in one project has no
		// relation to "P1" in another, so cross-project candidates keep the
		// legacy priority-then-created fairness instead of comparing stages
		// globally. Only within a project does stage outrank priority.
		if ready[i].Project != ready[j].Project {
			pi := priorityOrder(ready[i].Priority)
			pj := priorityOrder(ready[j].Priority)
			if pi != pj {
				return pi < pj
			}
			if ready[i].Created != ready[j].Created {
				return ready[i].Created < ready[j].Created
			}
			return ready[i].Project < ready[j].Project
		}
		si, sj := stageOrder(ready[i].Stage), stageOrder(ready[j].Stage)
		if si != sj {
			return si < sj
		}
		pi := priorityOrder(ready[i].Priority)
		pj := priorityOrder(ready[j].Priority)
		if pi != pj {
			return pi < pj
		}
		if ready[i].Created != ready[j].Created {
			return ready[i].Created < ready[j].Created
		}
		if ready[i].ID != ready[j].ID {
			return ready[i].ID < ready[j].ID
		}
		return ready[i].FilePath < ready[j].FilePath
	})
	return ready, nil
}

// dependencyGated reports whether fm is schedulable by status but held back
// by unresolved blocked_by dependencies (the IsReady dependency gate).
func dependencyGated(fm *yamlfrontmatter.Frontmatter, vaultPath string, lookup fmLookup) bool {
	if fm == nil || fm.Project == "" {
		return false
	}
	switch fm.Status {
	case "ready", "refining", "planning":
	default:
		return false
	}
	if isEmptyList(fm.BlockedBy) {
		return false
	}
	return !areBlockersDoneWith(vaultPath, fm.Project, fm.BlockedBy, lookup)
}

// frontmatter returns the parsed frontmatter for path, re-reading from disk
// only when the file changed since the cached entry (mtime or size differs).
// Removed/unreadable files drop their entry.
func (idx *Index) frontmatter(path string) (*yamlfrontmatter.Frontmatter, bool) {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	st, err := os.Stat(path)
	if err != nil {
		delete(idx.tasks, path)
		return nil, false
	}
	if e, ok := idx.tasks[path]; ok && e.mtime.Equal(st.ModTime()) && e.size == st.Size() {
		return e.fm, true
	}
	fm, err := readFrontmatter(path)
	if err != nil || fm == nil {
		delete(idx.tasks, path)
		return nil, false
	}
	idx.tasks[path] = &taskEntry{mtime: st.ModTime(), size: st.Size(), fm: fm}
	return fm, true
}

// frontmatterFor returns the frontmatter for path, preferring the index cache
// when lookup is non-nil and falling back to a disk read otherwise.
func frontmatterFor(path string, lookup fmLookup) (*yamlfrontmatter.Frontmatter, error) {
	if lookup != nil {
		if fm, ok := lookup(path); ok {
			return fm, nil
		}
	}
	return readFrontmatter(path)
}

// readFrontmatter reads only the file head when the frontmatter closes within
// it, falling back to a full read otherwise. Transient read failures retry
// like the pre-index readFileWithRetry (cloud-sync filesystems fire WRITE
// events before the write completes); parse errors are not retried — a
// half-written frontmatter self-heals on the next scan/event.
func readFrontmatter(path string) (*yamlfrontmatter.Frontmatter, error) {
	data, err := readFrontmatterData(path)
	if err != nil {
		return nil, err
	}
	return yamlfrontmatter.Parse(data)
}

// readFrontmatterData reads the frontmatter head with bounded retries on
// transient read failures.
func readFrontmatterData(path string) ([]byte, error) {
	const maxRetries = 5
	const retryDelay = 200 * time.Millisecond
	var lastErr error
	for attempt := range maxRetries {
		if attempt > 0 {
			time.Sleep(retryDelay)
		}
		data, err := readFrontmatterDataOnce(path)
		if err == nil {
			return data, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

func readFrontmatterDataOnce(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	head := make([]byte, frontmatterHeadSize)
	n, _ := io.ReadFull(f, head)
	_ = f.Close()
	data := head[:n]
	if !strings.Contains(string(data), "\n---") {
		full, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		data = full
	}
	return data, nil
}

// buildReadyTask assembles a ReadyTask from a parsed frontmatter.
func buildReadyTask(fm *yamlfrontmatter.Frontmatter, filePath, fileName string) ReadyTask {
	return ReadyTask{
		ID: fm.ID, Title: fm.Title, Project: fm.Project,
		NewProject: fm.NewProject, Priority: fm.Priority, Created: fm.Created,
		Stage:    fm.Stage,
		FilePath: filePath, FileName: fileName,
		Status: fm.Status, PlanApproved: fm.PlanApproved,
		MergeApproved: fm.MergeApproved, CloseApproved: fm.CloseApproved,
		ReqDoc: fm.ReqDoc, Template: fm.Template, Assignee: fm.Assignee,
		AutoApprove: fm.AutoApprove, AutoMerge: fm.AutoMerge, PendingReq: fm.PendingReq,
		PhaseErrorCode: fm.PhaseErrorCode,
		GrillDone:      fm.GrillDone, GrillPrevStatus: fm.GrillPrevStatus,
		GrillResolution: fm.GrillResolution, GrillContext: fm.GrillContext,
		GrillContinue: fm.GrillContinue, GrillParked: fm.GrillParked,
		PlanVersion:              fm.PlanVersion,
		PriorityAssessmentStatus: fm.PriorityAssessmentStatus,
		GrillHeartbeatAt:         fm.GrillHeartbeatAt,
		GrillTimeoutMinutes:      fm.GrillTimeoutMinutes,
		RefineReqHash:            fm.RefineReqHash,
		PlanReqHash:              fm.PlanReqHash,
		MergeRetryCount:          fm.MergeRetryCount,
		Maturity:                 fm.Maturity,
		ReviewFeedback:           fm.ReviewFeedback, ReworkResolution: fm.ReworkResolution,
		ClosureReason: fm.ClosureReason,
	}
}
