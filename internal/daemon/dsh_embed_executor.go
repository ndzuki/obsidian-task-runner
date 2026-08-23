package daemon

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// dshEmbedExecutor is the DSH-native embed adapter: it drives a long-lived
// `dsh --profile headless-agent-server` process over HTTP RPC (POST /agent/run)
// instead of spawning a short-lived headless process per phase. Two
// capabilities the spawn adapter cannot express are restored here:
//   - reasoningEffort is a native per-request field, restoring omp --thinking
//     per-phase semantics (priority=off, planning=high, round2=max).
//   - durable resume via the sessionId the agent-server returns (E4).
//
// It embeds dshExecutor only to reuse the SKILL.md body injection (dshTaskText);
// the spawn fields (dsh, defaultProfile) are unused by the embed path.
type dshEmbedExecutor struct {
	dshExecutor
	addr   string
	client *http.Client
}

// newDSHEmbedExecutor builds the embed adapter for a given agent-server address
// (host:port) and skill directory.
func newDSHEmbedExecutor(addr, skillDir string) *dshEmbedExecutor {
	return &dshEmbedExecutor{
		dshExecutor: dshExecutor{skillDir: skillDir},
		addr:        addr,
		client:      &http.Client{},
	}
}

func (e *dshEmbedExecutor) Name() string { return "dsh-embed" }

func (e *dshEmbedExecutor) Start(ctx context.Context, spec PhaseSpec, snap TaskSnapshot) (ExecutionHandle, error) {
	// 预生成 sessionId：daemon 侧持有它，中断时即使 /agent/run 响应未返回，
	// 也能用它持久化 executor_session_id 供 durable resume（否则 sessionId 由
	// agent-server 内部生成，中断瞬间拿不到，resume 退化为 fresh start）。
	return e.dispatch(ctx, spec, newSessionID())
}

// newSessionID 生成一个 agent-server 可接受的会话 id（daemon 侧预分配，
// 供 durable resume 持久化）。
func newSessionID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err == nil {
		return "session-" + hex.EncodeToString(b)
	}
	return fmt.Sprintf("session-%d", time.Now().UnixNano())
}

// embedResumeToken encodes everything Resume needs to re-attach a durable
// session: the agent-server sessionId plus the request fields (provider/model/
// skillPrompt/effort) required to rebuild the /agent/run payload. The daemon
// persists this JSON as the task's executor_session_id.
type embedResumeToken struct {
	SessionID       string `json:"sessionId"`
	Provider        string `json:"provider"`
	Model           string `json:"model"`
	SkillPrompt     string `json:"skillPrompt"`
	ReasoningEffort string `json:"reasoningEffort,omitempty"`
}

// Resume re-attaches to a durable session by decoding the resume token
// (JSON) and re-issuing /agent/run with the recorded sessionId. The agent-
// server resumes the session rather than starting a new one, so model
// context carries across a daemon restart.
//
// The CURRENT spec (phase/prompt/model/effort) drives the resumed run — the
// token only names the durable session. A daemon restart can dispatch a
// different phase than the one the token was recorded for (e.g. refining
// session interrupted, next scan routes to planning): re-injecting the
// token's stale prompt would run the wrong phase inside the resumed session.
// Token provider/model remain the fallback for legacy tokens without a spec.
func (e *dshEmbedExecutor) Resume(ctx context.Context, spec PhaseSpec, resumeToken string, timeout time.Duration) (ExecutionHandle, error) {
	if resumeToken == "" {
		return nil, fmt.Errorf("dsh-embed resume: empty resume token")
	}
	var tok embedResumeToken
	if err := json.Unmarshal([]byte(resumeToken), &tok); err != nil {
		return nil, fmt.Errorf("dsh-embed resume: invalid resume token: %w", err)
	}
	if tok.SessionID == "" || tok.Provider == "" || tok.Model == "" {
		return nil, fmt.Errorf("dsh-embed resume: token missing sessionId/provider/model")
	}
	provider, model := tok.Provider, tok.Model
	if spec.Model != "" {
		provider, model = mapDSHModel(spec.Model)
	}
	skillPrompt := spec.SkillPrompt
	if skillPrompt == "" {
		skillPrompt = tok.SkillPrompt // legacy token：退回 token 内 prompt
	}
	effort := mapDSHEffort(spec.ReasoningEffort)
	taskText := e.dshTaskText(skillPrompt)
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	return e.startRequest(ctx, spec.Phase, provider, model, taskText, effort, tok.SessionID, skillPrompt, timeout)
}

// Cancel aborts a wedged agent-server session (POST /agent/cancel): the model
// turn is cancelled and the session is disposed from the live registry, so
// the next resume finds it gone and falls back to a fresh start. Used by the
// daemon on phase timeout — without it, a hung turn (gateway stall, 6.8h
// observed on TASK-079 refining) would be re-attached to forever.
func (e *dshEmbedExecutor) Cancel(ctx context.Context, resumeToken string) error {
	if resumeToken == "" {
		return nil
	}
	var tok embedResumeToken
	if err := json.Unmarshal([]byte(resumeToken), &tok); err != nil || tok.SessionID == "" {
		return fmt.Errorf("dsh-embed cancel: invalid resume token")
	}
	body, err := json.Marshal(map[string]string{"sessionId": tok.SessionID})
	if err != nil {
		return err
	}
	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, "http://"+e.addr+"/agent/cancel", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("content-type", "application/json")
	resp, err := e.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("agent-server cancel HTTP %d", resp.StatusCode)
	}
	return nil
}

