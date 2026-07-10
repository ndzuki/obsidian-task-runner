package cli

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ndzuki/obsidian-task-runner/internal/task"
	"github.com/spf13/cobra"
)

var onReqChangedCmd = &cobra.Command{
	Use:   "on-req-changed <vault_path> <req_file_path>",
	Short: "Handle a requirement file change",
	Long: `Processes a changed requirement file: resets linked tasks to ready,
marks mid-execution tasks as pending_req, or auto-creates a new TASK document.`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		vaultPath := args[0]
		reqFile := args[1]

		// Make path relative to vault
		reqRel := reqFile
		if strings.HasPrefix(reqRel, vaultPath) {
			rel, err := filepath.Rel(vaultPath, reqFile)
			if err == nil {
				reqRel = rel
			}
		}

		fmt.Printf("需求文档变更: %s\n", reqRel)
		affected := task.OnReqChanged(vaultPath, reqRel)
		task.PrintAffected(affected)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(onReqChangedCmd)
}
