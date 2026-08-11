package daemon

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/ndzuki/obsidian-task-runner/internal/config"
	"github.com/ndzuki/obsidian-task-runner/internal/knowledge"
	"github.com/ndzuki/obsidian-task-runner/internal/logutil"
	"github.com/ndzuki/obsidian-task-runner/internal/notify"
	"github.com/ndzuki/obsidian-task-runner/internal/project"
	"github.com/ndzuki/obsidian-task-runner/internal/task"
	"github.com/ndzuki/obsidian-task-runner/internal/watch"
	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

type Runner struct {
	cfg                *config.Config
	logger             *log.Logger
	logWriter          *logutil.RotatingWriter
	taskRuns           sync.Map
	repoLocks          sync.Map
	scanMu             sync.Mutex // prevents overlapping scanAndProcess calls
	scanGateMu         sync.Mutex // serializes scan cycles; see requestScan
	scanActive         bool
	scanPending        bool
	lastScanAt         time.Time // last scan cycle start; throttles watcher bursts
	scanTimer          *time.Timer // deferred scan after minScanInterval (nil = none pending)
	scanMinInterval    time.Duration // watcher scan throttle; 0 disables (tests)
	worktreeCache      sync.Map   // taskRunKey → worktreePath (parallel warmup)
	implementationGate *implementationGate
	phaseGates         map[string]*phaseGate // phase → concurrency gate (nil/absent = unlimited)
	daemonCtx          context.Context // bound to daemon lifecycle; cancelled on shutdown
	phaseFailures      sync.Map   // taskPath → time.Time (cooldown after phase failure)
	grillNotified      sync.Map   // taskID → time.Time (last grilling notification)
	keyNotifyAt        sync.Map   // "key" → time.Time (API-key-unavailable toast debounce)
	refNotifyAt        sync.Map   // refPath → time.Time (knowledge intake validation toast debounce)
	refIndexRebuiltAt  sync.Map   // "last" → time.Time (References INDEX rebuild debounce)
	kbSyncAt           sync.Map   // "last" → time.Time (knowledge retrieval-store sync debounce)
	kbSyncRunning      atomic.Bool // true while a retrieval-store sync goroutine is in flight
	consolidatedAt     sync.Map   // reqDoc → time.Time (last PM consolidate dispatch per group)
	pmInFlight         sync.Map   // "distribute:<listPath>" / "consolidate:<taskPaths>" → true (PM session in flight)
	diagNotifyAt       sync.Map   // "project|key" → date (dependency-health / overlap / health-warning toast debounce)
	activeTasks        atomic.Int32 // dispatched task goroutines still running (shutdown drain)
	taskIdx            *task.Index  // frontmatter cache: watcher events invalidate, scans reuse
	gatedLogged        map[string]bool // task paths whose dependency-gate log was emitted
}

// Path tokens for watcher-event routing, built with the platform separator
// so daemon logic works identically on Windows and Unix.
const (
	tasksDirToken = string(filepath.Separator) + "Tasks" + string(filepath.Separator)
	notesDirToken = string(filepath.Separator) + "Notes" + string(filepath.Separator)
	adrDirToken   = string(filepath.Separator) + "Notes" + string(filepath.Separator) + "adr" + string(filepath.Separator)
	refsDirToken  = string(filepath.Separator) + "References" + string(filepath.Separator)
)

type repoLockMode uint8

const (
	repoLockNone repoLockMode = iota
	repoLockRead
	repoLockWrite
)

type preparedTask struct {
	task                   task.ReadyTask
	repoDir                string
	workDir                string
	lockMode               repoLockMode
	implementationReserved bool
}

func New(cfg *config.Config) *Runner {
	// Off-peak readiness uses the configured windows/timezone instead of the
	// hardcoded Beijing window.
	windows := cfg.OffPeakWindows
	tz := cfg.OffPeakTimezone
	task.OffPeakFn = func() bool { return task.IsOffPeakWith(windows, tz) }
	gates := make(map[string]*phaseGate)
	for phase, limit := range cfg.PhaseConcurrency {
		if limit > 0 {
			gates[phase] = newPhaseGate(limit)
		}
	}
	return &Runner{
		cfg:                cfg,
		implementationGate: newImplementationGate(cfg.MaxConcurrentTasks),
		phaseGates:         gates,
		daemonCtx:          context.Background(),
		taskIdx:            task.NewIndex(),
		gatedLogged:        map[string]bool{},
		scanMinInterval:    time.Duration(cfg.ScanMinIntervalSeconds) * time.Second,
	}
}

func (r *Runner) Run(ctx context.Context) error {
	if err := r.initLogging(); err != nil {
		return fmt.Errorf("init logging: %w", err)
	}
	// Optional pprof endpoint for heap/memory investigation, e.g.
	// OTG_PPROF_ADDR=127.0.0.1:6060 → go tool pprof http://127.0.0.1:6060/debug/pprof/heap
	if addr := os.Getenv("OTG_PPROF_ADDR"); addr != "" {
		go func() {
			r.logger.Printf("pprof listening on %s", addr)
			if err := http.ListenAndServe(addr, nil); err != nil {
				r.logger.Printf("pprof server: %v", err)
			}
		}()
	}
	defer func() {
		if err := r.logWriter.Close(); err != nil {
			r.logger.Printf("close log writer: %v", err)
		}
	}()

	if r.cfg.ObsidianVault == "" {
		return fmt.Errorf("obsidian_vault not configured")
	}
	// Bind task execution to the daemon lifecycle so shutdown cancels running
	// OMP sessions (graceful SIGTERM) instead of leaving them until systemd's
	// TimeoutStopSec hard-kills the process tree.
	r.daemonCtx = ctx

	// Start the filesystem watcher before acquiring the lock: events buffered
	// while the --once fallback runner holds the flock are consumed immediately
	// after it is released, so no file changes are silently missed.
	w, err := watch.New(r.cfg.ObsidianVault, 5*time.Second)
	if err != nil {
		return fmt.Errorf("start watcher: %w", err)
	}
	w.Start(ctx)

	// The --once fallback runner may hold the flock while a phase runs.
	// Wait for it instead of crashing: systemd Restart=on-failure would
	// otherwise restart-loop against the lock.
	var unlock func()
	var lastLog time.Time
	for {
		var err error
		unlock, err = acquireLock(r.cfg)
		if err == nil {
			break
		}
		if time.Since(lastLog) > time.Minute {
			r.logger.Printf("waiting for daemon lock: %v", err)
			lastLog = time.Now()
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(10 * time.Second):
		}
	}
	defer unlock()

	r.logger.Printf("daemon started, vault=%s", r.cfg.ObsidianVault)
	r.logger.Printf("concurrency gates: implementing=%d refining=%d planning=%d merge=%d priority=%d pm=%d",
		r.cfg.MaxConcurrentTasks,
		r.cfg.ConcurrencyFor("refining"), r.cfg.ConcurrencyFor("planning"),
		r.cfg.ConcurrencyFor("merge"), r.cfg.ConcurrencyFor("priority"), r.cfg.ConcurrencyFor("pm"))
	r.cleanupOldLogs()

	// Run an initial scan to catch any tasks that became ready while daemon was down.
	// Scans run on the scan-gate goroutine so this event loop never blocks
	// behind a long batch (e.g. a 1h Round 2 session): watcher events, the
	// periodic timer and SIGTERM stay responsive. Requests arriving while a
	// scan is running are coalesced into exactly one follow-up scan.

	ticker := time.NewTicker(time.Duration(r.cfg.PollIntervalMin) * time.Minute)
	defer ticker.Stop()

	r.requestScan()
	for {
		select {
		case <-ctx.Done():
			r.logger.Println("daemon shutting down")
			// Let the in-flight scan unwind: processBatch drains its task
			// goroutines (scanDrainTimeout), so their PHASE_INTERRUPTED
			// frontmatter write-backs land before the process exits.
			r.waitForScanExit()
			return nil
		case evt, ok := <-w.Events():
			if !ok {
				continue // events channel closed; ctx.Done is imminent
			}
			r.logger.Printf("watcher: %s %s changed", evt.Dir, filepath.Base(evt.Path))
			// Task write-backs invalidate the frontmatter index entry so the
			// next scan re-reads the file instead of serving stale state.
			if strings.Contains(evt.Path, tasksDirToken) {
				r.taskIdx.Invalidate(evt.Path)
			}
			// CONTEXT.md / ADR changes drop the per-project context caches so
			// the next dispatch injects fresh constraints and decisions.
			if strings.Contains(evt.Path, notesDirToken) {
				invalidateProjectContext(evt.Path)
			}
			// ADR writes get auto-tagged from the knowledge-base vocabulary
			// (user-reviewable, additive only) so extraction classifies by tag.
			// INDEX/COVERAGE are generated bookkeeping, not decision docs.
			if strings.Contains(evt.Path, adrDirToken) {
				name := filepath.Base(evt.Path)
				if strings.HasPrefix(name, "ADR-") && strings.HasSuffix(name, ".md") &&
					name != "ADR-INDEX.md" && name != "ADR-COVERAGE.md" {
					if err := knowledge.EnsureADRTags(evt.Path, filepath.Join(r.cfg.ObsidianVault, "References")); err != nil {
						r.logger.Printf("watcher: auto-tag %s: %v", name, err)
					}
				}
			}
			// Knowledge-base writes drop the classification index cache so new
			// topics/tags participate in extraction immediately. The intake
			// hook also validates the KB v2 six-field format and rebuilds
			// INDEX (debounced) — interactive/agent writes that skip the
			// skill's checks are caught here instead of silently polluting
			// the index.
			if strings.Contains(evt.Path, refsDirToken) {
				knowledge.InvalidateRefIndex(filepath.Join(r.cfg.ObsidianVault, "References"))
				// INDEX.md is a daemon-generated index, not a knowledge
				// document: its rebuild write must not trip the intake
				// frontmatter validation (and the debounced notification).
				if strings.HasSuffix(evt.Path, ".md") && filepath.Base(evt.Path) != "INDEX.md" {
					if verr := knowledge.ValidateRefFile(evt.Path); verr != nil {
						r.logger.Printf("knowledge-base intake: %s invalid: %v", filepath.Base(evt.Path), verr)
						// Debounced per file: watcher events for one broken
						// document (or a storm of invalid writes) must not
						// spam the desktop.
						if last, ok := r.refNotifyAt.Load(evt.Path); !ok || time.Since(last.(time.Time)) > 5*time.Minute {
							r.refNotifyAt.Store(evt.Path, time.Now())
							notify.SendTaskAction("knowledge", filepath.Base(evt.Path), "📄", "知识库格式不合规",
								verr.Error()+"; 请按 KB v2 六字段 frontmatter 修正，INDEX 将跳过该文档。", r.cfg.Notifications.Desktop)
						}
					}
				}
				r.maybeRebuildRefIndex()
				r.maybeSyncKnowledgeDB()
			}
			if evt.Dir == "Requirements" {
				reqRel, _ := filepath.Rel(r.cfg.ObsidianVault, evt.Path)
				var results []task.AffectedResult
				if _, statErr := os.Stat(evt.Path); os.IsNotExist(statErr) {
					results = task.OnReqDeleted(r.cfg.ObsidianVault, reqRel)
				} else {
					// A requirement under an unlisted project directory
					// auto-registers the project (name/path/project_id
					// derived from conventions) so the brand-new project
					// flows straight into task creation and refining without
					// manual config. Only on add/change — a delete must not
					// register an empty project.
					r.ensureProjectRegistered(reqRel)
					results = task.OnReqChanged(r.cfg.ObsidianVault, reqRel, r.cfg.DefaultAssignee)
				}
				// A REQ detail update reactivates the project's paused
				// decision list: the user rethinking the requirement is the
				// signal to resume reminders and the downstream flow
				// (pending_req → refining → maturity gate → consolidate/split
				// re-evaluation → planning). One reactivation per project.
				activated := make(map[string]bool)
				for _, result := range results {
					if proj := projectFromReqPath(reqRel); proj != "" && !activated[proj] {
						activated[proj] = true
						if ok, err := activatePausedDecisionList(r.cfg.ObsidianVault, proj); err != nil {
							r.logger.Printf("project %s: reactivate decision list: %v", proj, err)
						} else if ok {
							r.logger.Printf("project %s: decision list reactivated (REQ updated)", proj)
							notify.SendTaskAction("grilling", proj, "✅", "决策清单已重新激活",
								"需求已更新，Grilling-Decisions.md 已恢复提醒；补充决策后 daemon 自动继续拆分/规划流程。", r.cfg.Notifications.Desktop)
						}
					}
					switch result.Action {
					case "reset_to_ready", "rename_req":
						notify.SendTaskAction(result.TaskID, "", "🔄", "需求变更", "重新出计划", r.cfg.Notifications.Desktop)
					case "pending_req":
						notify.SendTaskAction(result.TaskID, "", "📌", "需求变更", "当前阶段完成后自动重新出计划", r.cfg.Notifications.Desktop)
					case "create_task":
						notify.SendTaskAction(result.TaskID, "", "🆕", "新任务已创建", "请填写 assignee 和 project 字段", r.cfg.Notifications.Desktop)
					case "req_missing":
						notify.SendTaskAction(result.TaskID, "", "🚫", "需求文件缺失", "TASK 已阻塞，恢复 REQ 后重试", r.cfg.Notifications.Desktop)
					case "warn_only":
						notify.SendTaskAction(result.TaskID, "", "⚠️", "需求变更", "请手动评估影响", r.cfg.Notifications.Desktop)
					default:
						r.logger.Printf("task %s: unknown OnReqChanged action %q", result.TaskID, result.Action)
					}
				}
			}
			select {
			case <-r.daemonCtx.Done():
				continue // re-enter select; ctx.Done branch handles shutdown
			case <-time.After(3 * time.Second):
			}
			r.requestScan()
		case <-ticker.C:
			r.logger.Println("timer: periodic scan")
			r.requestScan()
		}
	}
}

// minScanInterval throttles watcher-driven scans. Round 2 sessions write the
// TASK file on every progress step; without a floor each write triggers a
// full scan (observed: 2-3 scans/second, 066 的 14:43-14:47 连续 5 次 round2
// 互相 SIGTERM). 10s is short enough for responsive state pickup and long
// enough to absorb write bursts.
const minScanInterval = 10 * time.Second

// requestScan schedules one scan cycle on a dedicated goroutine. If a scan
// is already running the request is coalesced into exactly one follow-up
// scan, so bursts of watcher events cannot pile up scan goroutines. Scans
// started less than minScanInterval after the previous one are deferred to
// the interval boundary — write bursts (TASK frontmatter updates during
// Round 2) coalesce into a single scan instead of a per-write storm.
func (r *Runner) requestScan() {
	r.scanGateMu.Lock()
	defer r.scanGateMu.Unlock()
	if r.scanActive {
		r.scanPending = true
		return
	}
	if wait := r.scanMinInterval - time.Since(r.lastScanAt); wait > 0 {
		r.scanPending = true
		if r.scanTimer == nil {
			r.scanTimer = time.AfterFunc(wait, r.fireDeferredScan)
		}
		return
	}
	r.lastScanAt = time.Now()
	r.scanActive = true
	go r.runScanCycle()
}

// fireDeferredScan runs a scan that was deferred by the min-scan-interval
// throttle. The pending flag is consumed here so a burst of events produces
// exactly one scan after the floor elapses.
func (r *Runner) fireDeferredScan() {
	r.scanGateMu.Lock()
	r.scanTimer = nil
	pending := r.scanPending
	r.scanPending = false
	r.scanGateMu.Unlock()
	if pending && r.daemonCtx.Err() == nil {
		r.requestScan()
	}
}

