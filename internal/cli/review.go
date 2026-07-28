package cli

import (
	"encoding/json"
	"fmt"

	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
	"github.com/spf13/cobra"
)

var reviewJSON bool

var reviewCmd = &cobra.Command{
	Use:   "review <task_path>",
	Short: "Show review bundle for a task",
	Long: `Parses the frontmatter of a task Markdown file and displays the
fields relevant to the review phase: status, plan_version,
target_branch, and review_bundle.

Use --json for machine-readable output.`,
	Args: cobra.ExactArgs(1),
	RunE: runReview,
}

type reviewOutput struct {
	Status       string `json:"status"`
	PlanVersion  int    `json:"plan_version"`
	TargetBranch string `json:"target_branch"`
	ReviewBundle string `json:"review_bundle"`
}

func runReview(cmd *cobra.Command, args []string) error {
	taskPath := args[0]
	fm, err := yamlfrontmatter.ParseTaskDocument(taskPath)
	if err != nil {
		return fmt.Errorf("parse %s: %w", taskPath, err)
	}

	reviewBundle := ""
	if fm.Extra != nil {
		if v, ok := fm.Extra["review_bundle"]; ok {
			if s, ok2 := v.(string); ok2 {
				reviewBundle = s
			}
		}
	}

	out := reviewOutput{
		Status:       fm.Status,
		PlanVersion:  fm.PlanVersion,
		TargetBranch: fm.TargetBranch,
		ReviewBundle: reviewBundle,
	}

	if reviewJSON {
		data, _ := json.MarshalIndent(out, "", "  ")
		fmt.Fprintln(cmd.OutOrStdout(), string(data))
		return nil
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Status:        %s\n", out.Status)
	fmt.Fprintf(cmd.OutOrStdout(), "Plan version:  %d\n", out.PlanVersion)
	fmt.Fprintf(cmd.OutOrStdout(), "Target branch: %s\n", out.TargetBranch)
	if out.ReviewBundle != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Review bundle: %s\n", out.ReviewBundle)
	}
	return nil
}

func init() {
	reviewCmd.Flags().BoolVar(&reviewJSON, "json", false, "Output in JSON format")
	rootCmd.AddCommand(reviewCmd)
}
