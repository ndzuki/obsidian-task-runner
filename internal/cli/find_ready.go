package cli

import (
	"os"

	"github.com/ndzuki/obsidian-task-runner/internal/task"
	"github.com/spf13/cobra"
)

var findReadyCmd = &cobra.Command{
	Use:   "find-ready <vault_path>",
	Short: "Find ready-to-process tasks in a vault",
	Long:  `Scans the Tasks/ directory, parses frontmatter, and prints ready tasks as NDJSON.`,
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		tasks, err := task.FindReadyTasks(args[0])
		if err != nil {
			return err
		}
		if len(tasks) == 0 {
			os.Exit(0)
		}
		task.PrintReadyTasks(tasks)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(findReadyCmd)
}
