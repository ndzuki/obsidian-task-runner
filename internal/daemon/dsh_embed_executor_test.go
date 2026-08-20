package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMapDSHModel(t *testing.T) {
	cases := []struct {
		in                string
		wantProv, wantMod string
	}{
		{"", "deepseek_magic", "deepseek-v4-pro"},
		{"gateway/deepseek-v4-pro", "deepseek_magic", "deepseek-v4-pro"},
		{"gateway/gpt-5.4-mini", "deepseek_magic", "gpt-5.4-mini"},
		{"deepseek/deepseek-v4-flash", "ds-official", "deepseek-v4-flash"},
		{"deepseek_magic/deepseek-v4-pro", "deepseek_magic", "deepseek-v4-pro"}, // 已 DSH 形式直传
		{"ds-official/deepseek-v4-flash", "ds-official", "deepseek-v4-flash"},
	}
	for _, c := range cases {
		prov, mod := mapDSHModel(c.in)
		if prov != c.wantProv || mod != c.wantMod {
			t.Errorf("mapDSHModel(%q) = (%q, %q), want (%q, %q)", c.in, prov, mod, c.wantProv, c.wantMod)
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
		Timeout:         30 * time.Second,
	}, TaskSnapshot{})
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
	if res.ResumeToken != "session-abc" {
		t.Errorf("resumeToken = %q, want session-abc", res.ResumeToken)
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
	if gotReq.SessionID != "" {
		t.Errorf("sessionId = %q, want empty on first dispatch", gotReq.SessionID)
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
	if gotReq.Provider != "ds-official" || gotReq.Model != "deepseek-v4-flash" {
		t.Errorf("provider/model = %q/%q, want ds-official/deepseek-v4-flash", gotReq.Provider, gotReq.Model)
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
}
