package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ndzuki/obsidian-task-runner/internal/config"
)

func TestMapDSHModel(t *testing.T) {
	cases := []struct {
		in                string
		wantProv, wantMod string
	}{
		{"", "deepseek_magic", "deepseek-v4-pro"},
		{"gateway/deepseek-v4-pro", "deepseek_magic", "deepseek-v4-pro"},
		{"gateway/gpt-5.4-mini", "deepseek_magic", "gpt-5.4-mini"},
		{"deepseek/deepseek-v4-flash", "deepseek_magic", "deepseek-v4-flash"},
		{"deepseek_magic/deepseek-v4-pro", "deepseek_magic", "deepseek-v4-pro"}, // 已 DSH 形式直传
		{"openai/gpt-5.6-sol", "openai", "gpt-5.6-sol"},
		{"ds-official/deepseek-v4-flash", "ds-official", "deepseek-v4-flash"},
	}
	for _, c := range cases {
		prov, mod := mapDSHModel(c.in)
		if prov != c.wantProv || mod != c.wantMod {
			t.Errorf("mapDSHModel(%q) = (%q, %q), want (%q, %q)", c.in, prov, mod, c.wantProv, c.wantMod)
		}
	}
}

func TestIsFreeModelRoute(t *testing.T) {
	free := []string{
		"deepseek_magic/deepseek-v4-pro",
		"deepseek_magic/gpt-5.4-mini",
		"openai/gpt-5.6-sol",
		"gateway/deepseek-v4-pro",
		"deepseek/deepseek-v4-flash",
	}
	for _, model := range free {
		if !isFreeModelRoute(model) {
			t.Errorf("isFreeModelRoute(%q) = false, want true", model)
		}
	}
	paid := []string{
		"ds-official/deepseek-v4-pro",
		"google/gemini-2.5-pro",
		"anthropic/claude-sonnet-4-20250514",
	}
	for _, model := range paid {
		if isFreeModelRoute(model) {
			t.Errorf("isFreeModelRoute(%q) = true, want false", model)
		}
	}
}

