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
	cfg       *config.Config
	logger    *log.Logger
	logWriter *logutil.RotatingWriter
	taskRuns  sync.Map
	repoLocks sync.Map
	scanMu    sync.Mutex // prevents overlapping scanAndProcess calls
}

type preparedTask struct {
	task      task.ReadyTask
	repoDir   string
	workDir   string
	exclusive bool
}

type taskResult struct {
	repoDir   string
	exclusive bool
	processed int
}

func New(cfg *config.Config) *Runner {
	return &Runner{cfg: cfg}
}

func (r *Runner) Run(ctx context.Context) error {
	if err := r.initLogging(); err != nil {
		return fmt.Errorf("init logging: %w", err)
	}
	defer r.logWriter.Close()

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

	// Run an initial scan to catch any tasks that became ready while daemon was down
	go func() {
		time.Sleep(2 * time.Second) // let watcher initialize
		r.logger.Printf("running startup scan")
		if err := r.scanAndProcess(); err != nil {
			r.logger.Printf("startup scan error: %v", err)
		}
	}()

	ticker := time.NewTicker(time.Duration(r.cfg.PollIntervalMin) * time.Minute)
	defer ticker.Stop()

	r.scanAndProcess()

	for {
		select {
		case <-ctx.Done():
			r.logger.Println("daemon shutting down")
			return nil
		case evt := <-w.Events():
			r.logger.Printf("watcher: %s %s changed", evt.Dir, filepath.Base(evt.Path))
			if evt.Dir == "Requirements" {
				reqRel, _ := filepath.Rel(r.cfg.ObsidianVault, evt.Path)
				results := task.OnReqChanged(r.cfg.ObsidianVault, reqRel)
				for _, res := range results {
					switch res.Action {
					case "reset_to_ready":
						notify.SendTaskAction(res.TaskID, "", "🔄", "需求变更", "重新出计划", r.cfg.Notifications.Desktop)
					case "pending_req":
						notify.SendTaskAction(res.TaskID, "", "📌", "需求变更", "当前阶段完成后自动重新出计划", r.cfg.Notifications.Desktop)
					case "create_task":
						notify.SendTaskAction(res.TaskID, "", "🆕", "新任务已创建", "请填写 assignee 和 project 字段", r.cfg.Notifications.Desktop)
					case "warn_only":
						notify.SendTaskAction(res.TaskID, "", "⚠️", "需求变更", "请手动评估影响", r.cfg.Notifications.Desktop)
					default:
						r.logger.Printf("task %s: unknown OnReqChanged action %q", res.TaskID, res.Action)
					}
				}
			}
			time.Sleep(3 * time.Second) // wait for cloud-sync flush
			r.scanAndProcess()
		case <-ticker.C:
			r.logger.Println("timer: periodic scan")
			r.scanAndProcess()
		}
	}
}

// RunOnce performs a single scan-and-process cycle, used by the systemd timer.
// It respects the same flock as Run() to avoid concurrent OMP spawns.
func (r *Runner) RunOnce() error {
	if err := r.initLogging(); err != nil {
		return fmt.Errorf("init logging: %w", err)
	}
	defer r.logWriter.Close()
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
	defer r.scanMu.Unlock()
	tasks, err := task.FindReadyTasks(r.cfg.ObsidianVault)
	if err != nil {
		r.logger.Printf("scan error: %v", err)
	}
	r.logger.Printf("scan: %d ready tasks", len(tasks))
	if len(tasks) == 0 {
		return nil
	}
	for round := 0; round < 3; round++ {
		if r.processBatch(tasks) == 0 {
			break
		}
		// Wait for cloud-sync filesystems to flush OMP's writes before re-scanning
		time.Sleep(3 * time.Second)
		tasks, _ = task.FindReadyTasks(r.cfg.ObsidianVault)
		if len(tasks) == 0 {
			break
		}
	}
	return nil
}

