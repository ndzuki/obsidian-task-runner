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

	"github.com/ndzuki/obsidian-task-runner/internal/config"
)

// dshEmbedExecutor is the DSH-native embed adapter: it drives a long-lived
// `dsh --profile headless-agent-server` process over HTTP RPC (POST /agent/run)
// instead of spawning a short-lived headless process per phase. Two
// capabilities the spawn adapter cannot express are restored here:
//   - reasoningEffort is a native per-request field, restoring per-phase
//     reasoning-level semantics (priority=off, planning=high, round2=max).
//   - durable resume via the sessionId the agent-server returns (E4).
//
// It embeds dshExecutor only to reuse the SKILL.md body injection (dshTaskText);
// the spawn fields (dsh, defaultProfile) are unused by the embed path.
type dshEmbedExecutor struct {
	dshExecutor
	addr     string
	client   *http.Client
	fallback *config.FallbackConfig
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
	return e.dispatch(ctx, spec, snap.TaskID, newSessionID())
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
	// TaskID is the task this session was dispatched for. The agent-server
	// labels the session with it (taskId), and the daemon cancels still-working
	// sessions with the same taskId before a fresh Start — restarts must never
	// accumulate concurrent writers on one worktree. Legacy tokens lack it and
	// re-attach without re-labelling.
	TaskID string `json:"taskId,omitempty"`
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
	return e.startRequest(ctx, spec.Phase, spec.TaskStatus, tok.TaskID, provider, model, taskText, effort, tok.SessionID, skillPrompt, spec.ToolPolicy, timeout)
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
	return e.cancelSession(ctx, tok.SessionID)
}

// agentListEntry mirrors the GET /agents element the agent-server publishes
// (deploy/dsh-plugins/agent-server.mjs): the taskId label is the authoritative
// task identity; task is the display label derived from the prompt and only
// used as a fallback for sessions created before the taskId field existed.
type agentListEntry struct {
	SessionID string `json:"sessionId"`
	TaskID    string `json:"taskId"`
	Task      string `json:"task"`
	Status    string `json:"status"`
}

// CancelStaleTaskSessions cancels every agent-server session that is still
// working for the given task. It is the daemon's restart reconcile step: a
// previous daemon incarnation left its session running inside the (possibly
// externally managed) agent-server, and any fresh Start for the same task
// would otherwise add a second concurrent writer to the same worktree
// (observed: 3 parallel CI-fix/audit sessions after repeated daemon restarts).
// Only working sessions are cancelled — idle sessions are finished runs the
// durable-resume token may still re-attach, and cancelling them would burn
// resumable context. An enumeration failure is returned so the caller aborts
// the fresh Start instead of risking a parallel writer.
func (e *dshEmbedExecutor) CancelStaleTaskSessions(ctx context.Context, taskID string) error {
	if taskID == "" {
		return nil
	}
	agents, err := e.listAgents(ctx)
	if err != nil {
		return err
	}
	for _, a := range agents {
		if a.Status != "working" {
			continue // idle = finished run kept for durable resume — not a writer
		}
		if a.TaskID != taskID && a.Task != taskID {
			continue
		}
		if err := e.cancelSession(ctx, a.SessionID); err != nil {
			return fmt.Errorf("cancel stale session %s: %w", a.SessionID, err)
		}
	}
	return nil
}

// listAgents fetches the agent-server's live session registry (GET /agents).
func (e *dshEmbedExecutor) listAgents(ctx context.Context) ([]agentListEntry, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+e.addr+"/agents", nil)
	if err != nil {
		return nil, err
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("agent-server /agents HTTP %d", resp.StatusCode)
	}
	var agents []agentListEntry
	if err := json.Unmarshal(body, &agents); err != nil {
		return nil, fmt.Errorf("agent-server /agents bad response: %w", err)
	}
	return agents, nil
}

// cancelSession disposes one session on the agent-server (POST /agent/cancel).
func (e *dshEmbedExecutor) cancelSession(ctx context.Context, sessionID string) error {
	body, err := json.Marshal(map[string]string{"sessionId": sessionID})
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
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("agent-server cancel HTTP %d", resp.StatusCode)
	}
	return nil
}

