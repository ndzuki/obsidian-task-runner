// Package daemon provides the core task-runner daemon with fsnotify integration.
package daemon

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/ndzuki/obsidian-task-runner/internal/config"
	"github.com/ndzuki/obsidian-task-runner/internal/notify"
	"github.com/ndzuki/obsidian-task-runner/internal/project"
	"github.com/ndzuki/obsidian-task-runner/internal/task"
	"github.com/ndzuki/obsidian-task-runner/internal/watch"
	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

// Runner orchestrates the task-runner daemon.
type Runner struct {
	cfg    *config.Config
	logger *log.Logger
}

// New creates a new Runner.
func New(cfg *config.Config) *Runner {
	return &Runner{
		cfg:    cfg,
		logger: log.New(os.Stderr, "", log.LstdFlags),
	}
}

// Run starts the daemon in long-running mode with fsnotify.
func (r *Runner) Run(ctx context.Context) error {
	if r.cfg.ObsidianVault == "" {
		return fmt.Errorf("obsidian_vault not configured")
	}

	unlock, err := acquireLock(r.cfg)
	if err != nil {
		return err
	}
	defer unlock()

	r.logger.Printf("daemon started, vault=%s", r.cfg.ObsidianVault)

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
				task.OnReqChanged(r.cfg.ObsidianVault, reqRel)
			}
			r.scanAndProcess()
		case <-ticker.C:
			r.logger.Println("timer: periodic scan")
			r.scanAndProcess()
		}
	}
}

// RunOnce runs a single scan cycle and exits.
func (r *Runner) RunOnce() error {
	if r.cfg.ObsidianVault == "" {
		return fmt.Errorf("obsidian_vault not configured")
	}
	return r.scanAndProcess()
}

func (r *Runner) scanAndProcess() error {
	tasks, err := task.FindReadyTasks(r.cfg.ObsidianVault)
	if err != nil {
		r.logger.Printf("scan error: %v", err)
		return err
	}
	if len(tasks) == 0 {
		r.logger.Println("scan: no ready tasks")
		return nil
	}

	r.logger.Printf("scan: %d ready tasks", len(tasks))

	for round := 0; round < 3; round++ {
		processed := r.processBatch(tasks)
		if processed == 0 {
			break
		}
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

		taskPath := filepath.Join(r.cfg.ObsidianVault, "Tasks", t.FileName)

		if t.Status == "blocked" {
			yamlfrontmatter.Update(taskPath, map[string]interface{}{"status": "ready"})
			t.Status = "ready"
		}

		if t.PendingReq && t.Status != "ready" && t.Status != "plan-review" {
			r.logger.Printf("task %s: pending_req → resetting to ready", t.ID)
			yamlfrontmatter.Update(taskPath, map[string]interface{}{
				"status": "ready", "pending_req": false,
				"plan_approved": false, "merge_approved": false,
			})
			notify.SendTaskAction(t.ID, "🔄", "需求变更已并入", "自动根据新需求重新出计划")
			t.Status = "ready"
		}

		model := r.selectModel(t.Assignee)
		isMerge := t.MergeApproved && (t.Status == "review" || t.Status == "conflict")

		args := []string{"-m", model}
		if isMerge {
			args = append(args, "--approval-mode", "yolo")
			r.logger.Printf("task %s: Merge Phase authorized", t.ID)
			notify.SendTaskAction(t.ID, "🔀", "开始合并", "正在将功能分支合并到主分支")
		} else {
			args = append(args, "--auto-approve")
			if t.Status == "plan-review" && t.PlanApproved {
				notify.SendTaskAction(t.ID, "🚀", "开始实现", "OMP 正在执行")
			}
		}
		args = append(args, "-p", "/obsidian-task-runner "+t.ID)

		cmd := exec.Command(r.cfg.OMPCmd, args...)
		cmd.Dir = repoDir
		cmd.Stdout = os.Stderr
		cmd.Stderr = os.Stderr

		r.logger.Printf("task %s: executing OMP (model=%s, merge=%v)", t.ID, model, isMerge)
		if err := cmd.Run(); err != nil {
			r.logger.Printf("task %s: OMP failed: %v", t.ID, err)
		} else {
			r.logger.Printf("task %s: completed", t.ID)
			if _, err := os.Stat(taskPath); err == nil {
				notify.StatusNotify(taskPath)
			}
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
	switch assignee {
	case "deepseek":
		return r.cfg.OMPModelDeepSeek
	case "gpt":
		return r.cfg.OMPModelGPT
	default:
		return r.cfg.OMPModelFlash
	}
}

// SignalContext returns a context cancelled on SIGINT/SIGTERM.
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
