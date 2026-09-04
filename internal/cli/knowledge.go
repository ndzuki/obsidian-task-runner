package cli

import (
	"encoding/json"
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
	cmd.AddCommand(kbGapsCmd, kbUsageCmd, kbSearchCmd, kbIndexCmd, kbRebuildCmd, kbAbsorbCmd, kbHitCmd, kbPromoteCmd, kbAskCmd)
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
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "project %s: no knowledge gaps (every ADR has a References target)\n", args[0])
			return nil
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "project %s: %d ADR(s) without knowledge-base coverage:\n\n", args[0], len(gaps))
		for _, g := range gaps {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  %s  %s\n", g.ADR, g.Title)
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
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "project %s: %d referenced doc(s), %d delivered task(s) with application metric\n",
				args[0], len(refs), usage.ProjectApplied[args[0]])
			for _, r := range refs {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", r)
			}
			return nil
		}
		if len(usage.ProjectRefs) == 0 {
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "no project references recorded yet")
			return nil
		}
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), "project → referenced documents:")
		for project, refs := range usage.ProjectRefs {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  %s (%d docs, %d applied)\n", project, len(refs), usage.ProjectApplied[project])
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
		// --vault/--db override the vault-map targets so callers (e.g.
		// agent-server's interactive KB-first precompute) can search an
		// explicit global knowledge vault without a map file. Embedding
		// backend config (if any) still comes from the loaded map.
		if kbSearchVault != "" {
			cfg.ObsidianVault = kbSearchVault
		}
		if kbSearchDB != "" {
			cfg.KBDb = kbSearchDB
		}
		query := strings.Join(args, " ")
		dbPath := knowledge.KBPath(cfg.ObsidianVault, cfg.KBDb)
		// SQLite-backed retrieval: FTS5 BM25 (persistent, incremental) with
		// optional embedding cosine blend when the vector layer is healthy.
		var client *knowledge.EmbeddingClient
		weight := 0.0
		if cfg.KBEmbedding != nil {
			weight = cfg.KBEmbedding.Weight
			client = knowledge.NewEmbeddingClient(cfg.KBEmbedding)
			ready, stored := knowledge.VecStatus(dbPath)
			switch {
			case !ready:
				_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "kb_embedding configured but vector index missing — run `otg kb index`; falling back to BM25.")
				client = nil
			case stored != "" && stored != cfg.KBEmbedding.Model:
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "vector store built with %s, configured %s — run `otg kb index` to rebuild; falling back to BM25.\n", stored, cfg.KBEmbedding.Model)
				client = nil
			}
		}
		hits, err := knowledge.SearchKnowledgeDB(dbPath, query, kbSearchLimit, !kbSearchArchived, client, weight)
		if err != nil {
			return err
		}
		// Optional cross-encoder rerank: hybrid top-N → rerank → final
		// limit. RerankResults degrades silently when the backend is
		// unreachable — the hybrid order stands. Interactive KB-first
		// precompute uses --no-rerank so the warm path is fast; `otg kb ask`
		// keeps the full rerank path.
		if cfg.KBRerank != nil && len(hits) > 0 && !kbSearchNoRerank {
			rc := knowledge.NewRerankClient(cfg.KBRerank)
			if len(hits) > cfg.KBRerank.TopN {
				hits = hits[:cfg.KBRerank.TopN]
			}
			hits = knowledge.RerankResults(hits, query, rc, kbSearchLimit)
		}
		if kbSearchJSON {
			// Machine-readable output for the interactive KB-first precompute
			// (agent-server spawns `otg kb search --json`). Deterministic
			// field order; warnings stay on stderr. Nil hits encode as [] not
			// null so the consumer always sees an array.
			if hits == nil {
				hits = []knowledge.SearchResult{}
			}
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetEscapeHTML(false)
			return enc.Encode(hits)
		}
		if len(hits) == 0 {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "no local knowledge matched %q — try web_search/Context7\n", query)
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
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%.4f  %s%s\n      %s\n      %s\n", h.Score, h.Path, chunk, h.Title, summary)
		}
		return nil
	},
}

var (
	kbAskLimit int
	kbAskModel string
)