func (r *Runner) runScanCycle() {
	r.scanAndProcess()
	r.scanGateMu.Lock()
	r.scanActive = false
	// Clear the coalesced marker unconditionally; a follow-up scan runs only
	// while the daemon is still up. Once the context is cancelled (shutdown)
	// the marker is dropped so no extra scan goroutine is started behind
	// waitForScanExit.
	pending := r.scanPending
	r.scanPending = false
	run := pending && r.daemonCtx.Err() == nil
	r.scanGateMu.Unlock()
	if run {
		r.requestScan()
	}
}

// scanDrainTimeout bounds how long shutdown waits for in-flight task
// goroutines after their OMP children were SIGTERMed. Typical graceful
// persist is ~1-2s; the cap covers slower children without delaying
// systemd TimeoutStopSec.
const scanDrainTimeout = 10 * time.Second

// waitForScanExit blocks until the in-flight scan cycle and dispatched task
// goroutines unwind after shutdown. runTask's OMP children receive SIGTERM
// via daemonCtx (graceful persist; WaitDelay hard-kill only if the child
// ignores it), so the typical case is a quick child exit followed by the
// PHASE_INTERRUPTED write-back — this wait lets those write-backs land
// before the process exits. The cap is a safety net only, sized above the
// drain window so it never cuts the drain short.
func (r *Runner) waitForScanExit() {
	deadline := time.Now().Add(scanDrainTimeout + 5*time.Second)
	for time.Now().Before(deadline) {
		r.scanGateMu.Lock()
		active := r.scanActive
		r.scanGateMu.Unlock()
		if !active && r.activeTasks.Load() == 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	r.logger.Printf("scan cycle did not unwind within %v, exiting anyway", scanDrainTimeout+5*time.Second)
}

// RunOnce performs a single scan-and-process cycle, used by the systemd timer.
// It respects the same flock as Run() to avoid concurrent OMP spawns.
func (r *Runner) RunOnce() error {
	if err := r.initLogging(); err != nil {
		return fmt.Errorf("init logging: %w", err)
	}
	defer func() {
		if err := r.logWriter.Close(); err != nil {
			r.logger.Printf("close log writer: %v", err)
		}
	}()
	// Bind SIGTERM/SIGINT so a stopped --once instance cancels its batch
	// promptly instead of dying without signal handlers, which would orphan
	// the OMP children (they keep running unattached until systemd reaps the
	// cgroup). Cancellation also routes interrupted phases through the
	// PHASE_INTERRUPTED/merge-resume paths instead of failure handling.
	r.daemonCtx = SignalContext()
	if r.cfg.ObsidianVault == "" {
		return fmt.Errorf("obsidian_vault not configured")
	}
	unlock, err := acquireLock(r.cfg)
	if err != nil {
		r.logger.Printf("skipping (lock held by watcher daemon): %v", err)
		return nil // not an error — watcher daemon is handling it
	}
	defer unlock()
	r.scanAndProcess()
	// A --once run has no resident scan loop to pick up completed tasks, so
	// wait for the dispatched OMP sessions here (same synchronous semantics
	// as the pre-async processBatch). On SIGTERM the OMP children get a
	// graceful SIGTERM via daemonCtx; give their PHASE_INTERRUPTED
	// write-backs a bounded window before exiting.
	for r.activeTasks.Load() > 0 {
		if r.daemonCtx.Err() != nil {
			deadline := time.Now().Add(scanDrainTimeout)
			for time.Now().Before(deadline) && r.activeTasks.Load() > 0 {
				time.Sleep(50 * time.Millisecond)
			}
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	return nil
}

func (r *Runner) initLogging() error {
	logDir := r.cfg.LogDir
	if logDir == "" {
		home, _ := os.UserHomeDir()
		logDir = filepath.Join(home, ".omp", "logs")
	}
	logPath := filepath.Join(logDir, "otg-daemon.log")

	w, err := logutil.NewRotatingWriter(logPath, 10, 5, 30)
	if err != nil {
		return err
	}
	r.logWriter = w
	r.logger = log.New(w, "", log.LstdFlags)
	// Route the stdlib global logger (used by the notify package for
	// Kitty tab / grilling logs) into the same rotating file. Without
	// this, grilling-tab attempts are only visible in journald while the
	// daemon log file shows nothing — "no grilling tabs" looked like a
	// broken daemon instead of a fixable notification gap.
	log.SetOutput(w)
	return nil
}

// syncStageInheritance keeps TASK `stage` aligned with its REQ. Two modes:
//
//  1. Backfill — a task whose stage is empty inherits the REQ stage
//     (REQ → TASK one-way; the task records stage_source=req so it follows
//     later REQ stage changes).
//  2. Follow — a task whose stage_source is "req" tracks the REQ stage when
//     the PM re-stages the requirement; the task never drifts outside the
//     stage plan. Tasks staged by daemon auto-staging or by PM manual
//     assignment (stage_source empty) are deliberately left untouched — an
//     explicit assignment outranks inheritance.
func (r *Runner) syncStageInheritance() {
	projectsDir := filepath.Join(r.cfg.ObsidianVault, "Projects")
	projects, err := os.ReadDir(projectsDir)
	if err != nil {
		return
	}
	for _, projectEntry := range projects {
		if !projectEntry.IsDir() {
			continue
		}
		tasksDir := filepath.Join(projectsDir, projectEntry.Name(), "Tasks")
		entries, err := os.ReadDir(tasksDir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasPrefix(entry.Name(), "TASK-") || !strings.HasSuffix(entry.Name(), ".md") {
				continue
			}
			path := filepath.Join(tasksDir, entry.Name())
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			fm, err := yamlfrontmatter.Parse(data)
			if err != nil || fm == nil || fm.ReqDoc == "" {
				continue
			}
			// Explicitly assigned stages (daemon auto-staging / PM manual)
			// never follow REQ changes; only empty or REQ-inherited stages do.
			if fm.Stage != "" && fm.StageSource != "req" {
				continue
			}
			reqPath := fm.ReqDoc
			if !filepath.IsAbs(reqPath) {
				reqPath = filepath.Join(r.cfg.ObsidianVault, reqPath)
			}
			reqData, err := os.ReadFile(reqPath)
			if err != nil {
				continue
			}
			reqFM, err := yamlfrontmatter.Parse(reqData)
			if err != nil || reqFM == nil || reqFM.Stage == "" {
				continue
			}
			if fm.Stage == reqFM.Stage && fm.StageSource == "req" {
				continue // already aligned
			}
			if err := yamlfrontmatter.Update(path, map[string]interface{}{"stage": reqFM.Stage, "stage_source": "req"}); err != nil {
				r.logger.Printf("task %s: inherit stage from REQ failed: %v", fm.ID, err)
				continue
			}
			r.logger.Printf("task %s: stage=%s from REQ %s (stage_source=req)", fm.ID, reqFM.Stage, filepath.Base(fm.ReqDoc))
		}
	}
}

// syncDependencyInheritance backfills `blocked_by` on tasks whose REQ
// declares `depends_on` dependencies (REQ → TASK one-way inheritance; an
// existing task-level blocked_by is never overwritten — explicit PM/daemon
// assignments outrank inheritance). REQ depends_on references REQ ids
// ("023") or cross-project ids ("project:REQ-023"); the canonical TASK of a
// REQ carries the same numeric id, so the mapping is identity within a
// project. References to REQs whose canonical TASK does not exist yet are
// skipped — the task inherits them on the scan after it is created.
// This closes the automation loop: PM writes dependencies on the REQ (split
// distribute, manual), the daemon propagates them to tasks without manual
// blocked_by bookkeeping (release-manager lesson: 72 tasks with empty
// blocked_by collapsed the staging topology).
func (r *Runner) syncDependencyInheritance() {
	projectsDir := filepath.Join(r.cfg.ObsidianVault, "Projects")
	projects, err := os.ReadDir(projectsDir)
	if err != nil {
		return
	}
	for _, projectEntry := range projects {
		if !projectEntry.IsDir() {
			continue
		}
		projDir := filepath.Join(projectsDir, projectEntry.Name())
		tasksDir := filepath.Join(projDir, "Tasks")
		reqDeps, taskIDs := r.reqDependsOn(projDir, tasksDir)

		entries, err := os.ReadDir(tasksDir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasPrefix(entry.Name(), "TASK-") || !strings.HasSuffix(entry.Name(), ".md") {
				continue
			}
			path := filepath.Join(tasksDir, entry.Name())
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			fm, err := yamlfrontmatter.Parse(data)
			if err != nil || fm == nil || len(fm.BlockedBy) > 0 || fm.ReqDoc == "" {
				continue
			}
			reqID := reqIDFromPath(fm.ReqDoc)
			if reqID == "" {
				continue
			}
			deps := reqDeps[reqID]
			if len(deps) == 0 {
				continue
			}
			var inherited []string
			for _, dep := range deps {
				id := strings.TrimPrefix(dep, "REQ-")
				if strings.Contains(id, ":") {
					// Cross-project: "other:REQ-023" → "other:023" — the
					// reference target exists as a task in its own project;
					// existence is not verifiable here, keep it verbatim.
					parts := strings.SplitN(id, ":", 2)
					inherited = append(inherited, parts[0]+":"+strings.TrimPrefix(parts[1], "REQ-"))
					continue
				}
				if taskIDs[id] {
					inherited = append(inherited, id)
				}
			}
			if len(inherited) == 0 {
				continue
			}
			if err := yamlfrontmatter.Update(path, map[string]interface{}{"blocked_by": inherited}); err != nil {
				r.logger.Printf("task %s: inherit blocked_by from REQ failed: %v", fm.ID, err)
				continue
			}
			r.logger.Printf("task %s: inherited blocked_by=%v from REQ %s", fm.ID, inherited, reqID)
		}
	}
}

// reqDependsOn scans a project's Requirements/ for depends_on lists keyed by
// REQ id, plus the set of canonical task ids present in Tasks/.
func (r *Runner) reqDependsOn(projDir, tasksDir string) (map[string][]string, map[string]bool) {
	reqDeps := make(map[string][]string)
	reqDir := filepath.Join(projDir, "Requirements")
	if entries, err := os.ReadDir(reqDir); err == nil {
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasPrefix(entry.Name(), "REQ-") || !strings.HasSuffix(entry.Name(), ".md") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(reqDir, entry.Name()))
			if err != nil {
				continue
			}
			fm, err := yamlfrontmatter.Parse(data)
			if err != nil || fm == nil {
				continue
			}
			id := reqIDFromPath(entry.Name())
			if id == "" {
				continue
			}
			var deps []string
			if raw, ok := fm.Extra["depends_on"]; ok {
				for _, item := range toStringSlice(raw) {
					if item != "" {
						deps = append(deps, item)
					}
				}
			}
			reqDeps[id] = deps
		}
	}
	taskIDs := make(map[string]bool)
	if entries, err := os.ReadDir(tasksDir); err == nil {
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasPrefix(entry.Name(), "TASK-") || !strings.HasSuffix(entry.Name(), ".md") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(tasksDir, entry.Name()))
			if err != nil {
				continue
			}
			fm, err := yamlfrontmatter.Parse(data)
			if err != nil || fm == nil || fm.ID == "" {
				continue
			}
			taskIDs[fm.ID] = true
		}
	}
	return reqDeps, taskIDs
}

// reqIDFromPath extracts the numeric REQ id from a vault-relative path or
// filename (".../REQ-023-slug.md" → "023").
func reqIDFromPath(p string) string {
	base := filepath.Base(p)
	rest := strings.TrimPrefix(base, "REQ-")
	if rest == base {
		return ""
	}
	if idx := strings.IndexByte(rest, '-'); idx > 0 {
		return rest[:idx]
	}
	return strings.TrimSuffix(rest, ".md")
}

// toStringSlice normalizes YAML list values ([...] or "a,b") to strings.
func toStringSlice(raw interface{}) []string {
	switch v := raw.(type) {
	case []interface{}:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return v
	case string:
		if v == "" {
			return nil
		}
		return strings.Split(v, ",")
	}
	return nil
}
// compactOversizedTasks folds plan/prototype history for TASK docs above
// the oversize threshold (config compact_oversize_threshold_kb). Runs on
// every scan so bloated documents converge regardless of which path appended
// the history (including manual Round 1 sessions that bypass the
// planning-completion compact). Replan cycles append full plan/prototype
// copies per round; without the guard a gated task can grow past 400KB
// (TASK-066: 17 replans), and every later refining/planning/round2 session
// re-reads the whole file into context.
func (r *Runner) compactOversizedTasks() {
	projectsDir := filepath.Join(r.cfg.ObsidianVault, "Projects")
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		return
	}
	for _, projectEntry := range entries {
		if !projectEntry.IsDir() {
			continue
		}
		tasksDir := filepath.Join(projectsDir, projectEntry.Name(), "Tasks")
		taskEntries, err := os.ReadDir(tasksDir)
		if err != nil {
			continue
		}
		for _, te := range taskEntries {
			if te.IsDir() || !strings.HasPrefix(te.Name(), "TASK-") || !strings.HasSuffix(te.Name(), ".md") {
				continue
			}
			path := filepath.Join(tasksDir, te.Name())
			info, err := te.Info()
			if err != nil || info.Size() <= int64(r.cfg.CompactOversizeThresholdKB)*1024 {
				continue
			}
			compacted, cerr := task.CompactTaskHistory(path)
			if cerr != nil {
				r.logger.Printf("compact oversize task %s: %v", te.Name(), cerr)
				continue
			}
			if compacted {
				r.logger.Printf("task %s: oversized doc compacted (was %d bytes)", te.Name(), info.Size())
			}
		}
	}
}

func (r *Runner) scanAndProcess() error {
	r.scanMu.Lock()
	r.syncTaskSchemaDefaults()
	r.syncStageInheritance()
	r.syncDependencyInheritance()
	r.validateDependencyRefs()
	r.detectPlanFileOverlaps()
	r.autoCloseStaleMergedTasks()
	r.recoverUnExtractedKnowledge()
	r.resolveBlockedDependencies()
	r.parkedFactRecovery()
	r.compactOversizedTasks()
	// Discover requirement files the watcher never delivered (new
	// directories, daemon downtime). Runs before the TASK scan so freshly
	// created tasks dispatch in this very cycle.
	r.scanOrphanReqs()
	tasks, err := r.taskIdx.Scan(r.cfg.ObsidianVault)
	if err != nil {
		r.logger.Printf("scan error: %v", err)
	}
	// Explain ready-looking tasks held back by unresolved dependencies, but
	// only on state changes — a long-gated task must not spam every scan.
	if len(r.taskIdx.GatedPaths) > 0 || len(r.gatedLogged) > 0 {
		now := make(map[string]bool, len(r.taskIdx.GatedPaths))
		for _, p := range r.taskIdx.GatedPaths {
			now[p] = true
			if !r.gatedLogged[p] {
				r.logger.Printf("scan: %s not dispatched (blocked_by upstream not done)", filepath.Base(p))
			}
		}
		for p := range r.gatedLogged {
			if !now[p] {
				r.logger.Printf("scan: %s dependency gate cleared", filepath.Base(p))
			}
		}
		r.gatedLogged = now
	}
	r.logger.Printf("scan: %d ready tasks", len(tasks))
	r.scanMu.Unlock()

	// Single dispatch round: processBatch returns as soon as every
	// schedulable task is dispatched, never waiting for the tasks
	// themselves. Completed tasks trigger the next scan (runTask →
	// requestScan), which picks up downstream state changes; the old
	// adaptive-polling round loop is gone because it existed only to bridge
	// the batch-synchronous wait.
	r.processBatch(tasks)
	if r.daemonCtx.Err() == nil {
		// Skip on shutdown: a cancelled OMP would overwrite the
		// PHASE_INTERRUPTED write-back of an interrupted task phase.
		// Deterministic staging runs before PM consolidation so unstaged
		// tasks are phased in milliseconds instead of an LLM session, and
		// the PM input shrinks to genuine disputes only.
		r.processAutoStaging()
		// Deterministic archive fallback runs before PM consolidation so the
		// distribution sees the converged list (no stale 500KB read).
		r.autoArchiveDecisions()
		// PM consolidation runs before priority assessments so answered
		// decision lists un-park tasks as early as possible in the cycle.
		r.processGrillingConsolidation(r.daemonCtx)
		r.processPriorityAssessments(r.daemonCtx)
		r.projectHealthDiagnostics()
	}
	return nil
}

