package cli

import (
	"github.com/ndzuki/obsidian-task-runner/internal/config"
	"github.com/ndzuki/obsidian-task-runner/internal/daemon"
	"github.com/spf13/cobra"
)

var (
	daemonOnce     bool
	daemonMapFile  string
	daemonLogDir   string
	daemonSkillDir string
)

var daemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "Start the task-runner daemon",
	Long: `Starts the obsidian-task-runner daemon process.

In default mode, runs continuously with fsnotify file watching
and periodic polling as a backup.

With --once, runs a single scan cycle and exits (for systemd timer).`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(daemonMapFile)
		if err != nil {
			return err
		}
		if daemonLogDir != "" {
			cfg.LogDir = daemonLogDir
		}
		if daemonSkillDir != "" {
			cfg.SkillInstallDir = daemonSkillDir
		}
		r := daemon.New(cfg)
		if daemonOnce {
			return r.RunOnce()
		}
		return r.Run(daemon.SignalContext())
	},
}

func init() {
	daemonCmd.Flags().BoolVar(&daemonOnce, "once", false, "Run a single scan cycle and exit")
	daemonCmd.Flags().StringVar(&daemonMapFile, "map-file", "", "Path to vault-map.json")
	daemonCmd.Flags().StringVar(&daemonLogDir, "log-dir", "", "Log directory (default: ~/.omp/logs)")
	daemonCmd.Flags().StringVar(&daemonSkillDir, "skill-dir", "", "Skill install directory (default: ~/.omp/skills/obsidian-task-runner)")
	rootCmd.AddCommand(daemonCmd)
}