func TestMapDSHEffort(t *testing.T) {
	cases := map[string]string{
		"":      "",
		"off":   "",
		"low":   "low",
		"high":  "high",
		"max":   "xhigh",
		"bogus": "",
	}
	for in, want := range cases {
		if got := mapDSHEffort(in); got != want {
			t.Errorf("mapDSHEffort(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMapEmbedOutcome(t *testing.T) {
	cases := []struct {
		outcome, code string
		want          ExecOutcome
	}{
		{"completed", "", OutcomeSuccess},
		{"timeout", "", OutcomeTimedOut},
		{"quota", "", OutcomeQuotaExhausted},
		{"key_unavailable", "", OutcomeKeyUnavailable},
		{"interrupted", "", OutcomeInterrupted},
		{"error", "EMPTY_RESPONSE", OutcomeEmptyResponse},
		{"error", "SOME_OTHER", OutcomeFailed},
		{"bogus", "", OutcomeFailed},
	}
	for _, c := range cases {
		if got := mapEmbedOutcome(c.outcome, c.code); got != c.want {
			t.Errorf("mapEmbedOutcome(%q, %q) = %q, want %q", c.outcome, c.code, got, c.want)
		}
	}
}

// TestDSHEmbedExecutorRun 用 httptest stub server 联调 Start → Wait，断言请求
// 契约（task/provider/model/reasoningEffort）与响应解析（text/outcome/sessionId
// → ExecutionResult 各字段）。
func TestDSHEmbedExecutorRun(t *testing.T) {
	var gotReq agentRunRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/agent/run" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotReq); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(agentRunResponse{
			Text:      "plan written",
			Outcome:   "completed",
			SessionID: "session-abc",
		})
	}))
	defer srv.Close()

	e := newDSHEmbedExecutor(strings.TrimPrefix(srv.URL, "http://"), t.TempDir())
	handle, err := e.Start(context.Background(), PhaseSpec{
		Phase:           "planning",
		Model:           "gateway/deepseek-v4-pro",
		ReasoningEffort: "high",
		SkillPrompt:     "/obsidian-task-runner-round1 /vault/TASK.md",
		TaskStatus:      "planning",
		Timeout:         30 * time.Second,
	}, TaskSnapshot{TaskID: "TASK-001"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	res, err := handle.Wait()
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if res.Code != OutcomeSuccess {
		t.Fatalf("code = %q, want success (error=%q)", res.Code, res.Error)
	}
	if res.Stdout != "plan written" {
		t.Errorf("stdout = %q, want plan written", res.Stdout)
	}
	var tok embedResumeToken
	if err := json.Unmarshal([]byte(res.ResumeToken), &tok); err != nil || tok.SessionID != "session-abc" {
		t.Errorf("resumeToken = %q, want JSON with sessionId=session-abc (err=%v)", res.ResumeToken, err)
	}
	if gotReq.Provider != "deepseek_magic" || gotReq.Model != "deepseek-v4-pro" {
		t.Errorf("request provider/model = %q/%q, want deepseek_magic/deepseek-v4-pro", gotReq.Provider, gotReq.Model)
	}
	if gotReq.ReasoningEffort != "high" {
		t.Errorf("reasoningEffort = %q, want high", gotReq.ReasoningEffort)
	}
	if gotReq.Task == "" {
		t.Error("task must be non-empty (skill body injected)")
	}
	if gotReq.SessionID == "" || !strings.HasPrefix(gotReq.SessionID, "session-") {
		t.Errorf("sessionId = %q, want daemon-preallocated session- prefix (durable resume)", gotReq.SessionID)
	}
	if gotReq.Status != "planning" {
		t.Errorf("status = %q, want task status forwarded for agent-monitor", gotReq.Status)
	}
	if gotReq.TaskID != "TASK-001" {
		t.Errorf("taskId = %q, want TASK-001 forwarded (agent-server labels the session for restart reconcile)", gotReq.TaskID)
	}
	if res == nil || res.ResumeToken == "" {
		t.Fatal("resume token must be built")
	}
	var rtok embedResumeToken
	if err := json.Unmarshal([]byte(res.ResumeToken), &rtok); err != nil {
		t.Fatalf("resume token JSON: %v", err)
	}
	if rtok.TaskID != "TASK-001" {
		t.Errorf("resume token taskId = %q, want TASK-001 (Resume re-labels the session)", rtok.TaskID)
	}
}

// TestDSHEmbedExecutorRunFallback 断言 vault-map fallback 配置随 /agent/run
// 请求下发（daemon 控制模型 fallback 的通道）。
func TestDSHEmbedExecutorRunFallback(t *testing.T) {
	var gotReq agentRunRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotReq); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(agentRunResponse{Text: "ok", Outcome: "completed", SessionID: "s"})
	}))
	defer srv.Close()

	e := newDSHEmbedExecutor(strings.TrimPrefix(srv.URL, "http://"), t.TempDir())
	e.fallback = &config.FallbackConfig{
		Chains: []config.FallbackChain{{
			From: config.ModelRef{Provider: "deepseek_magic", Model: "deepseek-v4-pro"},
			To: []config.ModelRef{
				{Provider: "deepseek_magic", Model: "gpt-5.4-mini"},
				{Provider: "openai", Model: "gpt-5.6-sol"},
			},
		}},
		FallbackOnCodes: []string{"SERVER", "QUOTA"},
	}
	handle, err := e.Start(context.Background(), PhaseSpec{
		Phase:       "planning",
		Model:       "gateway/deepseek-v4-pro",
		SkillPrompt: "/obsidian-task-runner-round1 /vault/TASK.md",
		Timeout:     30 * time.Second,
	}, TaskSnapshot{TaskID: "TASK-001"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := handle.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if gotReq.Fallback == nil {
		t.Fatal("request fallback must be non-nil (vault-map fallback forwarded)")
	}
	if len(gotReq.Fallback.Chains) != 1 {
		t.Fatalf("fallback chains = %d, want 1", len(gotReq.Fallback.Chains))
	}
	chain := gotReq.Fallback.Chains[0]
	if chain.From.Provider != "deepseek_magic" || chain.From.Model != "deepseek-v4-pro" {
		t.Errorf("chain from = %q/%q, want deepseek_magic/deepseek-v4-pro", chain.From.Provider, chain.From.Model)
	}
	if len(chain.To) != 2 || chain.To[1].Provider != "openai" || chain.To[1].Model != "gpt-5.6-sol" {
		t.Errorf("chain to = %+v, want [deepseek_magic/gpt-5.4-mini openai/gpt-5.6-sol]", chain.To)
	}
	if len(gotReq.Fallback.FallbackOnCodes) != 2 {
		t.Errorf("fallbackOnCodes = %v, want [SERVER QUOTA]", gotReq.Fallback.FallbackOnCodes)
	}
}

// TestDSHEmbedExecutorOffSkipsEffort 断言 off 不传 reasoningEffort（模型默认）。
func TestDSHEmbedExecutorOffSkipsEffort(t *testing.T) {
	var gotReq agentRunRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotReq)
		_ = json.NewEncoder(w).Encode(agentRunResponse{Text: "ok", Outcome: "completed", SessionID: "s"})
	}))
	defer srv.Close()

	e := newDSHEmbedExecutor(strings.TrimPrefix(srv.URL, "http://"), t.TempDir())
	handle, err := e.Start(context.Background(), PhaseSpec{
		Phase:           "priority",
		Model:           "deepseek/deepseek-v4-flash",
		ReasoningEffort: "off",
		SkillPrompt:     "/obsidian-task-runner-priority /vault/REQ.md",
		Timeout:         30 * time.Second,
	}, TaskSnapshot{})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := handle.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if gotReq.ReasoningEffort != "" {
		t.Errorf("reasoningEffort = %q, want empty (off skips)", gotReq.ReasoningEffort)
	}
	if gotReq.Provider != "deepseek_magic" || gotReq.Model != "deepseek-v4-flash" {
		t.Errorf("provider/model = %q/%q, want deepseek_magic/deepseek-v4-flash", gotReq.Provider, gotReq.Model)
	}
}

