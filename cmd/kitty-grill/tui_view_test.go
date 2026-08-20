package main

import (
	"strings"
	"testing"
)

// 验证 View 渲染输出包含关键交互元素（问题、选项、推荐、理由、帮助栏）。
func TestQModelViewRenders(t *testing.T) {
	d := []decision{{
		ID:          "D1",
		Question:    "服务归属",
		Options:     []option{{ID: "A", Label: "release-manager"}, {ID: "B", Label: "deployd"}},
		Recommended: "A",
		Reason:      "基于 ADR",
	}}
	m := newQModel(d)
	v := m.View()
	for _, want := range []string{"D1", "服务归属", "release-manager", "⭐推荐", "推荐理由", "↑↓ 选择", "Enter/Space 确认"} {
		if !strings.Contains(v, want) {
			t.Errorf("View 缺少 %q", want)
		}
	}
}

// 验证确认选项后 View 显示已选标记 ✓。
func TestQModelViewMarksSelected(t *testing.T) {
	d := []decision{{
		ID:          "D1",
		Question:    "Q",
		Options:     []option{{ID: "A", Label: "a"}, {ID: "B", Label: "b"}},
		Recommended: "A",
	}}
	m := newQModel(d)
	m.answers[0] = "A"
	if !strings.Contains(m.View(), "✓") {
		t.Error("已确认选项应显示 ✓ 标记")
	}
}