// processBatch dispatches every schedulable task and returns immediately —
// it never waits for the dispatched work. OMP sessions (up to 1h for
// Round 2) run in their own goroutines tracked by runTask; a completed task
// triggers the next scan via requestScan, so batches can no longer freeze
// the scan loop (previously one long Round 2 stalled every plan-review
// transition and merge re-dispatch for up to an hour). Repository locks
// protect shared working directories; implementing tasks reserve daemon-wide
// capacity before their execution goroutine is created.
func (r *Runner) processBatch(tasks []task.ReadyTask) int {
	pending := r.prepareBatch(tasks)
	dispatched := 0

	for len(pending) > 0 {
		if r.daemonCtx.Err() != nil {
			// Shutdown: stop dispatching; in-flight tasks drain via
			// waitForScanExit, unscheduled remainder re-evaluates after
			// restart.
			r.logger.Printf("scheduler: shutdown, dropping %d unscheduled task(s)", len(pending))
			break
		}
		index := -1
		implementationBlocked := false
		for i := range pending {
			candidate := &pending[i]
			reservedImplementation := false
			if candidate.task.Status == "implementing" {
				acquired, _ := r.implementationGate.tryAcquireLocal()
				if !acquired {
					implementationBlocked = true
					continue
				}
				reservedImplementation = true
			}
			if !r.tryRepoLock(candidate.repoDir, candidate.lockMode) {
				if reservedImplementation {
					r.implementationGate.releaseLocal()
				}
				continue
			}
			candidate.implementationReserved = reservedImplementation
			index = i
			break
		}
		if index == -1 {
			// Nothing schedulable right now. Capacity/lock releases happen
			// inside runTask, which triggers the next scan — no need to wait
			// on release channels here.
			if implementationBlocked {
				r.logger.Printf("scheduler: %d tasks waiting for implementation capacity — will retry on next scan", len(pending))
			} else {
				r.logger.Printf("scheduler: %d tasks cannot be scheduled", len(pending))
			}
			break
		}

		candidate := pending[index]
		pending = append(pending[:index], pending[index+1:]...)
		dispatched++
		go r.runTask(candidate)
	}
	return dispatched
}

// projectFromReqPath extracts the project directory from a vault-relative
// requirement path ("Projects/002-magic-models-manager/Requirements/REQ-001.md"
// → "002-magic-models-manager"); legacy vault-root Requirements/ paths return
// "" (no per-project decision list).
func projectFromReqPath(reqRel string) string {
	parts := strings.Split(filepath.ToSlash(reqRel), "/")
	if len(parts) >= 3 && parts[0] == "Projects" {
		return parts[1]
	}
	return ""
}

// ensureProjectRegistered auto-registers an unlisted project into
// vault-map.json the first time a requirement appears under
// Projects/<dir>/ (project root or Requirements/). The vault-map name
// strips the numeric prefix ("010-demo" → "demo"), project_id derives from
// the directory, and path prefers the conventional repo checkout under
// new_project_root, falling back to the vault project directory when no
// checkout exists yet (e.g. vault-only demo projects). Idempotent: an
// already-mapped directory is a no-op, so every REQ event is safe to route
// through here.
func (r *Runner) ensureProjectRegistered(reqRel string) {
	dir := projectFromReqPath(reqRel)
	if dir == "" {
		return
	}
	mapFile := filepath.Join(r.cfg.SkillInstallDir, "config", "vault-map.json")
	if project.MatchVaultDir(mapFile, dir) != "" {
		return // already registered
	}
	name := stripNumericPrefix(dir)
	repoPath := filepath.Join(r.cfg.NewProjectRoot, name)
	path := repoPath
	if _, err := os.Stat(repoPath); err != nil {
		path = filepath.Join(r.cfg.ObsidianVault, "Projects", dir)
	}
	gitRemote := project.GitRemoteFor(mapFile, name)
	if err := project.RegisterProject(mapFile, name, path, gitRemote, false); err != nil {
		r.logger.Printf("auto-register project %s: %v", dir, err)
		return
	}
	r.logger.Printf("auto-registered project %q (vault dir %s) in vault-map", name, dir)
}

// stripNumericPrefix removes a "<digits>-" prefix from a project directory
// name ("010-demo" → "demo"); unprefixed names pass through unchanged.
func stripNumericPrefix(dir string) string {
	for i, c := range dir {
		if c >= '0' && c <= '9' {
			continue
		}
		if c == '-' && i > 0 {
			return dir[i+1:]
		}
		break
	}
	return dir
}

// scanOrphanReqs is the periodic-scan fallback for requirement files that
// never produced a watcher event (daemon down while the file was written,
// or a file moved into a directory created moments before the move, which
// inotify cannot deliver). It walks every project's Requirements/ and
// project root, auto-registers unlisted projects, and routes each REQ that
// has no canonical TASK through OnReqChanged so the task is created and
// refining starts on the next dispatch round. Idempotent: OnReqChanged
// no-ops once the canonical TASK exists.
func (r *Runner) scanOrphanReqs() {
	projectsDir := filepath.Join(r.cfg.ObsidianVault, "Projects")
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		return
	}
	for _, proj := range entries {
		if !proj.IsDir() {
			continue
		}
		// Requirements/ first (canonical), then the project root (a REQ
		// file placed next to the folder itself must not be lost either).
		for _, sub := range []string{"Requirements", ""} {
			dir := filepath.Join(projectsDir, proj.Name(), sub)
			files, err := os.ReadDir(dir)
			if err != nil {
				continue
			}
			for _, f := range files {
				if f.IsDir() {
					continue
				}
				abs := filepath.Join(dir, f.Name())
				reqRel, err := filepath.Rel(r.cfg.ObsidianVault, abs)
				if err != nil {
					continue
				}
				id, _ := task.ParseReqFilename(reqRel)
				if id == "" {
					continue
				}
				if r.hasCanonicalTask(reqRel) {
					continue
				}
				r.ensureProjectRegistered(reqRel)
				results := task.OnReqChanged(r.cfg.ObsidianVault, reqRel, r.cfg.DefaultAssignee)
				for _, res := range results {
					r.logger.Printf("scan: orphan REQ %s → %s (%s)", filepath.Base(abs), res.TaskID, res.Action)
				}
			}
		}
	}
}

// hasCanonicalTask reports whether a TASK file already exists for the
// requirement path (Tasks/<project>/TASK-<id>-<slug>.md).
func (r *Runner) hasCanonicalTask(reqRel string) bool {
	target := task.TaskFilenameForReq(reqRel)
	if target == "" {
		return true // unparsable filename; nothing to match
	}
	projDir := projectFromReqPath(reqRel)
	if projDir == "" {
		return true // legacy flat path; no canonical Tasks/ layout
	}
	_, err := os.Stat(filepath.Join(r.cfg.ObsidianVault, "Projects", projDir, "Tasks", target))
	return err == nil
}

// phaseGateKey maps a task's dispatch stage to its concurrency gate key.
// round2 is governed by implementationGate (max_concurrent_tasks) and the
// needs-grilling/plan-review interactive stages are unbounded — they do not
// start OMP sessions (or run Kitty, which is out-of-band).
func phaseGateKey(t task.ReadyTask) string {
	switch {
	case t.Status == "refining":
		return "refining"
	case t.Status == "planning":
		return "planning"
	case (t.Status == "review" || t.Status == "conflict") && t.MergeApproved:
		return "merge"
	case t.Status == "ready" && (t.PriorityAssessmentStatus == "pending" || t.PriorityAssessmentStatus == "failed"):
		return "priority"
	default:
		return ""
	}
}

// runTask executes one dispatched task to completion, then releases its
// repository lock and schedules a follow-up scan. The follow-up scan is what
// picks up downstream state changes (review → merge, plan-review →
// implementing, capacity released) — coalesced by the scan gate, so a
// burst of task completions costs exactly one extra scan.
func (r *Runner) runTask(p preparedTask) {
	r.activeTasks.Add(1)
	defer r.activeTasks.Add(-1)
	defer r.unlockRepo(p.repoDir, p.lockMode)
	// New-project tasks opting into remote creation get their GitHub remote
	// (name/description/README) before the OMP session starts; failure blocks
	// the task with an actionable error instead of failing the session.
	if p.task.Status == "implementing" && p.task.NewProject {
		if err := r.ensureRemoteRepository(p.task.FilePath, p.repoDir); err != nil {
			r.logger.Printf("task %s: remote repo creation failed: %v", p.task.ID, err)
			if uerr := yamlfrontmatter.Update(p.task.FilePath, map[string]interface{}{
				"status":            "blocked",
				"blocked_phase":     "implementing",
				"phase_error_code":  string(ErrRemotePartialCreate),
				"phase_error":       "GitHub remote creation failed: " + err.Error(),
				"resume_approved":   false,
			}); uerr != nil {
				r.logger.Printf("task %s: record remote-create failure: %v", p.task.ID, uerr)
			}
			return
		}
	}
	r.processPreparedTask(p)
	r.requestScan()
}

// prepareBatch resolves repositories and creates Round 2 worktrees before
// dispatching OMP.  Worktree setup is serialized per repository but does not
// consume an OMP concurrency slot.
//
// Grilling tasks (ready / needs-grilling) are handled inline — they do not
// need a repository and must not be blocked by repo resolution failures.
func (r *Runner) prepareBatch(tasks []task.ReadyTask) []preparedTask {
	pending := make([]preparedTask, 0, len(tasks))
	for _, t := range tasks {
		data, err := os.ReadFile(t.FilePath)
		if err != nil {
			r.logger.Printf("task %s: read frontmatter before dispatch: %v", t.ID, err)
			continue
		}
		fm, err := yamlfrontmatter.Parse(data)
		if err != nil || fm == nil {
			r.logger.Printf("task %s: parse frontmatter before dispatch: %v", t.ID, err)
			continue
		}

		if transition, ok := nextLocalTransition(fm); ok {
			r.logger.Printf("task %s: local transition %s → %s (%s)", t.ID, fm.Status, transition.Status, transition.Reason)
			if err := yamlfrontmatter.Update(t.FilePath, transition.Updates); err != nil {
				r.logger.Printf("task %s: apply local transition: %v", t.ID, err)
				continue
			}
			if strings.Contains(transition.Reason, "auto_approve") {
				// Opt-in plan automation: tell the user the plan gate was
				// skipped (they may want to review the plan anyway).
				notify.SendTaskAction(t.ID, t.Title, "⚡", "计划已自动批准",
					fmt.Sprintf("auto_approve 任务：v%d 计划直接进入实现（如需审阅请设 auto_approve: false）", fm.PlanVersion), r.cfg.Notifications.Desktop)
			}
			t.Status = transition.Status
			t.PlanApproved = false
			t.MergeApproved = false
			t.PendingReq = transition.Updates["pending_req"] == true
			if !transition.Dispatch {
				continue
			}
		}

		if t.Status == "needs-grilling" {
			if preempted, err := PreemptExpiredGrillLease(t.FilePath, time.Now()); err != nil {
				r.logger.Printf("task %s: preempt grilling lease: %v", t.ID, err)
			} else if preempted {
				r.logger.Printf("task %s: expired grilling lease preempted", t.ID)
			}
			// Async Grilling: user answered the questions offline in the TASK
			// file and set grill_continue=true → re-run the maturity gate with
			// the answers available. Fall through to normal dispatch below.
			if t.GrillContinue {
				r.logger.Printf("task %s: grill_continue=true, re-running maturity gate", t.ID)
				if err := yamlfrontmatter.Update(t.FilePath, map[string]interface{}{
					"status": "refining", "grill_continue": false, "grill_done": false, "grill_parked": false,
				}); err != nil {
					r.logger.Printf("task %s: apply grill_continue reset: %v", t.ID, err)
					continue
				}
				t.Status = "refining"
				t.GrillContinue = false
				// Fall through: dispatch re-enters refining (maturity gate).
			} else if t.GrillParked {
				// Parked: disputes consolidated into the project-level
				// Grilling-Decisions.md list. No per-task reminders — but the
				// user must be able to ANSWER the list without switching to
				// Obsidian: open one interactive decision tab per project
				// (5-min debounce) that walks the pending decisions; answers
				// are written back to the list and the daemon's answer-hash
				// change detection auto-distributes.
				if listPath := grillingDecisionListPath(r.cfg.ObsidianVault, t.Project); listPath != "" && grillingDecisionPending(listPath) > 0 && !grillingListPaused(listPath) {
					notify.TryKittyDecisionTab(t.Project, listPath, r.cfg.ObsidianVault)
				}
				r.logger.Printf("task %s: parked, waiting for project decision list", t.ID)
				continue
			} else {
				r.logger.Printf("task %s: waiting for grilling resolution", t.ID)
				// Debounce: suppress repeated reminders during active grilling sessions.
				if last, ok := r.grillNotified.Load(t.ID); ok {
					if time.Since(last.(time.Time)) < 5*time.Minute {
						continue
					}
				}
				r.grillNotified.Store(t.ID, time.Now())
				notify.SendGrillingReminder(t.ID, t.Title, t.ReqDoc, r.cfg.ObsidianVault, r.cfg.Notifications.Desktop)
				continue
			}
		}
		if t.Status == "closed" {
			continue
		}

		if t.Status == "blocked" || t.Status == "refining" || t.Status == "planning" {
			repoDir := ""
			if t.Project != "" {
				resolved, resolveErr := r.resolveRepo(t)
				if resolveErr != nil {
					r.logger.Printf("task %s: %v", t.ID, resolveErr)
					continue
				}
				repoDir = resolved
			}
			pending = append(pending, preparedTask{task: t, repoDir: repoDir, workDir: repoDir, lockMode: repoLockRead})
			continue
		}

		repoDir, err := r.resolveRepo(t)
		if err != nil {
			r.logger.Printf("task %s: %v", t.ID, err)
			continue
		}

		lockMode := repoLockWrite
		switch {
		case isRound2(t):
			lockMode = repoLockNone
		case (t.Status == "review" || t.Status == "conflict") && t.MergeApproved:
			// Merge pushes and merges via git/gh on the main checkout; it
			// never touches worktrees. Blocking on the repo write lock would
			// stall merges behind every planning/refining read lock (up to
			// 30-60min), freezing authorized merges. Worktree OMP sessions
			// already run lock-free for the same isolation reason.
			lockMode = repoLockNone
		}
		prepared := preparedTask{task: t, repoDir: repoDir, workDir: repoDir, lockMode: lockMode}
		if isRound2(t) {
			lock := r.repoLock(repoDir)
			// Non-blocking: a runTask holding the repo read lock (refining/
			// planning OMP, up to 30-60min) must not freeze the whole scan
			// behind this write lock — skip and retry on the next scan,
			// matching the tryRepoLock semantics of the dispatch loop.
			if !lock.TryLock() {
				r.logger.Printf("task %s: repo busy, deferring worktree prepare to next scan", t.ID)
				continue
			}
			workDir, worktreeErr := ensureTaskWorktree(repoDir, taskRunKey(t.FilePath), t.TargetBranch)
			lock.Unlock()
			if worktreeErr != nil {
				r.logger.Printf("task %s: prepare worktree: %v", t.ID, worktreeErr)
				continue
			}
			prepared.workDir = workDir
		}
		pending = append(pending, prepared)
		// ── Parallel warmup: pre-create worktree for plan-review tasks ──
		// While the user reviews the plan, create the worktree in the background
		// so Round 2 starts immediately when plan_approved becomes true.
		if t.Status == "plan-review" && !t.PlanApproved && !t.NewProject {
			warmKey := taskRunKey(t.FilePath)
			if _, warming := r.worktreeCache.LoadOrStore(warmKey, ""); !warming {
				warmRepo := repoDir
				warmBranch := t.TargetBranch
				go func() {
					lock := r.repoLock(warmRepo)
					// Non-blocking like the prepare path: never freeze scans
					// behind a busy repo (refining/planning read locks).
					if !lock.TryLock() {
						r.worktreeCache.Delete(warmKey)
						return
					}
					wtPath, wtErr := ensureTaskWorktree(warmRepo, warmKey, warmBranch)
					lock.Unlock()
					if wtErr != nil {
						r.logger.Printf("task %s: warm worktree failed: %v", t.ID, wtErr)
						r.worktreeCache.Delete(warmKey)
						return
					}
					r.worktreeCache.Store(warmKey, wtPath)
					r.logger.Printf("task %s: worktree warmed at %s", t.ID, wtPath)
				}()
			}
		}

	}
	return pending
}