// agentRunRequest/agentRunResponse mirror the agent-server RPC contract
// (docs/archive/embed-migration-plan.md §3).
type agentRunRequest struct {
	Task            string `json:"task"`
	Provider        string `json:"provider"`
	Model           string `json:"model"`
	ReasoningEffort string `json:"reasoningEffort,omitempty"`
	SessionID       string `json:"sessionId,omitempty"`
	// Status is the task's frontmatter status at dispatch time
	// (refining/planning/plan-review/implementing/review/...). The
	// agent-server relays it to the monitor so the NPC animation follows
	// the real task state; it is ignored by the model runtime.
	Status string `json:"status,omitempty"`
	// TaskID labels the session with the precise task identity. The
	// agent-server reports it via GET /agents so a restarted daemon can
	// cancel still-working sessions of the same task before a fresh Start.
	TaskID string `json:"taskId,omitempty"`
	// ToolPolicy restricts the session's tool surface (auditToolPolicy /
	// conventionsToolPolicy for the read-only review sessions). The
	// agent-server injects it as a hard session constraint and fails the run
	// when a disallowed tool call is observed.
	ToolPolicy string `json:"toolPolicy,omitempty"`
	// Fallback, when set, carries the vault-map fallback chains and
	// fallbackOnCodes for this session. The agent-server attaches it to the
	// agent so the fallback.mjs plugin uses daemon-controlled fallback
	// routing instead of the static cordis.patch.yml config.
	Fallback *config.FallbackConfig `json:"fallback,omitempty"`
}

type agentRunResponse struct {
	Text      string `json:"text"`
	Outcome   string `json:"outcome"`
	SessionID string `json:"sessionId"`
	ErrorCode string `json:"errorCode,omitempty"`
	Error     string `json:"error,omitempty"` // 失败详情消息（agent-server 8.24 起下发）
}

func (e *dshEmbedExecutor) dispatch(ctx context.Context, spec PhaseSpec, taskID, sessionID string) (ExecutionHandle, error) {
	provider, model := mapDSHModel(spec.Model)
	taskText := e.dshTaskText(spec.SkillPrompt)
	effort := mapDSHEffort(spec.ReasoningEffort)
	timeout := spec.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	return e.startRequest(ctx, spec.Phase, spec.TaskStatus, taskID, provider, model, taskText, effort, sessionID, spec.SkillPrompt, spec.ToolPolicy, timeout)
}

// startRequest builds and launches the /agent/run request. dispatch maps the
// model/effort and injects the skill body; Resume re-uses it with the
// already-mapped provider/model and re-injected task text.
func (e *dshEmbedExecutor) startRequest(ctx context.Context, phase, status, taskID, provider, model, taskText, effort, sessionID, skillPrompt, toolPolicy string, timeout time.Duration) (ExecutionHandle, error) {
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
			Status:          status,
			TaskID:          taskID,
			ToolPolicy:      toolPolicy,
			Fallback:        e.fallback,
		},
		// Fields recorded to build the resume token once the session id is
		// known (after the /agent/run response).
		provider:    provider,
		model:       model,
		effort:      effort,
		taskID:      taskID,
		skillPrompt: skillPrompt,
		idleWindow:  timeout,
		result:      make(chan *ExecutionResult, 1),
	}
	go h.run()
	return h, nil
}

// mapDSHModel splits a vault-map model identity into DSH provider/model
// route form. Empty input means "not configured" (no built-in routes).
func mapDSHModel(identity string) (provider, model string) {
	if identity == "" {
		return "", ""
	}
	if idx := strings.IndexByte(identity, '/'); idx > 0 {
		return identity[:idx], identity[idx+1:]
	}
	return identity, ""
}

// dshModelLabel renders a vault-map model identity in DSH route form
// ("provider/model"), for log headers and diagnostics.
func dshModelLabel(model string) string {
	provider, m := mapDSHModel(model)
	return provider + "/" + m
}

