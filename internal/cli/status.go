package cli

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/ndzuki/obsidian-task-runner/internal/config"
	"github.com/ndzuki/obsidian-task-runner/internal/task"
	"github.com/spf13/cobra"
)

var statusJSON bool

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show daemon status",
	Long: `Reports the daemon's current state: whether the lock is held
(indicating a running instance), last-scan timestamp, running and
ready task counts.

Uses the default vault-map.json path unless --map-file is set.`,
	RunE: runStatus,
}

type statusOutput struct {
	Vault        string `json:"vault"`
	LockStatus   string `json:"lock_status"`
	LastScan     string `json:"last_scan,omitempty"`
	RunningTasks int    `json:"running_tasks"`
	ReadyCount   int    `json:"ready_count"`
}

func runStatus(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load(daemonMapFile)
	if err != nil {
		return err
	}

	vaultHash := fmt.Sprintf("%x", sha256.Sum256([]byte(filepath.Clean(cfg.ObsidianVault))))[:16]
	lockFile := filepath.Join(os.TempDir(), "otg-daemon-"+vaultHash+".lock")

	lockStatus := "not_running"
	var lastScan string

	f, ferr := os.OpenFile(lockFile, os.O_RDWR, 0644)
	if ferr == nil {
		// Try non-blocking exclusive lock — if it fails the daemon holds it.
		if lockErr := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); lockErr != nil {
			lockStatus = "running"
		} else {
			syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
			lockStatus = "stopped"
		}
		f.Close()

		if fi, statErr := os.Stat(lockFile); statErr == nil {
			lastScan = fi.ModTime().Format("2006-01-02T15:04:05")
		}
	}

	readyCount := 0
	if cfg.ObsidianVault != "" {
		tasks, _ := task.FindReadyTasks(cfg.ObsidianVault)
		readyCount = len(tasks)
	}

	runningTasks := 0
	logDir := cfg.LogDir
	if logDir == "" {
		home, _ := os.UserHomeDir()
		logDir = filepath.Join(home, ".omp", "logs")
	}
	taskLogDir := filepath.Join(logDir, "tasks")
	matches, err := filepath.Glob(filepath.Join(taskLogDir, "TASK-*.pid"))
	if err == nil {
		for _, pidFile := range matches {
			data, err := os.ReadFile(pidFile)
			if err != nil {
				continue
			}
			var pid int
			if _, err := fmt.Sscanf(string(data), "%d", &pid); err != nil {
				continue
			}
			process, err := os.FindProcess(pid)
			if err != nil {
				continue
			}
			if process.Signal(syscall.Signal(0)) == nil {
				runningTasks++
			}
		}
	}

	out := statusOutput{
		Vault:        cfg.ObsidianVault,
		LockStatus:   lockStatus,
		LastScan:     lastScan,
		RunningTasks: runningTasks,
		ReadyCount:   readyCount,
	}

	if statusJSON {
		data, _ := json.MarshalIndent(out, "", "  ")
		fmt.Fprintln(cmd.OutOrStdout(), string(data))
		return nil
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Vault:        %s\n", out.Vault)
	fmt.Fprintf(cmd.OutOrStdout(), "Lock status:  %s\n", out.LockStatus)
	fmt.Fprintf(cmd.OutOrStdout(), "Last scan:    %s\n", out.LastScan)
	fmt.Fprintf(cmd.OutOrStdout(), "Running:      %d tasks\n", out.RunningTasks)
	fmt.Fprintf(cmd.OutOrStdout(), "Ready:        %d tasks\n", out.ReadyCount)
	return nil
}

func init() {
	statusCmd.Flags().BoolVar(&statusJSON, "json", false, "Output in JSON format")
	statusCmd.Flags().StringVar(&daemonMapFile, "map-file", "", "Path to vault-map.json")
	rootCmd.AddCommand(statusCmd)
}
