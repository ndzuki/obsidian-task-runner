package cli

import (
	"fmt"
	"strings"

	"github.com/ndzuki/obsidian-task-runner/internal/config"
	"github.com/ndzuki/obsidian-task-runner/internal/knowledge"
	"github.com/spf13/cobra"
)

func newKnowledgeCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "kb",
		Short: "Knowledge-base inspection commands",
	}
	cmd.AddCommand(kbGapsCmd, kbUsageCmd, kbSearchCmd)
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

// kbSearchCmd ranks References documents by BM25 relevance to the query.
var kbSearchCmd = &cobra.Command{
	Use:   "search <query>",
	Short: "Rank knowledge documents by relevance (BM25, local)",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(kbMapFile)
		if err != nil {
			return err
		}
		query := strings.Join(args, " ")
		idx, err := knowledge.BuildSearchIndex(cfg.ObsidianVault)
		if err != nil {
			return err
		}
		hits := idx.Search(query, kbSearchLimit)
		if len(hits) == 0 {
			fmt.Fprintf(cmd.OutOrStdout(), "no local knowledge matched %q — try web_search/Context7\n", query)
			return nil
		}
		for _, h := range hits {
			summary := h.Summary
			if r := []rune(summary); len(r) > 60 {
				summary = string(r[:57]) + "..."
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%.4f  %s\n      %s\n      %s\n", h.Score, h.Path, h.Title, summary)
		}
		return nil
	},
}

var kbSearchLimit int

func init() {
	kbGapsCmd.Flags().StringVar(&kbMapFile, "map-file", "", "path to vault-map.json")
	kbUsageCmd.Flags().StringVar(&kbMapFile, "map-file", "", "path to vault-map.json")
	kbSearchCmd.Flags().StringVar(&kbMapFile, "map-file", "", "path to vault-map.json")
	kbSearchCmd.Flags().IntVar(&kbSearchLimit, "limit", 5, "max results")
	rootCmd.AddCommand(newKnowledgeCommand())
}