func (r *Runner) processPreparedTask(prepared preparedTask) int {
	if prepared.implementationReserved {
		defer r.implementationGate.releaseLocal()
	}
	taskKey := taskRunKey(prepared.task.FilePath)
	if _, loaded := r.taskRuns.LoadOrStore(taskKey, struct{}{}); loaded {
		r.logger.Printf("task %s: skipping (already scheduled in this daemon)", prepared.task.ID)
		return 0
	}
	defer r.taskRuns.Delete(taskKey)

	return r.processBatchSequential([]task.ReadyTask{prepared.task}, prepared.workDir)
}

func (r *Runner) updateTaskFile(taskPath, taskID, taskTitle string, updates map[string]interface{}) error {
	if err := yamlfrontmatter.Update(taskPath, updates); err != nil {
		r.logger.Printf("task %s: frontmatter update failed: %v", taskID, err)
		notify.SendTaskAction(taskID, taskTitle, "🚫", "任务文档写入失败", err.Error(), r.cfg.Notifications.Desktop)
		return err
	}
	return nil
}

// validatePhaseCompletion checks that the task file is structurally valid after
// a phase has run.
func (r *Runner) validatePhaseCompletion(taskPath, taskID, phase string) error {
	if err := yamlfrontmatter.Validate(taskPath); err != nil {
		return fmt.Errorf("task %s: frontmatter corrupt after %s: %w", taskID, phase, err)
	}
	return nil
}

// validateChangedDocs scans git-tracked .md files modified in the working tree
// since the last commit and validates them with ValidateDocument. Salvageable
// corruption is auto-repaired with Repair (agent output leaked into the YAML
// block etc.); only documents that cannot be fixed automatically are surfaced
// to the user. Projects without a standalone checkout (repoDir falls back to
// the vault project directory) are skipped: git would resolve the enclosing
// vault repository, whose diff paths are repo-root-relative — joining them
// under repoDir fabricates wrong paths and false "damaged" notifications
// (observed for vault-only demo projects).
func (r *Runner) validateChangedDocs(repoDir, taskID, phase string) {
	top, err := gitTopLevel(repoDir)
	if err != nil || filepath.Clean(top) != filepath.Clean(repoDir) {
		r.logger.Printf("task %s: skip doc validation after %s (repoDir %s is not a git root)", taskID, phase, repoDir)
		return
	}
	files, err := gitDiffNameOnly(repoDir)
	if err != nil {
		r.logger.Printf("task %s: git diff scan failed: %v", taskID, err)
		return
	}
	for _, f := range files {
		if !strings.HasSuffix(f, ".md") {
			continue
		}
		absPath := filepath.Join(repoDir, f)
		if err := yamlfrontmatter.ValidateDocument(absPath); err != nil {
			if repairErr := yamlfrontmatter.Repair(absPath); repairErr == nil {
				// Repair guarantees syntactic validity only — required
				// fields are not backfilled. Re-validate; only a genuinely
				// fixed document is silent.
				if verifyErr := yamlfrontmatter.ValidateDocument(absPath); verifyErr == nil {
					r.logger.Printf("task %s: %s auto-repaired after %s (was: %v)", taskID, f, phase, err)
					continue
				}
			}
			r.logger.Printf("task %s: %s damaged after %s: %v", taskID, f, phase, err)
			notify.SendTaskAction(taskID, "", "📄", "文档损坏",
				fmt.Sprintf("%s 在 %s 阶段后被修改且无法自动修复，需要人工处理: %v", f, phase, err),
				r.cfg.Notifications.Desktop)
		}
	}
}

// gitTopLevel returns the absolute git repository root containing dir.
func gitTopLevel(dir string) (string, error) {
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse: %w: %s", err, out)
	}
	return strings.TrimSpace(string(out)), nil
}

// gitDiffNameOnly returns the list of files modified in the working tree
// relative to HEAD. Uses `git diff --name-only` for speed.
func gitDiffNameOnly(repoDir string) ([]string, error) {
	cmd := exec.Command("git", "-C", repoDir, "diff", "--name-only")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git diff: %w: %s", err, output)
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil, nil
	}
	return lines, nil
}

func taskRunKey(taskPath string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(taskPath)))
	return fmt.Sprintf("%x", sum[:8])
}

func taskPIDFile(taskLogDir, taskID, taskPath string) string {
	return filepath.Join(taskLogDir, fmt.Sprintf("TASK-%s-%s.pid", taskID, taskRunKey(taskPath)))
}

func (r *Runner) findReadyTasks() ([]task.ReadyTask, error) {
	tasks, err := r.taskIdx.Scan(r.cfg.ObsidianVault)
	if err != nil {
		return nil, err
	}
	adopted := r.adoptSurvivingImplementations()
	if len(adopted) == 0 {
		return tasks, nil
	}
	filtered := tasks[:0]
	for _, candidate := range tasks {
		if _, running := adopted[taskRunKey(candidate.FilePath)]; !running {
			filtered = append(filtered, candidate)
		}
	}
	return filtered, nil
}

func (r *Runner) taskLogDir() string {
	logDir := r.cfg.LogDir
	if logDir == "" {
		home, _ := os.UserHomeDir()
		logDir = filepath.Join(home, ".omp", "logs")
	}
	return filepath.Join(logDir, "tasks")
}

// resolveBlockedDependencies scans blocked tasks and auto-resumes phase-failure
// blocked upstream tasks referenced by blocked_by, unwinding dependency chains
// so downstream tasks can proceed without manual intervention.
// maxAutoResumeAttempts bounds how many times the dependency resolver may
// auto-approve a persistently failing upstream before requiring manual resume.
const maxAutoResumeAttempts = 2

// syncTaskSchemaDefaults backfills frontmatter fields added by newer daemon
// versions into old task documents, so lifecycle judgement never depends on
// keys the document never declared. Runs at the start of every scan;
// documents that are already complete are left untouched (no writes, so no
// scan feedback loop). Runs under scanMu: the writes are flock-protected
// against concurrent OMP frontmatter updates.
func (r *Runner) syncTaskSchemaDefaults() {
	projectsDir := filepath.Join(r.cfg.ObsidianVault, "Projects")
	projects, err := os.ReadDir(projectsDir)
	if err != nil {
		return
	}
	normalized := 0
	for _, project := range projects {
		if !project.IsDir() {
			continue
		}
		tasksDir := filepath.Join(projectsDir, project.Name(), "Tasks")
		entries, err := os.ReadDir(tasksDir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasPrefix(entry.Name(), "TASK-") || !strings.HasSuffix(entry.Name(), ".md") {
				continue
			}
			path := filepath.Join(tasksDir, entry.Name())
			updated, err := yamlfrontmatter.NormalizeTaskFrontmatter(path)
			if err != nil {
				r.logger.Printf("task %s: schema defaults sync failed: %v", strings.TrimPrefix(entry.Name(), "TASK-"), err)
				continue
			}
			if !updated {
				continue
			}
			normalized++
			// Post-normalize completeness check: backfill never fabricates
			// required fields, so a legacy document may still be missing
			// id/status/project/req_doc. Surface that as a diagnostic so
			// the task is not silently half-managed.
			if _, err := yamlfrontmatter.ParseTaskDocument(path); err != nil {
				r.logger.Printf("task %s: document incomplete after normalize: %v", strings.TrimPrefix(entry.Name(), "TASK-"), err)
			}
		}
	}
	if normalized > 0 {
		r.logger.Printf("schema defaults: normalized %d task document(s) (backfilled/reordered schema fields)", normalized)
	}
}

func (r *Runner) resolveBlockedDependencies() {
	projectsDir := filepath.Join(r.cfg.ObsidianVault, "Projects")
	projects, err := os.ReadDir(projectsDir)
	if err != nil {
		return
	}
	for _, projectEntry := range projects {
		if !projectEntry.IsDir() {
			continue
		}
		tasksDir := filepath.Join(projectsDir, projectEntry.Name(), "Tasks")
		entries, err := os.ReadDir(tasksDir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" {
				continue
			}
			taskPath := filepath.Join(tasksDir, entry.Name())
			data, err := os.ReadFile(taskPath)
			if err != nil {
				continue
			}
			fm, err := yamlfrontmatter.Parse(data)
			if err != nil || fm == nil || fm.Status != "blocked" {
				continue
			}
			projDir := filepath.Join(projectsDir, projectEntry.Name())
			// Prerequisite-gated tasks (AC-066-17 style entry gates) resume
			// ONLY when their blocked_by facts changed: every upstream task
			// is done AND carries no unresolved phase error (a stale
			// BASE_COMMIT_MISMATCH means the upstream PR never merged, so the
			// gate stays shut until the PR-closure loop fixes it).
			if fm.PhaseErrorCode == string(ErrPrerequisiteSmokeFailed) && !fm.ResumeApproved && len(fm.BlockedBy) > 0 {
				if r.prereqDepsSatisfied(projectsDir, projDir, fm) {
					r.logger.Printf("dependency: prerequisite facts changed, resuming TASK-%s (blocked_phase=%s)", fm.ID, fm.BlockedPhase)
					if err := yamlfrontmatter.Update(taskPath, map[string]interface{}{
						"resume_approved":   true,
						"auto_resume_pending": true,
					}); err != nil {
						r.logger.Printf("dependency: FAILED to resume gated TASK-%s: %v", fm.ID, err)
					}
				}
				continue // gated tasks do not auto-resume upstreams
			}
			if len(fm.BlockedBy) == 0 {
				continue
			}
			for _, ref := range fm.BlockedBy {
				r.autoResumePhaseFailureBlocker(projectsDir, projectEntry.Name(), fm.ID, ref)
			}
		}
	}
}

// parkedFactRecovery unparks needs-grilling+parked tasks whose blocked_by
// facts have all converged (every upstream done with no phase error) — the
// D-19 style "park until upstream changes" decision needs an exit without a
// distribute round-trip. Without it TASK-066 would stay parked forever after
// its upstream PRs merge. Recovery re-enters refining (maturity gate re-runs
// with the converged facts available).
func (r *Runner) parkedFactRecovery() {
	projectsDir := filepath.Join(r.cfg.ObsidianVault, "Projects")
	projects, err := os.ReadDir(projectsDir)
	if err != nil {
		return
	}
	for _, projectEntry := range projects {
		if !projectEntry.IsDir() {
			continue
		}
		projDir := filepath.Join(projectsDir, projectEntry.Name())
		tasksDir := filepath.Join(projDir, "Tasks")
		entries, err := os.ReadDir(tasksDir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasPrefix(entry.Name(), "TASK-") || !strings.HasSuffix(entry.Name(), ".md") {
				continue
			}
			path := filepath.Join(tasksDir, entry.Name())
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			fm, err := yamlfrontmatter.Parse(data)
			if err != nil || fm == nil || fm.Status != "needs-grilling" || !fm.GrillParked || len(fm.BlockedBy) == 0 {
				continue
			}
			if !r.prereqDepsSatisfied(projectsDir, projDir, fm) {
				continue
			}
			// A dispute park — grill_parked=true because its conflicts were
			// escalated into the project-level decision list — must NOT un-park
			// here. Its recovery gate is the list answers, which only PM
			// distribute consumes; blocked_by convergence is irrelevant to it.
			// TASK-068 un-parked every scan on landed blocked_by while D-88/89/90
			// stayed unanswered, looping refining. Only prerequisite-gate parks
			// (D-19 style, no list entry sourcing this task) exit on facts.
			if listPath := grillingDecisionListPath(r.cfg.ObsidianVault, fm.Project); listPath != "" &&
				grillingDecisionPendingForTask(listPath, fm.ID) > 0 {
				continue
			}
			r.logger.Printf("dependency: parked facts converged, un-parking TASK-%s (upstream all done+merged)", fm.ID)
			if err := yamlfrontmatter.Update(path, map[string]interface{}{
				"status":           "refining",
				"grill_parked":     false,
				"grill_done":       false,
				"grill_resolution": "",
				"grill_context":    "",
				"grill_prev_status": "",
			}); err != nil {
				r.logger.Printf("dependency: FAILED to un-park TASK-%s: %v", fm.ID, err)
			}
		}
	}
}

// prereqDepsSatisfied reports whether every blocked_by dependency of a
// prerequisite-gated task has actually converged: upstream status=done with
// no unresolved phase error (phase_error_code cleared means its PR merged —
// completeMerge clears it; a lingering BASE_COMMIT_MISMATCH/GIT_CONFLICT
// keeps the gate closed). The gate re-opens only on fact change, not on
// state, which is what makes the prereq gate loop-free.
func (r *Runner) prereqDepsSatisfied(projectsDir, projDir string, fm *yamlfrontmatter.Frontmatter) bool {
	for _, ref := range fm.BlockedBy {
		upstream, _, err := r.findTaskByRef(projectsDir, projDir, ref)
		if err != nil || upstream == nil {
			return false
		}
		if upstream.Status != "done" || upstream.PhaseErrorCode != "" {
			return false
		}
	}
	return true
}

// findTaskByRef resolves a blocked_by reference ("TASK-010" or
// "project-key:TASK-010") to the upstream task frontmatter and its absolute
// path. Unqualified references resolve within projDir; qualified ones use
// the project directory map. Returns nil when the task does not exist or
// the frontmatter id does not match the reference.
func (r *Runner) findTaskByRef(projectsDir, projDir, ref string) (*yamlfrontmatter.Frontmatter, string, error) {
	projName := ""
	id := strings.TrimPrefix(ref, "TASK-")
	if idx := strings.Index(ref, ":"); idx > 0 {
		projName = ref[:idx]
		id = strings.TrimPrefix(ref[idx+1:], "TASK-")
	}
	targetDir := projDir
	if projName != "" {
		if resolved := r.findProjectDirByKey(projName); resolved != "" {
			targetDir = resolved
		} else {
			return nil, "", fmt.Errorf("resolve project %q", projName)
		}
	}
	tasksDir := filepath.Join(targetDir, "Tasks")
	entries, err := os.ReadDir(tasksDir)
	if err != nil {
		return nil, "", err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "TASK-"+id+"-") && entry.Name() != "TASK-"+id+".md" {
			continue
		}
		path := filepath.Join(tasksDir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		upstream, err := yamlfrontmatter.Parse(data)
		if err != nil || upstream == nil || upstream.ID != id {
			continue
		}
		if projName != "" && upstream.Project != projName {
			continue
		}
		return upstream, path, nil
	}
	return nil, "", nil
}

// isAutoResumableError reports whether a phase_error_code represents a genuine
// transient phase failure that is safe to auto-resume. REQ_MISSING and other
// content/validation errors require human intervention.
func isAutoResumableError(code string) bool {
	switch code {
	case string(ErrModelFailed), string(ErrModelQuotaExhausted), string(ErrPhaseTimeout), string(ErrPhaseInterrupted),
		string(ErrPrerequisiteSmokeFailed):
		// Prerequisite smoke failures are resume-safe: the blocker is an
		// upstream fact (dependency PR merged / dependency task done), which
		// resolveBlockedDependencies verifies before approving the resume —
		// no user decision is being bypassed.
		return true
	default:
		// Empty code (legacy phase-failure blocks) is treated as resumable.
		return code == ""
	}
}

