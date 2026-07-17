// Package cli provides the cobra command tree for otg.
package cli

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/ndzuki/obsidian-task-runner/internal/config"
	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
	"github.com/spf13/cobra"
)

var cfg *config.Config

// Execute runs the root command.
func Execute() error {
	return rootCmd.Execute()
}

var rootCmd = &cobra.Command{
	Use:   "otg",
	Short: "Obsidian Task Runner — Go implementation",
	Long: `otg replaces the Bash+Python Obsidian Task Runner scripts with
a single compiled Go binary. It discovers ready tasks, handles
requirement changes, updates frontmatter, and manages the
Round 1 → Round 2 → Merge Phase lifecycle.`,
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version information",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("otg v0.1.0")
		fmt.Println("Obsidian Task Runner — Go edition")
	},
}

// updateStatusCmd replaces update_task_status.py.
var updateStatusCmd = &cobra.Command{
	Use:   "update-status <task_path> [field=value ...]",
	Short: "Update YAML frontmatter fields in a task document",
	Long: `Atomically update frontmatter fields in a task markdown file.
Automatically sets the updated timestamp. Supports type coercion
for bool (true/false), int, float, string, and list (comma-separated) values.

Examples:
  otg update-status task.md status=plan-review plan_version=1
  otg update-status task.md status=done merge_approved=false
  otg update-status task.md blocked_by=TASK-010,TASK-039`,
	Args: cobra.MinimumNArgs(1),
	RunE: runUpdateStatus,
}

func runUpdateStatus(cmd *cobra.Command, args []string) error {
	taskPath := args[0]
	updates := make(map[string]interface{})

	for _, arg := range args[1:] {
		eq := -1
		for i := 0; i < len(arg); i++ {
			if arg[i] == '=' {
				eq = i
				break
			}
		}
		if eq == -1 {
			fmt.Fprintf(os.Stderr, "warning: skipping invalid arg %q (expected key=value)\n", arg)
			continue
		}
		key, val := arg[:eq], arg[eq+1:]

		// List-type fields: blocked_by, blocks, tags
		if isListField(key) {
			if val == "" {
				updates[key] = []string{}
			} else {
				parts := strings.Split(val, ",")
				trimmed := make([]string, 0, len(parts))
				for _, p := range parts {
					p = strings.TrimSpace(p)
					if p != "" {
						trimmed = append(trimmed, p)
					}
				}
				updates[key] = trimmed
			}
			continue
		}

		// Type coercion
		switch {
		case val == "true":
			updates[key] = true
		case val == "false":
			updates[key] = false
		case isDigits(val):
			n, err := strconv.Atoi(val)
			if err == nil {
				updates[key] = n
			} else {
				updates[key] = val
			}
		default:
			updates[key] = val
		}
	}

	if len(updates) == 0 {
		return fmt.Errorf("no valid fields to update")
	}

	if err := yamlfrontmatter.Update(taskPath, updates); err != nil {
		return fmt.Errorf("update %s: %w", taskPath, err)
	}
	return nil
}

// isListField returns true for frontmatter fields that accept list values.
func isListField(key string) bool {
	switch key {
	case "blocked_by", "blocks", "tags":
		return true
	}
	return false
}

func isDigits(s string) bool {
	if len(s) == 0 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func init() {
	rootCmd.AddCommand(versionCmd)
	rootCmd.AddCommand(updateStatusCmd)
}
