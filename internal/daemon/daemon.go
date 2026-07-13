package daemon

import (
	"bufio"
	"context"
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
						notify.SendTaskAction(res.TaskID, "", "🔄", "需求变更", "重新出计划")
					case "pending_req":
						notify.SendTaskAction(res.TaskID, "", "📌", "需求变更", "当前阶段完成后自动重新出计划")
					case "create_task":
						notify.SendTaskAction(res.TaskID, "", "🆕", "新任务已创建", "请填写 assignee 和 project 字段")
					case "warn_only":
						notify.SendTaskAction(res.TaskID, "", "⚠️", "需求变更", "请手动评估影响")
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
	tasks, err := task.FindReadyTasks(r.cfg.ObsidianVault)
	if err != nil {
		r.logger.Printf("scan error: %v", err)
		return err
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

func (r *Runner) processBatch(tasks []task.ReadyTask) int {
	processed := 0
	for _, t := range tasks {
		repoDir, err := r.resolveRepo(t)
		if err != nil {
			r.logger.Printf("task %s: %v", t.ID, err)
			continue
		}
		// Skip if an OMP process is already running for this task.
		if isOMPRunning(t.ID) {
			r.logger.Printf("task %s: skipping (OMP already running)", t.ID)
			continue
		}
		taskPath := t.FilePath

		if t.Status == "blocked" {
			yamlfrontmatter.Update(taskPath, map[string]interface{}{"status": "ready", "pending_req": false})
			t.Status = "ready"
			t.PendingReq = false
			notify.SendTaskAction(t.ID, t.Title, "🔓", "解除阻塞", "必填字段已补齐，任务自动解除阻塞开始执行")
		}
		if t.PendingReq && t.Status != "ready" && t.Status != "plan-review" {
			r.logger.Printf("task %s: pending_req → resetting to ready", t.ID)
			yamlfrontmatter.Update(taskPath, map[string]interface{}{
				"status": "ready", "pending_req": false,
				"plan_approved": false, "merge_approved": false,
			})
			notify.SendTaskAction(t.ID, t.Title, "🔄", "需求变更已并入", "自动根据新需求重新出计划")
			t.Status = "ready"
		}

		model := r.selectModel(t.Assignee)
		isMerge := t.MergeApproved && (t.Status == "review" || t.Status == "conflict")

		args := []string{"--model", model}
		if isMerge {
			args = append(args, "--approval-mode", "yolo")
			r.logger.Printf("task %s: Merge Phase authorized", t.ID)
			notify.SendTaskAction(t.ID, t.Title, "🔀", "开始合并", "正在将功能分支合并到主分支")
		} else {
			args = append(args, "--auto-approve")
			if (t.Status == "plan-review" || t.Status == "implementing") && t.PlanApproved {
				notify.SendTaskAction(t.ID, t.Title, "🚀", "开始实现", "OMP 正在执行")
			} else if t.Status == "ready" {
				notify.SendTaskAction(t.ID, t.Title, "📝", "开始出计划", "OMP 正在分析需求并生成实现计划")
			} else if t.Status == "implementing" && !t.PlanApproved {
				notify.SendTaskAction(t.ID, t.Title, "🔄", "恢复处理", "Round 2 异常中断，回退到 Round 1 重新出计划")
			}
		}
		// Task audit log
		logDir := r.cfg.LogDir
		if logDir == "" {
			home, _ := os.UserHomeDir()
			logDir = filepath.Join(home, ".omp", "logs")
		}
		phase := "round1"
		if isMerge {
			phase = "merge"
		} else if t.Status == "plan-review" || (t.Status == "implementing" && t.PlanApproved) {
			phase = "round2"
		}
		// Set intermediate status for Round 2 to prevent re-scan duplicate spawn.
		// Round 1 (ready) is protected by isOMPRunning guard — no status change needed.
		if t.Status == "plan-review" {
			yamlfrontmatter.Update(taskPath, map[string]interface{}{"status": "implementing"})
			t.Status = "implementing"
		}
		args = append(args, "-p", "/obsidian-task-runner "+t.ID)
		taskLogDir := filepath.Join(logDir, "tasks")
		os.MkdirAll(taskLogDir, 0755)
		ts := time.Now().Format("20060102-150405")
		logPath := filepath.Join(taskLogDir, fmt.Sprintf("TASK-%s-%s-%s.log", t.ID, ts, phase))

		f, err := os.Create(logPath)
		if f != nil {
			header := fmt.Sprintf("# TASK-%s %s\n# model=%s phase=%s time=%s\n\n", t.ID, t.Title, model, phase, time.Now().Format(time.RFC3339))
			f.WriteString(header)
		}

		// Determine timeout based on phase
		var timeout time.Duration
		switch phase {
		case "round1":
			timeout = 30 * time.Minute
		case "round2":
			timeout = 60 * time.Minute
		case "merge":
			timeout = 15 * time.Minute
		default:
			timeout = 30 * time.Minute
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

		r.logger.Printf("task %s: executing OMP (model=%s, phase=%s, timeout=%v, log=%s)", t.ID, model, phase, timeout, logPath)
		err = cmd.Run()
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
					fmt.Sprintf("%s 模型的 token 配额已耗尽，%s", model, tokenErr))
			} else if errors.Is(err, context.DeadlineExceeded) {
				notify.SendTaskAction(t.ID, t.Title, "⏰", "执行超时",
					fmt.Sprintf("%s 模型 %v 无响应，任务自动超时", model, timeout))
			} else {
				notify.SendTaskAction(t.ID, t.Title, "💥", "进程异常", fmt.Sprintf("%s: %v", reason, err))
			}

			// Try fallback model if primary model failed (e.g., GPT → DeepSeek)
			if fallbackModel := r.cfg.FallbackModel(t.Assignee); fallbackModel != "" && fallbackModel != model {
				r.logger.Printf("task %s: retrying with fallback model %s", t.ID, fallbackModel)
				notify.SendTaskAction(t.ID, t.Title, "🔄", "模型切换",
					fmt.Sprintf("%s 不可用（%s），自动切换到 %s 继续执行", model, reason, fallbackModel))

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
				retryErr := retryCmd.Run()
				fbCancel()
				close(fbTailDone)
				if retryErr != nil {
					fbReason := "异常退出"
					if errors.Is(retryErr, context.DeadlineExceeded) {
						fbReason = "超时"
					}
					r.logger.Printf("task %s: fallback OMP also failed (%s): %v", t.ID, fbReason, retryErr)
					notify.SendTaskAction(t.ID, t.Title, "❌", "全部失败",
						fmt.Sprintf("%s 和 %s 均不可用（%s），请检查网络和 API 状态", model, fallbackModel, fbReason))
				} else {
					r.logger.Printf("task %s: completed via fallback model %s", t.ID, fallbackModel)
					if _, statErr := os.Stat(taskPath); statErr == nil {
						notify.StatusNotify(taskPath)
					}
				}
			}
		} else {
			r.logger.Printf("task %s: completed", t.ID)
			if _, statErr := os.Stat(taskPath); statErr == nil {
				notify.StatusNotify(taskPath)
			}
		}
		if f != nil {
			f.Close()
		}
		processed++
	}
	return processed
}

func (r *Runner) resolveRepo(t task.ReadyTask) (string, error) {
	mapFile := filepath.Join(r.cfg.SkillInstallDir, "config", "vault-map.json")
	result := project.ResolveProject(mapFile, t.Project, t.NewProject)
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

// isOMPRunning checks if an OMP process is already executing for the given task ID.
func isOMPRunning(taskID string) bool {
	cmd := exec.Command("pgrep", "-f", "obsidian-task-runner "+taskID)
	err := cmd.Run()
	return err == nil
}

func acquireLock(cfg *config.Config) (func(), error) {
	lockFile := filepath.Join(os.TempDir(), "otg-daemon.lock")
	f, err := os.OpenFile(lockFile, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return nil, fmt.Errorf("open lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("another daemon instance is running")
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
		os.Remove(lockFile)
	}, nil
}
