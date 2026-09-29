package cli

import (
	"errors"
	"fmt"
	"github.com/ndzuki/obsidian-task-runner/internal/config"
	"github.com/ndzuki/obsidian-task-runner/internal/project"
	"github.com/spf13/cobra"
)

var unregisterMapFile string

var unregisterProjectCmd = &cobra.Command{
	Use:   "unregister-project <name>",
	Short: "Remove a project from vault-map",
	Long: `Removes the project entry from vault-map.json. The project's checkout
directory and remote repository are left untouched. (Task-worktree cleanup was
removed on 2026-09-29 together with the otg daemon.)`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		cfg, err := config.Load(unregisterMapFile)
		if err != nil {
			return err
		}
		removedPath, err := project.UnregisterProject(cfg.ConfigPath, name)
		if err != nil {
			if errors.Is(err, project.ErrProjectNotFound) {
				return fmt.Errorf("project %q not found in %s", name, cfg.ConfigPath)
			}
			return err
		}
		// 注册了但未记录 path（配置异常）：条目已删，无法定位 worktree，提示即可。
		if removedPath == "" {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "unregistered project %q (no path recorded; worktree cleanup skipped)\n", name)
			return nil
		}
		// worktree 清理随 daemon 于 2026-09-29 退役：原 daemon.RemoveProjectWorktrees
		// 已不存在。残留 worktree 需人工清理（git worktree remove / prune）。
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "unregistered project %q (checkout %s left in place; task worktrees are no longer cleaned up — the daemon that owned them was retired 2026-09-29)\n", name, removedPath)
		return nil
	},
}

func init() {
	unregisterProjectCmd.Flags().StringVar(&unregisterMapFile, "map-file", "", "Path to vault-map.json")
	rootCmd.AddCommand(unregisterProjectCmd)
}
