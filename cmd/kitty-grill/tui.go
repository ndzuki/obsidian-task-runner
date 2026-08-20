package main

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// qModel is the Bubble Tea model for the batch questionnaire: one decision
// shown at a time, arrow keys move the option cursor, Enter confirms and
// advances, left/right jump between decisions, q submits when all are
// answered.
type qModel struct {
	decisions []decision
	idx       int
	cursor    int
	answers   []string // len == len(decisions)，已确认的选项 id（"" 未确认）
	width     int
	submitted bool
	aborted   bool
}

var (
	headerStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("11")).MarginBottom(1)
	questionStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
	cursorStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("2")).Bold(true)
	selectedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Bold(true)
	optionStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("7"))
	reasonStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Italic(true)
	helpStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	recStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
)

func newQModel(decisions []decision) qModel {
	answers := make([]string, len(decisions))
	// 默认光标落在推荐项上。
	cursor := 0
	if len(decisions) > 0 {
		for i, o := range decisions[0].Options {
			if o.ID == decisions[0].Recommended {
				cursor = i
				break
			}
		}
	}
	return qModel{decisions: decisions, answers: answers, cursor: cursor}
}

func (m qModel) Init() tea.Cmd { return nil }

func (m qModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			m.aborted = true
			return m, tea.Quit
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.decisions[m.idx].Options)-1 {
				m.cursor++
			}
		case "left", "h":
			if m.idx > 0 {
				m.idx--
				m.cursor = m.recommendedCursor(m.idx)
			}
		case "right", "l":
			if m.idx < len(m.decisions)-1 {
				m.idx++
				m.cursor = m.recommendedCursor(m.idx)
			}
		case "enter", " ":
			// 确认当前选项 → 记录 → 下一个决策点。
			opt := m.decisions[m.idx].Options[m.cursor]
			m.answers[m.idx] = opt.ID
			if m.idx < len(m.decisions)-1 {
				m.idx++
				m.cursor = m.recommendedCursor(m.idx)
			} else if m.allAnswered() {
				m.submitted = true
				return m, tea.Quit
			}
		case "tab":
			// 跳到下一个未确认的决策点。
			for i := 1; i <= len(m.decisions); i++ {
				n := (m.idx + i) % len(m.decisions)
				if m.answers[n] == "" {
					m.idx = n
					m.cursor = m.recommendedCursor(n)
					break
				}
			}
		case "q", "Q":
			if m.allAnswered() {
				m.submitted = true
				return m, tea.Quit
			}
		}
	}
	return m, nil
}

func (m qModel) recommendedCursor(idx int) int {
	for i, o := range m.decisions[idx].Options {
		if o.ID == m.decisions[idx].Recommended {
			return i
		}
	}
	return 0
}

func (m qModel) allAnswered() bool {
	for _, a := range m.answers {
		if a == "" {
			return false
		}
	}
	return true
}

// wrap breaks s into lines no wider than width runes. CJK text has no spaces,
// so it wraps per rune; ASCII text also wraps per rune (acceptable for option
// labels that are mostly short phrases).
func wrap(s string, width int) string {
	if width <= 0 || len([]rune(s)) <= width {
		return s
	}
	runes := []rune(s)
	var b strings.Builder
	for i := 0; i < len(runes); i++ {
		if i > 0 && i%width == 0 {
			b.WriteString("\n")
		}
		b.WriteRune(runes[i])
	}
	return b.String()
}

func (m qModel) View() string {
	if m.aborted {
		return ""
	}
	var b strings.Builder

	// 终端宽度（未收到 WindowSizeMsg 时用 80 兜底）。
	width := m.width
	if width < 40 {
		width = 80
	}
	labelW := width - 16 // 前缀 "❯   A. " + 后缀 " ⭐推荐" + 边距
	if labelW < 20 {
		labelW = 20
	}

	// 顶部进度条。
	answered := 0
	for _, a := range m.answers {
		if a != "" {
			answered++
		}
	}
	b.WriteString(headerStyle.Render(fmt.Sprintf("🟡 需求对齐问卷  %d/%d 已答", answered, len(m.decisions))))
	b.WriteString("\n")

	d := m.decisions[m.idx]
	b.WriteString(questionStyle.Render(wrap(fmt.Sprintf("D%d · %s", m.idx+1, d.Question), width-4)))
	b.WriteString("\n\n")

	for i, o := range d.Options {
		mark := "  "
		if m.answers[m.idx] == o.ID {
			mark = "✓ "
		}
		rec := ""
		if o.ID == d.Recommended {
			rec = " ⭐推荐"
		}
		line := fmt.Sprintf("%s%s. %s%s", mark, o.ID, wrap(o.Label, labelW), rec)
		if i == m.cursor {
			b.WriteString(cursorStyle.Render("❯ " + line))
		} else if m.answers[m.idx] == o.ID {
			b.WriteString(selectedStyle.Render(line))
		} else {
			b.WriteString(optionStyle.Render("  " + line))
		}
		b.WriteString("\n")
	}

	if d.Reason != "" {
		b.WriteString("\n")
		b.WriteString(reasonStyle.Render(wrap("  推荐理由: "+d.Reason, width-4)))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(helpStyle.Render("  ↓/j ↑/k 选选项 · ←/h →/l 切题 · Enter/Space 确认 · Tab 跳未答 · q 提交"))
	b.WriteString("\n")
	return b.String()
}

// runQuestionnaire renders the TUI, collects answers, and returns them in
// decision order (D1's answer first). aborted is true on ctrl+c/esc.
func runQuestionnaire(decisions []decision) (answers []string, aborted bool) {
	m := newQModel(decisions)
	p := tea.NewProgram(m)
	mm, err := p.Run()
	if err != nil {
		return nil, true
	}
	final := mm.(qModel)
	if final.aborted {
		return nil, true
	}
	return final.answers, false
}
