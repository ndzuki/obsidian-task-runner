package main

import (
	"encoding/json"
	"strings"
	"testing"
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

func TestRenderQuestionnaire(t *testing.T) {
	q := questionnaire{Decisions: []decision{
		{
			ID:       "D1",
			Question: "服务归属",
			Options: []option{
				{ID: "A", Label: "release-manager"},
				{ID: "B", Label: "deployd"},
			},
			Recommended: "A",
			Reason:      "基于 ADR",
		},
	}}
	out := renderQuestionnaire(q)
	for _, want := range []string{"D1: 服务归属", "[A]", "release-manager", "⭐推荐", "理由: 基于 ADR"} {
		if !strings.Contains(out, want) {
			t.Errorf("渲染输出缺少 %q，完整输出:\n%s", want, out)
		}
	}
}

func TestRenderQuestionnaireJSONRoundTrip(t *testing.T) {
	// 模拟模型的 JSON 问卷输出，验证 extractJSON + json.Unmarshal + render 闭环。
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
