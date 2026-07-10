package cli

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/ndzuki/obsidian-task-runner/internal/project"
	"github.com/spf13/cobra"
)

var resolvePathCmd = &cobra.Command{
	Use:   "resolve-path <map_file> <project_name>",
	Short: "Resolve a project name to a local path",
	Long:  `Uses vault-map.json to resolve a project name to its filesystem path.`,
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		isNew, _ := cmd.Flags().GetBool("new-project")
		result := project.ResolveProject(args[0], args[1], isNew)
		data, _ := json.Marshal(result)
		fmt.Println(string(data))
		if result.Error != "" {
			fmt.Fprintln(os.Stderr, result.Error)
		}
		if result.Status == "error" {
			return fmt.Errorf("resolution failed")
		}
		return nil
	},
}

var registerProjectCmd = &cobra.Command{
	Use:   "register-project <map_file> <project_name> <project_path>",
	Short: "Register a new project in vault-map.json",
	Long: `Add or update a project entry in vault-map.json for future task lookups.
Uses atomic write (tmp → fsync → rename) to prevent corruption.`,
	Args: cobra.RangeArgs(3, 4),
	RunE: func(cmd *cobra.Command, args []string) error {
		gitRemote := ""
		if len(args) >= 4 {
			gitRemote = args[3]
		}
		dryRun, _ := cmd.Flags().GetBool("dry-run")
		return project.RegisterProject(args[0], args[1], args[2], gitRemote, dryRun)
	},
}

func init() {
	resolvePathCmd.Flags().Bool("new-project", false, "Treat as a new project (use new_project_root)")
	registerProjectCmd.Flags().Bool("dry-run", false, "Preview changes without writing")
	rootCmd.AddCommand(resolvePathCmd)
	rootCmd.AddCommand(registerProjectCmd)
}
