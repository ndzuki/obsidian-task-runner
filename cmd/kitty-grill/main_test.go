package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestExtractJSON(t *testing.T) {
	text := "前置说明\n```json\n{\"decisions\":[{\"id\":\"D1\"}]}\n```\n后续"
	raw, ok := extractJSON(text)
	if !ok {
		t.Fatal("extractJSON 未找到 json 块")
	}
	if !strings.Contains(raw, `"decisions"`) {
		t.Fatalf("extractJSON 内容错误: %q", raw)
	}
}

func TestExtractJSONMissing(t *testing.T) {
	if _, ok := extractJSON("没有代码块"); ok {
		t.Fatal("extractJSON 应该返回 false")
	}
}

func TestExtractJSONRoundTrip(t *testing.T) {
	// 模拟模型的 JSON 问卷输出，验证 extractJSON + json.Unmarshal 闭环。
	modelReply := "```json\n" + `{"decisions":[{"id":"D1","question":"Q1","options":[{"id":"A","label":"选项A"},{"id":"B","label":"选项B"}],"recommended":"A","reason":"理由"}]}` + "\n```"
	raw, ok := extractJSON(modelReply)
	if !ok {
		t.Fatal("未找到 json 块")
	}
	var q questionnaire
	if err := json.Unmarshal([]byte(raw), &q); err != nil {
		t.Fatalf("解析 JSON 失败: %v", err)
	}
	if len(q.Decisions) != 1 || q.Decisions[0].Recommended != "A" {
		t.Fatalf("解析结果错误: %+v", q)
	}
}

func TestQModelRecommendedCursor(t *testing.T) {
	d := []decision{{
		ID:          "D1",
		Question:    "Q1",
		Options:     []option{{ID: "A", Label: "a"}, {ID: "B", Label: "b"}, {ID: "C", Label: "c"}},
		Recommended: "B",
	}}
	m := newQModel(d)
	if m.cursor != 1 {
		t.Fatalf("默认光标应落在推荐项 B（index 1），got %d", m.cursor)
	}
}

func TestQModelConfirmAdvances(t *testing.T) {
	d := []decision{
		{ID: "D1", Question: "Q1", Options: []option{{ID: "A", Label: "a"}, {ID: "B", Label: "b"}}, Recommended: "A"},
		{ID: "D2", Question: "Q2", Options: []option{{ID: "A", Label: "a"}, {ID: "B", Label: "b"}}, Recommended: "B"},
	}
	m := newQModel(d)
	// 确认 D1 的当前选项（A）→ 应记录 + 前进到 D2。
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m2 := updated.(qModel)
	if m2.answers[0] != "A" || m2.idx != 1 {
		t.Fatalf("确认后 answers[0]=%q idx=%d，期望 answers[0]=A idx=1", m2.answers[0], m2.idx)
	}
	if m2.submitted {
		t.Fatal("未答完 D2 时不应提交")
	}
}

func TestQModelAllAnswered(t *testing.T) {
	d := []decision{
		{ID: "D1", Question: "Q1", Options: []option{{ID: "A", Label: "a"}}, Recommended: "A"},
	}
	m := newQModel(d)
	m.answers = []string{"A"}
	if !m.allAnswered() {
		t.Fatal("allAnswered 应返回 true")
	}
}

func TestBuildGrillingPromptPrefixesTaskID(t *testing.T) {
	// 任务 ID 必须出现在 prompt 最前：agent-server 监控面板按第一个
	// TASK-xxx 打标签，后文即使引用其他任务也不能抢占标签（观测：
	// TASK-005 的 grilling 会话被误标为 TASK-058）。
	prompt := buildGrillingPrompt("005", "规划阶段重复会话防护",
		"Projects/003-obsidian-task-runner/Requirements/REQ-005-no-duplicate-planning-sessions.md", "/vault")
	prompt += "\n\n以下是需求文档全文…TASK-058 双会话…TASK-078 已关闭…"
	if !strings.HasPrefix(prompt, "任务 TASK-005") {
		t.Fatalf("prompt 应以「任务 TASK-005」开头，got: %.80q", prompt)
	}
	first := strings.Index(prompt, "TASK-")
	others := strings.Index(prompt[first+1:], "TASK-")
	if others >= 0 && strings.Index(prompt, "TASK-005") != first {
		t.Fatalf("第一个 TASK- 引用应为 TASK-005，got 首引用 %.12q", prompt[first:first+12])
	}
}