// TestDSHEmbedExecutorErrorOutcome 断言 error outcome 映射为 failed + errorCode。
func TestDSHEmbedExecutorErrorOutcome(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(agentRunResponse{Outcome: "quota", ErrorCode: "QUOTA", SessionID: "s"})
	}))
	defer srv.Close()

	e := newDSHEmbedExecutor(strings.TrimPrefix(srv.URL, "http://"), t.TempDir())
	handle, err := e.Start(context.Background(), PhaseSpec{
		Phase:       "planning",
		Model:       "gateway/deepseek-v4-pro",
		SkillPrompt: "/x /vault/T.md",
		Timeout:     30 * time.Second,
	}, TaskSnapshot{})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	res, err := handle.Wait()
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if res.Code != OutcomeQuotaExhausted {
		t.Fatalf("code = %q, want quota_exhausted", res.Code)
	}
}

// TestDSHEmbedExecutorErrorOutcomeCarriesMessage：agent-server 下发 error 消息
// （errorCode 缺省）时，ExecutionResult.Error 必须携带该详情——旧契约只有
// errorCode，失败只剩「agent-server outcome error」空原因（TASK-058 观测）。
func TestDSHEmbedExecutorErrorOutcomeCarriesMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(agentRunResponse{
			Outcome: "error", SessionID: "s", Error: "provider upstream 502 bad gateway",
		})
	}))
	defer srv.Close()

	e := newDSHEmbedExecutor(strings.TrimPrefix(srv.URL, "http://"), t.TempDir())
	handle, err := e.Start(context.Background(), PhaseSpec{
		Phase:       "planning",
		Model:       "gateway/deepseek-v4-pro",
		SkillPrompt: "/x /vault/T.md",
		Timeout:     30 * time.Second,
	}, TaskSnapshot{})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	res, err := handle.Wait()
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if res.Code != OutcomeFailed {
		t.Fatalf("code = %q, want failed", res.Code)
	}
	if !strings.Contains(res.Error, "provider upstream 502 bad gateway") {
		t.Fatalf("error = %q, want server message carried", res.Error)
	}
}

