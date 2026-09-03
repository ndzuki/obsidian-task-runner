package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// TestMain 防御：测试进程永远不允许继承真实 kitty 的窗口/套接字环境变量。
// 否则任何触发 closeOwnTab() 的测试（如 repl 零决策分支）都会用
// `kitty @ close-window --match id:<KITTY_WINDOW_ID>` 关闭用户正在跑
// `make test` 的那个 tab（2026-08-28 观测：make test 卡死 + kitty tab 被删）。
func TestMain(m *testing.M) {
	os.Unsetenv("KITTY_WINDOW_ID")
	os.Unsetenv("KITTY_LISTEN_ON")
	os.Exit(m.Run())
}

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

func TestExtractJSONUppercaseFence(t *testing.T) {
	text := "```JSON\n{\"decisions\":[]}\n```"
	raw, ok := extractJSON(text)
	if !ok {
		t.Fatal("extractJSON 应接受大写 JSON fence")
	}
	if !strings.Contains(raw, `"decisions"`) {
		t.Fatalf("extractJSON 内容错误: %q", raw)
	}
}

// TestParseQuestionnaireBareJSON guards the 2026-09-03 regression: the model
// (deepseek_magic / gpt-5.4-mini → deepseek-v4-flash) answered the
// decision-list questionnaire with a bare JSON object and no ```json fence.
// extractJSON 因此失败，repl 把合法问卷原文倒进 tab 并停在
// 「✍️ 你的决策 >」纯文本回退——用户看到的是无法作答的原始 JSON。
// reply 为事故现场抓取的真实模型回复原文（session-5fc0f291…）。
func TestParseQuestionnaireBareJSON(t *testing.T) {
	reply := `{"decisions":[{"id":"D-5","question":"REQ-002 平台 API 契约缺实际 curl 正文（AC-1 阻塞）——请提供权威 curl 以回填请求体字段表/鉴权头/查询接口契约","options":[{"id":"A","label":"提供国内测试环境完整页面请求 curl（目标 https://magic-web.internal.example/admin/platform/model/llm，建议的最小权威契约）"},{"id":"B","label":"提供三环境（国内测试/国内预发布/国外预发布）的完整 curl 各一份"},{"id":"C","label":"暂无法提供完整 curl，改为提供请求体字段表 + 鉴权头（header/cookie/token）摘要"}],"recommended":"A","reason":"建议行明确指定国内测试环境完整 curl 为最小权威契约，可解除 AC-1 阻塞并由 PM 据此回填 REQ-002 契约、D-6 查询接口块"}]}`
	q, ok := parseQuestionnaire(reply)
	if !ok {
		t.Fatal("裸 JSON 问卷应被解析（模型不总是输出 fenced block）")
	}
	if len(q.Decisions) != 1 || q.Decisions[0].ID != "D-5" || q.Decisions[0].Recommended != "A" {
		t.Fatalf("解析结果错误: %+v", q)
	}
	if len(q.Decisions[0].Options) != 3 {
		t.Fatalf("期望 3 个选项，got %d", len(q.Decisions[0].Options))
	}
}

func TestParseQuestionnaireProseWrapped(t *testing.T) {
	reply := "好的，以下是问卷：\n" +
		`{"decisions":[{"id":"D1","question":"Q1","options":[{"id":"A","label":"a"}],"recommended":"A","reason":"r"}]}` +
		"\n请逐项作答。"
	q, ok := parseQuestionnaire(reply)
	if !ok {
		t.Fatal("散文包裹的 JSON 问卷应被解析")
	}
	if len(q.Decisions) != 1 || q.Decisions[0].ID != "D1" {
		t.Fatalf("解析结果错误: %+v", q)
	}
}

func TestParseQuestionnaireFenced(t *testing.T) {
	reply := "```json\n{\"decisions\":[{\"id\":\"D1\"}]}\n```"
	q, ok := parseQuestionnaire(reply)
	if !ok || len(q.Decisions) != 1 {
		t.Fatalf("fenced 问卷解析失败: ok=%v q=%+v", ok, q)
	}
}

