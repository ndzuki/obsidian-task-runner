// Package cli provides the cobra command tree for otg.
package cli

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
	"github.com/spf13/cobra"
)

var (
	version string
	commit  string
)

// Execute runs the root command. version and commit are set via ldflags at build time.
func Execute(v, c string) error {
	version = v
	if version == "" {
		version = "dev"
	}
	commit = c
	if commit == "" {
		commit = "unknown"
	}
	return rootCmd.Execute()
}

var rootCmd = &cobra.Command{
	Use:   "otg",
	Short: "Obsidian Task Runner — Go edition",
	Long:  "Task lifecycle automation for Obsidian vaults. Watches project directories, drives OMP skills, and manages git worktrees.",
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version information",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("otg %s (%s)\n", version, commit)
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
	case "blocked_by", "tags":
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
	rootCmd.AddCommand(validateDocCmd)
	rootCmd.AddCommand(repairDocCmd)
	rootCmd.AddCommand(writeAdrCmd)
	rootCmd.AddCommand(validateAdrCmd)
	rootCmd.AddCommand(statusCmd)
	rootCmd.AddCommand(reviewCmd)
	rootCmd.AddCommand(ensureContextTermCmd)
}
var validateDocCmd = &cobra.Command{
	Use:   "validate-doc <path>",
	Short: "Validate any document (TASK/REQ/ADR) frontmatter and body",
	Long:  `Auto-detects document type and applies appropriate validation.`,
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := yamlfrontmatter.ValidateDocument(args[0]); err != nil {
			return fmt.Errorf("%s: %w", args[0], err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s: document OK\n", args[0])
		return nil
	},
}

// ── repair-doc ───────────────────────────────────────────────────────────────

var repairDocCmd = &cobra.Command{
	Use:   "repair-doc <task_path>",
	Short: "Repair corrupted frontmatter in a task document",
	Long: `Attempts to salvage a corrupted frontmatter by keeping only valid
key: value lines and discarding malformed text (e.g. OMP agent
output that leaked into the YAML block). If the file is already
valid, repair-doc is a no-op.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := yamlfrontmatter.Repair(args[0]); err != nil {
			return fmt.Errorf("%s: %w", args[0], err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s: repaired\n", args[0])
		return nil
	},
}