// TestDSHEmbedExecutorUnreachable 断言 agent-server 不可达映射为 failed。
func TestDSHEmbedExecutorUnreachable(t *testing.T) {
	e := newDSHEmbedExecutor("127.0.0.1:1", t.TempDir()) // 端口 1 不可达
	handle, err := e.Start(context.Background(), PhaseSpec{
		Phase:       "planning",
		Model:       "gateway/deepseek-v4-pro",
		SkillPrompt: "/x /vault/T.md",
		Timeout:     5 * time.Second,
	}, TaskSnapshot{})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	res, err := handle.Wait()
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if res.Code != OutcomeFailed {
		t.Fatalf("code = %q, want failed", res.Code)
	}
	if res.Error == "" {
		t.Error("error must be non-empty on unreachable agent-server")
	}
	// 断连也要保留 token：下一轮 scan resume 同一会话而非 fresh start，
	// 否则旧会话在 agent-server 里继续跑，出现同任务双会话并行。
	if res.ResumeToken == "" {
		t.Fatal("unreachable 时应持久化 ResumeToken（防 fresh start 双会话）")
	}
}

// TestDSHEmbedExecutorResumeTokenRoundTrip 断言 dispatch 返回的 ResumeToken
// 是 JSON 编码（sessionId + provider/model/skillPrompt/effort），且 Resume
// 能解码并带 sessionId 重新发 /agent/run。
func TestDSHEmbedExecutorResumeTokenRoundTrip(t *testing.T) {
	var gotReqs []agentRunRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req agentRunRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		gotReqs = append(gotReqs, req)
		_ = json.NewEncoder(w).Encode(agentRunResponse{Text: "done", Outcome: "completed", SessionID: "session-resume-1"})
	}))
	defer srv.Close()

	e := newDSHEmbedExecutor(strings.TrimPrefix(srv.URL, "http://"), t.TempDir())
	spec := PhaseSpec{
		Phase:           "planning",
		Model:           "gateway/deepseek-v4-pro",
		ReasoningEffort: "high",
		SkillPrompt:     "/obsidian-task-runner-round1 /vault/TASK.md",
		Timeout:         30 * time.Second,
	}
	h, err := e.Start(context.Background(), spec, TaskSnapshot{})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	res, err := h.Wait()
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if res.ResumeToken == "" {
		t.Fatal("ResumeToken must be non-empty after a durable session")
	}
	var tok embedResumeToken
	if err := json.Unmarshal([]byte(res.ResumeToken), &tok); err != nil {
		t.Fatalf("ResumeToken must be valid JSON: %v (got %q)", err, res.ResumeToken)
	}
	if tok.SessionID != "session-resume-1" || tok.Provider != "deepseek_magic" || tok.Model != "deepseek-v4-pro" || tok.SkillPrompt != spec.SkillPrompt || tok.ReasoningEffort != "high" {
		t.Fatalf("resume token fields wrong: %+v", tok)
	}

	// Resume 用 token 重新发请求，带 sessionId。timeout 传 0 走默认值。
	h2, err := e.Resume(context.Background(), PhaseSpec{Phase: "resume"}, res.ResumeToken, 0)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if _, err := h2.Wait(); err != nil {
		t.Fatalf("Resume Wait: %v", err)
	}
	if len(gotReqs) != 2 {
		t.Fatalf("request count = %d, want 2 (start + resume)", len(gotReqs))
	}
	if gotReqs[1].SessionID != "session-resume-1" {
		t.Errorf("resume sessionId = %q, want session-resume-1", gotReqs[1].SessionID)
	}
	if gotReqs[1].Provider != "deepseek_magic" || gotReqs[1].Model != "deepseek-v4-pro" {
		t.Errorf("resume provider/model = %q/%q, want deepseek_magic/deepseek-v4-pro", gotReqs[1].Provider, gotReqs[1].Model)
	}
}

// TestDSHEmbedExecutorResumeRejectsBadToken 断言 Resume 拒绝空/畸形 token。
func TestDSHEmbedExecutorResumeRejectsBadToken(t *testing.T) {
	e := newDSHEmbedExecutor("127.0.0.1:8799", t.TempDir())
	if _, err := e.Resume(context.Background(), PhaseSpec{}, "", 0); err == nil {
		t.Error("Resume(empty) must fail")
	}
	if _, err := e.Resume(context.Background(), PhaseSpec{}, "not-json", 0); err == nil {
		t.Error("Resume(not-json) must fail")
	}
	if _, err := e.Resume(context.Background(), PhaseSpec{}, `{"sessionId":"","provider":"p","model":"m"}`, 0); err == nil {
		t.Error("Resume(missing sessionId) must fail")
	}
}

