package main

import (
	"encoding/json"
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
