package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

// customOptionID 是每个决策点选项列表首位的合成选项：选中后进入自由文本
// 输入框，让用户填写自己的答案（如粘贴 curl 原文），而不限于模型给出的
// A/B/C 选项——2026-09-03 观测：magic-models-manager D-5 的答案是「粘贴
// 权威 curl」，纯选项问卷无法承载，用户要求增加自填入口。
const (
	customOptionID    = "CUSTOM"
	customOptionLabel = "填写你的答案（自由文本）"
)

// answerEntry is one confirmed answer: either a model option id or the
// user's own free text.
type answerEntry struct {
	id   string // 选项 id；自由文本时为 customOptionID
	text string // 自由文本内容（仅 id == customOptionID 时有效）
}

// qModel is the Bubble Tea model for the batch questionnaire: one decision
// shown at a time, arrow keys move the option cursor, Enter confirms and
// advances, left/right jump between decisions, q submits when all are
// answered. The first option of every decision is the custom free-text
// entry; selecting it switches into a textarea.
type qModel struct {
	decisions []decision
	idx       int
	cursor    int      // 组合列表下标：0=自填，1..n=模型选项
	answers   []string // len == len(decisions)，已确认的选项 id（"" 未确认；customOptionID=自填）
	custom    []string // 自由文本答案（answers[i]==customOptionID 时有效）
	inputMode bool
	ta        textarea.Model
	width     int
	submitted bool
	aborted   bool
}

var (
	headerStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("11")).MarginBottom(1)
	questionStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
	cursorStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("2")).Bold(true)
	selectedStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Bold(true)
	optionStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("7"))
	reasonStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Italic(true)
	helpStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	recStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	inputHintStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Bold(true)
)

func newQModel(decisions []decision) qModel {
	answers := make([]string, len(decisions))
	ta := textarea.New()
	ta.Placeholder = "在此填写你的答案（如粘贴完整 curl）…"
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	return qModel{
		decisions: decisions,
		answers:   answers,
		custom:    make([]string, len(decisions)),
		// 默认光标落在推荐项上（自填项排在首位但不是默认）。
		cursor: recommendedCursor(decisions, 0),
		ta:     ta,
	}
}

// recommendedCursor returns the combined-list cursor index of the
// recommended option for decision idx. The custom entry occupies index 0,
// so real options sit at index+1. Falls back to the first real option.
func recommendedCursor(decisions []decision, idx int) int {
	if idx < len(decisions) {
		for i, o := range decisions[idx].Options {
			if o.ID == decisions[idx].Recommended {
				return i + 1
			}
		}
	}
	return 1
}

func (m qModel) Init() tea.Cmd { return nil }

// combinedOptions returns the custom entry followed by the model's options.
func (m qModel) combinedOptions(idx int) []option {
	out := make([]option, 0, len(m.decisions[idx].Options)+1)
	out = append(out, option{ID: customOptionID, Label: customOptionLabel})
	return append(out, m.decisions[idx].Options...)
}

// enterInputMode switches to the free-text editor for the current decision,
// prefilled with any previously typed answer. Focus() 自带光标闪烁。
func (m qModel) enterInputMode() (qModel, tea.Cmd) {
	m.ta.SetValue(m.custom[m.idx])
	if m.width >= 40 {
		m.ta.SetWidth(m.width - 6)
	}
	m.inputMode = true
	return m, m.ta.Focus()
}

// confirmCustomAnswer records the textarea content as the free-text answer
// for the current decision and leaves input mode. Empty input cancels.
func (m qModel) confirmCustomAnswer() (qModel, tea.Cmd) {
	text := strings.TrimSpace(m.ta.Value())
	if text != "" {
		m.answers[m.idx] = customOptionID
		m.custom[m.idx] = text
	}
	m.inputMode = false
	m.ta.Blur()
	return m.advanceAfterAnswer()
}

// advanceAfterAnswer moves to the next decision or submits when everything
// is answered. It returns the updated model — the caller's copy must be
// replaced, not discarded.
func (m qModel) advanceAfterAnswer() (qModel, tea.Cmd) {
	if m.idx < len(m.decisions)-1 {
		m.idx++
		m.cursor = recommendedCursor(m.decisions, m.idx)
		return m, nil
	}
	if m.allAnswered() {
		m.submitted = true
		return m, tea.Quit
	}
	return m, nil
}