// TestDSHEmbedExecutorInterruptedPersistsResumeToken 断言中断时（ctx cancel、
// /agent/run 响应未返回）也能持久化 ResumeToken——daemon 预分配 sessionId，
// 使 durable resume 在 daemon 重启中断场景真正可用。
func TestDSHEmbedExecutorInterruptedPersistsResumeToken(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	h := &embedHandle{
		ctx:         ctx,
		cancel:      cancel,
		phase:       "round2",
		req:         agentRunRequest{SessionID: "session-daemon-allocated", Provider: "deepseek_magic", Model: "deepseek-v4-pro"},
		provider:    "deepseek_magic",
		model:       "deepseek-v4-pro",
		effort:      "high",
		skillPrompt: "/obsidian-task-runner-round2 /vault/TASK.md",
	}
	cancel() // 模拟 daemon shutdown 中断
	res, err := h.Wait()
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if res.Code != OutcomeInterrupted {
		t.Fatalf("code = %q, want interrupted", res.Code)
	}
	if res.ResumeToken == "" {
		t.Fatal("中断时应持久化 ResumeToken（daemon 预分配 sessionId）")
	}
	var tok embedResumeToken
	if err := json.Unmarshal([]byte(res.ResumeToken), &tok); err != nil {
		t.Fatalf("ResumeToken 应为合法 JSON: %v", err)
	}
	if tok.SessionID != "session-daemon-allocated" || tok.Model != "deepseek-v4-pro" {
		t.Fatalf("resume token 字段错误: %+v", tok)
	}
}

// TestDSHEmbedExecutorTimeoutPersistsResumeToken 断言 HTTP 超时（daemon 侧
// 30 分钟 deadline，/agent/run 响应未返回）也持久化 ResumeToken——超时后
// 会话可能仍在 agent-server 中运行，fresh start 会开双会话并行写。
func TestDSHEmbedExecutorTimeoutPersistsResumeToken(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	h := &embedHandle{
		ctx:         ctx,
		cancel:      cancel,
		phase:       "round1",
		req:         agentRunRequest{SessionID: "session-timeout-case", Provider: "deepseek_magic", Model: "deepseek-v4-pro"},
		provider:    "deepseek_magic",
		model:       "deepseek-v4-pro",
		effort:      "high",
		skillPrompt: "/obsidian-task-runner-round1 /vault/TASK.md",
	}
	time.Sleep(60 * time.Millisecond) // deadline 先到
	res, err := h.Wait()
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if res.Code != OutcomeTimedOut {
		t.Fatalf("code = %q, want timed-out", res.Code)
	}
	if res.ResumeToken == "" {
		t.Fatal("超时时应持久化 ResumeToken（会话可能仍在 agent-server 运行）")
	}
	var tok embedResumeToken
	if err := json.Unmarshal([]byte(res.ResumeToken), &tok); err != nil || tok.SessionID != "session-timeout-case" {
		t.Fatalf("resume token 字段错误: %+v (err=%v)", tok, err)
	}
}