func TestBuildGrillingPromptWithoutTaskKeepsOldShape(t *testing.T) {
	prompt := buildGrillingPrompt("", "某个标题", "", "")
	if strings.HasPrefix(prompt, "任务 TASK-") {
		t.Fatalf("无 taskID 时不得添加任务前缀：%.80q", prompt)
	}
	if !strings.Contains(prompt, "我要实现「某个标题」") {
		t.Fatalf("无 req 时应退回标题目标：%.80q", prompt)
	}
}

func TestGrillingStillActive(t *testing.T) {
	vault := t.TempDir()
	taskPath := filepath.Join(vault, "Projects", "003-demo", "Tasks", "TASK-005-no-dup.md")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	reqDoc := "Projects/003-demo/Requirements/REQ-005-no-dup.md"
	write := func(status string) {
		content := "---\nid: \"005\"\nstatus: " + status + "\n---\n# body\n"
		if err := os.WriteFile(taskPath, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write("needs-grilling")
	if active, status := grillingStillActive(vault, reqDoc, "005"); !active || status != "needs-grilling" {
		t.Fatalf("needs-grilling 应 active，got active=%v status=%q", active, status)
	}

	write("closed")
	if active, status := grillingStillActive(vault, reqDoc, "005"); active || status != "closed" {
		t.Fatalf("closed 应拦截，got active=%v status=%q", active, status)
	}

	write("done")
	if active, _ := grillingStillActive(vault, reqDoc, "005"); active {
		t.Fatal("done 应拦截")
	}

	// 任务文件缺失 / 无 taskID / 无 vault：守卫不得误伤 legacy 流程。
	if active, _ := grillingStillActive(vault, reqDoc, "999"); !active {
		t.Fatal("任务文件缺失时应视为 active（不拦截）")
	}
	if active, _ := grillingStillActive(vault, reqDoc, ""); !active {
		t.Fatal("无 taskID 时应视为 active（不拦截）")
	}
	if active, _ := grillingStillActive("", reqDoc, "005"); !active {
		t.Fatal("无 vault 时应视为 active（不拦截）")
	}
}

func TestResolveTaskFileFallsBackAcrossProjects(t *testing.T) {
	vault := t.TempDir()
	// reqDoc 指向 001 项目，但任务文件实际在 002 项目（模拟 req/task 项目错位，
	// 校验跨项目兜底扫描）。
	taskPath := filepath.Join(vault, "Projects", "002-other", "Tasks", "TASK-005-x.md")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(taskPath, []byte("---\nid: \"005\"\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := resolveTaskFile(vault, "Projects/001-demo/Requirements/REQ-005.md", "005")
	if got != taskPath {
		t.Fatalf("resolveTaskFile = %q, want %q", got, taskPath)
	}
}

func TestWritebackArgs(t *testing.T) {
	args := writebackArgs("/x/kitty-grill", "127.0.0.1:8799", "deepseek_magic", "gpt-5.4-mini", "high", "session-1", "D1=A D2=B")
	if len(args) != 14 {
		t.Fatalf("args len=%d, want 14: %v", len(args), args)
	}
	if args[0] != "/x/kitty-grill" || args[1] != "--writeback" {
		t.Fatalf("argv 应以 executable + --writeback 开头: %v", args)
	}
	joined := strings.Join(args, "\x00")
	for _, want := range []string{"--session\x00session-1", "--answers\x00D1=A D2=B", "--addr\x00127.0.0.1:8799"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("args 缺少 %q: %v", want, args)
		}
	}
}

func TestCloseTabArgs(t *testing.T) {
	args := closeTabArgs("win-42")
	want := []string{"kitty", "@", "close-window", "--match", "id:win-42"}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("closeTabArgs = %v, want %v", args, want)
		}
	}
}

func TestRunWriteback(t *testing.T) {
	var gotReq chatRequest
	var sawClose bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/agent/chat":
			if err := json.NewDecoder(r.Body).Decode(&gotReq); err != nil {
				t.Errorf("decode: %v", err)
			}
			_ = json.NewEncoder(w).Encode(chatResponse{
				Text:      "决策总结：已写回",
				Outcome:   "completed",
				SessionID: "session-1",
			})
		case "/agent/close":
			sawClose = true
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	}))
	defer srv.Close()

	if err := runWriteback(strings.TrimPrefix(srv.URL, "http://"), "deepseek_magic", "gpt-5.4-mini", "high", "session-1", "D1=A"); err != nil {
		t.Fatalf("runWriteback: %v", err)
	}
	if !sawClose {
		t.Fatal("runWriteback 成功后应调用 /agent/close 回收会话")
	}
	if gotReq.SessionID != "session-1" || gotReq.Message != "D1=A" {
		t.Fatalf("chat request = %+v, want session-1/D1=A", gotReq)
	}
}

