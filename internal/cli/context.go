package cli

import (
	"fmt"

	"github.com/ndzuki/obsidian-task-runner/internal/task"
	"github.com/spf13/cobra"
)

// ── ensure-context-term ────────────────────────────────────────────────────────

var ensureContextTermCmd = &cobra.Command{
	Use:   "ensure-context-term <project_dir> <term> <definition>",
	Short: "Append a domain term to CONTEXT.md ## Language section",
	Long: `Ensures a domain term exists in the project's CONTEXT.md Language
section. If the term already exists, it is a no-op. Otherwise,
the term is appended in **Term**: definition format.

Called by OMP skills (round1, ADR write) to auto-maintain the
shared domain vocabulary.`,
	Args: cobra.ExactArgs(3),
	RunE: runEnsureContextTerm,
}

func runEnsureContextTerm(cmd *cobra.Command, args []string) error {
	projectDir := args[0]
	term := args[1]
	definition := args[2]

	if err := task.EnsureContextTerm(projectDir, term, definition); err != nil {
		return fmt.Errorf("ensure context term: %w", err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "CONTEXT.md: term %q ensured\n", term)
	return nil
}