func TestParseQuestionnaireRejectsUnparseable(t *testing.T) {
	// 无 decisions 字段的 JSON 必须拒绝：否则会被误判为「零待答点」走
	// 干净退出，而非回退到 manualFill。
	if _, ok := parseQuestionnaire(`{"foo":1}`); ok {
		t.Fatal("无 decisions 字段的 JSON 应被拒绝")
	}
	if _, ok := parseQuestionnaire("没有代码块也不是 JSON"); ok {
		t.Fatal("纯文本应被拒绝")
	}
	if _, ok := parseQuestionnaire(`{"decisions":`); ok {
		t.Fatal("截断 JSON 应被拒绝")
	}
}

func TestParseQuestionnaireEmptyDecisionsOK(t *testing.T) {
	// decisions:[] 是合法空问卷（requirement-elaborator 的
	// fully_mature 形态）→ ok=true 且 len==0，repl 走 noPendingScreen。
	q, ok := parseQuestionnaire(`{"requirement":"REQ-065","maturity":"fully_mature","decisions":[]}`)
	if !ok {
		t.Fatal("decisions:[] 是合法空问卷，应解析成功")
	}
	if len(q.Decisions) != 0 {
		t.Fatalf("期望 0 个决策，got %+v", q.Decisions)
	}
}

// TestReplZeroDecisionsExitsCleanly guards the decision-list tab regression:
// when the model returns an empty questionnaire (all decisions answered
// between tab launch and questionnaire generation — 2026-08-24 19:32 观测),
// repl must NOT fall into the plain-text manualFill (which dumped the model's
// raw markdown and parked the tab at "✍️ 你的决策 >"). It shows the clean
// no-pending screen, recycles the chat session, and returns without reading
// stdin.
func TestReplZeroDecisionsExitsCleanly(t *testing.T) {
	var sawClose bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/agent/chat":
			_ = json.NewEncoder(w).Encode(chatResponse{
				Text:      "```json\n" + `{"requirement":"REQ-065","maturity":"fully_mature","decisions":[]}` + "\n```",
				Outcome:   "completed",
				SessionID: "session-zero",
			})
		case "/agent/close":
			sawClose = true
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	}))
	defer srv.Close()

	// repl 若退回 manualFill 会阻塞在 stdin 读取上——换一个永不写入的管道，
	// 并用超时守护：阻塞即回归。
	oldStdin := os.Stdin
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdin = pr
	defer func() { os.Stdin = oldStdin }()
	defer pw.Close()

	done := make(chan error, 1)
	go func() {
		done <- repl(strings.TrimPrefix(srv.URL, "http://"), "p", "m", "low", "prompt", "", "", "", "")
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("repl: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("repl blocked on stdin — regressed into manualFill instead of clean zero-decision exit")
	}
	if !sawClose {
		t.Fatal("zero-decision exit should recycle the chat session via /agent/close")
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
	// 自填项占组合下标 0，真实选项下标整体 +1：A=1 B=2 C=3。
	if m.cursor != 2 {
		t.Fatalf("默认光标应落在推荐项 B（组合下标 2），got %d", m.cursor)
	}
}

// TestQModelCustomOptionFirst guards the 2026-09-03 feature request: every
// decision's option list must start with the free-text entry so users can
// write their own answer (e.g. paste a curl) instead of being forced into
// A/B/C choices.
func TestQModelCustomOptionFirst(t *testing.T) {
	d := []decision{{
		ID:          "D1",
		Question:    "Q",
		Options:     []option{{ID: "A", Label: "a"}, {ID: "B", Label: "b"}},
		Recommended: "A",
	}}
	m := newQModel(d)
	combined := m.combinedOptions(0)
	if len(combined) != 3 || combined[0].ID != customOptionID {
		t.Fatalf("自填项必须是第一项: %+v", combined)
	}
	if m.cursor != 1 {
		t.Fatalf("默认光标应在推荐项 A（组合下标 1），got %d", m.cursor)
	}
	v := m.View()
	if !strings.Contains(v, "✍️") || !strings.Contains(v, customOptionLabel) {
		t.Fatalf("View 应渲染自填项:\n%s", v)
	}
}

func TestQModelSelectCustomEntersInputMode(t *testing.T) {
	d := []decision{{
		ID: "D1", Question: "Q", Options: []option{{ID: "A", Label: "a"}}, Recommended: "A",
	}}
	m := newQModel(d)
	m.cursor = 0
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m2 := updated.(qModel)
	if !m2.inputMode {
		t.Fatal("选中首项 Enter 应进入自由文本输入")
	}
	if !m2.ta.Focused() {
		t.Fatal("进入输入模式后 textarea 应聚焦")
	}
}

// TestQModelCustomAnswerRecordsAndAdvances drives the full free-text flow:
// enter input mode, type a multi-line curl, confirm with Ctrl+D.
func TestQModelCustomAnswerRecordsAndAdvances(t *testing.T) {
	d := []decision{
		{ID: "D1", Question: "Q1", Options: []option{{ID: "A", Label: "a"}}, Recommended: "A"},
		{ID: "D2", Question: "Q2", Options: []option{{ID: "A", Label: "a"}}, Recommended: "A"},
	}
	m := newQModel(d)
	m.cursor = 0
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(qModel)
	if !m.inputMode {
		t.Fatal("应进入输入模式")
	}
	for _, k := range []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune("curl -X POST https://x")},
		{Type: tea.KeyEnter},
		{Type: tea.KeyRunes, Runes: []rune("-H 'Cookie: t=1'")},
		{Type: tea.KeyCtrlD},
	} {
		updated, _ = m.Update(k)
		m = updated.(qModel)
	}
	if m.inputMode {
		t.Fatal("Ctrl+D 应确认并退出输入模式")
	}
	want := "curl -X POST https://x\n-H 'Cookie: t=1'"
	if m.answers[0] != customOptionID || m.custom[0] != want {
		t.Fatalf("自填答案未记录: answers=%q custom=%q", m.answers[0], m.custom[0])
	}
	if m.idx != 1 {
		t.Fatalf("确认后应前进到 D2，got idx=%d", m.idx)
	}
}

