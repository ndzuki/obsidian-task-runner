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

// Resume re-attaches to a durable session by its sessionId (E4). Until the
// frontmatter executor_session_id plumbing lands, resume is unsupported and the
// daemon falls back to frontmatter re-dispatch (same as spawn today).
func (e *dshEmbedExecutor) Resume(context.Context, string) (ExecutionHandle, error) {
	return nil, ErrResumeUnsupported
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

	timeout := spec.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)

	h := &embedHandle{
		ctx:    ctx,
		cancel: cancel,
		client: e.client,
		addr:   e.addr,
		phase:  spec.Phase,
		req: agentRunRequest{
			Task:            taskText,
			Provider:        provider,
			Model:           model,
			ReasoningEffort: mapDSHEffort(spec.ReasoningEffort),
			SessionID:       sessionID,
		},
		result: make(chan *ExecutionResult, 1),
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
	result chan *ExecutionResult
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
		ResumeToken: parsed.SessionID,
	}
	if res.Code != OutcomeSuccess && res.Error == "" {
		res.Error = "agent-server outcome " + parsed.Outcome
	}
	return res
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
