package daemon

import (
	"bytes"
	"context"
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
	return e.dispatch(ctx, spec, "")
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
func (e *dshEmbedExecutor) Resume(ctx context.Context, resumeToken string) (ExecutionHandle, error) {
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
	// provider/model are already in DSH route form (recorded at dispatch);
	// skillPrompt is the original slash prompt, re-injected here.
	taskText := e.dshTaskText(tok.SkillPrompt)
	return e.startRequest(ctx, "resume", tok.Provider, tok.Model, taskText, tok.ReasoningEffort, tok.SessionID, tok.SkillPrompt, 30*time.Minute)
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

// mapDSHModel translates the OMP model identity into DSH's provider/model route
// form: "gateway/<model>" → deepseek_magic (same gateway.magicrew.io baseURL),
// "deepseek/<model>" → ds-official (official DeepSeek). Identities already in
// DSH form ("deepseek_magic/<model>", "ds-official/<model>") pass through.
func mapDSHModel(ompModel string) (provider, model string) {
	if ompModel == "" {
		return "deepseek_magic", "deepseek-v4-pro"
	}
	if idx := strings.IndexByte(ompModel, '/'); idx > 0 {
		p, m := ompModel[:idx], ompModel[idx+1:]
		switch p {
		case "gateway":
			return "deepseek_magic", m
		case "deepseek":
			return "ds-official", m
		default:
			return p, m
		}
	}
	return "deepseek_magic", ompModel
}

// mapDSHEffort maps omp --thinking to the DSH reasoningEffort the model profile
// declares. off and empty mean "no explicit effort" (model default) — the
// agent-server rejects "off" as UNSUPPORTED_REASONING_EFFORT because the profile
// declares only low/high/xhigh. max maps to xhigh (whose wire value is "max").
func mapDSHEffort(ompEffort string) string {
	switch ompEffort {
	case "low":
		return "low"
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
			return &ExecutionResult{Phase: h.phase, Code: OutcomeTimedOut, Error: "agent-server request timed out"}, nil
		}
		return &ExecutionResult{Phase: h.phase, Code: OutcomeInterrupted, Error: "agent-server request cancelled"}, nil
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
			return &ExecutionResult{Phase: h.phase, Code: OutcomeTimedOut, Error: err.Error()}
		case context.Canceled:
			return &ExecutionResult{Phase: h.phase, Code: OutcomeInterrupted, Error: err.Error()}
		default:
			return &ExecutionResult{Phase: h.phase, Code: OutcomeFailed, Error: "agent-server unreachable: " + err.Error()}
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
