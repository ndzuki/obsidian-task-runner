package cli

import (
	"errors"
	"fmt"
	"github.com/ndzuki/obsidian-task-runner/internal/config"
	"github.com/ndzuki/obsidian-task-runner/internal/daemon"
	"github.com/ndzuki/obsidian-task-runner/internal/project"
	"github.com/spf13/cobra"
)

var unregisterMapFile string

var unregisterProjectCmd = &cobra.Command{
	Use:   "unregister-project <name>",
	Short: "Remove a project from vault-map and clean up its task worktrees",
	Long: `Removes the project entry from vault-map.json and deletes its task
worktrees. Must be run while the entry still exists — its checkout path is
needed to locate the worktrees. The project's checkout directory and remote
repository are left untouched.`,
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
		if err := daemon.RemoveProjectWorktrees(cfg.WorktreeBase, removedPath); err != nil {
			// 清理失败必须返回非零退出码：此时条目已删、无法重跑，静默会让
			// 残留 worktree 重新落入永久孤儿状态。
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "unregistered project %q (checkout %s left in place), but worktree cleanup reported: %v\n", name, removedPath, err)
			return fmt.Errorf("unregistered project %q but worktree cleanup failed: %w", name, err)
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "unregistered project %q (checkout %s left in place, worktrees removed)\n", name, removedPath)
		return nil
	},
}

func init() {
	unregisterProjectCmd.Flags().StringVar(&unregisterMapFile, "map-file", "", "Path to vault-map.json")
	rootCmd.AddCommand(unregisterProjectCmd)
}
