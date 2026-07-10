package daemon

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
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

func (r *Runner) RunOnce() error {
	if err := r.initLogging(); err != nil {
		return fmt.Errorf("init logging: %w", err)
	}
	defer r.logWriter.Close()
	if r.cfg.ObsidianVault == "" {
		return fmt.Errorf("obsidian_vault not configured")
	}
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
	if len(tasks) == 0 {
		return nil
	}
	r.logger.Printf("scan: %d ready tasks", len(tasks))
	for round := 0; round < 3; round++ {
		if r.processBatch(tasks) == 0 {
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

		// Task audit log
		logDir := r.cfg.LogDir
		if logDir == "" {
			home, _ := os.UserHomeDir()
			logDir = filepath.Join(home, ".omp", "logs")
		}
		taskLogDir := filepath.Join(logDir, "tasks")
		os.MkdirAll(taskLogDir, 0755)

		phase := "round1"
		if isMerge {
			phase = "merge"
		} else if t.Status == "plan-review" {
			phase = "round2"
		}
		ts := time.Now().Format("20060102-150405")
		logPath := filepath.Join(taskLogDir, fmt.Sprintf("TASK-%s-%s-%s.log", t.ID, ts, phase))

		f, err := os.Create(logPath)
		if f != nil {
			header := fmt.Sprintf("# TASK-%s %s\n# model=%s phase=%s time=%s\n\n", t.ID, t.Title, model, phase, time.Now().Format(time.RFC3339))
			f.WriteString(header)
		}

		cmd := exec.Command(r.cfg.OMPCmd, args...)
		cmd.Dir = repoDir
		if f != nil {
			cmd.Stdout = io.MultiWriter(f, os.Stderr)
			cmd.Stderr = io.MultiWriter(f, os.Stderr)
		} else {
			cmd.Stdout = os.Stderr
			cmd.Stderr = os.Stderr
		}

		r.logger.Printf("task %s: executing OMP (model=%s, merge=%v, log=%s)", t.ID, model, isMerge, logPath)
		if err := cmd.Run(); err != nil {
			r.logger.Printf("task %s: OMP failed: %v", t.ID, err)
		} else {
			r.logger.Printf("task %s: completed", t.ID)
			if _, err := os.Stat(taskPath); err == nil {
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
