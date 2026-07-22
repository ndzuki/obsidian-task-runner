// Package notify provides desktop notifications.
package notify

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"syscall"

	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

// StatusNotify sends a desktop notification for a task status change.
func StatusNotify(taskPath string, notifyEnabled bool) {
	if !notifyEnabled {
		return
	}
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
	case "refining":
		urgency = "low"
		icon = "emblem-system"
		title = fmt.Sprintf("🔍 T%s %s: 需求成熟度检查中", fm.ID, fm.Title)
		body = "正在 headless 评估需求规格成熟度"
	case "planning":
		urgency = "low"
		icon = "emblem-system"
		title = fmt.Sprintf("📝 T%s %s: 计划生成中", fm.ID, fm.Title)
		body = "正在生成版本化实现计划"
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
	var fm *yamlfrontmatter.Frontmatter
	var lastErr error
	for i := range maxRetries {
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, err
			}
			fm = nil
			lastErr = err
		} else {
			fm, lastErr = yamlfrontmatter.Parse(data)
			if lastErr == nil && fm != nil {
				return fm, nil
			}
		}
		if i < maxRetries-1 {
			time.Sleep(200 * time.Millisecond)
		}
	}
	return fm, lastErr
}

// Send sends a generic notification.
func Send(title, body string, notifyEnabled bool) {
	if !notifyEnabled {
		return
	}
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
func SendTaskAction(taskID, taskTitle, emoji, title, description string, notifyEnabled bool) {
	if !notifyEnabled {
		return
	}
	prefix := fmt.Sprintf("T%s", taskID)
	if taskTitle != "" {
		prefix = fmt.Sprintf("T%s %s", taskID, taskTitle)
	}
	Send(fmt.Sprintf("%s %s: %s", emoji, prefix, title), description, notifyEnabled)
}

// SendGrillingNotification notifies the user that a task needs interactive
// grilling. Tries Kitty tab first; falls back to desktop notification.
func SendGrillingNotification(taskID, taskTitle, reqDoc, vaultPath string, notifyEnabled bool) {
	if !notifyEnabled {
		return
	}
	title := fmt.Sprintf("🟡 T%s 需要需求对齐", taskID)
	if taskTitle != "" {
		title = fmt.Sprintf("🟡 T%s %s 需要需求对齐", taskID, taskTitle)
	}
	body := fmt.Sprintf("需求文档: %s\n请在 OMP 中输入：对 %s 进行需求详细化", reqDoc, reqDoc)

	if tryKittyTab(taskID, taskTitle, reqDoc, vaultPath) {
		return
	}
	// Fallback to desktop notification
	Send(title, body, notifyEnabled)
}

// SendGrillingReminder re-notifies the user that a task is still waiting for grilling.
// Also tries to open a Kitty tab (debounced) in case the user missed the first one.
func SendGrillingReminder(taskID, taskTitle, reqDoc, vaultPath string, notifyEnabled bool) {
	if !notifyEnabled {
		return
	}
	if tryKittyTab(taskID, taskTitle, reqDoc, vaultPath) {
		return
	}
	title := fmt.Sprintf("⏳ T%s 仍在等待需求对齐", taskID)
	if taskTitle != "" {
		title = fmt.Sprintf("⏳ T%s %s 仍在等待需求对齐", taskID, taskTitle)
	}
	Send(title, "请在终端中完成交互式 grilling 对话。完成后 daemon 自动继续。", notifyEnabled)
}

// kittyDebounce uses a file-based timestamp so the debounce survives daemon
// restarts. Without this, every daemon restart triggers a new tab.
var kittyDebounceFile = func() string {
	if user := os.Getenv("USER"); user != "" {
		return filepath.Join(os.TempDir(), "otg-kitty-grilling-"+user+".lock")
	}
	if homeDir, err := os.UserHomeDir(); err == nil {
		return filepath.Join(homeDir, ".otg-kitty-grilling.lock")
	}
	return filepath.Join(os.TempDir(), "otg-kitty-grilling.lock")
}()
const kittyDebounceInterval = 5 * time.Minute

func tryKittyTab(taskID, taskTitle, reqDoc, vaultPath string) bool {
	// Acquire file lock to prevent concurrent tab creation
	lockFile, err := os.OpenFile(kittyDebounceFile, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		log.Printf("grilling tab: cannot open lock: %v", err)
		return false
	}
	defer lockFile.Close()
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX); err != nil {
		log.Printf("grilling tab: cannot acquire lock: %v", err)
		return false
	}
	defer syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)

	// Debounce: skip if last tab was created within 5 minutes
	if data, err := os.ReadFile(kittyDebounceFile); err == nil {
		if t, err := time.Parse(time.RFC3339, strings.TrimSpace(string(data))); err == nil {
			if time.Since(t) < kittyDebounceInterval {
				log.Printf("grilling tab: debounced (last was %v ago)", time.Since(t))
				return false
			}
		}
	}
	os.WriteFile(kittyDebounceFile, []byte(time.Now().Format(time.RFC3339)), 0644)

	if _, err := exec.LookPath("kitty"); err != nil {
		log.Printf("grilling tab: kitty not in PATH: %v", err)
		return false
	}
	// Detect Kitty socket — systemd services lack KITTY_LISTEN_ON env var.
	kittyEnv := os.Environ()
	if os.Getenv("KITTY_LISTEN_ON") == "" {
		if entries, err := os.ReadDir("/tmp"); err == nil {
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), "kitty-") {
					kittyEnv = append(kittyEnv, "KITTY_LISTEN_ON=unix:/tmp/"+e.Name())
					log.Printf("grilling tab: auto-detected kitty socket %s", e.Name())
					break
				}
			}
		} else {
			log.Printf("grilling tab: cannot read /tmp: %v", err)
		}
	} else {
		log.Printf("grilling tab: KITTY_LISTEN_ON already set")
	}

	tabTitle := fmt.Sprintf("Grilling %s", taskID)
	if taskTitle != "" {
		tabTitle = fmt.Sprintf("Grilling %s — %s", taskID, taskTitle)
	}
	if runes := []rune(tabTitle); len(runes) > 60 {
		tabTitle = string(runes[:57]) + "..."
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	lsCmd := exec.CommandContext(ctx, "kitty", "@", "ls")
	lsCmd.Env = kittyEnv
	lsOutput, err := lsCmd.Output()
	if err != nil {
		log.Printf("grilling tab: kitty @ ls failed: %v", err)
		return false
	}
	// At most one Grilling session at any time — prevent flooding tabs.
	if strings.Contains(string(lsOutput), "Grilling") {
		log.Printf("grilling tab: another Grilling session is active, skipping %s", taskID)
		return true // don't create duplicate — wait for active session to finish
	}

	var prompt string
	if reqDoc != "" {
		prompt = fmt.Sprintf("对 %s 进行需求详细化。请使用 skill://requirement-elaborator 加载需求文档，识别其中的模糊点和未明确的技术决策，逐一向我提问以达成共识。", filepath.Join(vaultPath, reqDoc))
	} else {
		prompt = "请使用 skill://requirement-elaborator 帮我进行需求详细化。先询问我要实现什么功能，然后逐一向我追问技术细节以达成共识。"
	}

	tid := taskID; if tid == "" { tid = "?" }
	ttl := taskTitle; if ttl == "" { ttl = "(no title)" }
	rd := reqDoc; if rd == "" { rd = "(未指定)" }
	script := fmt.Sprintf(`cat <<'GRILLING_EOF'

╔══════════════════════════════════════════════════════════════╗
║  🟡 需求对齐 — TASK-%s: %s
║
║  需求文档: %s
║
║  OMP 正在加载 requirement-elaborator 并主动向你提问…
╚══════════════════════════════════════════════════════════════╝

GRILLING_EOF
exec omp %s`, tid, ttl, rd, fmt.Sprintf("%q", prompt))
	cmd := exec.Command("kitty", "@", "launch",
		"--type=tab",
		"--title", tabTitle,
		"bash", "-c", script,
	)
	cmd.Env = append(kittyEnv, "OBSIDIAN_VAULT="+vaultPath)
	return cmd.Run() == nil
}
