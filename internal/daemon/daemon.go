package daemon

import (
	"bufio"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ndzuki/obsidian-task-runner/internal/config"
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
	worktreeCache      sync.Map   // taskRunKey → worktreePath (parallel warmup)
	implementationGate *implementationGate
	daemonCtx          context.Context // bound to daemon lifecycle; cancelled on shutdown
}

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
type taskResult struct {
	repoDir   string
	lockMode  repoLockMode
	processed int
}

func New(cfg *config.Config) *Runner {
	return &Runner{
		cfg:                cfg,
		implementationGate: newImplementationGate(cfg.MaxConcurrentTasks),
		daemonCtx:          context.Background(),
	}
}

func (r *Runner) Run(ctx context.Context) error {
	if err := r.initLogging(); err != nil {
		return fmt.Errorf("init logging: %w", err)
	}
	defer func() {
		if err := r.logWriter.Close(); err != nil {
			r.logger.Printf("close log writer: %v", err)
		}
	}()

	if r.cfg.ObsidianVault == "" {
		return fmt.Errorf("obsidian_vault not configured")
	}

	unlock, err := acquireLock(r.cfg)
	if err != nil {
		return err
	}
	defer unlock()

	r.logger.Printf("daemon started, vault=%s", r.cfg.ObsidianVault)
	r.cleanupOldLogs()

	w, err := watch.New(r.cfg.ObsidianVault, 5*time.Second)
	if err != nil {
		return fmt.Errorf("start watcher: %w", err)
	}
	w.Start(ctx)

	// Run an initial scan to catch any tasks that became ready while daemon was down.

	ticker := time.NewTicker(time.Duration(r.cfg.PollIntervalMin) * time.Minute)
	defer ticker.Stop()

	if err := r.scanAndProcess(); err != nil {
		r.logger.Printf("initial scan error: %v", err)
	}
	for {
		select {
		case <-ctx.Done():
			r.logger.Println("daemon shutting down")
			return nil
		case evt := <-w.Events():
			r.logger.Printf("watcher: %s %s changed", evt.Dir, filepath.Base(evt.Path))
			if evt.Dir == "Requirements" {
				reqRel, _ := filepath.Rel(r.cfg.ObsidianVault, evt.Path)
				var results []task.AffectedResult
				if _, statErr := os.Stat(evt.Path); os.IsNotExist(statErr) {
					results = task.OnReqDeleted(r.cfg.ObsidianVault, reqRel)
				} else {
					results = task.OnReqChanged(r.cfg.ObsidianVault, reqRel)
				}
				for _, result := range results {
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
			time.Sleep(3 * time.Second)
			if err := r.scanAndProcess(); err != nil {
				r.logger.Printf("event scan error: %v", err)
			}
		case <-ticker.C:
			r.logger.Println("timer: periodic scan")
			if err := r.scanAndProcess(); err != nil {
				r.logger.Printf("periodic scan error: %v", err)
			}
		}
	}
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
	if r.cfg.ObsidianVault == "" {
		return fmt.Errorf("obsidian_vault not configured")
	}
	unlock, err := acquireLock(r.cfg)
	if err != nil {
		r.logger.Printf("skipping (lock held by watcher daemon): %v", err)
		return nil // not an error — watcher daemon is handling it
	}
	defer unlock()
	return r.scanAndProcess()
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
	return nil
}

func (r *Runner) scanAndProcess() error {
	r.scanMu.Lock()
	tasks, err := task.FindReadyTasks(r.cfg.ObsidianVault)
	if err != nil {
		r.logger.Printf("scan error: %v", err)
	}
	r.logger.Printf("scan: %d ready tasks", len(tasks))
	if len(tasks) == 0 {
		r.scanMu.Unlock()
		r.processPriorityAssessments(context.Background())
		return nil
	}
	r.scanMu.Unlock()

	for round := 0; round < 3; round++ {
		if r.processBatch(tasks) == 0 {
			break
		}
		// Adaptive polling: check every 500ms for cloud-sync flush before re-scanning
		// Adaptive polling: re-scan every 500ms for state changes after OMP dispatch.
		// Covers filesystems where fsnotify is unreliable (e.g. Vault git sync).
		for range 60 {
			time.Sleep(500 * time.Millisecond)
			r.scanMu.Lock()
			tasks, _ = r.findReadyTasks()
			r.scanMu.Unlock()
			if len(tasks) > 0 {
				break
			}
		}
		if len(tasks) == 0 {
			break
		}
	}
	r.processPriorityAssessments(context.Background())
	return nil
}

// processBatch dispatches every schedulable task. Repository locks protect
// shared working directories; implementing tasks must reserve daemon-wide
// capacity before their execution goroutine is created.
func (r *Runner) processBatch(tasks []task.ReadyTask) int {
	pending := r.prepareBatch(tasks)
	done := make(chan taskResult, len(pending))
	processed := 0
	running := 0

	for len(pending) > 0 || running > 0 {
		var implementationChanged <-chan struct{}
		implementationBlocked := false
		for {
			index := -1
			for i := range pending {
				candidate := &pending[i]
				reservedImplementation := false
				if candidate.task.Status == "implementing" {
					acquired, changed := r.implementationGate.tryAcquireLocal()
					if !acquired {
						implementationBlocked = true
						implementationChanged = changed
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
				break
			}

			candidate := pending[index]
			pending = append(pending[:index], pending[index+1:]...)
			running++
			go func(p preparedTask) {
				done <- taskResult{
					repoDir:   p.repoDir,
					lockMode:  p.lockMode,
					processed: r.processPreparedTask(p),
				}
			}(candidate)
		}

		if running == 0 {
			if implementationBlocked {
				if r.implementationGate.localActive() == 0 {
					r.logger.Printf("scheduler: %d tasks waiting for adopted implementations — will retry on next scan", len(pending))
				} else if implementationChanged != nil {
					<-implementationChanged
					continue
				}
			}
			r.logger.Printf("scheduler: %d tasks cannot be scheduled", len(pending))
			break
		}

		result := <-done
		running--
		processed += result.processed
		r.unlockRepo(result.repoDir, result.lockMode)
	}

	return processed
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
			r.logger.Printf("task %s: waiting for grilling resolution", t.ID)
			notify.SendGrillingReminder(t.ID, t.Title, t.ReqDoc, r.cfg.ObsidianVault, r.cfg.Notifications.Desktop)
			continue
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
		if isRound2(t) {
			lockMode = repoLockNone
		}
		prepared := preparedTask{task: t, repoDir: repoDir, workDir: repoDir, lockMode: lockMode}
		if isRound2(t) {
			lock := r.repoLock(repoDir)
			lock.Lock()
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
					lock.Lock()
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

func (r *Runner) updateTask(t task.ReadyTask, updates map[string]interface{}) error {
	return r.updateTaskFile(t.FilePath, t.ID, t.Title, updates)
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
// since the last commit and validates them with ValidateDocument. Corrupted
// documents (CONTEXT.md, ADR files, etc.) are logged but do not
// halt the pipeline.
func (r *Runner) validateChangedDocs(repoDir, taskID, phase string) {
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
			r.logger.Printf("task %s: %s damaged after %s: %v", taskID, f, phase, err)
			notify.SendTaskAction(taskID, "", "📄", "文档损坏",
				fmt.Sprintf("%s 在 %s 阶段后被修改但无法通过校验: %v", f, phase, err),
				r.cfg.Notifications.Desktop)
		}
	}
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
	tasks, err := task.FindReadyTasks(r.cfg.ObsidianVault)
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

// reqHashChanged reports whether the requirement document hash differs between
// refine_req_hash and plan_req_hash, indicating the requirement changed after
// the last plan was generated.
func reqHashChanged(t task.ReadyTask) bool {
	return t.RefineReqHash != "" && t.PlanReqHash != "" && t.RefineReqHash != t.PlanReqHash
}

func (r *Runner) processBatchSequential(tasks []task.ReadyTask, repoDir string) int {
	processed := 0
	for _, t := range tasks {
		taskPath := t.FilePath

		if t.Status == "blocked" {
			// Check if this is a phase-failure blocked task waiting for resume.
			if data, err := os.ReadFile(taskPath); err == nil {
				if fm, err := yamlfrontmatter.Parse(data); err == nil && fm != nil {
					if fm.BlockedPhase != "" && fm.ResumeApproved {
						r.logger.Printf("task %s: resume approved, restoring %s", t.ID, fm.BlockedPhase)
						if err := yamlfrontmatter.Update(taskPath, map[string]interface{}{
							"status":          fm.BlockedPhase,
							"blocked_phase":   "",
							"phase_error":     "",
							"phase_log":       "",
							"resume_approved": false,
						}); err != nil {
							r.logger.Printf("task %s: restore blocked phase: %v", t.ID, err)
							continue
						}
						t.Status = fm.BlockedPhase
						// Fall through to normal dispatch below.
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

		if t.MergeApproved && (t.Status == "review" || t.Status == "conflict") {
			if err := r.processMergeTask(t, repoDir); err != nil {
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
			// ── Refining early-out: skip to planning if fully_mature and hash unchanged ──
			if t.Maturity == "fully_mature" && !reqHashChanged(t) {
				r.logger.Printf("task %s: fully mature, req hash unchanged → planning", t.ID)
				phase = "planning"
				skillPrompt = "/obsidian-task-runner-round1 " + t.FilePath
			} else {
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
			}
			notify.SendTaskAction(t.ID, t.Title, "🚀", "开始实现", "OMP 正在执行", r.cfg.Notifications.Desktop)
		default:
			r.logger.Printf("task %s: unknown dispatch status=%s", t.ID, t.Status)
			continue
		}

		args := []string{"--model", model, "--auto-approve", "-p", skillPrompt}

		if needsContextInjection(t.Status) {
			if projDir := resolveVaultProjectDir(r.cfg.ObsidianVault, t.Project); projDir != "" {
				reqPath := filepath.Join(r.cfg.ObsidianVault, t.ReqDoc)
				if ctx := BuildProjectContext(projDir, reqPath); ctx != "" {
					skillPrompt = ctx + "\n\n" + skillPrompt
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
		cancel()
		close(tailDone) // signal tail goroutine to stop

		if runErr != nil {
			reason := "异常退出"
			failureCode := ErrModelFailed
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				reason = fmt.Sprintf("超时（%v 无响应）", timeout)
				failureCode = ErrPhaseTimeout
			}
			r.logger.Printf("task %s: OMP failed (%s): %v", t.ID, reason, runErr)

			if tokenErr := checkTokenQuota(logPath, model); tokenErr != "" {
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
			if fallbackModel := r.cfg.FallbackModel(t.Assignee); fallbackModel != "" && fallbackModel != model {
				r.logger.Printf("task %s: retrying with fallback model %s", t.ID, fallbackModel)
				notify.SendTaskAction(t.ID, t.Title, "🔄", "模型切换",
					fmt.Sprintf("%s 不可用（%s），自动切换到 %s 继续执行", model, reason, fallbackModel), r.cfg.Notifications.Desktop)

				fallbackArgs := []string{"--model", fallbackModel}
				fallbackArgs = append(fallbackArgs, args[2:]...)
				fbCtx, fbCancel := context.WithTimeout(context.Background(), timeout)
				retryCmd := exec.CommandContext(fbCtx, r.cfg.OMPCmd, fallbackArgs...)
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
					fbCancel()
					close(fbTailDone)
					fellback = true
				} else {
					if err := os.WriteFile(pidFile, []byte(formatPIDRecord(retryCmd.Process.Pid)), 0o600); err != nil {
						r.logger.Printf("task %s: write fallback PID file: %v", t.ID, err)
					}
					retryErr := retryCmd.Wait()
					fbCancel()
					close(fbTailDone)
					if retryErr != nil {
						fbReason := "异常退出"
						if errors.Is(fbCtx.Err(), context.DeadlineExceeded) {
							fbReason = "超时"
							failureCode = ErrPhaseTimeout
						}
						r.logger.Printf("task %s: fallback OMP also failed (%s): %v", t.ID, fbReason, retryErr)
						notify.SendTaskAction(t.ID, t.Title, "❌", "全部失败",
							fmt.Sprintf("%s 和 %s 均不可用（%s），请检查网络和 API 状态", model, fallbackModel, fbReason), r.cfg.Notifications.Desktop)
						fellback = true
					} else {
						r.logger.Printf("task %s: completed via fallback model %s", t.ID, fallbackModel)
						if err := r.validatePhaseCompletion(taskPath, t.ID, phase); err != nil {
							r.logger.Printf("task %s: phase validation failed: %v", t.ID, err)
						}
						r.validateChangedDocs(repoDir, t.ID, phase)
						if _, statErr := os.Stat(taskPath); statErr == nil {
							notify.StatusNotify(taskPath, r.cfg.Notifications.Desktop)
						}
						r.clearPhaseRetry(taskPath, phase)
					}
				}
			}
			noFallback := r.cfg.FallbackModel(t.Assignee) == "" || r.cfg.FallbackModel(t.Assignee) == model
			if fellback || noFallback {
				r.handlePhaseFailure(taskPath, t.ID, t.Title, phase, failureCode, reason, logPath)
			}
		} else {
			r.logger.Printf("task %s: completed", t.ID)
			if err := r.validatePhaseCompletion(taskPath, t.ID, phase); err != nil {
				r.logger.Printf("task %s: phase validation failed: %v", t.ID, err)
			}
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

// handlePhaseFailure tracks retry counts for refining/planning phases and
// transitions the task to blocked after the second consecutive failure.
func (r *Runner) handlePhaseFailure(taskPath, taskID, taskTitle, phase string, code ErrorCode, reason, logPath string) {
	policy := recoveryForPhase(phase, code)
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
		if err := yamlfrontmatter.Update(taskPath, map[string]interface{}{
			"status":           "blocked",
			"blocked_phase":    "implementing",
			"phase_error_code": string(code),
			"phase_error":      reason,
			"phase_log":        logPath,
			"resume_approved":  false,
		}); err != nil {
			r.logger.Printf("task %s: record Round 2 failure: %v", taskID, err)
		}
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
	if result.Status == "new" {
		if err := os.MkdirAll(result.Path, 0755); err != nil {
			return "", fmt.Errorf("create new project %s: %w", result.Path, err)
		}
	}
	return result.Path, nil
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
	go func() {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
		<-ch
		cancel()
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

// hasNonEmptyList returns true if v is a non-empty slice.
// Mirrors task.isEmptyList but works on the any-typed frontmatter fields.
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
		// Vault dirs use "NNN-name" format; match by suffix after the numeric prefix.
		name := e.Name()
		if idx := strings.IndexByte(name, '-'); idx > 0 {
			if name[idx+1:] == projectName {
				return filepath.Join(projectsDir, name)
			}
		}
	}
	return ""
}
func hasNonEmptyList(v any) bool {

	switch val := v.(type) {
	case []interface{}:
		return len(val) > 0
	case []string:
		return len(val) > 0
	}
	return false
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