// autoResumePhaseFailureBlocker looks up the task referenced by a blocked_by
// entry ("TASK-010" or "project-key:TASK-010") and approves its resume if it
// is blocked on a phase failure (blocked_phase set) but not yet resumed.
func (r *Runner) autoResumePhaseFailureBlocker(projectsDir, downstreamProjDir, downstreamTaskID, ref string) {
	projName := ""
	id := strings.TrimPrefix(ref, "TASK-")
	if idx := strings.Index(ref, ":"); idx > 0 {
		projName = ref[:idx]
		id = strings.TrimPrefix(ref[idx+1:], "TASK-")
	}
	// Unqualified references resolve within the downstream project only.
	downstreamAbs := filepath.Join(projectsDir, downstreamProjDir)
	if projName == "" {
		r.autoResumeInProject(downstreamAbs, downstreamAbs, downstreamTaskID, "", id)
		return
	}
	// Exact dir-name pass, then suffix pass, then frontmatter-project pass are
	// handled by findProjectDirByKey — reuse it to avoid single-pass ambiguity
	// when both "release-manager" and "001-release-manager" directories exist.
	if resolved := r.findProjectDirByKey(projName); resolved != "" {
		r.autoResumeInProject(resolved, downstreamAbs, downstreamTaskID, projName, id)
	}
}

// autoResumeInProject looks up a task by ID within a single project directory
// and approves its resume if blocked on a phase failure. It skips the upstream
// when resuming it would create a dependency cycle (upstream transitively
// blocked_by the downstream task).
func (r *Runner) autoResumeInProject(projDir, downstreamProjDir, downstreamTaskID, expectProject, id string) {
	tasksDir := filepath.Join(projDir, "Tasks")
	entries, err := os.ReadDir(tasksDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "TASK-"+id+"-") && entry.Name() != "TASK-"+id+".md" {
			continue
		}
		path := filepath.Join(tasksDir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		upstream, err := yamlfrontmatter.Parse(data)
		if err != nil || upstream == nil || upstream.ID != id {
			// Filename prefix alone is not authoritative; the frontmatter id
			// must match the blocked_by reference.
			continue
		}
		// For qualified refs, the frontmatter project must match the reference key.
		if expectProject != "" && upstream.Project != expectProject {
			continue
		}
		if upstream.Status == "blocked" && upstream.BlockedPhase != "" && !upstream.ResumeApproved && isAutoResumableError(upstream.PhaseErrorCode) {
			if upstream.AutoResumeCount >= maxAutoResumeAttempts {
				r.logger.Printf("dependency: TASK-%s exceeded %d auto-resume attempts, manual resume required", id, maxAutoResumeAttempts)
				notify.SendTaskAction(id, upstream.Title, "🧩", "自动恢复达上限",
					fmt.Sprintf("上游任务已自动重试 %d 次仍失败（%s），请修复后手动设置 resume_approved=true", maxAutoResumeAttempts, upstream.PhaseError),
					r.cfg.Notifications.Desktop)
				return
			}
			if r.dependencyCycle(projDir, downstreamProjDir, downstreamTaskID, upstream, map[string]bool{}) {
				r.logger.Printf("dependency: skip auto-resume TASK-%s — would create dependency cycle", id)
				return
			}
			if err := yamlfrontmatter.Update(path, map[string]interface{}{"resume_approved": true, "auto_resume_pending": true}); err != nil {
				r.logger.Printf("dependency: FAILED to auto-resume upstream TASK-%s: %v", id, err)
				return
			}
			r.logger.Printf("dependency: auto-resumed blocked upstream TASK-%s (blocked_phase=%s) to unwind blocked_by chain", id, upstream.BlockedPhase)
		}
		return
	}
}

// dependencyCycle reports whether the candidate upstream transitively depends
// on the downstream project/task being resolved, which would make auto-resume
// unsafe (A blocked_by B and B blocked_by A).
// candidateProjDir is the absolute project directory of the candidate; all
// unqualified blocked_by refs resolve relative to it. Project identity is
// compared by absolute directory path only.
func (r *Runner) dependencyCycle(candidateProjDir, downstreamProjDir, downstreamTaskID string, candidate *yamlfrontmatter.Frontmatter, visited map[string]bool) bool {
	for _, ref := range candidate.BlockedBy {
		projDir := candidateProjDir
		id := strings.TrimPrefix(ref, "TASK-")
		if idx := strings.Index(ref, ":"); idx > 0 {
			projName := ref[:idx]
			id = strings.TrimPrefix(ref[idx+1:], "TASK-")
			if resolved := r.findProjectDirByKey(projName); resolved != "" {
				projDir = resolved
			} else {
				continue
			}
		}
		key := projDir + "/" + id
		if visited[key] {
			continue
		}
		visited[key] = true
		// Downstream task referenced as blocker → cycle. Both dirs are absolute.
		if projDir == downstreamProjDir && id == downstreamTaskID {
			return true
		}
		path := r.findTaskPath(projDir, id)
		if path == "" {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		fm, err := yamlfrontmatter.Parse(data)
		if err != nil || fm == nil {
			continue
		}
		if r.dependencyCycle(projDir, downstreamProjDir, downstreamTaskID, fm, visited) {
			return true
		}
	}
	return false
}

// findProjectDirByKey locates a project directory by vault-map key: exact name
// match first, then numeric-prefix suffix match.
func (r *Runner) findProjectDirByKey(key string) string {
	projectsDir := filepath.Join(r.cfg.ObsidianVault, "Projects")
	projects, err := os.ReadDir(projectsDir)
	if err != nil {
		return ""
	}
	// Exact directory-name match first (covers keys that equal the dir name,
	// including hyphenated names like "release-manager").
	for _, projectEntry := range projects {
		if projectEntry.IsDir() && projectEntry.Name() == key {
			return filepath.Join(projectsDir, key)
		}
	}
	// Then numeric-prefix suffix match: only dirs whose prefix is ALL digits
	// ("001-alpha" → "alpha") qualify; "release-manager" must never map to
	// "manager".
	for _, projectEntry := range projects {
		if !projectEntry.IsDir() {
			continue
		}
		name := projectEntry.Name()
		if project.ExtractProjectID(name) == "" || len(name) <= len(project.ExtractProjectID(name))+1 {
			continue
		}
		suffix := name[len(project.ExtractProjectID(name))+1:]
		if suffix == key {
			return filepath.Join(projectsDir, name)
		}
	}
	// Finally: a directory whose task frontmatter project field equals the key
	// (mirrors AreBlockersDone's fallback when directory name and key differ).
	for _, projectEntry := range projects {
		if !projectEntry.IsDir() {
			continue
		}
		tasksDir := filepath.Join(projectsDir, projectEntry.Name(), "Tasks")
		tasks, err := os.ReadDir(tasksDir)
		if err != nil {
			continue
		}
		for _, t := range tasks {
			if t.IsDir() || filepath.Ext(t.Name()) != ".md" {
				continue
			}
			data, err := os.ReadFile(filepath.Join(tasksDir, t.Name()))
			if err != nil {
				continue
			}
			fm, err := yamlfrontmatter.Parse(data)
			if err != nil || fm == nil || fm.Project != key {
				continue
			}
			return filepath.Join(projectsDir, projectEntry.Name())
		}
	}
	return ""
}

// findTaskPath resolves a task file path within a project directory by ID.
func (r *Runner) findTaskPath(projDir, id string) string {
	tasksDir := filepath.Join(projDir, "Tasks")
	entries, err := os.ReadDir(tasksDir)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, "TASK-"+id+"-") && name != "TASK-"+id+".md" {
			continue
		}
		path := filepath.Join(tasksDir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		fm, err := yamlfrontmatter.Parse(data)
		if err != nil || fm == nil || fm.ID != id {
			continue
		}
		return path
	}
	return ""
}

func (r *Runner) adoptSurvivingImplementations() map[string]struct{} {
	adopted := make(map[string]struct{})
	projectsDir := filepath.Join(r.cfg.ObsidianVault, "Projects")
	projects, err := os.ReadDir(projectsDir)
	if err != nil {
		return adopted
	}
	taskLogDir := r.taskLogDir()
	for _, projectEntry := range projects {
		if !projectEntry.IsDir() {
			continue
		}
		tasksDir := filepath.Join(projectsDir, projectEntry.Name(), "Tasks")
		entries, err := os.ReadDir(tasksDir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" {
				continue
			}
			taskPath := filepath.Join(tasksDir, entry.Name())
			data, err := os.ReadFile(taskPath)
			if err != nil {
				continue
			}
			fm, err := yamlfrontmatter.Parse(data)
			if err != nil || fm == nil || fm.Status != "implementing" {
				continue
			}
			pidFile := taskPIDFile(taskLogDir, fm.ID, taskPath)
			pidData, err := os.ReadFile(pidFile)
			if err != nil {
				continue
			}
			pid, startTime, recordedCmd := parsePIDRecord(pidData)
			if pid == 0 || !procAlive(pid) {
				if err := os.Remove(pidFile); err != nil && !os.IsNotExist(err) {
					r.logger.Printf("task %s: remove stale PID file: %v", fm.ID, err)
				}
				continue
			}
			if !startTime.IsZero() && !pidMatchesTask(pid, startTime, recordedCmd) {
				r.logger.Printf("task %s: PID %d reused by different process — ignoring", fm.ID, pid)
				if err := os.Remove(pidFile); err != nil && !os.IsNotExist(err) {
					r.logger.Printf("task %s: remove stale PID file: %v", fm.ID, err)
				}
				continue
			}
			adopted[taskRunKey(taskPath)] = struct{}{}
			if r.implementationGate.adopt(pid) {
				r.logger.Printf("task %s: adopted surviving implementation PID %d", fm.ID, pid)
				go r.watchAdoptedImplementation(pid, pidFile)
			}
		}
	}
	return adopted
}

func (r *Runner) watchAdoptedImplementation(pid int, pidFile string) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for procExists(pid) {
		<-ticker.C
	}
	if err := os.Remove(pidFile); err != nil && !os.IsNotExist(err) {
		r.logger.Printf("remove adopted PID file %s: %v", pidFile, err)
	}
	r.implementationGate.releaseAdopted(pid)
}
func (r *Runner) repoLock(repoDir string) *sync.RWMutex {
	lock, _ := r.repoLocks.LoadOrStore(repoDir, &sync.RWMutex{})
	return lock.(*sync.RWMutex)
}

func (r *Runner) tryRepoLock(repoDir string, mode repoLockMode) bool {
	if mode == repoLockNone || repoDir == "" {
		return true
	}
	lock := r.repoLock(repoDir)
	if mode == repoLockRead {
		return lock.TryRLock()
	}
	return lock.TryLock()
}

func (r *Runner) unlockRepo(repoDir string, mode repoLockMode) {
	if mode == repoLockNone || repoDir == "" {
		return
	}
	lock := r.repoLock(repoDir)
	if mode == repoLockRead {
		lock.RUnlock()
		return
	}
	lock.Unlock()
}

func isRound2(t task.ReadyTask) bool {
	return (t.Status == "plan-review" || t.Status == "implementing") && !t.NewProject
}

