// Package notify provides desktop notifications.
package notify

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"

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
		title = fmt.Sprintf("📋 T%s %s: v%d 计划已生成", fm.ID, fm.Title, fm.PlanVersion)
		body = fmt.Sprintf("请审阅 v%d 计划，确认后设 plan_approved: true 并保存", fm.PlanVersion)
		if fm.PendingReq {
			body += "\n⚠️ 注意：需求文档有更新，这是基于最新需求的重新规划"
		}
	case "review":
		urgency = "normal"
		icon = "emblem-default"
		title = fmt.Sprintf("✅ T%s %s: 代码已实现", fm.ID, fm.Title)
		reviewer := fm.Reviewer
		if reviewer == "" {
			reviewer = "你"
		}
		if fm.PendingReq {
			body = fmt.Sprintf("代码已实现，但需求文档有新变更。下一步：\n"+
				"  ① 先合并当前版本：设 merge_approved: true → 自动合并\n"+
				"  ② 直接出 v%d 新计划：将 status 改为 ready\n"+
				"请 %s 根据情况选择操作", fm.PlanVersion+1, reviewer)
		} else {
			body = fmt.Sprintf("请 %s review 代码，确认无误后设 merge_approved: true", reviewer)
		}
	case "conflict":
		urgency = "critical"
		icon = "emblem-important"
		title = fmt.Sprintf("⚠️ T%s %s: 合并冲突", fm.ID, fm.Title)
		body = "自动合并失败，存在冲突文件，请手动解决"
	case "done":
		urgency = "normal"
		icon = "emblem-favorite"
		title = fmt.Sprintf("🎉 T%s %s: 已完成", fm.ID, fm.Title)
		body = "代码已合并并推送至远程仓库"
	case "implementing":
		urgency = "normal"
		icon = "emblem-system"
		title = fmt.Sprintf("⏳ T%s %s: 仍在执行中", fm.ID, fm.Title)
		body = "任务未正常结束（可能进程中断）"
	case "error", "failed":
		urgency = "critical"
		icon = "dialog-error"
		title = fmt.Sprintf("❌ T%s %s: 执行失败", fm.ID, fm.Title)
		body = "请检查日志"
	case "blocked":
		urgency = "normal"
		icon = "dialog-warning"
		title = fmt.Sprintf("⏸️ T%s %s: 已被阻塞", fm.ID, fm.Title)
		body = "缺少必填字段或被依赖阻塞，请检查 blocked_by 和必填字段"
	default:
		fmt.Fprintf(os.Stderr, "notify: unknown status %q for task %s\n", fm.Status, fm.ID)
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

// parseFile reads and parses a task document frontmatter with retry for cloud-sync filesystems.
func parseFile(path string) (*yamlfrontmatter.Frontmatter, error) {
	const maxRetries = 5
	for i := 0; i < maxRetries; i++ {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		fm, err := yamlfrontmatter.Parse(data)
		if err == nil && fm != nil {
			return fm, nil
		}
		if i < maxRetries-1 {
			time.Sleep(200 * time.Millisecond)
		}
	}
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

// SendTaskAction sends a bounded action notification with the task ID and title.
func SendTaskAction(taskID, taskTitle, emoji, title, description string) {
	prefix := fmt.Sprintf("T%s", taskID)
	if taskTitle != "" {
		prefix = fmt.Sprintf("T%s %s", taskID, taskTitle)
	}
	Send(fmt.Sprintf("%s %s: %s", emoji, prefix, title), description)
}

// SendGrillingNotification notifies the user that a task needs interactive
// grilling. Tries Kitty tab first; falls back to desktop notification.
func SendGrillingNotification(taskID, taskTitle, reqDoc, vaultPath string) {
	title := fmt.Sprintf("🟡 T%s 需要需求对齐", taskID)
	if taskTitle != "" {
		title = fmt.Sprintf("🟡 T%s %s 需要需求对齐", taskID, taskTitle)
	}
	body := fmt.Sprintf("需求文档: %s\n请在 OMP 中输入：对 %s 进行需求详细化", reqDoc, reqDoc)

	if tryKittyTab(taskID, taskTitle, reqDoc, vaultPath) {
		return
	}
	// Fallback to desktop notification
	Send(title, body)
}

// SendGrillingReminder re-notifies the user that a task is still waiting for grilling.
// Does NOT open a Kitty tab — only desktop notification.
func SendGrillingReminder(taskID, taskTitle string) {
	title := fmt.Sprintf("⏳ T%s 仍在等待需求对齐", taskID)
	if taskTitle != "" {
		title = fmt.Sprintf("⏳ T%s %s 仍在等待需求对齐", taskID, taskTitle)
	}
	Send(title, "请在终端中完成交互式 grilling 对话。完成后 daemon 自动继续。")
}

// kittyDebounce prevents flooding the user with multiple Kitty tabs.
var (
	lastKittyTab time.Time
	kittyMu      sync.Mutex
)

// tryKittyTab attempts to open a new Kitty tab for interactive grilling.
// Only one tab per 30 s to avoid flooding. Returns true if successful.
// All dynamic content is embedded directly in the shell command via %q quoting
// because kitty @ launch does NOT forward the parent process environment.
func tryKittyTab(taskID, taskTitle, reqDoc, vaultPath string) bool {
	kittyMu.Lock()
	if time.Since(lastKittyTab) < 30*time.Second {
		kittyMu.Unlock()
		return false
	}
	lastKittyTab = time.Now()
	kittyMu.Unlock()

	if _, err := exec.LookPath("kitty"); err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, "kitty", "@", "ls").Run(); err != nil {
		return false
	}

	tabTitle := fmt.Sprintf("Grilling %s", taskID)
	if taskTitle != "" {
		tabTitle = fmt.Sprintf("Grilling %s — %s", taskID, taskTitle)
	}
	if runes := []rune(tabTitle); len(runes) > 60 {
		tabTitle = string(runes[:57]) + "..."
	}

	// Build script with values embedded via Go %q (safe shell quoting).
	// Heredoc is quoted ('EOF') to prevent shell expansion.
	script := fmt.Sprintf(`cat <<'GRILLING_EOF'

╔══════════════════════════════════════════════════════════════╗
║  🟡 需求对齐 — TASK-%s: %s
║
║  需求文档: %s
║
║  OMP 正在加载需求文档并通过 requirement-elaborator
║  逐一向你提问来对齐需求细节...
╚══════════════════════════════════════════════════════════════╝

GRILLING_EOF
export OBSIDIAN_VAULT=%s
omp -p %s; exec bash`,
		taskID, taskTitle, reqDoc,
		vaultPath,
		fmt.Sprintf("%q", fmt.Sprintf("对 %s 进行需求详细化。请使用 skill://requirement-elaborator 加载需求文档，识别其中的模糊点和未明确的技术决策，逐一向我提问以达成共识。", reqDoc)),
	)

	cmd := exec.Command("kitty", "@", "launch",
		"--type=tab",
		"--title", tabTitle,
		"bash", "-c", script,
	)
	return cmd.Run() == nil
}