func TestRunWritebackRejectsMissingInput(t *testing.T) {
	if err := runWriteback("127.0.0.1:1", "p", "m", "low", "", "D1=A"); err == nil {
		t.Fatal("缺 session 应报错")
	}
	if err := runWriteback("127.0.0.1:1", "p", "m", "low", "session-1", ""); err == nil {
		t.Fatal("缺 answers 应报错")
	}
}

func TestRunWritebackServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"session not found"}`, http.StatusInternalServerError)
	}))
	defer srv.Close()
	if err := runWriteback(strings.TrimPrefix(srv.URL, "http://"), "p", "m", "low", "session-1", "D1=A"); err == nil {
		t.Fatal("HTTP 500 应报错")
	}
}

func TestCloseChatSession(t *testing.T) {
	var gotPath, gotSession string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		var body struct {
			SessionID string `json:"sessionId"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotSession = body.SessionID
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	closeChatSession(strings.TrimPrefix(srv.URL, "http://"), "session-1")
	if gotPath != "/agent/close" || gotSession != "session-1" {
		t.Fatalf("closeChatSession → %s session=%q, want /agent/close session-1", gotPath, gotSession)
	}
	// 空参数静默跳过。
	closeChatSession("", "")
	closeChatSession(strings.TrimPrefix(srv.URL, "http://"), "")
}

// TestRunWritebackRetriesThenSucceeds：模型级失败（outcome error）应重试同一
// 会话，第二次成功后返回 nil（观测：TASK-058 决策写回一次失败即丢答案）。
func TestRunWritebackRetriesThenSucceeds(t *testing.T) {
	old := writebackRetryBackoff
	writebackRetryBackoff = 0
	t.Cleanup(func() { writebackRetryBackoff = old })

	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/agent/chat":
			calls++
			if calls == 1 {
				_ = json.NewEncoder(w).Encode(chatResponse{Outcome: "error", SessionID: "session-1", ErrorCode: ""})
				return
			}
			_ = json.NewEncoder(w).Encode(chatResponse{Text: "写回完成", Outcome: "completed", SessionID: "session-1"})
		case "/agent/close":
			_, _ = w.Write([]byte(`{"ok":true}`))
		}
	}))
	defer srv.Close()

	if err := runWriteback(strings.TrimPrefix(srv.URL, "http://"), "p", "m", "low", "session-1", "D1=A"); err != nil {
		t.Fatalf("runWriteback 重试后应成功: %v", err)
	}
	if calls != 2 {
		t.Fatalf("chat calls = %d, want 2（一次失败 + 一次重试成功）", calls)
	}
}

// TestRunWritebackExhaustsRetriesAndNotifies：全部尝试失败后返回错误并触发
// 桌面提醒钩子（用户不会再静默发现任务仍被阻塞）。
func TestRunWritebackExhaustsRetriesAndNotifies(t *testing.T) {
	old := writebackRetryBackoff
	writebackRetryBackoff = 0
	var notified bool
	oldNotify := notifyWritebackFailure
	notifyWritebackFailure = func(error) { notified = true }
	t.Cleanup(func() {
		writebackRetryBackoff = old
		notifyWritebackFailure = oldNotify
	})

	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = json.NewEncoder(w).Encode(chatResponse{Outcome: "error", SessionID: "session-1"})
	}))
	defer srv.Close()

	err := runWriteback(strings.TrimPrefix(srv.URL, "http://"), "p", "m", "low", "session-1", "D1=A")
	if err == nil {
		t.Fatal("全部尝试失败应返回错误")
	}
	if calls != writebackMaxAttempts {
		t.Fatalf("chat calls = %d, want %d", calls, writebackMaxAttempts)
	}
	if !notified {
		t.Fatal("重试耗尽后应触发桌面提醒")
	}
}