func TestQModelCustomEmptyInputCancels(t *testing.T) {
	d := []decision{{
		ID: "D1", Question: "Q", Options: []option{{ID: "A", Label: "a"}}, Recommended: "A",
	}}
	m := newQModel(d)
	m.cursor = 0
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(qModel)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
	m = updated.(qModel)
	if m.inputMode {
		t.Fatal("空输入确认应退出输入模式")
	}
	if m.answers[0] != "" {
		t.Fatalf("空输入不应记为答案，got %q", m.answers[0])
	}
}

func TestQModelEscCancelsCustomKeepsPrevious(t *testing.T) {
	d := []decision{{
		ID: "D1", Question: "Q", Options: []option{{ID: "A", Label: "a"}}, Recommended: "A",
	}}
	m := newQModel(d)
	m.answers[0] = "A"
	m.cursor = 0
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(qModel)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("junk")})
	m = updated.(qModel)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(qModel)
	if m.inputMode {
		t.Fatal("Esc 应退出输入模式")
	}
	if m.answers[0] != "A" || m.custom[0] != "" {
		t.Fatalf("Esc 取消不得覆盖原答案: answers=%q custom=%q", m.answers[0], m.custom[0])
	}
}

// TestBuildAnswerMessage verifies the write-back message shape: one decision
// per line, free text embedded verbatim (multi-line ok), unanswered skipped.
func TestBuildAnswerMessage(t *testing.T) {
	decisions := []decision{{ID: "D1"}, {ID: "D2"}, {ID: "D3"}}
	answers := []answerEntry{
		{id: "A"},
		{id: customOptionID, text: "curl -X POST https://x\n-H 'Cookie: t=1'"},
		{},
	}
	msg := buildAnswerMessage(decisions, answers)
	want := "D1=A\nD2=curl -X POST https://x\n-H 'Cookie: t=1'"
	if msg != want {
		t.Fatalf("buildAnswerMessage = %q, want %q", msg, want)
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
	args := writebackArgs("/x/kitty-grill", "127.0.0.1:8799", "deepseek_magic", "gpt-5.4-mini", "high", "session-1", "D1=A D2=B", "/tmp/wb-ctx.json")
	if len(args) != 16 {
		t.Fatalf("args len=%d, want 16: %v", len(args), args)
	}
	if args[0] != "/x/kitty-grill" || args[1] != "--writeback" {
		t.Fatalf("argv 应以 executable + --writeback 开头: %v", args)
	}
	joined := strings.Join(args, "\x00")
	for _, want := range []string{"--session\x00session-1", "--answers\x00D1=A D2=B", "--addr\x00127.0.0.1:8799", "--ctx\x00/tmp/wb-ctx.json"} {
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

// TestCloseOwnTabNoopUnderTest 守护 make test 卡死 + kitty tab 被删的回归：
// closeOwnTab() 在 go test 进程里必须是硬 no-op——即使 PATH 里有 kitty、环境
// 注入了 KITTY_WINDOW_ID（用户在 kitty tab 里跑 make test 就是这种状态），也
// 绝不能执行 `kitty @ close-window` 去关用户正在跑测试的那个 tab。
func TestCloseOwnTabNoopUnderTest(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "kitty-called")
	kittyScript := "#!/bin/sh\ntouch '" + marker + "'\n"
	if err := os.WriteFile(filepath.Join(dir, "kitty"), []byte(kittyScript), 0o755); err != nil {
		t.Fatal(err)
	}
	// 手工恢复被 TestMain 清掉的环境，模拟「在真实 kitty tab 里跑测试」。
	t.Setenv("PATH", dir)
	t.Setenv("KITTY_WINDOW_ID", "win-42")
	t.Setenv("KITTY_LISTEN_ON", "unix:/tmp/kitty-test-sock")

	closeOwnTab()

	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("closeOwnTab 在测试进程里必须 no-op：不得执行 kitty @ close-window（否则会关掉用户跑 make test 的 tab）")
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

	if err := runWriteback(strings.TrimPrefix(srv.URL, "http://"), "deepseek_magic", "gpt-5.4-mini", "high", "session-1", "D1=A", nil); err != nil {
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
	if err := runWriteback("127.0.0.1:1", "p", "m", "low", "", "D1=A", nil); err == nil {
		t.Fatal("缺 session 应报错")
	}
	if err := runWriteback("127.0.0.1:1", "p", "m", "low", "session-1", "", nil); err == nil {
		t.Fatal("缺 answers 应报错")
	}
}

func TestRunWritebackServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"session not found"}`, http.StatusInternalServerError)
	}))
	defer srv.Close()
	if err := runWriteback(strings.TrimPrefix(srv.URL, "http://"), "p", "m", "low", "session-1", "D1=A", nil); err == nil {
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

	if err := runWriteback(strings.TrimPrefix(srv.URL, "http://"), "p", "m", "low", "session-1", "D1=A", nil); err != nil {
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
	var notifiedWhat string
	oldNotify := notifyWritebackFailure
	notifyWritebackFailure = func(what string, err error) {
		notified = true
		notifiedWhat = what
	}
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

	err := runWriteback(strings.TrimPrefix(srv.URL, "http://"), "p", "m", "low", "session-1", "D1=A",
		&writebackContext{Kind: "req", TaskID: "058"})
	if err == nil {
		t.Fatal("全部尝试失败应返回错误")
	}
	// 3 次同会话重试 + 1 次 fresh fallback（同样失败）= 4 次 /agent/chat。
	if calls != writebackMaxAttempts+1 {
		t.Fatalf("chat calls = %d, want %d（重试耗尽 + fresh fallback）", calls, writebackMaxAttempts+1)
	}
	if !strings.Contains(err.Error(), "fresh fallback failed") {
		t.Fatalf("错误应标注 fresh fallback 也失败，got %q", err)
	}
	if !notified {
		t.Fatal("重试耗尽后应触发桌面提醒")
	}
	if notifiedWhat != "TASK-058" {
		t.Fatalf("桌面提醒应标明失败对象 TASK-058，got %q", notifiedWhat)
	}
}

// TestRunWritebackOutcomeErrorCarriesDetail：outcome=error 时错误必须携带
// 服务端下发的 error 详情（观测：TASK-058 写回日志只有
// 「agent-server outcome error: 」空原因，失败不可诊断）。
func TestRunWritebackOutcomeErrorCarriesDetail(t *testing.T) {
	old := writebackRetryBackoff
	writebackRetryBackoff = 0
	t.Cleanup(func() { writebackRetryBackoff = old })

	var notified bool
	var notifiedErr error
	oldNotify := notifyWritebackFailure
	notifyWritebackFailure = func(_ string, err error) {
		notified = true
		notifiedErr = err
	}
	t.Cleanup(func() { notifyWritebackFailure = oldNotify })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(chatResponse{
			Outcome:   "error",
			SessionID: "session-1",
			Error:     "provider upstream returned 500 (internal server error)",
		})
	}))
	defer srv.Close()

	err := runWriteback(strings.TrimPrefix(srv.URL, "http://"), "p", "m", "low", "session-1", "D1=A", nil)
	if err == nil {
		t.Fatal("全部尝试失败应返回错误")
	}
	if !strings.Contains(err.Error(), "provider upstream returned 500") {
		t.Fatalf("错误应包含服务端详情，got %q", err)
	}
	if !notified || notifiedErr == nil || !strings.Contains(notifiedErr.Error(), "provider upstream returned 500") {
		t.Fatalf("桌面提醒应携带同一详情，got %v", notifiedErr)
	}
}

// TestRunWritebackOutcomeErrorWithoutDetailPlaceholder：服务端既无 code 也无
// message 时给出占位文案，不再出现完全空白的失败原因。
func TestRunWritebackOutcomeErrorWithoutDetailPlaceholder(t *testing.T) {
	old := writebackRetryBackoff
	writebackRetryBackoff = 0
	t.Cleanup(func() { writebackRetryBackoff = old })

	var notifiedErr error
	oldNotify := notifyWritebackFailure
	notifyWritebackFailure = func(_ string, err error) { notifiedErr = err }
	t.Cleanup(func() { notifyWritebackFailure = oldNotify })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(chatResponse{Outcome: "error", SessionID: "session-1"})
	}))
	defer srv.Close()

	err := runWriteback(strings.TrimPrefix(srv.URL, "http://"), "p", "m", "low", "session-1", "D1=A", nil)
	if err == nil {
		t.Fatal("全部尝试失败应返回错误")
	}
	if strings.HasSuffix(strings.TrimSpace(err.Error()), ":") {
		t.Fatalf("错误不应以空白结尾，got %q", err)
	}
	if !strings.Contains(err.Error(), "no details") {
		t.Fatalf("空详情应有占位文案，got %q", err)
	}
	if notifiedErr == nil || !strings.Contains(notifiedErr.Error(), "no details") {
		t.Fatalf("桌面提醒应携带占位文案，got %v", notifiedErr)
	}
}

// TestRunWritebackSessionGoneViaOutcomeErrorFallsBackFresh：会话丢失经
// outcome=error + error 消息表达时（服务端带回详情后的形态），第一次失败即
// 识别 session gone，不再空转 3 次重试同一死会话。
func TestRunWritebackSessionGoneViaOutcomeErrorFallsBackFresh(t *testing.T) {
	old := writebackRetryBackoff
	writebackRetryBackoff = 0
	t.Cleanup(func() { writebackRetryBackoff = old })

	var chatCalls int
	var secondSession string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/agent/chat":
			chatCalls++
			if chatCalls == 1 {
				_ = json.NewEncoder(w).Encode(chatResponse{
					Outcome:   "error",
					SessionID: "session-1",
					Error:     `session "session-1" not found`,
				})
				return
			}
			var req chatRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			secondSession = req.SessionID
			_ = json.NewEncoder(w).Encode(chatResponse{Text: "写回完成", Outcome: "completed", SessionID: "session-fresh"})
		case "/agent/close":
			_, _ = w.Write([]byte(`{"ok":true}`))
		}
	}))
	defer srv.Close()

	ctx := &writebackContext{
		SkillPrompt:   "原始问卷指令：把决策写回清单",
		Questionnaire: questionnaire{Decisions: []decision{{ID: "D1", Question: "Q1"}}},
		Answers:       "D1=A",
	}
	if err := runWriteback(strings.TrimPrefix(srv.URL, "http://"), "p", "m", "low", "session-1", "D1=A", ctx); err != nil {
		t.Fatalf("session 丢失后应经 fresh fallback 成功: %v", err)
	}
	if chatCalls != 2 {
		t.Fatalf("chat calls = %d, want 2（一次 session-gone 即降级 + 一次 fresh）", chatCalls)
	}
	if secondSession != "" {
		t.Fatalf("fresh fallback 应用新会话（空 sessionId），got %q", secondSession)
	}
}

// TestRunWritebackSessionGoneFallsBackFresh：session not found 是永久性错误，
// 重试同一会话无意义——应降级为全新会话（ctx 重建 prompt）完成写回。
// 观测：TASK-058 决策写回 3 次重试全撞 session not found，答案丢失。
func TestRunWritebackSessionGoneFallsBackFresh(t *testing.T) {
	old := writebackRetryBackoff
	writebackRetryBackoff = 0
	t.Cleanup(func() { writebackRetryBackoff = old })

	var chatCalls int
	var secondSession, secondMsg string
	var sawClose bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/agent/chat":
			chatCalls++
			var req chatRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			if chatCalls == 1 {
				http.Error(w, `{"error":"session \"session-1\" not found"}`, http.StatusInternalServerError)
				return
			}
			secondSession = req.SessionID
			secondMsg = req.Message
			_ = json.NewEncoder(w).Encode(chatResponse{Text: "写回完成", Outcome: "completed", SessionID: "session-fresh"})
		case "/agent/close":
			sawClose = true
			_, _ = w.Write([]byte(`{"ok":true}`))
		}
	}))
	defer srv.Close()

	ctx := &writebackContext{
		SkillPrompt:   "原始问卷指令：把决策写回清单",
		Questionnaire: questionnaire{Decisions: []decision{{ID: "D1", Question: "Q1"}}},
		Answers:       "D1=A",
	}
	if err := runWriteback(strings.TrimPrefix(srv.URL, "http://"), "p", "m", "low", "session-1", "D1=A", ctx); err != nil {
		t.Fatalf("session 丢失后应经 fresh fallback 成功: %v", err)
	}
	if chatCalls != 2 {
		t.Fatalf("chat calls = %d, want 2（一次 session-gone + 一次 fresh）", chatCalls)
	}
	if secondSession != "" {
		t.Fatalf("fresh fallback 应用新会话（空 sessionId），got %q", secondSession)
	}
	if !strings.Contains(secondMsg, "D1=A") || !strings.Contains(secondMsg, "无需再次生成问卷") {
		t.Fatalf("fresh prompt 应包含答案与跳过指令，got %.200q", secondMsg)
	}
	if !sawClose {
		t.Fatal("fresh 会话写回成功后应 /agent/close")
	}
}

// TestRunWritebackSessionGoneWithoutCtxNotifies：无上下文可重建时，会话丢失
// 立即失败 + 桌面提醒（不再空转重试）。
func TestRunWritebackSessionGoneWithoutCtxNotifies(t *testing.T) {
	old := writebackRetryBackoff
	writebackRetryBackoff = 0
	var notified bool
	oldNotify := notifyWritebackFailure
	notifyWritebackFailure = func(_ string, err error) { notified = true }
	t.Cleanup(func() {
		writebackRetryBackoff = old
		notifyWritebackFailure = oldNotify
	})

	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Error(w, `{"error":"session not found"}`, http.StatusInternalServerError)
	}))
	defer srv.Close()

	if err := runWriteback(strings.TrimPrefix(srv.URL, "http://"), "p", "m", "low", "session-1", "D1=A", nil); err == nil {
		t.Fatal("无 ctx 且会话丢失应失败")
	}
	if calls != 1 {
		t.Fatalf("chat calls = %d, want 1（session gone 不重试）", calls)
	}
	if !notified {
		t.Fatal("会话丢失且无法重建时应桌面提醒")
	}
}

// TestWritebackContextFreshPrompt：重建 prompt 必须自包含原始指令、答案与
// 问卷全文（新会话无任何旧上下文可依赖）。
func TestWritebackContextFreshPrompt(t *testing.T) {
	ctx := writebackContext{
		SkillPrompt:   "对 X 进行需求详细化…写回指令…",
		Answers:       "D1=A D2=B",
		Questionnaire: questionnaire{Decisions: []decision{{ID: "D1", Question: "Q1"}}},
	}
	prompt := ctx.FreshPrompt()
	for _, want := range []string{"对 X 进行需求详细化", "D1=A D2=B", "无需再次生成问卷", "```json", `"id":"D1"`} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("FreshPrompt 缺少 %q：%.300q", want, prompt)
		}
	}
}