// kbAskCmd answers a question over the knowledge base: hybrid retrieval →
// cited reference block → streamed chat completion (kb_chat must be
// configured). Sources are printed deterministically — the model never
// invents citations.
var kbAskCmd = &cobra.Command{
	Use:   "ask <question>",
	Short: "Answer a question over the knowledge base (retrieval + generation)",
	Long: `Answers a question grounded in References/ documents: hybrid retrieval
(BM25 + embedding) fetches the top references, they are cited as [N] blocks
in the prompt, and the kb_chat model streams the answer. The printed
「参考资料」 list is the actual retrieval result — the model cannot invent
sources. Requires kb_embedding (retrieval) and kb_chat (generation).`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(kbMapFile)
		if err != nil {
			return err
		}
		if cfg.KBEmbedding == nil {
			return fmt.Errorf("kb_embedding not configured — semantic retrieval is required for `kb ask` (add kb_embedding to vault-map.json)")
		}
		if cfg.KBChat == nil {
			return fmt.Errorf("kb_chat not configured — add kb_chat to vault-map.json, e.g. {\"url\":\"http://127.0.0.1:11434\",\"model\":\"qwen3:1.7b\"}")
		}
		query := strings.Join(args, " ")
		dbPath := knowledge.KBPath(cfg.ObsidianVault, cfg.KBDb)
		client := knowledge.NewEmbeddingClient(cfg.KBEmbedding)
		ready, stored := knowledge.VecStatus(dbPath)
		switch {
		case !ready:
			_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "warning: vector index missing — run `otg kb index` first")
		case stored != "" && stored != cfg.KBEmbedding.Model:
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: vector store built with %s, configured %s — run `otg kb index`\n", stored, cfg.KBEmbedding.Model)
		}
		chat := knowledge.NewChatClient(cfg.KBChat)
		opts := knowledge.AskOptions{
			Limit: kbAskLimit, Model: kbAskModel,
			Stream: func(s string) error {
				_, werr := fmt.Fprint(cmd.OutOrStdout(), s)
				return werr
			},
		}
		if cfg.KBRerank != nil {
			opts.Rerank = knowledge.NewRerankClient(cfg.KBRerank)
			opts.RerankTopN = cfg.KBRerank.TopN
		}
		refs, err := knowledge.AskKnowledgeDB(dbPath, query, opts, client, chat)
		if err != nil {
			return err
		}
		if len(refs) == 0 {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "no local knowledge matched %q — try web_search/Context7\n", query)
			return nil
		}
		_, _ = fmt.Fprintln(cmd.OutOrStdout())
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), "\n参考资料：")
		for _, r := range refs {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "- %s（%s）\n", r.Path, r.Title)
		}
		return nil
	},
}

