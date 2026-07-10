// Package notify provides desktop notifications.
package notify

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

// StatusNotify sends a desktop notification for a task status change.
func StatusNotify(taskPath string) {
	if _, err := exec.LookPath("notify-send"); err != nil {
		return // silent skip
	}

	fm, err := parseFile(taskPath)
	if err != nil || fm == nil {
		return
	}

	var urgency, icon, title, body string
	switch fm.Status {
	case "plan-review":
		urgency = "normal"
		icon = "dialog-information"
		title = fmt.Sprintf("📋 Task %s: 计划已生成", fm.ID)
		body = fmt.Sprintf("%s\n请审阅计划，确认后设 plan_approved: true 并保存", fm.Title)
	case "review":
		urgency = "normal"
		icon = "emblem-default"
		title = fmt.Sprintf("✅ Task %s: 代码已实现", fm.ID)
		reviewer := fm.Reviewer
		if reviewer == "" {
			reviewer = "你"
		}
		body = fmt.Sprintf("%s\n请 %s review 代码，确认无误后设 merge_approved: true", fm.Title, reviewer)
	case "conflict":
		urgency = "critical"
		icon = "emblem-important"
		title = fmt.Sprintf("⚠️ Task %s: 合并冲突", fm.ID)
		body = fmt.Sprintf("%s\n自动合并失败，存在冲突文件，请手动解决", fm.Title)
	case "done":
		urgency = "normal"
		icon = "emblem-favorite"
		title = fmt.Sprintf("🎉 Task %s: 已完成", fm.ID)
		body = fmt.Sprintf("%s\n代码已合并并推送至远程仓库", fm.Title)
	case "implementing":
		urgency = "normal"
		icon = "emblem-system"
		title = fmt.Sprintf("⏳ Task %s: 仍在执行中", fm.ID)
		body = fmt.Sprintf("%s\n任务未正常结束（可能进程中断）", fm.Title)
	case "error", "failed":
		urgency = "critical"
		icon = "dialog-error"
		title = fmt.Sprintf("❌ Task %s: 执行失败", fm.ID)
		body = fmt.Sprintf("%s\n请检查日志", fm.Title)
	default:
		return
	}

	cmd := exec.Command("notify-send",
		"--urgency="+urgency,
		"--app-name=OMP Task Runner",
		"--icon="+icon,
		title, body,
	)
	cmd.Run() // fire and forget
}

// parseFile reads and parses a task document frontmatter.
func parseFile(path string) (*yamlfrontmatter.Frontmatter, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return yamlfrontmatter.Parse(data)
}

// Send sends a generic notification.
func Send(title, body string) {
	if _, err := exec.LookPath("notify-send"); err != nil {
		return
	}
	cmd := exec.Command("notify-send",
		"--app-name=OMP Task Runner",
		title, body,
	)
	cmd.Run()
}

// SendTaskAction sends a bounded action notification with the task ID.
func SendTaskAction(taskID, emoji, title, description string) {
	msg := taskID + ": " + description
	if strings.Contains(description, "\n") {
		msg = description
	}
	Send(emoji + " Task " + taskID + ": " + title, msg)
}
