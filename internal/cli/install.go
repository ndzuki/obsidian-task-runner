package cli

import (
	"os"
	"path/filepath"
	"strconv"

	"github.com/ndzuki/obsidian-task-runner/internal/install"
	"github.com/spf13/cobra"
)

var (
	installForce   bool
	installDryRun  bool
	installVault   string
	installNewRoot string
	installNotif   bool
	installPoll    int
	installSystemd bool
)

var installCmd = &cobra.Command{
	Use:   "install",
	Short: "Install skill to ~/.omp/skills/ and configure systemd",
	Long: `Installs the obsidian-task-runner skill to ~/.omp/skills/,
creates the OMP agent symlink, generates vault-map.json, configures
shell environment, and optionally registers systemd units.

Environment variables can also be used to configure installation:
  OBSIDIAN_VAULT, NEW_PROJECT_ROOT, NOTIFY_ENABLED,
  POLL_INTERVAL_MINUTES, SYSTEMD_ENABLED, SKILL_INSTALL_DIR`,
	RunE: runInstall,
}

func runInstall(cmd *cobra.Command, args []string) error {
	home, _ := os.UserHomeDir()

	vault := installVault
	if v := os.Getenv("OBSIDIAN_VAULT"); v != "" && vault == "" {
		vault = v
	}
	if vault == "" {
		vault = filepath.Join(home, "Documents", "Obsidian", "MainVault")
	}

	newRoot := installNewRoot
	if v := os.Getenv("NEW_PROJECT_ROOT"); v != "" && newRoot == "" {
		newRoot = v
	}
	if newRoot == "" {
		newRoot = filepath.Join(home, "src")
	}

	skillDir := os.Getenv("SKILL_INSTALL_DIR")
	if skillDir == "" {
		skillDir = filepath.Join(home, ".omp", "skills", "obsidian-task-runner")
	}

	if v := os.Getenv("NOTIFY_ENABLED"); v != "" {
		installNotif, _ = strconv.ParseBool(v)
	}
	if v := os.Getenv("POLL_INTERVAL_MINUTES"); v != "" {
		installPoll, _ = strconv.Atoi(v)
	}
	if v := os.Getenv("SYSTEMD_ENABLED"); v != "" {
		installSystemd, _ = strconv.ParseBool(v)
	}

	opts := install.Options{
		ObsidianVault:   vault,
		NewProjectRoot:  newRoot,
		SkillInstallDir: skillDir,
		NotifyEnabled:   installNotif,
		PollIntervalMin: installPoll,
		SystemdEnabled:  installSystemd,
		Force:           installForce,
		DryRun:          installDryRun,
		RestartSystemd:  installSystemd,
	}

	return install.Run(opts)
}

func init() {
	installCmd.Flags().BoolVarP(&installDryRun, "dry-run", "n", false, "Preview changes without applying")
	installCmd.Flags().BoolVar(&installForce, "force", false, "Force overwrite of all files")
	installCmd.Flags().StringVar(&installVault, "vault", "", "Obsidian vault path")
	installCmd.Flags().StringVar(&installNewRoot, "new-project-root", "", "New project root directory")
	installCmd.Flags().BoolVar(&installNotif, "notifications", true, "Enable desktop notifications")
	installCmd.Flags().IntVar(&installPoll, "poll-interval", 30, "Polling interval in minutes")
	installCmd.Flags().BoolVar(&installSystemd, "systemd", true, "Register systemd units")
	rootCmd.AddCommand(installCmd)
}