// processBatch schedules OMP executions up to the configured limit. Only an
// executing OMP process consumes a global slot; tasks waiting for a repository
// exclusive phase remain pending so Round 2 worktree tasks can use that slot.
func (r *Runner) processBatch(tasks []task.ReadyTask) int {
	limit := r.cfg.MaxConcurrentTasks
	if limit < 1 {
		limit = 1
	}

	pending := r.prepareBatch(tasks)
	done := make(chan taskResult, limit)
	processed := 0
	running := 0

	for len(pending) > 0 || running > 0 {
		for running < limit {
			index := -1
			for i, candidate := range pending {
				if candidate.exclusive && !r.repoLock(candidate.repoDir).TryLock() {
					continue
				}
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
					exclusive: p.exclusive,
					processed: r.processPreparedTask(p),
				}
			}(candidate)
		}

		if running == 0 {
			r.logger.Printf("scheduler: %d tasks cannot be scheduled", len(pending))
			break
		}

		result := <-done
		running--
		processed += result.processed
		if result.exclusive {
			r.repoLock(result.repoDir).Unlock()
		}
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
		// ── Grilling: handle before repo resolution ──
		if t.Status == "ready" {
			r.logger.Printf("task %s: ready → refining", t.ID)
			if err := yamlfrontmatter.Update(t.FilePath, map[string]interface{}{
				"status": "refining",
			}); err != nil {
				r.logger.Printf("task %s: failed to set refining: %v", t.ID, err)
				continue
			}
			t.Status = "refining"
			pending = append(pending, preparedTask{task: t, exclusive: false})
			continue
		}
		if t.Status == "refining" || t.Status == "planning" || t.Status == "blocked" {
			pending = append(pending, preparedTask{task: t, exclusive: false})
			continue
		}
		if t.Status == "needs-grilling" {
			if t.PendingReq {
				if err := r.updateTask(t, map[string]interface{}{"status": "refining", "grill_done": false, "grill_resolution": "", "grill_context": "", "grill_prev_status": ""}); err != nil {
					continue
				}
				notify.SendTaskAction(t.ID, t.Title, "🔄", "需求变更已并入", "重新进入 maturity gate", r.cfg.Notifications.Desktop)
				continue
			}
			if t.GrillPrevStatus == "implementing" && t.PlanVersion == 0 {
				if err := r.updateTask(t, map[string]interface{}{"status": "plan-review", "grill_done": true, "grill_context": "", "grill_prev_status": ""}); err != nil {
					continue
				}
				notify.SendTaskAction(t.ID, t.Title, "🔧", "实现受阻（无计划）", "已返回 plan-review", r.cfg.Notifications.Desktop)
				continue
			}
			if t.GrillDone || t.PlanApproved {
				switch t.GrillResolution {
				case "resume":
					prev := t.GrillPrevStatus
					if prev == "" {
						prev = "implementing"
					}
					if err := r.updateTask(t, map[string]interface{}{"status": prev, "grill_done": false, "grill_resolution": "", "grill_context": "", "grill_prev_status": ""}); err != nil {
						continue
					}
					notify.SendTaskAction(t.ID, t.Title, "✅", "阻塞已解决", "恢复实现", r.cfg.Notifications.Desktop)
				case "replan":
					if err := r.updateTask(t, map[string]interface{}{"status": "refining", "grill_done": false, "pending_req": true, "grill_resolution": "", "grill_context": "", "grill_prev_status": ""}); err != nil {
						continue
					}
					notify.SendTaskAction(t.ID, t.Title, "✅", "需求/计划已更新", "进入 maturity gate", r.cfg.Notifications.Desktop)
				default:
					r.logger.Printf("task %s: grill_done but no resolution — waiting", t.ID)
					notify.SendGrillingReminder(t.ID, t.Title, t.ReqDoc, r.cfg.ObsidianVault, r.cfg.Notifications.Desktop)
				}
				continue
			}
			r.logger.Printf("task %s: still waiting for grilling", t.ID)
			notify.SendGrillingReminder(t.ID, t.Title, t.ReqDoc, r.cfg.ObsidianVault, r.cfg.Notifications.Desktop)
			continue
		}

		// ── review/conflict/done + pending_req → force refining ──
		if (t.Status == "review" || t.Status == "conflict" || t.Status == "done") && t.PendingReq {
			r.logger.Printf("task %s: %s + pending_req → refining", t.ID, t.Status)
			if err := r.updateTask(t, map[string]interface{}{"status": "refining", "merge_approved": false}); err != nil {
				continue
			}
			notify.SendTaskAction(t.ID, t.Title, "🔄", "需求变更", "已取消 Merge 授权并返回 maturity gate", r.cfg.Notifications.Desktop)
			continue
		}
		// ── premature plan_approved reset ──
		if t.PlanApproved && t.Status != "plan-review" && t.Status != "implementing" {
			r.logger.Printf("task %s: plan_approved=true but status=%s → resetting", t.ID, t.Status)
			if err := r.updateTask(t, map[string]interface{}{"plan_approved": false}); err != nil {
				continue
			}
			}

		repoDir, err := r.resolveRepo(t)
		if err != nil {
			r.logger.Printf("task %s: %v", t.ID, err)
			continue
		}

		prepared := preparedTask{
			task:      t,
			repoDir:   repoDir,
			workDir:   repoDir,
			exclusive: !isRound2(t),
		}
		if !prepared.exclusive {
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
	}
	return pending
}