func ensureTaskWorktree(repoDir, taskID, targetBranch string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	repoHash := fmt.Sprintf("%x", sha256.Sum256([]byte(repoDir)))
	path := filepath.Join(home, ".omp", "worktrees", repoHash[:12], "TASK-"+taskID)
	if _, err := os.Stat(path); err == nil {
		cmd := exec.Command("git", "-C", path, "rev-parse", "--is-inside-work-tree")
		if output, err := cmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("validate existing worktree %s: %w: %s", path, err, strings.TrimSpace(string(output)))
		}
		if targetBranch != "" {
			branch, branchErr := gitCurrentBranch(path)
			if branchErr != nil {
				return "", branchErr
			}
			if branch != targetBranch {
				return "", fmt.Errorf("existing worktree %s uses branch %q, want %q", path, branch, targetBranch)
			}
		}
		return path, nil
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("stat worktree path: %w", err)
	}
	// If the main repo is already on targetBranch, use it directly.
	// Avoids "already used by worktree" error when user manually checked out the branch.
	if targetBranch != "" {
		if current, err := gitCurrentBranch(repoDir); err == nil && current == targetBranch {
			return repoDir, nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return "", fmt.Errorf("create worktree parent: %w", err)
	}

	args := []string{"-C", repoDir, "worktree", "add"}
	if targetBranch == "" {
		args = append(args, "--detach", path, "HEAD")
	} else if gitBranchExists(repoDir, targetBranch) {
		args = append(args, path, targetBranch)
	} else {
		args = append(args, "-b", targetBranch, path, "HEAD")
	}
	cmd := exec.Command("git", args...)
	if output, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("create worktree: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return path, nil
}

func gitBranchExists(repoDir, branch string) bool {
	cmd := exec.Command("git", "-C", repoDir, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	return cmd.Run() == nil
}

func gitCurrentBranch(workDir string) (string, error) {
	cmd := exec.Command("git", "-C", workDir, "branch", "--show-current")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("resolve worktree branch: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output)), nil
}

// reqHash computes the SHA-256 of the requirement document, or "" when the
// document is unavailable. Refining audits against this hash, so the refining
// early-out can compare the last AUDIT hash with the current requirement
// without an LLM read.
func (r *Runner) reqHash(reqDoc string) string {
	if reqDoc == "" {
		return ""
	}
	reqPath := filepath.Join(r.cfg.ObsidianVault, reqDoc)
	data, err := os.ReadFile(reqPath)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// compactPlanHistory folds old plan versions after a successful planning
// round, on both the primary and fallback completion paths.
func (r *Runner) compactPlanHistory(taskPath, phase string) {
	if phase != "planning" {
		return
	}
	if compacted, cerr := task.CompactPlanHistory(taskPath, 3); cerr != nil {
		r.logger.Printf("task %s: compact plan history: %v", filepath.Base(taskPath), cerr)
	} else if compacted {
		r.logger.Printf("task %s: plan history compacted (kept 3 versions)", filepath.Base(taskPath))
	}
}

// ensureReqHash precomputes the requirement document SHA-256 into the task's
// refine_req_hash frontmatter (daemon-side, zero LLM tokens) so the
// refining/planning skills can trust the stored hash instead of reading the
// full REQ to compute it.
func (r *Runner) ensureReqHash(taskPath, reqDoc string) {
	hash := r.reqHash(reqDoc)
	if hash == "" {
		return
	}
	raw, err := os.ReadFile(taskPath)
	if err != nil {
		return
	}
	fm, err := yamlfrontmatter.Parse(raw)
	if err != nil || fm == nil {
		return
	}
	if fm.RefineReqHash == hash {
		return
	}
	if err := yamlfrontmatter.Update(taskPath, map[string]interface{}{"refine_req_hash": hash}); err != nil {
		r.logger.Printf("task %s: precompute req hash: %v", filepath.Base(taskPath), err)
	}
}

func (r *Runner) processBatchSequential(tasks []task.ReadyTask, repoDir string) int {
	processed := 0
	for _, t := range tasks {
		taskPath := t.FilePath

		if t.Status == "blocked" {
			// Cooldown: don't touch a task that was recently blocked by phase failure.
			if ts, ok := r.phaseFailures.Load(taskPath); ok {
				if time.Since(ts.(time.Time)) < 2*time.Minute {
					continue
				}
				r.phaseFailures.Delete(taskPath)
			}
			// Check if this is a phase-failure blocked task waiting for resume.
			if data, err := os.ReadFile(taskPath); err == nil {
				if fm, err := yamlfrontmatter.Parse(data); err == nil && fm != nil {
					if fm.BlockedPhase != "" && fm.ResumeApproved {
						r.logger.Printf("task %s: resume approved, restoring %s", t.ID, fm.BlockedPhase)
						// Manual resume (no pending marker) resets the failure budget;
						// resolver approvals keep the count so retries stay bounded.
						if err := r.restoreBlockedPhase(taskPath, fm.BlockedPhase, !fm.AutoResumePending); err != nil {
							r.logger.Printf("task %s: restore blocked phase: %v", t.ID, err)
							continue
						}
						t.Status = fm.BlockedPhase
						// Fall through to normal dispatch below.
					} else if fm.BlockedPhase != "" && fm.PhaseErrorCode == task.PhaseErrorCodeInterrupted {
						// Interrupted by daemon shutdown: the skill contract
						// (docs/workflow.md 3.2) promises automatic re-scheduling
						// on restart with NO manual resume_approved. Current
						// daemons keep the phase status and only write
						// PHASE_INTERRUPTED, but legacy daemons wrote interrupted
						// phases as blocked (observed: TASK-015, 8/5 era). Self-heal
						// those leftovers exactly like the API-key probe.
						r.logger.Printf("task %s: PHASE_INTERRUPTED, restoring %s", t.ID, fm.BlockedPhase)
						if err := r.restoreBlockedPhase(taskPath, fm.BlockedPhase, false); err != nil {
							r.logger.Printf("task %s: restore interrupted phase: %v", t.ID, err)
							continue
						}
						t.Status = fm.BlockedPhase
						// Fall through to normal dispatch below.
					} else if fm.BlockedPhase != "" && fm.PhaseErrorCode == string(ErrAPIKeyUnavailable) {
						// Blocked on missing API key (e.g. KeePassXC locked): probe the
						// key source each scan and auto-resume once it becomes
						// available. No manual resume_approved needed.
						if !apiKeyAvailable() {
							// Key still unavailable: stay blocked, do not launch OMP.
							continue
						}
						r.logger.Printf("task %s: API key available, restoring %s", t.ID, fm.BlockedPhase)
						if err := r.restoreBlockedPhase(taskPath, fm.BlockedPhase, false); err != nil {
							r.logger.Printf("task %s: restore API-key blocked phase: %v", t.ID, err)
							continue
						}
						t.Status = fm.BlockedPhase
						// Fall through to normal dispatch below.
					} else if fm.BlockedPhase != "" {
						// Phase-failure blocked, waiting for manual resume. Remain blocked.
						continue
					} else if fm.BlockedPhase == "" && (fm.PhaseError != "" || fm.PhaseErrorCode != "") {
						// Defensive: blocked without blocked_phase → infer implementing.
						r.logger.Printf("task %s: blocked without blocked_phase, defaulting to implementing", t.ID)
						if err := r.updateTaskFile(taskPath, t.ID, t.Title, map[string]interface{}{
							"blocked_phase": "implementing",
						}); err != nil {
							r.logger.Printf("task %s: failed to fix blocked_phase: %v", t.ID, err)
						}
						continue
					} else {
						// Normal auto-unblock. R1: skip ready→refining when a plan already exists.
						dest := "ready"
						if fm.GrillPrevStatus == "implementing" && fm.PlanVersion > 0 {
							dest = "plan-review"
							r.logger.Printf("task %s: deps done, plan v%d exists → plan-review", t.ID, fm.PlanVersion)
						}
						if err := r.updateTaskFile(taskPath, t.ID, t.Title, map[string]interface{}{
							"status":            dest,
							"pending_req":       false,
							"blocked_by":        []string{},
							"blocked_phase":     "",
							"phase_error":       "",
							"phase_log":         "",
							"grill_prev_status": "",
							"grill_continue":    false,
						}); err != nil {
							r.logger.Printf("task %s: failed to unblock: %v", t.ID, err)
							continue
						}
						t.Status = dest
						t.PendingReq = false
						if dest == "ready" {
							notify.SendTaskAction(t.ID, t.Title, "🔓", "解除阻塞", "必填字段已补齐，依赖已满足，任务自动解除阻塞开始执行", r.cfg.Notifications.Desktop)
						} else {
							notify.SendTaskAction(t.ID, t.Title, "🔓", "解除阻塞", "依赖已满足，任务进入 plan-review", r.cfg.Notifications.Desktop)
						}
						processed++
						continue // let next scan round handle dispatch with proper worktree setup
					}
				}
			}
		}

		// Auto-merge gate: a fresh review (Round 2 completed, no failure) with
		// auto_merge=true is approved without a manual gate. Merge-failure
		// fallbacks carry phase_error_code, so they stay manual.
		if t.Status == "review" && !t.MergeApproved && t.AutoMerge && !t.PendingReq && t.PhaseErrorCode == "" {
			if err := yamlfrontmatter.Update(taskPath, map[string]interface{}{"merge_approved": true}); err != nil {
				r.logger.Printf("task %s: auto-approve merge failed: %v", t.ID, err)
			} else {
				t.MergeApproved = true
				r.logger.Printf("task %s: auto-approve merge (auto_merge=true)", t.ID)
			}
		}

		if t.MergeApproved && (t.Status == "review" || t.Status == "conflict") {
			// Environmental merge failures (network/GitHub API) retry with a
			// short backoff here instead of waiting for the next scan batch,
			// which can be stalled behind a long Round 2 session.
			if err := r.processMergeTaskWithRetry(t, repoDir); err != nil {
				r.logger.Printf("task %s: Merge Phase: %v", t.ID, err)
			}
			processed++
			continue
		}
		// ── Direct phase dispatch ──
		model := r.selectModel(t.Assignee)
		var phase, skillPrompt string

		switch {
		case t.Status == "ready" && (t.PriorityAssessmentStatus == "pending" || t.PriorityAssessmentStatus == "failed"):
			phase = "priority"
			model = r.cfg.Model("default")
			skillPrompt = "/obsidian-task-runner-priority " + t.FilePath
			r.logger.Printf("task %s: priority assessment (model=%s)", t.ID, model)
		case t.Status == "refining":
			model = r.cfg.Model("default")
			// ── Refining early-out: skip to planning when the maturity gate
			// has already passed for the CURRENT requirement. A REQ change
			// after the last plan (pending_req, refine_req_hash !=
			// plan_req_hash) must still reach planning to regenerate the
			// plan; routing it back into the gate would re-run the same
			// audit every scan forever (TASK-067: 30+ identical rounds).
			// The gate re-runs only when the stored audit predates the
			// current REQ hash. ──
			if t.Maturity == "fully_mature" && t.RefineReqHash != "" && t.RefineReqHash == r.reqHash(t.ReqDoc) {
				r.logger.Printf("task %s: fully mature, audit current → planning", t.ID)
				phase = "planning"
				// Planning runs on the TASK assignee's model (docs/workflow.md
				// 1.1); the refining early-out must not inherit the default
				// model used for the maturity gate.
				model = r.selectModel(t.Assignee)
				skillPrompt = "/obsidian-task-runner-round1 " + t.FilePath
			} else {
				phase = "refining"
				skillPrompt = "/obsidian-task-runner-refining " + t.FilePath
				r.logger.Printf("task %s: maturity gate (model=%s)", t.ID, model)
			}
		case t.Status == "planning":
			phase = "planning"
			skillPrompt = "/obsidian-task-runner-round1 " + t.FilePath
			r.logger.Printf("task %s: plan generation (model=%s)", t.ID, model)
		case t.Status == "plan-review" || t.Status == "implementing":
			phase = "round2"
			skillPrompt = "/obsidian-task-runner-round2 " + t.FilePath
			if t.Status == "plan-review" {
				if err := yamlfrontmatter.Update(taskPath, map[string]interface{}{"status": "implementing"}); err != nil {
					r.logger.Printf("task %s: set implementing: %v", t.ID, err)
					continue
				}
				// Notify only on the plan-review → implementing transition.
				// Resumed sessions (daemon restarts) must not re-spam the
				// "OMP 正在执行" notification on every scan.
				notify.SendTaskAction(t.ID, t.Title, "🚀", "开始实现", "OMP 正在执行", r.cfg.Notifications.Desktop)
			}
		default:
			r.logger.Printf("task %s: unknown dispatch status=%s", t.ID, t.Status)
			continue
		}

		// Phase concurrency gate: bounds simultaneous OMP sessions per phase
		// (refining/planning/merge/priority/pm). Acquired here — not in the
		// scheduler loop — so every dispatch path is covered, including the
		// in-place restores (API-key recovery, resume, grill_continue) that
		// fall through to normal dispatch inside this function.
		if gk := phaseGateKey(t); gk != "" {
			if gate := r.phaseGates[gk]; gate != nil {
				if ok, _ := gate.tryAcquire(); !ok {
					r.logger.Printf("task %s: phase gate %s full (%d running), deferring to next scan", t.ID, gk, r.cfg.ConcurrencyFor(gk))
					continue
				}
				defer gate.release()
			}
		}

		// API key preflight: never launch an OMP session that will fail on a
		// missing key. The task keeps its status and retries on the next
		// scan — no failure write-back, no retry budget spent. The toast is
		// debounced to one per 5 minutes instead of one per failing task
		// (observed: 40+ simultaneous "API Key 不可用" notifications when a
		// key outage hit a large batch).
		if !apiKeyAvailable() {
			r.logger.Printf("task %s: API key unavailable, deferring %s to next scan", t.ID, phase)
			r.notifyKeyUnavailable()
			continue
		}

		// Precompute the requirement hash into frontmatter so the
		// refining/planning skills do not read the full REQ document just to
		// hash it — the dominant token cost for large requirement docs.
		if phase == "refining" || phase == "planning" {
			r.ensureReqHash(taskPath, t.ReqDoc)
		}

		var thinking string
		switch phase {
		case "priority":
			thinking = "off"
		case "round2":
			thinking = "max"
		case "planning":
			thinking = "high"
		default:
			thinking = "low"
		}

		args := []string{"--model", model, "--auto-approve", "-p", skillPrompt, "--thinking", thinking}

		if needsContextInjection(t.Status) {
			if projDir := resolveVaultProjectDir(r.cfg.ObsidianVault, t.Project); projDir != "" {
				reqPath := filepath.Join(r.cfg.ObsidianVault, t.ReqDoc)
				if ctx := BuildProjectContext(projDir, reqPath); ctx != "" {
					skillPrompt = fmt.Sprintf("%s\n\n<project_context>\n## 项目上下文（daemon 自动注入，配合 skill://knowledge-base 交叉引用 References）\n项目: %s\n\n%s\n</project_context>", skillPrompt, t.Project, ctx)
					args[4] = skillPrompt
					r.logger.Printf("task %s: injected project context (%d bytes)", t.ID, len(ctx))
				} else {
					r.logger.Printf("task %s: no project context available", t.ID)
				}
			}
		}
		logDir := r.cfg.LogDir
		if logDir == "" {
			home, _ := os.UserHomeDir()
			logDir = filepath.Join(home, ".omp", "logs")
		}
		taskLogDir := filepath.Join(logDir, "tasks")
		if err := os.MkdirAll(taskLogDir, 0o700); err != nil {
			r.logger.Printf("task %s: create task log directory: %v", t.ID, err)
			continue
		}
		ts := time.Now().Format("20060102-150405")
		taskKey := taskRunKey(t.FilePath)
		logPath := filepath.Join(taskLogDir, fmt.Sprintf("TASK-%s-%s-%s-%s.log", t.ID, taskKey, ts, phase))

		f, createErr := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if createErr != nil {
			r.logger.Printf("task %s: create task log: %v", t.ID, createErr)
		}
		if f != nil {
			header := fmt.Sprintf("# TASK-%s %s\n# model=%s phase=%s time=%s\n\n", t.ID, t.Title, model, phase, time.Now().Format(time.RFC3339))
			if _, writeErr := f.WriteString(header); writeErr != nil {
				r.logger.Printf("task %s: write task log header: %v", t.ID, writeErr)
			}
		}
		timeout := r.cfg.PhaseTimeout(phase)
		if timeout <= 0 {
			timeout = 30 * time.Minute
		}
		pidFile := taskPIDFile(taskLogDir, t.ID, t.FilePath)
		if phase == "priority" || phase == "refining" || phase == "planning" || phase == "round2" {
			if data, err := os.ReadFile(pidFile); err == nil {
				var existingPID int
				if _, scanErr := fmt.Sscanf(string(data), "%d", &existingPID); scanErr == nil {
					if procAlive(existingPID) {
						r.logger.Printf("task %s: OMP already running (PID %d), skipping", t.ID, existingPID)
						continue
					}
				}
			}
		}

		ctx, cancel := context.WithTimeout(r.daemonCtx, timeout)
		cmd := exec.CommandContext(ctx, r.cfg.OMPCmd, args...)
		// Graceful shutdown: on ctx cancellation (daemon stop or phase timeout)
		// send SIGTERM so omp can persist its session, then hard-kill after
		// WaitDelay if it does not exit.
		cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
		cmd.WaitDelay = 30 * time.Second
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Dir = repoDir
		if f != nil {
			cmd.Stdout = io.MultiWriter(f, os.Stderr)
			cmd.Stderr = io.MultiWriter(f, os.Stderr)
		} else {
			cmd.Stdout = os.Stderr
			cmd.Stderr = os.Stderr
		}

		// Tail OMP's own log into the task log for full implementation trace
		ompLogPath := filepath.Join(logDir, "omp."+time.Now().Format("2006-01-02")+".log")
		tailDone := make(chan struct{})
		go tailOMPLog(ompLogPath, f, tailDone)
		// Start OMP and write PID file for crash recovery.
		if startErr := cmd.Start(); startErr != nil {
			r.logger.Printf("task %s: OMP start failed: %v", t.ID, startErr)
			cancel()
			close(tailDone)
			continue
		}
		if err := os.WriteFile(pidFile, []byte(formatPIDRecord(cmd.Process.Pid)), 0o644); err != nil {
			r.logger.Printf("task %s: write PID file: %v", t.ID, err)
		}
		defer func() {
			if err := os.Remove(pidFile); err != nil && !os.IsNotExist(err) {
				r.logger.Printf("task %s: remove PID file: %v", t.ID, err)
			}
		}()

		r.logger.Printf("task %s: executing OMP (model=%s, phase=%s, timeout=%v, log=%s)", t.ID, model, phase, timeout, logPath)
		runErr := cmd.Wait()
		// Capture shutdown state BEFORE cancel(): context.Canceled after
		// cancel() is indistinguishable from a real daemon shutdown, and any
		// non-zero OMP exit would be misrouted as "interrupted by shutdown",
		// skipping failure recovery (fallback/blocked/retry) entirely.
		shutdownInterrupt := r.daemonCtx.Err() != nil
		cancel()
		close(tailDone) // signal tail goroutine to stop

		if runErr != nil && shutdownInterrupt {
			// Daemon-initiated shutdown (ctx canceled): the OMP was killed by
			// our own SIGTERM, not a genuine failure. Keep the task status
			// untouched and skip failure/fallback handling — the pid file is
			// already removed, so the next scan re-dispatches the task
			// automatically. Deploy-time daemon restarts are therefore
			// lossless: no blocked state, no manual resume_approved.
			r.logger.Printf("task %s: OMP interrupted by daemon shutdown, status=%s kept for auto-resume", t.ID, t.Status)
			if err := yamlfrontmatter.Update(taskPath, map[string]interface{}{
				"phase_error_code": string(ErrPhaseInterrupted),
				"phase_error":      "daemon 重启中断，等待自动恢复",
				"phase_log":        logPath,
			}); err != nil {
				r.logger.Printf("task %s: record interruption: %v", t.ID, err)
			}
		} else if runErr != nil {
			reason := "异常退出"
			failureCode := ErrModelFailed
			signalKilled := false
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				reason = fmt.Sprintf("超时（%v 无响应）", timeout)
				failureCode = ErrPhaseTimeout
			} else if exitErr, ok := runErr.(*exec.ExitError); ok && exitErr.ExitCode() == -1 {
				reason = "进程被终止（内存不足或系统信号）"
				signalKilled = true
			}
			r.logger.Printf("task %s: OMP failed (%s): %v", t.ID, reason, runErr)

			if keyErr := checkAPIKeyUnavailable(logPath); keyErr != "" {
				failureCode = ErrAPIKeyUnavailable
				// Debounced: one toast per 5 minutes, not one per failing task.
				r.notifyKeyUnavailable()
			} else if tokenErr := checkTokenQuota(logPath, model); tokenErr != "" {
				failureCode = ErrModelQuotaExhausted
				notify.SendTaskAction(t.ID, t.Title, "💰", "Token 不足",
					fmt.Sprintf("%s 模型的 token 配额已耗尽，%s", model, tokenErr), r.cfg.Notifications.Desktop)
			} else if failureCode == ErrPhaseTimeout {
				notify.SendTaskAction(t.ID, t.Title, "⏰", "执行超时",
					fmt.Sprintf("%s 模型 %v 无响应，任务自动超时", model, timeout), r.cfg.Notifications.Desktop)
			} else {
				notify.SendTaskAction(t.ID, t.Title, "💥", "进程异常", fmt.Sprintf("%s: %v", reason, runErr), r.cfg.Notifications.Desktop)
			}

			fellback := false
			// Do not start a fallback while the daemon is shutting down — the
			// OMP would be killed right after. Phase timeout still falls back:
			// the fallback command gets its own fresh timeout budget.
			// (This branch is only reachable when shutdownInterrupt is false,
			// so the old ctx.Err()==Canceled check — always true after
			// cancel() — silently disabled fallback forever.)
			if !signalKilled && failureCode != ErrAPIKeyUnavailable {
				if fallbackModel := r.cfg.FallbackModelFor(t.Assignee); fallbackModel != "" && fallbackModel != model {
					r.logger.Printf("task %s: retrying with fallback model %s", t.ID, fallbackModel)
					notify.SendTaskAction(t.ID, t.Title, "🔄", "模型切换",
						fmt.Sprintf("%s 不可用（%s），自动切换到 %s 继续执行", model, reason, fallbackModel), r.cfg.Notifications.Desktop)

				fallbackArgs := []string{"--model", fallbackModel}
				fallbackArgs = append(fallbackArgs, args[2:]...)
				fbCtx, fbCancel := context.WithTimeout(r.daemonCtx, timeout)
				retryCmd := exec.CommandContext(fbCtx, r.cfg.OMPCmd, fallbackArgs...)
				retryCmd.Cancel = func() error { return retryCmd.Process.Signal(syscall.SIGTERM) }
				retryCmd.WaitDelay = 30 * time.Second
				retryCmd.Dir = repoDir
				if f != nil {
					retryCmd.Stdout = io.MultiWriter(f, os.Stderr)
					retryCmd.Stderr = io.MultiWriter(f, os.Stderr)
				} else {
					retryCmd.Stdout = os.Stderr
					retryCmd.Stderr = os.Stderr
				}
				fbTailDone := make(chan struct{})
				go tailOMPLog(ompLogPath, f, fbTailDone)
				if fbStartErr := retryCmd.Start(); fbStartErr != nil {
					r.logger.Printf("task %s: fallback OMP start failed: %v", t.ID, fbStartErr)
					fbShutdown := r.daemonCtx.Err() != nil
					fbCancel()
					close(fbTailDone)
					if !fbShutdown {
						fellback = true
					} else {
						// Shutdown raced the fallback start: keep status, auto-resume later.
						r.logger.Printf("task %s: fallback interrupted by daemon shutdown, status=%s kept", t.ID, t.Status)
					}
				} else {
					if err := os.WriteFile(pidFile, []byte(formatPIDRecord(retryCmd.Process.Pid)), 0o600); err != nil {
						r.logger.Printf("task %s: write fallback PID file: %v", t.ID, err)
					}
					retryErr := retryCmd.Wait()
					fbShutdown := r.daemonCtx.Err() != nil
					fbCancel()
					close(fbTailDone)
					if retryErr != nil {
						if fbShutdown {
							// Shutdown interrupted the running fallback: keep status.
							r.logger.Printf("task %s: fallback interrupted by daemon shutdown (main failure: %s), status=%s kept", t.ID, reason, t.Status)
							if err := yamlfrontmatter.Update(taskPath, map[string]interface{}{
								"phase_error_code": string(ErrPhaseInterrupted),
								"phase_error":      "daemon 重启中断，等待自动恢复",
								"phase_log":        logPath,
							}); err != nil {
								r.logger.Printf("task %s: record interruption: %v", t.ID, err)
							}
						} else {
							fbReason := "异常退出"
							if errors.Is(fbCtx.Err(), context.DeadlineExceeded) {
								fbReason = "超时"
								failureCode = ErrPhaseTimeout
							}
							r.logger.Printf("task %s: fallback OMP also failed (%s): %v", t.ID, fbReason, retryErr)
							notify.SendTaskAction(t.ID, t.Title, "❌", "全部失败",
								fmt.Sprintf("%s 和 %s 均不可用（%s），请检查网络和 API 状态", model, fallbackModel, fbReason), r.cfg.Notifications.Desktop)
							fellback = true
						}
					} else {
						r.logger.Printf("task %s: completed via fallback model %s", t.ID, fallbackModel)
						if err := r.validatePhaseCompletion(taskPath, t.ID, phase); err != nil {
							r.logger.Printf("task %s: phase validation failed: %v", t.ID, err)
						}
						r.compactPlanHistory(taskPath, phase)
						r.validateChangedDocs(repoDir, t.ID, phase)
						if _, statErr := os.Stat(taskPath); statErr == nil {
							notify.StatusNotify(taskPath, r.cfg.Notifications.Desktop)
						}
						r.clearPhaseRetry(taskPath, phase)
						r.clearPhaseError(taskPath, t.ID)
					}
				}
				}
			}
			noFallback := r.cfg.FallbackModelFor(t.Assignee) == "" || r.cfg.FallbackModelFor(t.Assignee) == model
			if fellback || noFallback {
				r.handlePhaseFailure(taskPath, t.ID, t.Title, t.Status, phase, failureCode, reason, logPath)
			}
		} else {
			r.logger.Printf("task %s: completed", t.ID)
			if err := r.validatePhaseCompletion(taskPath, t.ID, phase); err != nil {
				r.logger.Printf("task %s: phase validation failed: %v", t.ID, err)
			}
			// A successful planning round folds the plan-history section down
			// to the newest versions — large TASK docs are mostly historical
			// plan blocks that every later session would otherwise re-read.
			r.compactPlanHistory(taskPath, phase)
			r.validateChangedDocs(repoDir, t.ID, phase)
			if phase == "round2" && t.TargetBranch == "" {
				if branch, err := gitCurrentBranch(repoDir); err == nil && branch != "" && branch != "HEAD" {
					if updateErr := r.updateTaskFile(taskPath, t.ID, t.Title, map[string]interface{}{"target_branch": branch}); updateErr != nil {
						r.logger.Printf("task %s: write target_branch: %v", t.ID, updateErr)
					}
				}
			}
			if _, statErr := os.Stat(taskPath); statErr == nil {
				notify.StatusNotify(taskPath, r.cfg.Notifications.Desktop)
			}
			r.clearPhaseRetry(taskPath, phase)
			r.clearPhaseError(taskPath, t.ID)
		}
		if f != nil {
			if err := f.Close(); err != nil {
				r.logger.Printf("task %s: close task log: %v", t.ID, err)
			}
		}
		processed++
	}
	return processed
}

// restoreBlockedPhase applies the shared resume updates for a blocked task
// (manual resume, PHASE_INTERRUPTED self-heal, and API-key probe all use the
// same field set). resetBudget additionally clears the auto-resume failure
// budget — only manual resumes do that.
func (r *Runner) restoreBlockedPhase(taskPath, phase string, resetBudget bool) error {
	updates := map[string]interface{}{
		"status":              phase,
		"blocked_phase":       "",
		"phase_error":         "",
		"phase_log":           "",
		"phase_error_code":    "",
		"resume_approved":     false,
		"auto_resume_pending": false,
	}
	if resetBudget {
		updates["auto_resume_count"] = 0
	}
	return yamlfrontmatter.Update(taskPath, updates)
}

// handlePhaseFailure tracks retry counts for refining/planning phases and
// transitions the task to blocked after the second consecutive failure.
func (r *Runner) handlePhaseFailure(taskPath, taskID, taskTitle, status, phase string, code ErrorCode, reason, logPath string) {
	// Auto-sink the first occurrence of this failure code+phase into the
	// knowledge base as a bug pattern (dedup inside AppendFailurePattern).
	// Synchronous: small IO, and the file-level dedup store needs no locking.
	if err := knowledge.AppendFailurePattern(r.cfg.ObsidianVault, string(code), phase, taskID, logPath); err != nil {
		r.logger.Printf("task %s: knowledge base pattern sink failed: %v", taskID, err)
	}
	policy := recoveryForPhase(phase, code)
	if policy == recoveryBlock {
		if err := yamlfrontmatter.Update(taskPath, map[string]interface{}{
			"status":           "blocked",
			"blocked_phase":    status,
			"phase_error_code": string(code),
			"phase_error":      reason,
			"phase_log":        logPath,
			"resume_approved":  false,
		}); err != nil {
			r.logger.Printf("task %s: record phase block: %v", taskID, err)
		}
		if code == ErrAPIKeyUnavailable {
			// Debounced: one toast per 5 minutes, not one per failing task.
			r.notifyKeyUnavailable()
		}
		return
	}
	if policy == recoveryConflict {
		if err := yamlfrontmatter.Update(taskPath, map[string]interface{}{
			"status":           "conflict",
			"merge_approved":   false,
			"phase_error_code": string(code),
			"phase_error":      reason,
			"phase_log":        logPath,
		}); err != nil {
			r.logger.Printf("task %s: record merge conflict: %v", taskID, err)
		}
		return
	}
	if policy == recoveryReview {
		if err := yamlfrontmatter.Update(taskPath, map[string]interface{}{
			"status":           "review",
			"merge_approved":   false,
			"phase_error_code": string(code),
			"phase_error":      reason,
			"phase_log":        logPath,
		}); err != nil {
			r.logger.Printf("task %s: record merge failure: %v", taskID, err)
		}
		return
	}
	if policy == recoveryFallbackThenBlock {
		// Only a failure that follows an auto-resume (pending marker set) counts
		// against the budget; an initial failure or one after a manual resume
		// leaves the count untouched so auto-resume gets its full 2 attempts.
		attempts := 0
		if data, err := os.ReadFile(taskPath); err == nil {
			if fm, err := yamlfrontmatter.Parse(data); err == nil && fm != nil && fm.AutoResumePending {
				attempts = fm.AutoResumeCount + 1
			}
		}
		if err := yamlfrontmatter.Update(taskPath, map[string]interface{}{
			"status":              "blocked",
			"blocked_phase":       "implementing",
			"phase_error_code":    string(code),
			"phase_error":         reason,
			"phase_log":           logPath,
			"resume_approved":     false,
			"auto_resume_count":   attempts,
			"auto_resume_pending": false,
		}); err != nil {
			r.logger.Printf("task %s: record Round 2 failure: %v", taskID, err)
		}
		r.phaseFailures.Store(taskPath, time.Now())
		return
	}

	var retryField string
	switch phase {
	case "refining":
		retryField = "refine_retry_count"
	case "planning":
		retryField = "planning_retry_count"
	default:
		return
	}
	data, err := os.ReadFile(taskPath)
	if err != nil {
		r.logger.Printf("task %s: cannot read retry count (%v), assuming first failure", taskID, err)
	}
	currentRetry := 0
	if err == nil {
		fm, parseErr := yamlfrontmatter.Parse(data)
		if parseErr == nil && fm != nil {
			currentRetry = fm.RefineRetryCount
			if phase == "planning" {
				currentRetry = fm.PlanningRetryCount
			}
		}
	}
	if currentRetry == 0 {
		if err := yamlfrontmatter.Update(taskPath, map[string]interface{}{
			retryField:         1,
			"phase_error_code": string(code),
			"phase_error":      reason,
			"phase_log":        logPath,
		}); err != nil {
			r.logger.Printf("task %s: record retry: %v", taskID, err)
		}
		return
	}
	if err := yamlfrontmatter.Update(taskPath, map[string]interface{}{
		"status":           "blocked",
		"blocked_phase":    phase,
		"phase_error_code": string(code),
		"phase_error":      reason,
		"phase_log":        logPath,
		retryField:         0,
	}); err != nil {
		r.logger.Printf("task %s: record blocked phase: %v", taskID, err)
		return
	}
	notify.SendTaskAction(taskID, taskTitle, "🚫", "阶段失败",
		fmt.Sprintf("阶段 %s 连续失败两次，任务已阻塞。修复后设置 resume_approved: true 恢复。", phase),
		r.cfg.Notifications.Desktop)
}
func (r *Runner) clearPhaseRetry(taskPath, phase string) {
	var err error
	switch phase {
	case "refining":
		err = yamlfrontmatter.Update(taskPath, map[string]interface{}{"refine_retry_count": 0})
	case "planning":
		err = yamlfrontmatter.Update(taskPath, map[string]interface{}{"planning_retry_count": 0})
	}
	if err != nil {
		r.logger.Printf("clear %s retry count: %v", phase, err)
	}
}

// clearPhaseError clears stale phase_error fields after a phase succeeds,
// so an earlier interruption marker (PHASE_INTERRUPTED) does not linger on
// the task.
func (r *Runner) clearPhaseError(taskPath, taskID string) {
	if err := yamlfrontmatter.Update(taskPath, map[string]interface{}{
		"phase_error_code": "",
		"phase_error":      "",
		"phase_log":        "",
	}); err != nil {
		r.logger.Printf("task %s: clear phase error: %v", taskID, err)
	}
}

func (r *Runner) resolveRepo(t task.ReadyTask) (string, error) {
	mapFile := filepath.Join(r.cfg.SkillInstallDir, "config", "vault-map.json")
	projectName := t.Project
	result := project.ResolveProject(mapFile, projectName, t.NewProject)

	// If direct lookup fails, try matching Vault directory name to vault-map key
	// e.g., "001-release-manager" → "release-manager"
	if result.Status == "error" {
		if mapped := project.MatchVaultDir(mapFile, projectName); mapped != "" {
			projectName = mapped
			result = project.ResolveProject(mapFile, projectName, t.NewProject)
		}
	}

	if result.Status == "error" {
		return "", fmt.Errorf("resolve project: %s", result.Error)
	}
	if result.Status == "existing" {
		// Vault-fallback promotion: a registered project whose path is not a
		// git root (registered via the Vault project dir fallback) but has a
		// conventional git_remote must run in its own checkout — otherwise
		// worktrees and merges silently target the enclosing Vault repo
		// (wrong-repo merges, observed with TASK-001-demo). Promotion
		// failures fall back to the registered path: read-only phases must
		// not block, and the merge-time ensureGitRemote guard still refuses
		// pushes to a mismatched origin.
		promoted, promoteErr := r.ensureProjectCheckout(t, result.Path)
		if promoteErr != nil {
			r.logger.Printf("task %s: promote project %s to standalone checkout: %v", t.ID, projectName, promoteErr)
			return result.Path, nil
		}
		return promoted, nil
	}
	if result.Status == "new" {
		if err := os.MkdirAll(result.Path, 0755); err != nil {
			return "", fmt.Errorf("create new project %s: %w", result.Path, err)
		}
		// Auto-register the new project in vault-map.json (name/path/
		// git_remote/project_id generated from existing conventions) so later
		// scans resolve it as "existing" without manual configuration.
		gitRemote := project.GitRemoteFor(mapFile, projectName)
		if err := project.RegisterProject(mapFile, projectName, result.Path, gitRemote, false); err != nil {
			r.logger.Printf("task %s: register new project %s: %v", t.ID, projectName, err)
		}
		// Seed the CONTEXT.md skeleton so context injection has a base and
		// agents build on it instead of creating ad-hoc files.
		if err := r.ensureProjectContext(result.Path); err != nil {
			r.logger.Printf("task %s: seed CONTEXT.md: %v", t.ID, err)
		}
	}
	return result.Path, nil
}

// ensureProjectContext creates the Notes/CONTEXT.md skeleton for a brand-new
// project when it does not exist yet. Agents fill in the sections during the
// first rounds.
func (r *Runner) ensureProjectContext(projDir string) error {
	notesDir := filepath.Join(projDir, "Notes")
	if err := os.MkdirAll(notesDir, 0o755); err != nil {
		return err
	}
	contextPath := filepath.Join(notesDir, "CONTEXT.md")
	if _, err := os.Stat(contextPath); err == nil {
		return nil // already exists
	}
	content := `# <Project> Context

本文件定义 <project> 流水线自动化中的共享领域词汇。

## Language

**（术语）**: （定义）。_Avoid_: （反义词）

## Development Constraints

- （开发约束，如技术栈、边界）

## Anti-patterns

- （反模式）

## Reference Map
| 领域关键词 | 参考路径 |
|---|---|
| （关键词） | （References 路径） |
`
	return os.WriteFile(contextPath, []byte(content), 0o644)
}

func (r *Runner) selectModel(assignee string) string {
	return r.cfg.Model(assignee)
}

// cleanupOldLogs removes task log files older than 7 days.
func (r *Runner) cleanupOldLogs() {
	logDir := r.cfg.LogDir
	if logDir == "" {
		home, _ := os.UserHomeDir()
		logDir = filepath.Join(home, ".omp", "logs")
	}
	taskLogDir := filepath.Join(logDir, "tasks")
	entries, err := os.ReadDir(taskLogDir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-7 * 24 * time.Hour)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			path := filepath.Join(taskLogDir, entry.Name())
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				r.logger.Printf("remove old task log %s: %v", path, err)
			}
		}
	}
}

func SignalContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	// Register before the goroutine starts: a SIGTERM arriving between the
	// Notify call and the goroutine launch would otherwise hit the default
	// handler and kill the process.
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		select {
		case <-ch:
			signal.Stop(ch)
			cancel()
		case <-ctx.Done():
			signal.Stop(ch)
		}
	}()
	return ctx
}

// tokenQuotaPatterns matches log lines indicating token quota exhaustion.
var tokenQuotaPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)insufficient_quota`),
	regexp.MustCompile(`(?i)rate_limit_exceeded`),
	regexp.MustCompile(`(?i)\bquota\b.*\b(exceeded|exhausted|insufficient|limit)\b`),
	regexp.MustCompile(`(?i)\bbilling\b`),
	regexp.MustCompile(`(?i)余额不足`),
	regexp.MustCompile(`(?i)充值`),
	regexp.MustCompile(`(?i)tokens?\s*(limit|quota|exhausted)`),
	regexp.MustCompile(`(?i)429\s`),
}

// keyUnavailablePatterns matches OMP log lines indicating the provider API key
// could not be resolved (typically KeePassXC/secret service locked or missing).
var keyUnavailablePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)no api key found`),
	regexp.MustCompile(`(?i)no api key`),
	regexp.MustCompile(`(?i)missing.*api[_-]?key`),
}

