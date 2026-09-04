package daemon

import (
	"context"
	"net/http"
	"time"

	"github.com/ndzuki/obsidian-task-runner/internal/knowledge"
	"github.com/ndzuki/obsidian-task-runner/internal/vaultweb"
)

// startVaultWeb 在 daemon 进程内启动只读 vault 看板 HTTP API（Phase 4），供
// DSH web 的 vault-dashboard 插件使用。cfg.VaultWebAddr 为空或 vault 未配置时
// no-op；生命周期由 stopVaultWeb 收口（与 agent-server 同属 daemon 托管）。
func (r *Runner) startVaultWeb() error {
	addr := r.cfg.VaultWebAddr
	if addr == "" || r.cfg.ObsidianVault == "" {
		return nil
	}
	srv := &http.Server{
		Addr: addr,
		Handler: vaultweb.NewWithAgentServer(r.cfg.ObsidianVault, r.cfg.AgentServerAddr).
			WithKBSearch(r.kbSearchForHTTP).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	r.vaultWebServer = srv
	go func() {
		r.logger.Printf("vault dashboard API listening on http://%s", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			r.logger.Printf("vault web serve: %v", err)
		}
	}()
	return nil
}

// kbSearchForHTTP answers the vaultweb /api/kb/search endpoint in-process
// (B2), mirroring `otg kb search` semantics: FTS5 BM25 + optional embedding
// blend (with the same VecStatus health gate — vector layer missing/model
// mismatch falls back to BM25) + optional cross-encoder rerank. Consumers
// (agent-server / kb-preflight) hit this instead of spawning the otg binary;
// when the daemon or endpoint is unavailable they fall back to spawn.
// rerank=false lets the interactive precompute path stay fast: the hybrid
// result is already good enough for top-N injection, and the cross-encoder
// remains enabled for `otg kb ask` / explicit deep search.
func (r *Runner) kbSearchForHTTP(query string, limit int, rerank bool) ([]knowledge.SearchResult, error) {
	dbPath := knowledge.KBPath(r.cfg.ObsidianVault, r.cfg.KBDb)
	var client *knowledge.EmbeddingClient
	weight := 0.0
	if r.cfg.KBEmbedding != nil {
		weight = r.cfg.KBEmbedding.Weight
		client = knowledge.NewEmbeddingClient(r.cfg.KBEmbedding)
		ready, stored := knowledge.VecStatus(dbPath)
		if !ready {
			r.logger.Printf("kb http search: vector index missing, falling back to BM25")
			client = nil
		} else if stored != "" && stored != r.cfg.KBEmbedding.Model {
			r.logger.Printf("kb http search: vector store built with %s, configured %s — falling back to BM25", stored, r.cfg.KBEmbedding.Model)
			client = nil
		}
	}
	hits, err := knowledge.SearchKnowledgeDB(dbPath, query, limit, true, client, weight)
	if err != nil {
		return nil, err
	}
	if rerank && r.cfg.KBRerank != nil && len(hits) > 0 {
		rc := knowledge.NewRerankClient(r.cfg.KBRerank)
		if len(hits) > r.cfg.KBRerank.TopN {
			hits = hits[:r.cfg.KBRerank.TopN]
		}
		hits = knowledge.RerankResults(hits, query, rc, limit)
	}
	return hits, nil
}

// stopVaultWeb 优雅关闭内嵌 vault 看板服务。
func (r *Runner) stopVaultWeb() {
	if r.vaultWebServer == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := r.vaultWebServer.Shutdown(ctx); err != nil {
		r.logger.Printf("vault web shutdown: %v", err)
	}
	r.vaultWebServer = nil
}
