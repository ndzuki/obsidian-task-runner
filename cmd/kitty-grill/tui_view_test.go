package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-runewidth"
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
	for _, want := range []string{"D1", "服务归属", "release-manager", "⭐推荐", "推荐理由", "↓/j", "↑/k", "Enter/Space 确认", "←/h", "→/l"} {
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

// 验证自由文本输入模式的 View 渲染（提示语 + 按键说明）。
func TestQModelViewInputMode(t *testing.T) {
	d := []decision{{
		ID:          "D1",
		Question:    "Q",
		Options:     []option{{ID: "A", Label: "a"}},
		Recommended: "A",
	}}
	m := newQModel(d)
	m.cursor = 0
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(qModel)
	if !m.inputMode {
		t.Fatal("应先进入输入模式")
	}
	v := m.View()
	for _, want := range []string{"✍️", "请填写你的答案", "Ctrl+D", "Esc"} {
		if !strings.Contains(v, want) {
			t.Errorf("输入模式 View 缺少 %q:\n%s", want, v)
		}
	}
}

// 验证 wrap 按宽度换行，长文本不再溢出。
func TestWrapBreaksLongText(t *testing.T) {
	s := "这是一个很长的选项说明，超过宽度应该换行而不是被截断看不到"
	out := wrap(s, 10)
	lines := strings.Split(out, "\n")
	if len(lines) < 2 {
		t.Fatalf("wrap 应换行，实际: %q", out)
	}
	for _, l := range lines {
		if runewidth.StringWidth(l) > 10 {
			t.Errorf("换行后单行超宽: %q (宽度 %d)", l, runewidth.StringWidth(l))
		}
	}
}

// 验证 emoji（双列宽）也被正确计入宽度。
func TestWrapCountsEmojiWidth(t *testing.T) {
	s := "⭐⭐⭐⭐⭐⭐" // 6 个 emoji = 12 列
	out := wrap(s, 8)
	lines := strings.Split(out, "\n")
	if len(lines) < 2 {
		t.Fatalf("emoji 应按显示宽度换行，实际: %q", out)
	}
	for _, l := range lines {
		if runewidth.StringWidth(l) > 8 {
			t.Errorf("emoji 行超宽: %q (宽度 %d)", l, runewidth.StringWidth(l))
		}
	}
}

func TestWrapShortTextUnchanged(t *testing.T) {
	s := "短文本"
	if wrap(s, 10) != s {
		t.Errorf("短文本不应换行")
	}
}

// 验证 prefetchContext 注入需求文档 + 引用的 ADR 全文。
func TestPrefetchContextInjectsReqAndADR(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	reqPath := filepath.Join(vault, "Projects", "001", "Requirements", "REQ-001.md")
	adrDir := filepath.Join(vault, "Notes", "adr")
	if err := os.MkdirAll(filepath.Dir(reqPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(adrDir, 0o755); err != nil {
		t.Fatal(err)
	}
	reqContent := "需求：实现同步服务\n详见 ADR-005 和 ADR-006 的约束。"
	if err := os.WriteFile(reqPath, []byte(reqContent), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(adrDir, "ADR-005-build.md"), []byte("ADR-005 内容"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := prefetchContext(vault, "Projects/001/Requirements/REQ-001.md")
	for _, want := range []string{"需求：实现同步服务", "ADR-005", "ADR-005 内容"} {
		if !strings.Contains(ctx, want) {
			t.Errorf("prefetch 缺少 %q，完整:\n%s", want, ctx)
		}
	}
}

func TestPrefetchContextEmptyWhenNoReq(t *testing.T) {
	if prefetchContext("", "") != "" {
		t.Error("无 req 时应返回空")
	}
}
