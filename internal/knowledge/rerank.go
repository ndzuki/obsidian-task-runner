package knowledge

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/ndzuki/obsidian-task-runner/internal/config"
)

// RerankClient talks to a cross-encoder rerank backend — OpenAI-compatible
// /v1/rerank (llama.cpp server implements this shape) or llama.cpp native
// /rerank. Optional: when kb_rerank is configured, `otg kb search` reranks
// the hybrid top-N with a dedicated cross-encoder before trimming to the
// final limit. Ollama's server shipped no rerank route as of 0.32.x, so
// this points at a separate llama-server instance.
type RerankClient struct {
	cfg    *config.KBRerankConfig
	client *http.Client
}

// NewRerankClient wraps the configured rerank backend. A nil cfg returns nil
// — callers then skip the rerank stage.
func NewRerankClient(cfg *config.KBRerankConfig) *RerankClient {
	if cfg == nil {
		return nil
	}
	return &RerankClient{
		cfg:    cfg,
		client: &http.Client{Timeout: 60 * time.Second},
	}
}

// Rerank returns per-document relevance scores against query, in input
// order. Backend selection: "llamacpp" targets the native /rerank route,
// everything else the OpenAI-compatible /v1/rerank (with a one-shot
// fallback to /rerank when the first route 404s).
func (c *RerankClient) Rerank(query string, documents []string) ([]float64, error) {
	if c == nil || len(documents) == 0 {
		return nil, nil
	}
	body, err := json.Marshal(map[string]any{
		"model": c.cfg.Model, "query": query, "documents": documents,
	})
	if err != nil {
		return nil, err
	}
	var lastErr error
	for _, path := range c.routes() {
		req, err := http.NewRequest(http.MethodPost, c.cfg.URL+path, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := c.client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		data, rerr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
		if rerr != nil {
			return nil, rerr
		}
		if resp.StatusCode == http.StatusNotFound && len(c.routes()) > 1 {
			lastErr = fmt.Errorf("rerank %s: %d", path, http.StatusNotFound)
			continue // route not served — try the sibling
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("rerank %s: %s: %s", path, resp.Status, strings.TrimSpace(string(data)))
		}
		var payload struct {
			Results []struct {
				Index int     `json:"index"`
				Score float64 `json:"relevance_score"`
			} `json:"results"`
		}
		if err := json.Unmarshal(data, &payload); err != nil {
			return nil, fmt.Errorf("rerank decode: %w", err)
		}
		if len(payload.Results) != len(documents) {
			return nil, fmt.Errorf("rerank: got %d scores for %d documents", len(payload.Results), len(documents))
		}
		scores := make([]float64, len(documents))
		for _, r := range payload.Results {
			if r.Index >= 0 && r.Index < len(scores) {
				scores[r.Index] = r.Score
			}
		}
		return scores, nil
	}
	return nil, fmt.Errorf("rerank unreachable: %v", lastErr)
}

// routes returns the endpoint candidates in try order: the configured
// backend's native route first, then the sibling.
func (c *RerankClient) routes() []string {
	if c.cfg.Backend == "llamacpp" {
		return []string{"/rerank", "/v1/rerank"}
	}
	return []string{"/v1/rerank", "/rerank"}
}

// RerankResults reranks hybrid search results with the cross-encoder and
// returns the top limit results in relevance order. The candidate text is
// title + summary + best chunk (chunk text is embedded and stored at index
// time). Any backend failure degrades gracefully: the input order is kept.
func RerankResults(results []SearchResult, query string, client *RerankClient, limit int) []SearchResult {
	if client == nil || len(results) == 0 || limit <= 0 {
		return results
	}
	docs := make([]string, len(results))
	for i, r := range results {
		var b strings.Builder
		b.WriteString(r.Title)
		if r.Summary != "" {
			b.WriteString("\n")
			b.WriteString(r.Summary)
		}
		if r.ChunkText != "" {
			b.WriteString("\n")
			b.WriteString(r.ChunkText)
		}
		docs[i] = b.String()
	}
	scores, err := client.Rerank(query, docs)
	if err != nil {
		// Graceful degradation — hybrid order stands, but the count
		// contract (limit) still holds: never hand back the untrimmed
		// candidate pool when the reranker is down.
		if len(results) > limit {
			results = results[:limit]
		}
		return results
	}
	ranked := make([]SearchResult, len(results))
	copy(ranked, results)
	for i := range ranked {
		ranked[i].Score = scores[i]
	}
	sort.SliceStable(ranked, func(a, b int) bool { return ranked[a].Score > ranked[b].Score })
	if len(ranked) > limit {
		ranked = ranked[:limit]
	}
	return ranked
}