func (m qModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		if m.width >= 40 {
			m.ta.SetWidth(m.width - 6)
		}
		return m, nil
	case tea.KeyMsg:
		if m.inputMode {
			return m.updateInput(msg)
		}
		switch msg.String() {
		case "ctrl+c", "esc":
			m.aborted = true
			return m, tea.Quit
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.combinedOptions(m.idx))-1 {
				m.cursor++
			}
		case "left", "h":
			if m.idx > 0 {
				m.idx--
				m.cursor = recommendedCursor(m.decisions, m.idx)
			}
		case "right", "l":
			if m.idx < len(m.decisions)-1 {
				m.idx++
				m.cursor = recommendedCursor(m.decisions, m.idx)
			}
		case "enter", " ":
			// 确认当前选项 → 记录 → 下一个决策点。
			if m.cursor == 0 {
				return m.enterInputMode()
			}
			opt := m.decisions[m.idx].Options[m.cursor-1]
			m.answers[m.idx] = opt.ID
			return m.advanceAfterAnswer()
		case "tab":
			// 跳到下一个未确认的决策点。
			for i := 1; i <= len(m.decisions); i++ {
				n := (m.idx + i) % len(m.decisions)
				if m.answers[n] == "" {
					m.idx = n
					m.cursor = recommendedCursor(m.decisions, n)
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

// updateInput handles keys while the free-text editor is focused.
func (m qModel) updateInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		m.aborted = true
		return m, tea.Quit
	case "esc":
		// 取消：丢弃本次输入，保留原答案（如有）。
		m.inputMode = false
		m.ta.Blur()
		return m, nil
	case "ctrl+d", "ctrl+s":
		return m.confirmCustomAnswer()
	default:
		var cmd tea.Cmd
		m.ta, cmd = m.ta.Update(msg)
		return m, cmd
	}
}

func (m qModel) allAnswered() bool {
	for _, a := range m.answers {
		if a == "" {
			return false
		}
	}
	return true
}

// wrap breaks s into lines no wider than width display columns. It uses
// runewidth so CJK/emoji (2 columns each) are counted correctly — a naive
// rune count would let a 64-rune Chinese label render 128 columns wide and
// overflow the terminal.
func wrap(s string, width int) string {
	if width <= 0 {
		return s
	}
	var b strings.Builder
	lineW := 0
	for _, r := range s {
		w := runewidth.RuneWidth(r)
		if w < 1 {
			w = 1
		}
		if lineW > 0 && lineW+w > width {
			b.WriteString("\n")
			lineW = 0
		}
		b.WriteRune(r)
		lineW += w
	}
	return b.String()
}

// customPreview renders the confirmed free-text answer as a compact preview.
func customPreview(text string, width int) string {
	oneLine := strings.ReplaceAll(text, "\n", " ⏎ ")
	if width > 4 {
		if r := []rune(oneLine); len(r) > width {
			oneLine = string(r[:width-1]) + "…"
		}
	}
	return oneLine
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

	if m.inputMode {
		b.WriteString(inputHintStyle.Render("✍️  请填写你的答案（Enter 换行 · Ctrl+D 确认 · Esc 取消）"))
		b.WriteString("\n\n")
		b.WriteString(m.ta.View())
		b.WriteString("\n")
		return b.String()
	}

	for i, o := range m.combinedOptions(m.idx) {
		mark := "  "
		if m.answers[m.idx] == o.ID {
			mark = "✓ "
		}
		rec := ""
		if o.ID == d.Recommended {
			rec = " ⭐推荐"
		}
		line := fmt.Sprintf("%s%s. %s%s", mark, o.ID, wrap(o.Label, labelW), rec)
		if o.ID == customOptionID {
			line = fmt.Sprintf("%s✍️ %s", mark, wrap(o.Label, labelW))
		}
		if i == m.cursor {
			b.WriteString(cursorStyle.Render("❯ " + line))
		} else if m.answers[m.idx] == o.ID {
			b.WriteString(selectedStyle.Render(line))
		} else {
			b.WriteString(optionStyle.Render("  " + line))
		}
		b.WriteString("\n")
		if o.ID == customOptionID && m.answers[m.idx] == customOptionID {
			b.WriteString(selectedStyle.Render("     " + customPreview(m.custom[m.idx], labelW)))
			b.WriteString("\n")
		}
	}

	if d.Reason != "" {
		b.WriteString("\n")
		b.WriteString(reasonStyle.Render(wrap("  推荐理由: "+d.Reason, width-4)))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(helpStyle.Render("  ↓/j ↑/k 选选项 · ←/h →/l 切题 · Enter/Space 确认 · ✍️ 首项自填答案 · Tab 跳未答 · q 提交"))
	b.WriteString("\n")
	return b.String()
}

// runQuestionnaire renders the TUI, collects answers, and returns them in
// decision order (D1's answer first). aborted is true on ctrl+c/esc.
func runQuestionnaire(decisions []decision) (answers []answerEntry, aborted bool) {
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
	out := make([]answerEntry, 0, len(decisions))
	for i := range decisions {
		if final.answers[i] == customOptionID {
			out = append(out, answerEntry{id: customOptionID, text: final.custom[i]})
		} else {
			out = append(out, answerEntry{id: final.answers[i]})
		}
	}
	return out, false
}

// buildAnswerMessage assembles the user's answers into the write-back
// message format the model consumes: one decision per line, "D-n=<答案>".
// Free-text answers are embedded verbatim (possibly multi-line, until the
// next "D-n=" line or end of message) instead of being space-joined, so
// pasted curl commands survive the round-trip.
func buildAnswerMessage(decisions []decision, answers []answerEntry) string {
	var parts []string
	for i, a := range answers {
		if i >= len(decisions) || a.id == "" {
			continue
		}
		if a.id == customOptionID {
			parts = append(parts, fmt.Sprintf("%s=%s", decisions[i].ID, a.text))
			continue
		}
		parts = append(parts, fmt.Sprintf("%s=%s", decisions[i].ID, a.id))
	}
	return strings.Join(parts, "\n")
}
