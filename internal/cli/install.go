package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

var installCmd = &cobra.Command{
	Use:   "install",
	Short: "Install skill to ~/.omp/skills/ and configure systemd",
	Long: `Install the obsidian-task-runner skill to ~/.omp/skills/,
create the OMP agent symlink, generate vault-map.json, and
optionally configure systemd units.

Environment:
  OBSIDIAN_VAULT        Obsidian vault path (required)
  NEW_PROJECT_ROOT       New project root (default: $HOME/src)
  NOTIFY_ENABLED         Enable desktop notifications (default: true)
  POLL_INTERVAL_MINUTES  Poll interval (default: 30)
  SYSTEMD_ENABLED        Register systemd units (default: true)
  SKILL_INSTALL_DIR      Install path (default: ~/.omp/skills/...)`,
	RunE: runInstall,
}

var dryRun bool

func runInstall(cmd *cobra.Command, args []string) error {
	if dryRun {
		fmt.Println("[DRY RUN] Would install skill to ~/.omp/skills/obsidian-task-runner")
		fmt.Println("[DRY RUN] Would create OMP agent symlink")
		fmt.Println("[DRY RUN] Would generate vault-map.json")
		fmt.Println("[DRY RUN] Would configure systemd")
		return nil
	}
	return fmt.Errorf("install subcommand: full implementation in Phase 4")
}

func init() {
	installCmd.Flags().BoolVarP(&dryRun, "dry-run", "n", false, "Preview changes without applying")
}
