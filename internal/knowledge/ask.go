package knowledge

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ndzuki/obsidian-task-runner/internal/config"
)

// ChatClient talks to an ollama or OpenAI-compatible chat backend — the
// generation stage of retrieval-augmented knowledge search (`otg kb ask`).
type ChatClient struct {
	cfg    *config.KBChatConfig
	client *http.Client
}

// NewChatClient wraps the configured chat backend. A nil cfg returns nil.
func NewChatClient(cfg *config.KBChatConfig) *ChatClient {
	if cfg == nil {
		return nil
	}
	return &ChatClient{
		cfg:    cfg,
		client: &http.Client{Timeout: 5 * time.Minute},
	}
}

// Complete streams a chat completion; fragment is called for each text
// chunk as it arrives (may be nil to buffer). Returns the full answer.
// model overrides the configured model ("" = configured).
func (c *ChatClient) Complete(system, user, model string, fragment func(string) error) (string, error) {
	if c == nil {
		return "", fmt.Errorf("chat client not configured")
	}
	if model == "" {
		model = c.cfg.Model
	}
	switch c.cfg.Backend {
	case "", "ollama":
		return c.completeOllama(system, user, model, fragment)
	case "openai":
		return c.completeOpenAI(system, user, model, fragment)
	default:
		return "", fmt.Errorf("unknown chat backend %q", c.cfg.Backend)
	}
}

// completeOllama streams POST {base}/api/chat — newline-delimited JSON, one
// {"message":{"content":...}} per line while stream:true.
func (c *ChatClient) completeOllama(system, user, model string, fragment func(string) error) (string, error) {
	body, err := json.Marshal(map[string]any{
		"model":    model,
		"stream":   true,
		"options":  map[string]any{"temperature": c.cfg.Temperature},
		"messages": []map[string]string{{"role": "system", "content": system}, {"role": "user", "content": user}},
	})
	if err != nil {
		return "", err
	}
	resp, err := c.client.Post(c.cfg.URL+"/api/chat", "application/json", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("ollama chat: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("ollama chat: %s: %s", resp.Status, string(data))
	}
	var answer strings.Builder
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var line struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			Done bool `json:"done"`
		}
		if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
			return "", fmt.Errorf("ollama chat stream decode: %w", err)
		}
		if line.Message.Content == "" {
			if line.Done {
				break
			}
			continue
		}
		answer.WriteString(line.Message.Content)
		if fragment != nil {
			if err := fragment(line.Message.Content); err != nil {
				return "", err
			}
		}
	}
	if err := sc.Err(); err != nil {
		return "", fmt.Errorf("ollama chat stream: %w", err)
	}
	return answer.String(), nil
}

// completeOpenAI streams POST {base}/chat/completions — SSE events, each
// data: line carrying choices[0].delta.content.
func (c *ChatClient) completeOpenAI(system, user, model string, fragment func(string) error) (string, error) {
	body, err := json.Marshal(map[string]any{
		"model":       model,
		"stream":      true,
		"temperature": c.cfg.Temperature,
		"messages":    []map[string]string{{"role": "system", "content": system}, {"role": "user", "content": user}},
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequest(http.MethodPost, c.cfg.URL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("openai chat: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("openai chat: %s: %s", resp.Status, string(data))
	}
	var answer strings.Builder
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var ev struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			return "", fmt.Errorf("openai chat stream decode: %w", err)
		}
		for _, ch := range ev.Choices {
			if ch.Delta.Content == "" {
				continue
			}
			answer.WriteString(ch.Delta.Content)
			if fragment != nil {
				if err := fragment(ch.Delta.Content); err != nil {
					return "", err
				}
			}
		}
	}
	if err := sc.Err(); err != nil {
		return "", fmt.Errorf("openai chat stream: %w", err)
	}
	return answer.String(), nil
}

// askSystemPrompt grounds the model in the retrieval results: answers must
// cite [N] markers and must not invent sources the retrieval did not find.
const askSystemPrompt = `你是 Obsidian 知识库的问答助手。仅依据下方「参考资料」回答用户问题，参考资料来自 References/ 目录的本地知识文档。

规则：
1. 参考资料以 [N] 编号标注来源；回答中需要引用时用 [N] 标注。
2. 若参考资料不足以回答问题，直接说明「知识库中没有找到相关信息」，不要编造或推测。
3. 用中文回答，要点清晰、简洁。`

// AskOptions carries knobs for one kb ask invocation.
type AskOptions struct {
	// Limit is the number of retrieved references (5 default).
	Limit int
	// Model overrides the configured chat model ("" = configured).
	Model string
	// Stream receives answer fragments as they arrive (may be nil).
	Stream func(string) error
	// MaxRefChars caps each reference's body in the prompt (1500 default).
	MaxRefChars int
	// Rerank optionally reranks the hybrid candidates with a cross-encoder
	// (nil = skip). The retrieval fetches RerankTopN candidates (bounded by
	// the corpus) and trims back to Limit after reranking — otherwise the
	// rerank stage would have nothing to reorder.
	Rerank *RerankClient
	// RerankTopN is the candidate count fed to the reranker (20 default;
	// ignored when Rerank is nil).
	RerankTopN int
}

// AskKnowledgeDB runs retrieval-augmented generation: hybrid top-k → cited
// reference block → streamed chat completion. The returned reference list
// is deterministic — the model never invents sources, the CLI prints the
// actual retrieved paths alongside the answer.
func AskKnowledgeDB(dbPath, query string, opts AskOptions, embed *EmbeddingClient, chat *ChatClient) ([]SearchResult, error) {
	if embed == nil || chat == nil {
		return nil, fmt.Errorf("kb ask requires both kb_embedding and kb_chat configured")
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 5
	}
	weight := embed.cfg.Weight
	if weight <= 0 {
		weight = 0.5
	}
	// Fetch enough candidates for the rerank stage (when configured): the
	// reranker reorders RerankTopN and the result is trimmed back to limit.
	fetchLimit := limit
	if opts.Rerank != nil {
		topN := opts.RerankTopN
		if topN <= 0 {
			topN = 20
		}
		if topN > fetchLimit {
			fetchLimit = topN
		}
	}
	refs, err := SearchKnowledgeDB(dbPath, query, fetchLimit, true, embed, weight)
	if err != nil {
		return nil, err
	}
	if len(refs) == 0 {
		return nil, nil
	}
	if opts.Rerank != nil {
		refs = RerankResults(refs, query, opts.Rerank, limit)
		if len(refs) == 0 {
			return nil, nil
		}
	}

	maxChars := opts.MaxRefChars
	if maxChars <= 0 {
		maxChars = 1500
	}
	var b strings.Builder
	b.WriteString("参考资料：\n")
	for i, r := range refs {
		fmt.Fprintf(&b, "[%d] %s（%s）\n", i+1, r.Path, r.Title)
		if r.Summary != "" {
			b.WriteString(r.Summary)
			b.WriteString("\n")
		}
		if r.ChunkText != "" {
			body := r.ChunkText
			if len(body) > maxChars {
				body = body[:maxChars]
			}
			b.WriteString(body)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	b.WriteString("问题：")
	b.WriteString(query)

	_, err = chat.Complete(askSystemPrompt, b.String(), opts.Model, opts.Stream)
	if err != nil {
		return nil, err
	}
	return refs, nil
}