// TestDSHEmbedExecutorTimeoutActiveVsWedged guards the TASK-065 fix: when the
// phase HTTP wait expires, the handle probes GET /agents. A session with
// recent events is classified timeout_active (do NOT cancel — the model is
// still working); a session idle past the window stays timeout (wedged turn,
// caller cancels).
func TestDSHEmbedExecutorTimeoutActiveVsWedged(t *testing.T) {
	tests := []struct {
		name        string
		lastEventAt int64 // 0 → /agents reports nothing (legacy server)
		want        ExecOutcome
	}{
		{"active session continues", time.Now().UnixMilli(), OutcomeTimedOutActive},
		{"idle session is wedged", time.Now().Add(-2 * time.Hour).UnixMilli(), OutcomeTimedOut},
		{"legacy server without activity", 0, OutcomeTimedOut},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/agents" && r.Method == http.MethodGet {
					w.Header().Set("content-type", "application/json")
					_ = json.NewEncoder(w).Encode([]map[string]any{{
						"sessionId":   "session-065",
						"lastEventAt": tt.lastEventAt,
					}})
					return
				}
				// /agent/run blocks until the client's deadline fires.
				select {}
			}))
			defer srv.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			h := &embedHandle{
				ctx:        ctx,
				cancel:     cancel,
				client:     &http.Client{},
				addr:       strings.TrimPrefix(srv.URL, "http://"),
				phase:      "round2",
				req:        agentRunRequest{SessionID: "session-065", Provider: "deepseek_magic", Model: "deepseek-v4-pro"},
				provider:   "deepseek_magic",
				model:      "deepseek-v4-pro",
				idleWindow: 30 * time.Minute,
			}
			res, err := h.Wait()
			if err != nil {
				t.Fatalf("Wait: %v", err)
			}
			if res.Code != tt.want {
				t.Fatalf("code = %q, want %q (error=%q)", res.Code, tt.want, res.Error)
			}
			if res.ResumeToken == "" {
				t.Fatal("resume token must persist in both timeout variants")
			}
		})
	}
}

// TestDSHEmbedExecutorCancelStaleTaskSessions 守护会话残留修复的 RPC 侧契约：
// CancelStaleTaskSessions 枚举 GET /agents，只 cancel 与目标 taskId 匹配且仍
// status=working 的会话（idle 会话是待 durable resume 的已完成 run，不得动；
// taskId 缺失时回退 task 展示标签精确匹配）。枚举失败必须报错——调用方据此
// 中止 fresh Start，避免在未知写者集合上叠加新会话。
func TestDSHEmbedExecutorCancelStaleTaskSessions(t *testing.T) {
	var cancels []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/agents":
			w.Header().Set("content-type", "application/json")
			_ = json.NewEncoder(w).Encode([]map[string]string{
				{"sessionId": "stale-1", "taskId": "TASK-065", "status": "working"},
				{"sessionId": "stale-2", "taskId": "", "task": "TASK-065", "status": "working"},
				{"sessionId": "keep-idle", "taskId": "TASK-065", "status": "idle"},
				{"sessionId": "other-task", "taskId": "TASK-066", "status": "working"},
				{"sessionId": "working-nolabel", "status": "working"},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/agent/cancel":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			cancels = append(cancels, body["sessionId"])
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	e := newDSHEmbedExecutor(strings.TrimPrefix(srv.URL, "http://"), t.TempDir())
	if err := e.CancelStaleTaskSessions(context.Background(), "TASK-065"); err != nil {
		t.Fatalf("CancelStaleTaskSessions: %v", err)
	}
	if len(cancels) != 2 || cancels[0] != "stale-1" || cancels[1] != "stale-2" {
		t.Fatalf("cancelled sessions = %v, want [stale-1 stale-2]（只取消同任务 working 会话；idle 留给 resume，别任务/无标签不动）", cancels)
	}
}

// TestDSHEmbedExecutorCancelStaleTaskSessionsEnumFailure：/agents 枚举失败时
// 必须返回错误（调用方中止 fresh Start），绝不能静默通过后在未知写者集合上
// 叠新会话。
func TestDSHEmbedExecutorCancelStaleTaskSessionsEnumFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	e := newDSHEmbedExecutor(strings.TrimPrefix(srv.URL, "http://"), t.TempDir())
	if err := e.CancelStaleTaskSessions(context.Background(), "TASK-065"); err == nil {
		t.Fatal("枚举失败必须报错，调用方需据此中止 fresh Start")
	}
}

// TestDSHEmbedExecutorCancelStaleTaskSessionsEmptyTaskNoop：无任务标识的会话
// （pm/grilling）不参与 reconcile，空 taskId 直接 no-op 不发请求。
func TestDSHEmbedExecutorCancelStaleTaskSessionsEmptyTaskNoop(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request for empty taskId: %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()
	e := newDSHEmbedExecutor(strings.TrimPrefix(srv.URL, "http://"), t.TempDir())
	if err := e.CancelStaleTaskSessions(context.Background(), ""); err != nil {
		t.Fatalf("empty taskId must be a no-op: %v", err)
	}
}