// mapDSHEffort maps the former --thinking levels to the DSH reasoningEffort
// the model profile declares. off and empty mean "no explicit effort" (model default) — the
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
	// idleWindow is the phase timeout this dispatch waited. When the HTTP
	// wait times out, the handle distinguishes a WEDGED session (no recent
	// event for the whole window — a hung model turn) from an ACTIVE one
	// (events within the window — a legitimately long turn like a real
	// smoke Round 2). Only wedged sessions get cancelled.
	idleWindow time.Duration
	// provider/model/effort/skillPrompt are recorded at dispatch so the
	// resume token can be built once the session id arrives in the response.
	provider    string
	model       string
	effort      string
	taskID      string
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
			// 有近期活动的会话判 timeout_active（下一轮继续等），无活动的
			// 才是 wedged（卡死的模型 turn），交由 caller cancel。
			return h.timeoutResult("agent-server request timed out"), nil
		}
		return &ExecutionResult{Phase: h.phase, Code: OutcomeInterrupted, Error: "agent-server request cancelled", ResumeToken: h.buildResumeToken(h.req.SessionID)}, nil
	}
}

// timeoutResult classifies an expired HTTP wait: OutcomeTimedOutActive when
// the agent-server session produced events within the idle window (turn is
// alive — do NOT cancel), OutcomeTimedOut otherwise (wedged turn — cancel).
func (h *embedHandle) timeoutResult(reason string) *ExecutionResult {
	code := OutcomeTimedOut
	if h.sessionActive() {
		code = OutcomeTimedOutActive
	}
	return &ExecutionResult{Phase: h.phase, Code: code, Error: reason, ResumeToken: h.buildResumeToken(h.req.SessionID)}
}

// sessionActive probes GET /agents (fresh short-lived context — the dispatch
// ctx is expired) and reports whether this handle's session had an event
// within the idle window. Servers without the lastEventAt field (or probe
// failures) report inactive so legacy wedged-turn handling is preserved.
func (h *embedHandle) sessionActive() bool {
	if h.addr == "" || h.req.SessionID == "" {
		return false
	}
	probeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, "http://"+h.addr+"/agents", nil)
	if err != nil {
		return false
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil || resp.StatusCode != http.StatusOK {
		return false
	}
	var agents []struct {
		SessionID   string `json:"sessionId"`
		LastEventAt int64  `json:"lastEventAt"`
	}
	if err := json.Unmarshal(body, &agents); err != nil {
		return false
	}
	window := h.idleWindow
	if window <= 0 {
		window = 30 * time.Minute
	}
	for _, a := range agents {
		if a.SessionID != h.req.SessionID {
			continue
		}
		if a.LastEventAt <= 0 {
			return false // older agent-server without activity reporting
		}
		return time.Since(time.UnixMilli(a.LastEventAt)) < window
	}
	return false
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
			// 同 Wait() 超时分支：会话可能仍在 agent-server 中运行；按
			// 活动度区分 timeout_active（继续等）与 wedged（cancel）。
			return h.timeoutResult(err.Error())
		case context.Canceled:
			return &ExecutionResult{Phase: h.phase, Code: OutcomeInterrupted, Error: err.Error(), ResumeToken: h.buildResumeToken(h.req.SessionID)}
		default:
			// 连接失败：会话在 agent-server 里可能已建（请求发出后进程死亡），
			// 保留 token 让下一轮 scan resume（持久层找不到则 agent-server
			// 宽容创建），避免丢上下文 fresh start。
			return &ExecutionResult{Phase: h.phase, Code: OutcomeFailed, Error: "agent-server unreachable: " + err.Error(), ResumeToken: h.buildResumeToken(h.req.SessionID)}
		}
	}
	defer func() { _ = resp.Body.Close() }()
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
	// 失败详情：errorCode（分类码）+ error（消息）都带上——只留分类码时
	// 「agent-server outcome error」无任何可诊断信息（TASK-058 写回观测）。
	resErr := strings.TrimSpace(parsed.ErrorCode)
	if msg := strings.TrimSpace(parsed.Error); msg != "" {
		if resErr == "" {
			resErr = msg
		} else if !strings.Contains(resErr, msg) {
			resErr += ": " + msg
		}
	}
	res := &ExecutionResult{
		Phase:       h.phase,
		Code:        mapEmbedOutcome(parsed.Outcome, parsed.ErrorCode),
		Error:       resErr,
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
		TaskID:          h.taskID,
	}
	if b, err := json.Marshal(tok); err == nil {
		return string(b)
	}
	return sessionID
}

// mapEmbedOutcome maps the agent-server outcome string to the closed ExecOutcome
// set (docs/archive/embed-migration-plan.md §3).
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
