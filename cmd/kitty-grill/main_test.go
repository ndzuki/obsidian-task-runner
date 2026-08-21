package main

import (
	"encoding/json"
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
