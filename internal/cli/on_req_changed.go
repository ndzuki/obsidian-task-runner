package cli

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ndzuki/obsidian-task-runner/internal/config"
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

		// default_assignee 取自 vault-map，预写新建 TASK 的模型委派；
		// 为空则保持旧行为（blocked 等人工补填）。
		//
		// 空路径 ⇒ config.Load 回退默认 vault-map.json。原实现读包级 `kbMapFile`，
		// 但本命令从未给它注册 `--map-file`（注册只挂在已删除的 `otg kb` 子命令上），
		// 故运行时它恒为 ""——这里显式传 "" 保持原行为，不引入新的默认值。
		cfg, err := config.Load("")
		if err != nil {
			return err
		}
		affected := task.OnReqChanged(vaultPath, reqRel, cfg.DefaultAssignee)
		task.PrintAffected(affected)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(onReqChangedCmd)
}
