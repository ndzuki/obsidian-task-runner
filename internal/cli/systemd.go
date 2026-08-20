package cli

import (
	"os"
	"path/filepath"
	"strconv"

	"github.com/ndzuki/obsidian-task-runner/internal/install"
	"github.com/spf13/cobra"
)

var (
	installSystemdVault  string
	installSystemdPoll   int
	installSystemdDryRun bool
)

var installSystemdCmd = &cobra.Command{
	Use:   "install-systemd",
	Short: "Generate and enable user systemd units for the daemon",
	Long: `(Re)generates the user systemd units (omp-task-runner.timer,
omp-task-watcher.service, omp-task-runner.service) into
~/.config/systemd/user/, then enables and starts them.

Use this after moving the vault or changing the polling interval, or
when the units were never installed (e.g. a fresh machine where
'make install-force' only starts existing units).

Resolution order:
  vault:         --vault > OBSIDIAN_VAULT > vault-map.json > ~/Documents/Obsidian/MainVault
  poll interval: --poll-interval > POLL_INTERVAL_MINUTES > vault-map.json > 30`,
	Args: cobra.NoArgs,
	RunE: runInstallSystemd,
}

func runInstallSystemd(cmd *cobra.Command, _ []string) error {
	home, _ := os.UserHomeDir()
	skillDir := os.Getenv("SKILL_INSTALL_DIR")
	if skillDir == "" {
		skillDir = filepath.Join(home, ".dsh", "skills", "obsidian-task-runner")
	}

	vault := resolveInstallVault(skillDir, installSystemdVault)
	poll := resolveInstallPoll(installSystemdPoll, cmd.Flags().Changed("poll-interval"), skillDir)

	return install.ConfigureSystemd(install.Options{
		ObsidianVault:   vault,
		PollIntervalMin: poll,
		DryRun:          installSystemdDryRun,
	})
}

// resolveInstallPoll 确定 systemd timer 轮询间隔，优先级：
// 显式 flag > POLL_INTERVAL_MINUTES 环境变量 > vault-map.json 的
// poll_interval_minutes > 默认 30 分钟。
func resolveInstallPoll(flagVal int, flagChanged bool, skillDir string) int {
	poll := flagVal
	if !flagChanged {
		if v := os.Getenv("POLL_INTERVAL_MINUTES"); v != "" {
			poll, _ = strconv.Atoi(v)
		}
	}
	if poll == 0 {
		if _, p := readVaultMap(skillDir); p > 0 {
			poll = p
		}
	}
	if poll == 0 {
		poll = 30
	}
	return poll
}

func init() {
	installSystemdCmd.Flags().StringVar(&installSystemdVault, "vault", "", "Obsidian vault path")
	installSystemdCmd.Flags().IntVar(&installSystemdPoll, "poll-interval", 0, "Polling interval in minutes (default: POLL_INTERVAL_MINUTES > vault-map.json value, then 30)")
	installSystemdCmd.Flags().BoolVarP(&installSystemdDryRun, "dry-run", "n", false, "Preview changes without applying")
	rootCmd.AddCommand(installSystemdCmd)
}