// notifyKeyUnavailable sends one debounced desktop toast for an API-key
// outage (5-minute window, daemon-global). Without this, a key outage
// hitting a large batch produced one notification per failing task
// (observed: 40+ simultaneous toasts).
func (r *Runner) notifyKeyUnavailable() {
	if last, ok := r.keyNotifyAt.Load("key"); ok && time.Since(last.(time.Time)) < 5*time.Minute {
		return
	}
	r.keyNotifyAt.Store("key", time.Now())
	notify.SendTaskAction("", "API Key 不可用", "🔐", "等待 API Key",
		"KeePassXC 未解锁或 key 不可达，任务已暂停等待。解锁后 daemon 自动恢复，无需手动操作。", r.cfg.Notifications.Desktop)
}

// checkAPIKeyUnavailable scans the OMP log for missing API key errors.
// Returns a human-readable message if found, empty string otherwise.
func checkAPIKeyUnavailable(logPath string) string {	f, err := os.Open(logPath)
	if err != nil {
		return ""
	}
	defer func() {
		if err := f.Close(); err != nil {
			log.Printf("close key log %s: %v", logPath, err)
		}
	}()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		for _, pat := range keyUnavailablePatterns {
			if pat.MatchString(line) {
				return "OMP 无法获取模型 API Key（KeePassXC 未解锁或 key 未配置）"
			}
		}
	}
	return ""
}

// apiKeyProbe is the pluggable key-availability check; tests override it via
// apiKeyProbe.Store. Atomic so concurrent task goroutines (async dispatch)
// can read it while a test replaces it — a plain variable races when a
// runTask goroutine outlives its test and the next test swaps the probe.
var apiKeyProbe atomic.Value // func() bool

func init() {
	apiKeyProbe.Store(func() bool { return defaultAPIKeyProbe() })
}

// apiKeyAvailable probes whether any provider API key is reachable: either an
// env var override (DEEPSEEK_API_KEY / CODEX_API_KEY as used by
// ~/.omp/get-api-key.sh) or the KeePassXC database password in the secret
// service. The probe is cheap (sub-second) and runs before dispatching OMP so
// key-less runs never start a headless session.
func apiKeyAvailable() bool {
	probe, ok := apiKeyProbe.Load().(func() bool)
	if !ok {
		return false
	}
	return probe()
}

func defaultAPIKeyProbe() bool {
	if os.Getenv("DEEPSEEK_API_KEY") != "" || os.Getenv("CODEX_API_KEY") != "" {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "secret-tool", "lookup", "app", "keepassxc", "type", "db-password").Output()
	return err == nil && len(bytes.TrimSpace(out)) > 0
}

// checkTokenQuota scans the OMP log for token quota exhaustion errors.
// Returns a human-readable message if found, empty string otherwise.
func checkTokenQuota(logPath, model string) string {
	f, err := os.Open(logPath)
	if err != nil {
		return ""
	}
	defer func() {
		if err := f.Close(); err != nil {
			log.Printf("close quota log %s: %v", logPath, err)
		}
	}()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		for _, pat := range tokenQuotaPatterns {
			if pat.MatchString(line) {
				provider := detectProvider(model)
				return fmt.Sprintf("请前往 %s 平台充值后续航", provider)
			}
		}
	}
	return ""
}

// detectProvider returns a human-readable provider name from a model ID.
func detectProvider(model string) string {
	if strings.Contains(model, "deepseek") {
		return "DeepSeek"
	}
	if strings.Contains(model, "gpt") || strings.Contains(model, "openai") {
		return "OpenAI"
	}
	if strings.Contains(model, "claude") || strings.Contains(model, "anthropic") {
		return "Anthropic"
	}
	if strings.Contains(model, "gemini") {
		return "Google Gemini"
	}
	return model
}

// noisePatterns match noisy OMP debug lines to exclude from task logs.
var noisePatterns = []*regexp.Regexp{
	regexp.MustCompile(`TTSR ast match reported parse errors`),
	regexp.MustCompile(`Auto-compaction threshold decision`),
}

// tailOMPLog reads new lines from OMP's structured log and writes non-noisy lines to the task log.
func tailOMPLog(logPath string, taskLog *os.File, done <-chan struct{}) {
	if taskLog == nil || logPath == "" {
		return
	}
	f, err := os.Open(logPath)
	if err != nil {
		return
	}
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		log.Printf("seek OMP log %s: %v", logPath, err)
		return
	}

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	scanner := bufio.NewScanner(f)
	for {
		select {
		case <-done:
			for scanner.Scan() {
				if !isNoise(scanner.Text()) {
					if _, err := taskLog.Write(append(scanner.Bytes(), '\n')); err != nil {
						log.Printf("write tailed OMP log: %v", err)
					}
				}
			}
			return
		case <-ticker.C:
			for scanner.Scan() {
				if !isNoise(scanner.Text()) {
					if _, err := taskLog.Write(append(scanner.Bytes(), '\n')); err != nil {
						log.Printf("write tailed OMP log: %v", err)
					}
				}
			}
		}
	}
}

func isNoise(line string) bool {
	for _, pat := range noisePatterns {
		if pat.MatchString(line) {
			return true
		}
	}
	return false
}

func acquireLock(cfg *config.Config) (func(), error) {
	vaultHash := fmt.Sprintf("%x", sha256.Sum256([]byte(filepath.Clean(cfg.ObsidianVault))))[:16]
	lockFile := filepath.Join(os.TempDir(), "otg-daemon-"+vaultHash+".lock")
	f, err := os.OpenFile(lockFile, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return nil, fmt.Errorf("open lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if err := f.Close(); err != nil {
			log.Printf("close daemon lock after flock failure: %v", err)
		}
		return nil, fmt.Errorf("another daemon instance is running for this vault")
	}
	return func() {
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_UN); err != nil {
			log.Printf("unlock daemon lock: %v", err)
		}
		if err := f.Close(); err != nil {
			log.Printf("close daemon lock: %v", err)
		}
		if err := os.Remove(lockFile); err != nil && !os.IsNotExist(err) {
			log.Printf("remove daemon lock %s: %v", lockFile, err)
		}
	}, nil
}

// procAlive checks if a process with the given PID is running and not a zombie.
func procAlive(pid int) bool {
	if data, err := os.ReadFile(filepath.Join("/proc", fmt.Sprint(pid), "stat")); err == nil {
		fields := strings.Fields(string(data))
		if len(fields) > 2 && fields[2] == "Z" {
			return false
		}
	}
	return procExists(pid)
}

// procExists checks if a PID exists via signal 0.
func procExists(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return process.Signal(syscall.Signal(0)) == nil
}

// maybeRebuildRefIndex rebuilds References/INDEX.md after writes, debounced
// to 10 seconds so agent intake batches (multiple file writes) trigger one
// rebuild. The first write after the window rebuilds synchronously enough to
// keep INDEX fresh for the next retrieval.
func (r *Runner) maybeRebuildRefIndex() {
	now := time.Now()
	if last, ok := r.refIndexRebuiltAt.Load("last"); ok && now.Sub(last.(time.Time)) < 10*time.Second {
		return
	}
	r.refIndexRebuiltAt.Store("last", now)
	if n, err := knowledge.RebuildINDEX(r.cfg.ObsidianVault); err != nil {
		r.logger.Printf("knowledge-base INDEX rebuild failed: %v", err)
	} else {
		r.logger.Printf("knowledge-base INDEX rebuilt: %d entries", n)
	}
}

// maybeSyncKnowledgeDB incrementally syncs the retrieval store (SQLite FTS +
// vectors) after References/ writes, debounced to 10 seconds like the INDEX
// rebuild. Runs in a goroutine so the watcher event loop never blocks behind
// embedding; kbSyncRunning single-flights overlapping triggers. SyncKnowledgeDB
// is idempotent (content_hash comparison): an unchanged vault is a cheap no-op,
// and a re-trigger after a long embed pass picks up writes that arrived during
// the window. Embedding failures are non-fatal (FTS still commits) and logged
// without a desktop notification — the same policy as the merge path.
func (r *Runner) maybeSyncKnowledgeDB() {
	now := time.Now()
	if last, ok := r.kbSyncAt.Load("last"); ok && now.Sub(last.(time.Time)) < 10*time.Second {
		return
	}
	if !r.kbSyncRunning.CompareAndSwap(false, true) {
		return
	}
	r.kbSyncAt.Store("last", now)
	go func() {
		defer r.kbSyncRunning.Store(false)
		dbPath := knowledge.KBPath(r.cfg.ObsidianVault, r.cfg.KBDb)
		var client *knowledge.EmbeddingClient
		if r.cfg.KBEmbedding != nil {
			client = knowledge.NewEmbeddingClient(r.cfg.KBEmbedding)
		}
		stats, err := knowledge.SyncKnowledgeDB(r.cfg.ObsidianVault, dbPath, client)
		if err != nil {
			r.logger.Printf("knowledge-base store sync failed: %v", err)
			return
		}
		if stats.VecError != nil {
			r.logger.Printf("knowledge-base store synced (FTS, vectors failed): %d docs: %v", stats.TotalDocs, stats.VecError)
		} else if stats.VectorsRefreshed {
			r.logger.Printf("knowledge-base store synced: %d docs, %d chunks", stats.TotalDocs, stats.TotalChunks)
		}
	}()
}

// resolveVaultProjectDir resolves a project name to its vault directory.
// Accepts both the vault-map name ("magic-models-manager") and the full
// directory name with numeric prefix ("002-magic-models-manager") — task
// frontmatter historically carries either form.
func resolveVaultProjectDir(vaultPath, projectName string) string {
	projectsDir := filepath.Join(vaultPath, "Projects")
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		// Exact directory match first (prefixed project values like
		// "002-magic-models-manager"), then suffix match for unprefixed
		// vault-map names ("magic-models-manager").
		name := e.Name()
		if name == projectName {
			return filepath.Join(projectsDir, name)
		}
		if idx := strings.IndexByte(name, '-'); idx > 0 {
			if name[idx+1:] == projectName {
				return filepath.Join(projectsDir, name)
			}
		}
	}
	return ""
}

// needsContextInjection returns true for task phases that benefit from
// project context (constraints, domain terms, ADRs) in the OMP prompt.
func needsContextInjection(status string) bool {
	switch status {
	case "refining", "planning", "implementing", "plan-review":
		return true
	}
	return false
}

// formatPIDRecord writes PID+start_time+cmd for identity verification.
func formatPIDRecord(pid int) string {
	const longForm = "2006-01-02 15:04:05 -0700"
	bootTime, _ := procStartTime(pid)
	cmd, _ := os.Executable()
	return fmt.Sprintf("%d\n%s\n%s\n", pid, bootTime.Format(longForm), cmd)
}

func procStartTime(pid int) (time.Time, error) {
	data, err := os.ReadFile(filepath.Join("/proc", fmt.Sprint(pid), "stat"))
	if err != nil {
		return time.Time{}, err
	}
	fields := strings.Fields(string(data))
	if len(fields) < 22 {
		return time.Time{}, fmt.Errorf("unexpected stat format")
	}
	startTicks, _ := strconv.ParseInt(fields[21], 10, 64)
	uptimeData, _ := os.ReadFile("/proc/uptime")
	uptimeFields := strings.Fields(string(uptimeData))
	if len(uptimeFields) < 1 {
		return time.Time{}, fmt.Errorf("unexpected uptime format")
	}
	uptimeSec, _ := strconv.ParseFloat(uptimeFields[0], 64)
	startSec := float64(startTicks) / float64(syscall.Getpagesize()/4096*100)
	bootTime := time.Now().Add(-time.Duration(uptimeSec * float64(time.Second)))
	return bootTime.Add(time.Duration(startSec * float64(time.Second))), nil
}

func parsePIDRecord(data []byte) (int, time.Time, string) {
	lines := strings.SplitN(strings.TrimSpace(string(data)), "\n", 3)
	if len(lines) == 0 {
		return 0, time.Time{}, ""
	}
	pid, err := strconv.Atoi(lines[0])
	if err != nil {
		return 0, time.Time{}, ""
	}
	if len(lines) < 3 {
		return pid, time.Time{}, ""
	}
	const longForm = "2006-01-02 15:04:05 -0700"
	startTime, err := time.Parse(longForm, lines[1])
	if err != nil {
		return pid, time.Time{}, lines[2]
	}
	return pid, startTime, lines[2]
}

func pidMatchesTask(pid int, recordedStart time.Time, recordedCmd string) bool {
	if recordedStart.IsZero() || recordedCmd == "" {
		return true
	}
	actualStart, err := procStartTime(pid)
	if err != nil {
		return false
	}
	return actualStart.Equal(recordedStart)
}