// TestBuildGrillingPromptRequiresChangeType guards the 2026-09-01 regression:
// the questionnaire model wrote confirmation-only decisions back into REQ-025
// without a `> 变更类型:` annotation, so the daemon treated the change as
// breaking and reopened three done tasks.
func TestBuildGrillingPromptRequiresChangeType(t *testing.T) {
	prompt := buildGrillingPrompt("001", "标题", "Projects/001-demo/Requirements/REQ-001-demo.md", "/vault")
	for _, want := range []string{"> 变更类型: breaking|additive|cosmetic", "cosmetic"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("buildGrillingPrompt 缺少 %q：%.400q", want, prompt)
		}
	}
}

// TestRunWritebackCancelsWhenTaskLeftGrilling guards the async-writeback gap:
// the task may have moved on between questionnaire generation and submission.
func TestRunWritebackCancelsWhenTaskLeftGrilling(t *testing.T) {
	dir := t.TempDir()
	tasksDir := filepath.Join(dir, "Projects", "001-demo", "Tasks")
	if err := os.MkdirAll(tasksDir, 0755); err != nil {
		t.Fatal(err)
	}
	taskPath := filepath.Join(tasksDir, "TASK-001-demo.md")
	if err := os.WriteFile(taskPath, []byte("---\nid: \"001\"\nstatus: done\nproject: demo\nreq_doc: R.md\n---\n"), 0644); err != nil {
		t.Fatal(err)
	}
	ctx := &writebackContext{TaskID: "001", VaultPath: dir, ReqDoc: "R.md"}
	err := runWriteback("127.0.0.1:9", "openai", "gpt-5.6-luna", "low", "session-1", "D1=A", ctx)
	if err == nil || !strings.Contains(err.Error(), "已离开 grilling") {
		t.Fatalf("runWriteback = %v, want task-left-grilling cancellation", err)
	}
}
