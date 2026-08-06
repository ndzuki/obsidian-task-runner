package cli

import (
	"fmt"

	"github.com/ndzuki/obsidian-task-runner/internal/config"
	"github.com/ndzuki/obsidian-task-runner/internal/knowledge"
	"github.com/spf13/cobra"
)

func newKnowledgeCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "kb",
		Short: "Knowledge-base inspection commands",
	}
	cmd.AddCommand(kbGapsCmd, kbUsageCmd)
	return cmd
}

// kbGapsCmd reports project ADRs with no matching knowledge-base document.
var kbGapsCmd = &cobra.Command{
	Use:   "gaps <project>",
	Short: "List project ADRs with no matching References document (知识缺口)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(kbMapFile)
		if err != nil {
			return err
		}
		gaps, err := knowledge.ScanKnowledgeGaps(cfg.ObsidianVault, args[0])
		if err != nil {
			return err
		}
		if len(gaps) == 0 {
			fmt.Fprintf(cmd.OutOrStdout(), "project %s: no knowledge gaps (every ADR has a References target)\n", args[0])
			return nil
		}
		fmt.Fprintf(cmd.OutOrStdout(), "project %s: %d ADR(s) without knowledge-base coverage:\n\n", args[0], len(gaps))
		for _, g := range gaps {
			fmt.Fprintf(cmd.OutOrStdout(), "  %s  %s\n", g.ADR, g.Title)
		}
		return nil
	},
}

// kbUsageCmd prints the project ↔ document reference graph.
var kbUsageCmd = &cobra.Command{
	Use:   "usage [project]",
	Short: "Show which projects reference which knowledge documents",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(kbMapFile)
		if err != nil {
			return err
		}
		usage, err := knowledge.ScanProjectUsage(cfg.ObsidianVault)
		if err != nil {
			return err
		}
		if len(args) == 1 {
			refs := usage.ProjectRefs[args[0]]
			fmt.Fprintf(cmd.OutOrStdout(), "project %s: %d referenced doc(s), %d delivered task(s) with application metric\n",
				args[0], len(refs), usage.ProjectApplied[args[0]])
			for _, r := range refs {
				fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", r)
			}
			return nil
		}
		if len(usage.ProjectRefs) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "no project references recorded yet")
			return nil
		}
		fmt.Fprintln(cmd.OutOrStdout(), "project → referenced documents:")
		for project, refs := range usage.ProjectRefs {
			fmt.Fprintf(cmd.OutOrStdout(), "  %s (%d docs, %d applied)\n", project, len(refs), usage.ProjectApplied[project])
		}
		return nil
	},
}

var kbMapFile string

func init() {
	kbGapsCmd.Flags().StringVar(&kbMapFile, "map-file", "", "path to vault-map.json")
	kbUsageCmd.Flags().StringVar(&kbMapFile, "map-file", "", "path to vault-map.json")
	rootCmd.AddCommand(newKnowledgeCommand())
}