// kbAbsorbCmd sinks interactive-session knowledge (daily DSH conversations
// outside the task pipeline) into the knowledge base. Reads the lesson from
// stdin: a "## 踩坑记录"-style block by default, or free-text project
// experience with --summary. Deduplicated against existing notes.
var kbAbsorbCmd = &cobra.Command{
	Use:   "absorb",
	Short: "Sink interactive-session lessons into the knowledge base",
	Long: `Sinks a lesson from an interactive execution session into References/:
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
		dbPath := knowledge.KBPath(cfg.ObsidianVault, cfg.KBDb)
		res, err := knowledge.AbsorbKnowledgeDB(cfg.ObsidianVault, kbAbsorbProject, string(data), kbAbsorbSummary, dbPath)
		if err != nil {
			return err
		}
		for _, e := range res.Errors {
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "absorb: %s\n", e)
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "absorbed: %d appended, %d duplicates, %d archived\n",
			res.Appended, res.Duplicates, len(res.Archived))
		for _, t := range res.Touched {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  wrote %s\n", t)
		}
		for _, a := range res.Archived {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  archived %q\n", a)
		}
		if res.Appended+res.Duplicates+len(res.Archived)+len(res.Errors) == 0 {
			_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "no lessons parsed — expected 踩坑记录 format (see --help)")
		}
		// The knowledge base changed: refresh INDEX and incrementally sync
		// the retrieval store so new lessons are immediately searchable.
		// Sync is idempotent (content_hash) and never rolls back on
		// embedding failure — FTS keeps working without ollama.
		if _, rerr := knowledge.RebuildINDEX(cfg.ObsidianVault); rerr != nil {
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: INDEX rebuild failed: %v\n", rerr)
		}
		var client *knowledge.EmbeddingClient
		if cfg.KBEmbedding != nil {
			client = knowledge.NewEmbeddingClient(cfg.KBEmbedding)
		}
		stats, serr := knowledge.SyncKnowledgeDB(cfg.ObsidianVault, dbPath, client)
		if serr != nil {
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: store sync failed: %v\n", serr)
		} else {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "store synced: %d docs (+%d -%d), %d chunks\n",
				stats.TotalDocs, stats.Added, stats.Removed, stats.TotalChunks)
			if stats.VecSkipped {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "kb_embedding not configured — FTS-only store (add kb_embedding to vault-map.json for semantic search)")
			} else if stats.VecError != nil {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: vector refresh failed: %v (BM25 still works)\n", stats.VecError)
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
		n, err := knowledge.IncrementHits(cfg.ObsidianVault, knowledge.KBPath(cfg.ObsidianVault, cfg.KBDb), []string{args[0]})
		if err != nil {
			return err
		}
		if n == 0 {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "no document bumped (missing: %s)\n", args[0])
			return nil
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "hits+1 on %s\n", args[0])
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
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "promoted %s\n", m)
		}
		if len(moved) == 0 {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "no documents reached hits≥%d\n", kbPromoteMinHits)
			return nil
		}
		// Rebuild INDEX and sync the store so the new core/ paths are
		// immediately searchable (moved files change layer + path → sync
		// picks them up as updates).
		n, rerr := knowledge.RebuildINDEX(cfg.ObsidianVault)
		if rerr != nil {
			return rerr
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "rebuilt INDEX.md: %d entries\n", n)
		dbPath := knowledge.KBPath(cfg.ObsidianVault, cfg.KBDb)
		var client *knowledge.EmbeddingClient
		if cfg.KBEmbedding != nil {
			client = knowledge.NewEmbeddingClient(cfg.KBEmbedding)
		}
		stats, serr := knowledge.SyncKnowledgeDB(cfg.ObsidianVault, dbPath, client)
		if serr != nil {
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: store sync failed: %v\n", serr)
		} else if stats.VecError != nil {
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: vector refresh failed: %v\n", stats.VecError)
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
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "rebuilt INDEX.md: %d entries\n", n)
		return nil
	},
}

// kbIndexCmd rebuilds the retrieval store from scratch — documents + FTS
// always, embeddings when configured. The store is derived data, so a
// rebuild is always safe.
var kbIndexCmd = &cobra.Command{
	Use:   "index",
	Short: "Rebuild the knowledge retrieval store (FTS + optional vectors)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(kbMapFile)
		if err != nil {
			return err
		}
		dbPath := knowledge.KBPath(cfg.ObsidianVault, cfg.KBDb)
		var client *knowledge.EmbeddingClient
		if cfg.KBEmbedding == nil {
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "kb_embedding not configured — building FTS-only store (add kb_embedding to vault-map.json for semantic search)")
		} else {
			client = knowledge.NewEmbeddingClient(cfg.KBEmbedding)
		}
		stats, err := knowledge.RebuildKnowledgeDB(cfg.ObsidianVault, dbPath, client)
		if err != nil {
			return fmt.Errorf("rebuild store: %w", err)
		}
		if stats.VecError != nil {
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: vector refresh failed: %v (FTS-only store)\n", stats.VecError)
		}
		if client != nil {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "rebuilt store: %d documents, %d chunks (%s, model %s)\n",
				stats.TotalDocs, stats.TotalChunks, cfg.KBEmbedding.Backend, cfg.KBEmbedding.Model)
		} else {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "rebuilt store: %d documents (FTS only)\n", stats.TotalDocs)
		}
		return nil
	},
}

var (
	kbSearchLimit    int
	kbSearchArchived bool
	kbSearchVault    string
	kbSearchDB       string
	kbSearchJSON     bool
	kbSearchNoRerank bool
)

func init() {
	kbGapsCmd.Flags().StringVar(&kbMapFile, "map-file", "", "path to vault-map.json")
	kbUsageCmd.Flags().StringVar(&kbMapFile, "map-file", "", "path to vault-map.json")
	kbSearchCmd.Flags().StringVar(&kbMapFile, "map-file", "", "path to vault-map.json")
	kbSearchCmd.Flags().IntVar(&kbSearchLimit, "limit", 5, "max results")
	kbSearchCmd.Flags().BoolVar(&kbSearchArchived, "archived", false, "include the archived/ layer in search (default: core + extended)")
	kbSearchCmd.Flags().StringVar(&kbSearchVault, "vault", "", "explicit knowledge vault root (overrides map-file obsidian_vault)")
	kbSearchCmd.Flags().StringVar(&kbSearchDB, "db", "", "explicit kb store path (overrides map-file kb_db)")
	kbSearchCmd.Flags().BoolVar(&kbSearchJSON, "json", false, "emit machine-readable JSON array (for interactive KB-first precompute)")
	kbSearchCmd.Flags().BoolVar(&kbSearchNoRerank, "no-rerank", false, "skip cross-encoder rerank (fast precompute/hybrid path)")
	kbAskCmd.Flags().StringVar(&kbMapFile, "map-file", "", "path to vault-map.json")
	kbAskCmd.Flags().IntVar(&kbAskLimit, "limit", 5, "max retrieved references")
	kbAskCmd.Flags().StringVar(&kbAskModel, "model", "", "chat model override (default: kb_chat.model)")
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