// agentRunRequest/agentRunResponse mirror the agent-server RPC contract
// (docs/embed-migration-plan.md §3).
type agentRunRequest struct {
	Task            string `json:"task"`
	Provider        string `json:"provider"`
	Model           string `json:"model"`
	ReasoningEffort string `json:"reasoningEffort,omitempty"`
	SessionID       string `json:"sessionId,omitempty"`
}

type agentRunResponse struct {
	Text      string `json:"text"`
	Outcome   string `json:"outcome"`
	SessionID string `json:"sessionId"`
	ErrorCode string `json:"errorCode,omitempty"`
}

func (e *dshEmbedExecutor) dispatch(ctx context.Context, spec PhaseSpec, sessionID string) (ExecutionHandle, error) {
	provider, model := mapDSHModel(spec.Model)
	taskText := e.dshTaskText(spec.SkillPrompt)
	effort := mapDSHEffort(spec.ReasoningEffort)
	timeout := spec.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	return e.startRequest(ctx, spec.Phase, provider, model, taskText, effort, sessionID, spec.SkillPrompt, timeout)
}

// startRequest builds and launches the /agent/run request. dispatch maps the
// OMP model/effort and injects the skill body; Resume re-uses it with the
// already-mapped provider/model and re-injected task text.
func (e *dshEmbedExecutor) startRequest(ctx context.Context, phase, provider, model, taskText, effort, sessionID, skillPrompt string, timeout time.Duration) (ExecutionHandle, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)

	h := &embedHandle{
		ctx:    ctx,
		cancel: cancel,
		client: e.client,
		addr:   e.addr,
		phase:  phase,
		req: agentRunRequest{
			Task:            taskText,
			Provider:        provider,
			Model:           model,
			ReasoningEffort: effort,
			SessionID:       sessionID,
		},
		// Fields recorded to build the resume token once the session id is
		// known (after the /agent/run response).
		provider:    provider,
		model:       model,
		effort:      effort,
		skillPrompt: skillPrompt,
		result:      make(chan *ExecutionResult, 1),
	}
	go h.run()
	return h, nil
}

// mapDSHModel translates a vault-map model identity into DSH's provider/model
// route form. Legacy identities are normalized to the free channels:
//   - "gateway/<model>"  → deepseek_magic (same gateway.internal.example baseURL)
//   - "deepseek/<model>" → deepseek_magic (free magic channel; the paid
//     official channel is only reachable via the explicit "ds-official" key)
//
// Identities already in DSH route form ("deepseek_magic/<model>",
// "openai/<model>", "ds-official/<model>") pass through unchanged.
func mapDSHModel(ompModel string) (provider, model string) {
	if ompModel == "" {
		return "deepseek_magic", "deepseek-v4-pro"
	}
	if idx := strings.IndexByte(ompModel, '/'); idx > 0 {
		p, m := ompModel[:idx], ompModel[idx+1:]
		switch p {
		case "gateway", "deepseek":
			return "deepseek_magic", m
		default:
			return p, m
		}
	}
	return "deepseek_magic", ompModel
}

// isFreeModelRoute reports whether a vault-map model identity resolves to a
// free channel (deepseek_magic or openai). It is used for the "free models
// exhausted — consider assignee=ds-official" reminder.
func isFreeModelRoute(ompModel string) bool {
	provider, _ := mapDSHModel(ompModel)
	return provider == "deepseek_magic" || provider == "openai"
}

// dshModelLabel renders a vault-map model identity in DSH route form
// ("provider/model"), for log headers and diagnostics.
func dshModelLabel(model string) string {
	provider, m := mapDSHModel(model)
	return provider + "/" + m
}

// mapDSHEffort maps omp --thinking to the DSH reasoningEffort the model profile
// declares. off and empty mean "no explicit effort" (model default) — the
// agent-server rejects "off" as UNSUPPORTED_REASONING_EFFORT because the profile
// declares only low/high/xhigh. max maps to xhigh (whose wire value is "max").
func mapDSHEffort(ompEffort string) string {
	switch ompEffort {
	case "low":
		return "low"
	case "medium":
		return "medium"
	case "high":
		return "high"
	case "max":
		return "xhigh"
	default:
		return ""
	}
}

// embedHandle is the live handle for one /agent/run dispatch.
type embedHandle struct {
	ctx    context.Context
	cancel context.CancelFunc
	client *http.Client
	addr   string
	phase  string
	req    agentRunRequest
	// provider/model/effort/skillPrompt are recorded at dispatch so the
	// resume token can be built once the session id arrives in the response.
	provider    string
	model       string
	effort      string
	skillPrompt string
	result      chan *ExecutionResult
}