func (r *Runner) processPreparedTask(prepared preparedTask) int {
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

// validateChangedDocs scans git-tracked .md files modified in the working tree
// since the last commit and validates them with ValidateDocument. Corrupted
// documents (memory.md, CONTEXT.md, ADR files, etc.) are logged but do not
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
func (r *Runner) validatePhaseCompletion(taskPath, taskID, phase string) error {
	if err := yamlfrontmatter.Validate(taskPath); err != nil {
		return fmt.Errorf("task %s: frontmatter corrupt after %s: %w", taskID, phase, err)
	}
	return nil
}

func taskRunKey(taskPath string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(taskPath)))
	return fmt.Sprintf("%x", sum[:8])
}

func taskPIDFile(taskLogDir, taskID, taskPath string) string {
	return filepath.Join(taskLogDir, fmt.Sprintf("TASK-%s-%s.pid", taskID, taskRunKey(taskPath)))
}

func (r *Runner) repoLock(repoDir string) *sync.Mutex {
	lock, _ := r.repoLocks.LoadOrStore(repoDir, &sync.Mutex{})
	return lock.(*sync.Mutex)
}

func isRound2(t task.ReadyTask) bool {
	return t.PlanApproved && (t.Status == "plan-review" || t.Status == "implementing") && !t.NewProject
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
						if err := r.updateTaskFile(taskPath, t.ID, t.Title, map[string]interface{}{
							"status":          fm.BlockedPhase,
							"blocked_phase":   "",
							"phase_error":     "",
							"phase_log":       "",
							"resume_approved": false,
						}); err != nil {
							continue
						}
						t.Status = fm.BlockedPhase
						// Fall through to normal dispatch below.
					} else {
						// Normal auto-unblock: blocked→ready
						if err := yamlfrontmatter.Update(taskPath, map[string]interface{}{"status": "ready", "pending_req": false, "blocked_by": []string{}}); err != nil {
							r.logger.Printf("task %s: failed to unblock: %v", t.ID, err)
							continue
						}
						t.Status = "ready"
						t.PendingReq = false
						notify.SendTaskAction(t.ID, t.Title, "🔓", "解除阻塞", "必填字段已补齐，依赖已满足，任务自动解除阻塞开始执行", r.cfg.Notifications.Desktop)
						processed++
						continue // let next scan round pick up the ready task for refining
					}
				}
			}
		}

		// ── Direct phase dispatch ──
		model := r.selectModel(t.Assignee)
		isMerge := t.MergeApproved && (t.Status == "review" || t.Status == "conflict")
		var phase, skillPrompt string

		switch {
		case t.Status == "refining":
			phase = "refining"
			model = r.cfg.Model("default")
			skillPrompt = "/obsidian-task-runner-refining " + t.FilePath
			r.logger.Printf("task %s: maturity gate (model=%s)", t.ID, model)
		case t.Status == "planning":
			phase = "planning"
			skillPrompt = "/obsidian-task-runner-round1 " + t.FilePath
			r.logger.Printf("task %s: plan generation (model=%s)", t.ID, model)
		case isMerge:
			phase = "merge"
			skillPrompt = "/obsidian-task-runner-merge " + t.FilePath
			r.logger.Printf("task %s: Merge Phase authorized", t.ID)
			notify.SendTaskAction(t.ID, t.Title, "🔀", "开始合并", "正在将功能分支合并到主分支", r.cfg.Notifications.Desktop)
		case t.Status == "plan-review" || t.Status == "implementing":
			phase = "round2"
			skillPrompt = "/obsidian-task-runner-round2 " + t.FilePath
			if t.Status == "plan-review" {
				if err := r.updateTaskFile(taskPath, t.ID, t.Title, map[string]interface{}{"status": "implementing"}); err != nil {
					continue
				}
				t.Status = "implementing"
			}
			notify.SendTaskAction(t.ID, t.Title, "🚀", "开始实现", "OMP 正在执行", r.cfg.Notifications.Desktop)
		default:
			r.logger.Printf("task %s: unknown dispatch status=%s", t.ID, t.Status)
			continue
		}

		args := []string{"--model", model}
		if isMerge {
			args = append(args, "--approval-mode", "yolo")
		} else {
			args = append(args, "--auto-approve")
		}
		args = append(args, "-p", skillPrompt)
		logDir := r.cfg.LogDir
		if logDir == "" {
			home, _ := os.UserHomeDir()
			logDir = filepath.Join(home, ".omp", "logs")
		}
		taskLogDir := filepath.Join(logDir, "tasks")
		os.MkdirAll(taskLogDir, 0755)
		ts := time.Now().Format("20060102-150405")
		taskKey := taskRunKey(t.FilePath)
		logPath := filepath.Join(taskLogDir, fmt.Sprintf("TASK-%s-%s-%s-%s.log", t.ID, taskKey, ts, phase))

		f, err := os.Create(logPath)
		if f != nil {
			header := fmt.Sprintf("# TASK-%s %s\n# model=%s phase=%s time=%s\n\n", t.ID, t.Title, model, phase, time.Now().Format(time.RFC3339))
			f.WriteString(header)
		}

		// Determine timeout based on phase
		var timeout time.Duration
		switch phase {
		case "refining":
			timeout = 15 * time.Minute
		case "planning":
			timeout = 30 * time.Minute
		case "round2":
			timeout = 60 * time.Minute
		case "merge":
			timeout = 15 * time.Minute
		default:
			timeout = 30 * time.Minute
		}
		pidFile := taskPIDFile(taskLogDir, t.ID, t.FilePath)
		if phase == "refining" || phase == "planning" || phase == "round2" {
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

		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		cmd := exec.CommandContext(ctx, r.cfg.OMPCmd, args...)
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

		// Start OMP and write PID file for crash recovery
		if startErr := cmd.Start(); startErr != nil {
			r.logger.Printf("task %s: OMP start failed: %v", t.ID, startErr)
			cancel()
			close(tailDone)
			continue
		}
		_ = os.WriteFile(pidFile, []byte(fmt.Sprintf("%d", cmd.Process.Pid)), 0644)
		defer os.Remove(pidFile)

		r.logger.Printf("task %s: executing OMP (model=%s, phase=%s, timeout=%v, log=%s)", t.ID, model, phase, timeout, logPath)
		err = cmd.Wait()
		cancel()
		close(tailDone) // signal tail goroutine to stop

		if err != nil {
			reason := "异常退出"
			if errors.Is(err, context.DeadlineExceeded) {
				reason = fmt.Sprintf("超时（%v 无响应）", timeout)
			}
			r.logger.Printf("task %s: OMP failed (%s): %v", t.ID, reason, err)

			// Check if the failure is due to token quota exhaustion
			if tokenErr := checkTokenQuota(logPath, model); tokenErr != "" {
				notify.SendTaskAction(t.ID, t.Title, "💰", "Token 不足",
					fmt.Sprintf("%s 模型的 token 配额已耗尽，%s", model, tokenErr), r.cfg.Notifications.Desktop)
			} else if errors.Is(err, context.DeadlineExceeded) {
				notify.SendTaskAction(t.ID, t.Title, "⏰", "执行超时",
					fmt.Sprintf("%s 模型 %v 无响应，任务自动超时", model, timeout), r.cfg.Notifications.Desktop)
			} else {
				notify.SendTaskAction(t.ID, t.Title, "💥", "进程异常", fmt.Sprintf("%s: %v", reason, err), r.cfg.Notifications.Desktop)
			}

			// Try fallback model if primary model failed (e.g., GPT → DeepSeek)
			fellback := false
			if fallbackModel := r.cfg.FallbackModel(t.Assignee); fallbackModel != "" && fallbackModel != model {
				r.logger.Printf("task %s: retrying with fallback model %s", t.ID, fallbackModel)
				notify.SendTaskAction(t.ID, t.Title, "🔄", "模型切换",
					fmt.Sprintf("%s 不可用（%s），自动切换到 %s 继续执行", model, reason, fallbackModel), r.cfg.Notifications.Desktop)

				fallbackArgs := []string{"--model", fallbackModel}
				fallbackArgs = append(fallbackArgs, args[2:]...) // skip --model and old model value

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
					_ = os.WriteFile(pidFile, []byte(fmt.Sprintf("%d", retryCmd.Process.Pid)), 0644)
					retryErr := retryCmd.Wait()
					fbCancel()
					close(fbTailDone)
					if retryErr != nil {
						fbReason := "异常退出"
						if errors.Is(retryErr, context.DeadlineExceeded) {
							fbReason = "超时"
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
			// Phase retry/blocked: only if fallback also failed, or no fallback was attempted
			noFallback := r.cfg.FallbackModel(t.Assignee) == "" || r.cfg.FallbackModel(t.Assignee) == model
			if fellback || noFallback {
				r.handlePhaseFailure(taskPath, t.ID, t.Title, phase, reason, logPath)
			}
		} else {
			r.logger.Printf("task %s: completed", t.ID)
			if err := r.validatePhaseCompletion(taskPath, t.ID, phase); err != nil {
				r.logger.Printf("task %s: phase validation failed: %v", t.ID, err)
			}
			r.validateChangedDocs(repoDir, t.ID, phase)
			if _, statErr := os.Stat(taskPath); statErr == nil {
				notify.StatusNotify(taskPath, r.cfg.Notifications.Desktop)
			}
			r.clearPhaseRetry(taskPath, phase)
		}
		if f != nil {
			f.Close()
		}
		processed++
	}
	return processed
}

// handlePhaseFailure tracks retry counts for refining/planning phases and
// transitions the task to blocked after the second consecutive failure.
func (r *Runner) handlePhaseFailure(taskPath, taskID, taskTitle, phase, reason, logPath string) {
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
		if parseErr != nil || fm == nil {
			r.logger.Printf("task %s: cannot parse retry count (%v), assuming first failure", taskID, parseErr)
		} else {
			currentRetry = fm.RefineRetryCount
			if phase == "planning" {
				currentRetry = fm.PlanningRetryCount
			}
		}
	}

	if currentRetry == 0 {
		if err := r.updateTaskFile(taskPath, taskID, taskTitle, map[string]interface{}{retryField: 1}); err != nil {
			return
		}
		r.logger.Printf("task %s: %s auto-retry (1/2)", taskID, phase)
	} else {
		if err := r.updateTaskFile(taskPath, taskID, taskTitle, map[string]interface{}{
			"status":        "blocked",
			"blocked_phase": phase,
			"phase_error":   reason,
			"phase_log":     logPath,
			retryField:      0,
		}); err != nil {
			return
		}
		notify.SendTaskAction(taskID, taskTitle, "🚫", "阶段失败",
			fmt.Sprintf("阶段 %s 连续失败两次，任务已阻塞。修复后设置 resume_approved: true 恢复。", phase),
			r.cfg.Notifications.Desktop)
		r.logger.Printf("task %s: %s failed twice → blocked", taskID, phase)
	}
}
func (r *Runner) clearPhaseRetry(taskPath, phase string) {
	switch phase {
	case "refining":
		if err := r.updateTaskFile(taskPath, "", "", map[string]interface{}{"refine_retry_count": 0}); err != nil {
			r.logger.Printf("task %s: clear retry count failed: %v", taskPath, err)
		}
	case "planning":
		if err := r.updateTaskFile(taskPath, "", "", map[string]interface{}{"planning_retry_count": 0}); err != nil {
			r.logger.Printf("task %s: clear retry count failed: %v", taskPath, err)
		}
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
		os.MkdirAll(result.Path, 0755)
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
			os.Remove(path)
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
	defer f.Close()

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
	f.Seek(0, io.SeekEnd)
	defer f.Close()

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	scanner := bufio.NewScanner(f)
	for {
		select {
		case <-done:
			for scanner.Scan() {
				if !isNoise(scanner.Text()) {
					taskLog.Write(append(scanner.Bytes(), '\n'))
				}
			}
			return
		case <-ticker.C:
			for scanner.Scan() {
				if !isNoise(scanner.Text()) {
					taskLog.Write(append(scanner.Bytes(), '\n'))
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
		f.Close()
		return nil, fmt.Errorf("another daemon instance is running for this vault")
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
		os.Remove(lockFile)
	}, nil
}

// procAlive checks if a process with the given PID is still running.
func procAlive(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// Signal 0 is a null signal — checks existence without affecting the process
	err = process.Signal(syscall.Signal(0))
	return err == nil
}
