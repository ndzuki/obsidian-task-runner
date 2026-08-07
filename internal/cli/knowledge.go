package cli

import (
	"fmt"
	"io"
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
	cmd.AddCommand(kbGapsCmd, kbUsageCmd, kbSearchCmd, kbIndexCmd, kbRebuildCmd, kbAbsorbCmd, kbHitCmd, kbPromoteCmd)
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

// kbSearchCmd ranks References documents by relevance: BM25 plus embedding
// cosine similarity when kb_embedding is configured and the vector index
// exists.
var kbSearchCmd = &cobra.Command{
	Use:   "search <query>",
	Short: "Rank knowledge documents by relevance (BM25 + optional embedding)",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(kbMapFile)
		if err != nil {
			return err
		}
		query := strings.Join(args, " ")
		// Cached BM25: at 10k documents a full rebuild per query is
		// 80-100s; the gob cache makes repeated queries sub-second.
		idx, err := knowledge.BuildSearchIndexCached(cfg.ObsidianVault, !kbSearchArchived)
		if err != nil {
			return err
		}
		var hits []knowledge.SearchResult
		if cfg.KBEmbedding != nil {
			client := knowledge.NewEmbeddingClient(cfg.KBEmbedding)
			vectors := knowledge.LoadVectorsFor(cfg.ObsidianVault, cfg.KBEmbedding.Model)
			if len(vectors) > 0 {
				hits = idx.SearchHybrid(query, kbSearchLimit, vectors, cfg.KBEmbedding.Weight, client.Embed)
			} else if stored := knowledge.VectorsModel(cfg.ObsidianVault); stored != "" && stored != cfg.KBEmbedding.Model {
				fmt.Fprintf(cmd.OutOrStdout(), "vector store built with %s, configured %s — run `otg kb index` to rebuild; falling back to BM25.\n", stored, cfg.KBEmbedding.Model)
			} else if knowledge.VectorStoreCorrupt(cfg.ObsidianVault) {
				fmt.Fprintln(cmd.OutOrStdout(), "vector store corrupt or unreadable — run `otg kb index` to rebuild; falling back to BM25.")
			} else {
				fmt.Fprintln(cmd.OutOrStdout(), "kb_embedding configured but vector index missing — run `otg kb index`; falling back to BM25.")
			}
		}
		if hits == nil {
			hits = idx.Search(query, kbSearchLimit)
		}
		if len(hits) == 0 {
			fmt.Fprintf(cmd.OutOrStdout(), "no local knowledge matched %q — try web_search/Context7\n", query)
			return nil
		}
		for _, h := range hits {
			summary := h.Summary
			if r := []rune(summary); len(r) > 60 {
				summary = string(r[:57]) + "..."
			}
			chunk := ""
			if h.Chunk != "" {
				chunk = "  → " + h.Chunk
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%.4f  %s%s\n      %s\n      %s\n", h.Score, h.Path, chunk, h.Title, summary)
		}
		return nil
	},
}

// kbAbsorbCmd sinks interactive-session knowledge (daily OMP conversations
// outside the task pipeline) into the knowledge base. Reads the lesson from
// stdin: a "## 踩坑记录"-style block by default, or free-text project
// experience with --summary. Deduplicated against existing notes.
var kbAbsorbCmd = &cobra.Command{
	Use:   "absorb",
	Short: "Sink interactive-session lessons into the knowledge base",
	Long: `Sinks a lesson from an interactive OMP session into References/:
- default mode expects a 踩坑记录 block on stdin:
    ### 2026-08-07: {phenomenon}
    - 现象: ...
    - 失败方案: ...
    - 根因: ...
    - 成功方案: ...
    - 相关文档: extended/tools/kulala-http-client.md (optional)
- --summary treats stdin as free-text project experience and appends a
  「实践经验」 note under the best-matching document.
Duplicate lessons (same normalized title or failed approach) are skipped.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(kbMapFile)
		if err != nil {
			return err
		}
		data, err := io.ReadAll(cmd.InOrStdin())
		if err != nil {
			return fmt.Errorf("read stdin: %w", err)
		}
		res, err := knowledge.AbsorbKnowledge(cfg.ObsidianVault, kbAbsorbProject, string(data), kbAbsorbSummary)
		if err != nil {
			return err
		}
		for _, e := range res.Errors {
			fmt.Fprintf(cmd.ErrOrStderr(), "absorb: %s\n", e)
		}
		fmt.Fprintf(cmd.OutOrStdout(),
			"absorbed: %d appended, %d duplicates, %d archived\n",
			res.Appended, res.Duplicates, len(res.Archived))
		for _, t := range res.Touched {
			fmt.Fprintf(cmd.OutOrStdout(), "  wrote %s\n", t)
		}
		for _, a := range res.Archived {
			fmt.Fprintf(cmd.OutOrStdout(), "  archived %q\n", a)
		}
		if res.Appended+res.Duplicates+len(res.Archived)+len(res.Errors) == 0 {
			fmt.Fprintln(cmd.ErrOrStderr(), "no lessons parsed — expected 踩坑记录 format (see --help)")
		}
		// The knowledge base changed: refresh INDEX and the embedding vectors
		// so the new lessons are immediately retrievable. Embedding refresh is
		// incremental (unchanged docs skip in <500ms) and non-blocking on
		// failure — BM25 retrieval keeps working without ollama.
		if _, rerr := knowledge.RebuildINDEX(cfg.ObsidianVault); rerr != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: INDEX rebuild failed: %v\n", rerr)
		}
		if cfg.KBEmbedding != nil {
			client := knowledge.NewEmbeddingClient(cfg.KBEmbedding)
			if n, verr := knowledge.BuildVectors(cfg.ObsidianVault, client); verr != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: vector refresh failed: %v (BM25 still works)\n", verr)
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "vectors refreshed: %d docs\n", n)
			}
		}
		return nil
	},
}

var (
	kbAbsorbProject string
	kbAbsorbSummary bool
)

// kbHitCmd bumps the heat counter of a knowledge document after a successful
// interactive application — the manual twin of the automatic merge/absorb
// bumps.
var kbHitCmd = &cobra.Command{
	Use:   "hit <ref-path>",
	Short: "Bump a knowledge document's application heat (+1 hits)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(kbMapFile)
		if err != nil {
			return err
		}
		n, err := knowledge.IncrementHits(cfg.ObsidianVault, []string{args[0]})
		if err != nil {
			return err
		}
		if n == 0 {
			fmt.Fprintf(cmd.OutOrStdout(), "no document bumped (missing: %s)\n", args[0])
			return nil
		}
		fmt.Fprintf(cmd.OutOrStdout(), "hits+1 on %s\n", args[0])
		return nil
	},
}

// kbPromoteCmd moves extended/ documents whose hits reach --min-hits into
// core/, so frequently reused experience joins the primary retrieval layer.
var kbPromoteCmd = &cobra.Command{
	Use:   "promote",
	Short: "Move high-heat extended/ documents into core/",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(kbMapFile)
		if err != nil {
			return err
		}
		moved, err := knowledge.PromoteToCore(cfg.ObsidianVault, kbPromoteMinHits)
		if err != nil {
			return err
		}
		for _, m := range moved {
			fmt.Fprintf(cmd.OutOrStdout(), "promoted %s\n", m)
		}
		if len(moved) == 0 {
			fmt.Fprintf(cmd.OutOrStdout(), "no documents reached hits≥%d\n", kbPromoteMinHits)
			return nil
		}
		// Rebuild INDEX and refresh vectors so the new core/ paths are
		// immediately searchable (vector keys are path-based).
		n, rerr := knowledge.RebuildINDEX(cfg.ObsidianVault)
		if rerr != nil {
			return rerr
		}
		fmt.Fprintf(cmd.OutOrStdout(), "rebuilt INDEX.md: %d entries\n", n)
		if cfg.KBEmbedding != nil {
			client := knowledge.NewEmbeddingClient(cfg.KBEmbedding)
			if _, verr := knowledge.BuildVectors(cfg.ObsidianVault, client); verr != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: vector refresh failed: %v\n", verr)
			}
		}
		return nil
	},
}

var kbPromoteMinHits int

// kbRebuildCmd regenerates References/INDEX.md from knowledge frontmatter.
// The watcher does not listen to References/ (only Projects/), so manual or
// agent-driven knowledge writes need an explicit rebuild entry point.
var kbRebuildCmd = &cobra.Command{
	Use:   "rebuild-index",
	Short: "Rebuild References/INDEX.md from knowledge frontmatter",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(kbMapFile)
		if err != nil {
			return err
		}
		n, err := knowledge.RebuildINDEX(cfg.ObsidianVault)
		if err != nil {
			return fmt.Errorf("rebuild index: %w", err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "rebuilt INDEX.md: %d entries\n", n)
		return nil
	},
}

// kbIndexCmd builds the embedding vector store for semantic search.
var kbIndexCmd = &cobra.Command{
	Use:   "index",
	Short: "Build the embedding vector index for knowledge search",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(kbMapFile)
		if err != nil {
			return err
		}
		if cfg.KBEmbedding == nil {
			return fmt.Errorf("kb_embedding not configured in vault-map.json (add {backend,url,model})")
		}
		client := knowledge.NewEmbeddingClient(cfg.KBEmbedding)
		n, err := knowledge.BuildVectors(cfg.ObsidianVault, client)
		if err != nil {
			return fmt.Errorf("build vectors: %w", err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "embedded %d documents (%s, model %s)\n", n, cfg.KBEmbedding.Backend, cfg.KBEmbedding.Model)
		return nil
	},
}

var (
	kbSearchLimit   int
	kbSearchArchived bool
)

func init() {
	kbGapsCmd.Flags().StringVar(&kbMapFile, "map-file", "", "path to vault-map.json")
	kbUsageCmd.Flags().StringVar(&kbMapFile, "map-file", "", "path to vault-map.json")
	kbSearchCmd.Flags().StringVar(&kbMapFile, "map-file", "", "path to vault-map.json")
	kbSearchCmd.Flags().IntVar(&kbSearchLimit, "limit", 5, "max results")
	kbSearchCmd.Flags().BoolVar(&kbSearchArchived, "archived", false, "include the archived/ layer in search (default: core + extended)")
	kbIndexCmd.Flags().StringVar(&kbMapFile, "map-file", "", "path to vault-map.json")
	kbRebuildCmd.Flags().StringVar(&kbMapFile, "map-file", "", "path to vault-map.json")
	kbAbsorbCmd.Flags().StringVar(&kbMapFile, "map-file", "", "path to vault-map.json")
	kbAbsorbCmd.Flags().StringVar(&kbAbsorbProject, "project", "interactive", "source project name for provenance")
	kbAbsorbCmd.Flags().BoolVar(&kbAbsorbSummary, "summary", false, "treat stdin as free-text project experience")
	kbHitCmd.Flags().StringVar(&kbMapFile, "map-file", "", "path to vault-map.json")
	kbPromoteCmd.Flags().StringVar(&kbMapFile, "map-file", "", "path to vault-map.json")
	kbPromoteCmd.Flags().IntVar(&kbPromoteMinHits, "min-hits", 3, "hits threshold for core promotion")
	rootCmd.AddCommand(newKnowledgeCommand())
}