func (h *embedHandle) PID() int { return 0 }

func (h *embedHandle) Wait() (*ExecutionResult, error) {
	defer h.cancel()
	select {
	case res := <-h.result:
		return res, nil
	case <-h.ctx.Done():
		if h.ctx.Err() == context.DeadlineExceeded {
			// 请求超时但会话可能仍在 agent-server 中运行：持久化 token，
			// 下一轮 scan resume 同一会话——fresh start 会开双会话并行写。
			return &ExecutionResult{Phase: h.phase, Code: OutcomeTimedOut, Error: "agent-server request timed out", ResumeToken: h.buildResumeToken(h.req.SessionID)}, nil
		}
		return &ExecutionResult{Phase: h.phase, Code: OutcomeInterrupted, Error: "agent-server request cancelled", ResumeToken: h.buildResumeToken(h.req.SessionID)}, nil
	}
}

func (h *embedHandle) run() { h.result <- h.doRequest() }

func (h *embedHandle) doRequest() *ExecutionResult {
	data, err := json.Marshal(h.req)
	if err != nil {
		return &ExecutionResult{Phase: h.phase, Code: OutcomeFailed, Error: err.Error()}
	}
	httpReq, err := http.NewRequestWithContext(h.ctx, http.MethodPost, "http://"+h.addr+"/agent/run", bytes.NewReader(data))
	if err != nil {
		return &ExecutionResult{Phase: h.phase, Code: OutcomeFailed, Error: err.Error()}
	}
	httpReq.Header.Set("content-type", "application/json")

	resp, err := h.client.Do(httpReq)
	if err != nil {
		switch h.ctx.Err() {
		case context.DeadlineExceeded:
			// 同 Wait() 超时分支：会话可能仍在 agent-server 中运行。
			return &ExecutionResult{Phase: h.phase, Code: OutcomeTimedOut, Error: err.Error(), ResumeToken: h.buildResumeToken(h.req.SessionID)}
		case context.Canceled:
			return &ExecutionResult{Phase: h.phase, Code: OutcomeInterrupted, Error: err.Error(), ResumeToken: h.buildResumeToken(h.req.SessionID)}
		default:
			// 连接失败：会话在 agent-server 里可能已建（请求发出后进程死亡），
			// 保留 token 让下一轮 scan resume（持久层找不到则 agent-server
			// 宽容创建），避免丢上下文 fresh start。
			return &ExecutionResult{Phase: h.phase, Code: OutcomeFailed, Error: "agent-server unreachable: " + err.Error(), ResumeToken: h.buildResumeToken(h.req.SessionID)}
		}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return &ExecutionResult{Phase: h.phase, Code: OutcomeFailed, Error: err.Error()}
	}
	if resp.StatusCode != http.StatusOK {
		return &ExecutionResult{
			Phase: h.phase,
			Code:  OutcomeFailed,
			Error: fmt.Sprintf("agent-server HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body))),
		}
	}
	var parsed agentRunResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return &ExecutionResult{Phase: h.phase, Code: OutcomeFailed, Error: "agent-server bad response: " + err.Error()}
	}
	res := &ExecutionResult{
		Phase:       h.phase,
		Code:        mapEmbedOutcome(parsed.Outcome, parsed.ErrorCode),
		Error:       parsed.ErrorCode,
		Stdout:      parsed.Text,
		ResumeToken: h.buildResumeToken(parsed.SessionID),
	}
	if res.Code != OutcomeSuccess && res.Error == "" {
		res.Error = "agent-server outcome " + parsed.Outcome
	}
	return res
}

// buildResumeToken encodes the durable session id plus the request fields
// needed to re-attach it. Returns "" when the session id is empty (the
// agent-server did not open a durable session).
func (h *embedHandle) buildResumeToken(sessionID string) string {
	if sessionID == "" {
		return ""
	}
	tok := embedResumeToken{
		SessionID:       sessionID,
		Provider:        h.provider,
		Model:           h.model,
		SkillPrompt:     h.skillPrompt,
		ReasoningEffort: h.effort,
	}
	if b, err := json.Marshal(tok); err == nil {
		return string(b)
	}
	return sessionID
}

// mapEmbedOutcome maps the agent-server outcome string to the closed ExecOutcome
// set (docs/embed-migration-plan.md §3).
func mapEmbedOutcome(outcome, errorCode string) ExecOutcome {
	switch outcome {
	case "completed":
		return OutcomeSuccess
	case "timeout":
		return OutcomeTimedOut
	case "quota":
		return OutcomeQuotaExhausted
	case "key_unavailable":
		return OutcomeKeyUnavailable
	case "interrupted":
		return OutcomeInterrupted
	case "error":
		if errorCode == "EMPTY_RESPONSE" {
			return OutcomeEmptyResponse
		}
		return OutcomeFailed
	default:
		return OutcomeFailed
	}
}

// dshEmbedExecutor must satisfy PhaseExecutor.
var _ PhaseExecutor = (*dshEmbedExecutor)(nil)
