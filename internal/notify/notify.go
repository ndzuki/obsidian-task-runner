// Package notify provides desktop notifications.
package notify

import (
	"encoding/json"
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
		} else if fm.PhaseErrorCode != "" {
			urgency = "critical"
			icon = "emblem-important"
			title = fmt.Sprintf("⚠️ T%s %s: 自动合并被中止", fm.ID, fm.Title)
			body = fmt.Sprintf("原因：%s\n请 %s 处理后设 merge_approved: true 重新授权合并", fm.PhaseError, reviewer)
		} else if fm.AutoMerge {
			body = fmt.Sprintf("代码已实现并通过检查，正在自动合并（%s 无需操作）", reviewer)
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
		if fm.PhaseErrorCode != "" {
			// A phase actually failed/interrupted — surface the cause, not a
			// generic "may have crashed" guess.
			urgency = "critical"
			icon = "dialog-error"
			title = fmt.Sprintf("⚠️ T%s %s: 实现会话异常", fm.ID, fm.Title)
			body = fmt.Sprintf("阶段错误：%s（%s）", fm.PhaseError, fm.PhaseErrorCode)
		} else {
			// Normal completion that left the task implementing (e.g. an
			// entry-gate re-verification round): the session ended fine; the
			// task is waiting on an upstream fact. The old copy ("任务未正常
			// 结束（可能进程中断）") was misleading — it fired on every
			// completed implementing session, not only on crashes.
			body = "实现会话正常结束，等待上游依赖/门禁（daemon 冷却后自动复验）"
		}
	case "error", "failed":
		urgency = "critical"
		icon = "dialog-error"
		title = fmt.Sprintf("❌ T%s %s: 执行失败", fm.ID, fm.Title)
		body = "请检查日志"
	case "blocked":
		urgency = "normal"
		icon = "dialog-warning"
		title = fmt.Sprintf("⏸️ T%s %s: 已被阻塞", fm.ID, fm.Title)
		switch fm.PhaseErrorCode {
		case "PREREQUISITE_SMOKE_FAILED":
			// Entry gate: the blocker is an upstream fact (dependency PR
			// merged / task done), which the daemon re-checks every scan —
			// no user action needed beyond merging the upstream PR.
			body = "前置门禁未通过：上游依赖事实未收敛（PR 未合入/依赖未 done），daemon 按事实自动恢复中，无需人工"
		case "PHASE_INTERRUPTED":
			body = "阶段被 daemon 重启中断，重启后自动恢复"
		case "MODEL_QUOTA_EXHAUSTED":
			body = "模型额度耗尽，daemon 已停止自动重试——请检查 vault-map.json 的 models 配置或更换任务 assignee 后 resume_approved=true 恢复"
		case "MODEL_FAILED", "PHASE_TIMEOUT":
			body = "阶段执行失败（" + fm.PhaseErrorCode + "），daemon 自动重试中"
		default:
			body = "缺少必填字段或被依赖阻塞，请检查 blocked_by 和必填字段"
		}
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
	case "closed":
		urgency = "normal"
		icon = "emblem-default"
		title = fmt.Sprintf("🚫 T%s %s: 已关闭", fm.ID, fm.Title)
		body = fmt.Sprintf("关闭原因: %s", fm.ClosureReason)
		if fm.ClosureNote != "" {
			body += fmt.Sprintf("\n备注: %s", fm.ClosureNote)
		}
	default:
		fmt.Fprintf(os.Stderr, "notify: unknown status %q for task %s\n", fm.Status, fm.ID)
		return
	}

	cmd := exec.Command("notify-send",
		"--urgency="+urgency,
		"--app-name=Obsidian Task Runner",
		"--icon="+icon,
		title, body,
	)
	if err := cmd.Run(); err != nil {
		log.Printf("notify: notify-send failed: %v", err)
	}
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
		"--app-name=Obsidian Task Runner",
		title, body,
	)
	if err := cmd.Run(); err != nil {
		log.Printf("notify: notify-send failed: %v", err)
	}
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
// grilling. Tries Kitty tab first (always); falls back to desktop notification
// only if Kitty is unavailable and desktop notifications are enabled.
func SendGrillingNotification(taskID, taskTitle, reqDoc, vaultPath, addr, provider, model string, notifyEnabled bool) {
	title := fmt.Sprintf("🟡 T%s 需要需求对齐", taskID)
	if taskTitle != "" {
		title = fmt.Sprintf("🟡 T%s %s 需要需求对齐", taskID, taskTitle)
	}
	body := fmt.Sprintf("需求文档: %s\n请在 DSH 会话中发起：对 %s 进行需求详细化", reqDoc, reqDoc)

	if tryKittyTab(taskID, taskTitle, reqDoc, vaultPath, addr, provider, model) {
		return
	}
	// Fallback to desktop notification
	Send(title, body, notifyEnabled)
}

// SendGrillingReminder re-notifies the user that a task is still waiting for grilling.
// Kitty always attempted; desktop only if enabled.
func SendGrillingReminder(taskID, taskTitle, reqDoc, vaultPath, addr, provider, model string, notifyEnabled bool) {
	if tryKittyTab(taskID, taskTitle, reqDoc, vaultPath, addr, provider, model) {
		return
	}
	if !notifyEnabled {
		return
	}
	title := fmt.Sprintf("⏳ T%s 仍在等待需求对齐", taskID)
	if taskTitle != "" {
		title = fmt.Sprintf("⏳ T%s %s 仍在等待需求对齐", taskID, taskTitle)
	}
	Send(title, "请在终端中完成交互式 grilling 对话。完成后 daemon 自动继续。", notifyEnabled)
}

// kittyDebounceDir 返回 grilling debounce 文件目录（XDG cache 下）。
// 与任务 frontmatter 锁一致：不放在 /tmp——tmpfs 挂载且无 swap 时，
// 残留文件占不可回收内存（2026-08-14 实测系统 OOM 的直接推手之一）。
func kittyDebounceDir() string {
	cacheDir, err := os.UserCacheDir()
	if err != nil || cacheDir == "" {
		cacheDir = os.TempDir()
	}
	dir := filepath.Join(cacheDir, "otg", "locks")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return os.TempDir()
	}
	return dir
}

// CleanStaleKittyDebounceFiles 删除超过 24h 且无人持有的 grilling
// debounce 文件。debounce 窗口只有 5 分钟，24h 前的文件内容必然已过期
// （跨重启的 debounce 语义不受影响——删除与否行为一致），flock 探测
// 防清理瞬间被持有。daemon 启动 + 周期 ticker 调用，防无限累积。
func CleanStaleKittyDebounceFiles() {
	dir := kittyDebounceDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-24 * time.Hour)
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "otg-kitty-grilling-") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		f, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			continue
		}
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			_ = f.Close()
			continue // 仍被持有
		}
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
		_ = os.Remove(path)
	}
}

// kittyDebounce uses a file-based timestamp so the debounce survives daemon
// restarts. Without this, every daemon restart triggers a new tab.
func kittyDebounceFile(taskID string) string {
	if user := os.Getenv("USER"); user != "" {
		return filepath.Join(kittyDebounceDir(), fmt.Sprintf("otg-kitty-grilling-%s-%s.lock", user, taskID))
	}
	if homeDir, err := os.UserHomeDir(); err == nil {
		return filepath.Join(homeDir, fmt.Sprintf(".otg-kitty-grilling-%s.lock", taskID))
	}
	return filepath.Join(kittyDebounceDir(), fmt.Sprintf("otg-kitty-grilling-%s.lock", taskID))
}

const kittyDebounceInterval = 5 * time.Minute

// kittyAvailable reports whether the kitty binary is reachable.
func kittyAvailable() bool {
	_, err := exec.LookPath("kitty")
	return err == nil
}

// grillExecPath resolves the absolute path of the kitty-grill client so tabs
// launched via kitty @ launch survive the kitty server's own environment:
// launched children inherit the kitty instance's PATH (not the daemon's),
// which often lacks kitty-grill. Falls back to the bare name when not
// resolvable here.
func grillExecPath() string {
	path, err := exec.LookPath("kitty-grill")
	if err != nil {
		log.Printf("grilling tab: kitty-grill not in daemon PATH: %v (falling back to bare kitty-grill)", err)
		return "kitty-grill"
	}
	return path
}

// kittyLaunchEnv returns the environment for kitty control commands,
// auto-detecting the socket when KITTY_LISTEN_ON is unset (systemd services
// lack it).
func kittyLaunchEnv() []string {
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
	return kittyEnv
}

func tryKittyTab(taskID, taskTitle, reqDoc, vaultPath, addr, provider, model string) bool {
	// Acquire file lock to prevent concurrent tab creation
	lockFile, err := os.OpenFile(kittyDebounceFile(taskID), os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		log.Printf("grilling tab: cannot open lock: %v", err)
		return false
	}
	defer func() {
		if err := lockFile.Close(); err != nil {
			log.Printf("grilling tab: close lock file: %v", err)
		}
	}()
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX); err != nil {
		log.Printf("grilling tab: cannot acquire lock: %v", err)
		return false
	}
	defer func() {
		if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN); err != nil {
			log.Printf("grilling tab: unlock lock file: %v", err)
		}
	}()

	// Debounce: skip if last tab was created within 5 minutes
	if data, err := os.ReadFile(kittyDebounceFile(taskID)); err == nil {
		if t, err := time.Parse(time.RFC3339, strings.TrimSpace(string(data))); err == nil {
			if time.Since(t) < kittyDebounceInterval {
				log.Printf("grilling tab: debounced (last was %v ago), tab already exists", time.Since(t))
				return true
			}
		}
	}
	if err := os.WriteFile(kittyDebounceFile(taskID), []byte(time.Now().Format(time.RFC3339)), 0644); err != nil {
		log.Printf("grilling tab: write debounce timestamp: %v", err)
	}

	if !kittyAvailable() {
		log.Printf("grilling tab: kitty not in PATH: %v", exec.ErrNotFound)
		return false
	}
	kittyEnv := kittyLaunchEnv()

	tabTitle := fmt.Sprintf("Grilling %s", taskID)
	if taskTitle != "" {
		tabTitle = fmt.Sprintf("Grilling %s — %s", taskID, taskTitle)
	}
	if runes := []rune(tabTitle); len(runes) > 60 {
		tabTitle = string(runes[:57]) + "..."
	}

	lsCmd := exec.Command("kitty", "@", "ls")
	lsCmd.Env = kittyEnv
	lsOutput, err := lsCmd.Output()
	if err != nil {
		// kitty @ ls failed — the debounce timestamp was just written by this
		// call, so the old dedup check here was always fresh and silently
		// swallowed ls failures (no tab, no desktop fallback). Report and let
		// the caller fall back to a desktop notification; the daemon's
		// in-memory reminder debounce still limits frequency.
		log.Printf("grilling tab: kitty @ ls failed: %v", err)
		return false
	}
	shouldLaunch, err := shouldLaunchKittyTab(lsOutput, taskID, tabTitle)
	if err != nil {
		log.Printf("grilling tab: cannot inspect existing tabs for %s: %v", taskID, err)
		return false
	}
	if !shouldLaunch {
		log.Printf("grilling tab: existing tab for %s, skipping", taskID)
		return true
	}

	tid := taskID
	if tid == "" {
		tid = "?"
	}
	ttl := taskTitle
	if ttl == "" {
		ttl = "(no title)"
	}
	rd := reqDoc
	if rd == "" {
		rd = "(未指定)"
	}
	script := fmt.Sprintf(`cat <<'GRILLING_EOF'

╔══════════════════════════════════════════════════════════════╗
║  🟡 需求对齐 — TASK-%s: %s
║
║  需求文档: %s
║
║  DSH 正在加载 requirement-elaborator 并主动向你提问…
╚══════════════════════════════════════════════════════════════╝

GRILLING_EOF
exec %s --task %s --title %s --req %s --vault %s --addr %s --provider %s --model %s --effort high`, tid, ttl, rd, grillExecPath(),
		fmt.Sprintf("%q", tid), fmt.Sprintf("%q", ttl), fmt.Sprintf("%q", rd), fmt.Sprintf("%q", vaultPath),
		fmt.Sprintf("%q", addr), fmt.Sprintf("%q", provider), fmt.Sprintf("%q", model))
	cmd := exec.Command("kitty", "@", "launch",
		"--type=tab",
		"--title", tabTitle,
		"bash", "-c", script,
	)
	cmd.Env = append(kittyEnv, "OBSIDIAN_VAULT="+vaultPath)
	if err := cmd.Run(); err != nil {
		log.Printf("grilling tab: kitty @ launch failed for %s: %v", taskID, err)
		return false
	}
	return true
}

// TryKittyDecisionTab opens a Kitty tab with an interactive execution session that
// walks the user through the pending decision points of the project-level
// Grilling-Decisions.md — answers are written back to the list file directly,
// and the daemon's answer-hash change detection picks them up for automatic
// distribution. One tab per project (debounce key = decisions-<project>), so
// parked tasks share a single interaction surface instead of one per task.
func TryKittyDecisionTab(project, listPath, vaultPath, addr, provider, model string) bool {
	if !kittyAvailable() {
		return false
	}
	debounceKey := "decisions-" + project
	lockFile, err := os.OpenFile(kittyDebounceFile(debounceKey), os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		log.Printf("decision tab: cannot open lock: %v", err)
		return false
	}
	defer func() { _ = lockFile.Close() }()
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX); err != nil {
		log.Printf("decision tab: cannot acquire lock: %v", err)
		return false
	}
	defer func() { _ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN) }()

	if data, err := os.ReadFile(kittyDebounceFile(debounceKey)); err == nil {
		if t, err := time.Parse(time.RFC3339, strings.TrimSpace(string(data))); err == nil && time.Since(t) < kittyDebounceInterval {
			log.Printf("decision tab: debounced for %s (tab likely open)", project)
			return true
		}
	}
	if err := os.WriteFile(kittyDebounceFile(debounceKey), []byte(time.Now().Format(time.RFC3339)), 0644); err != nil {
		log.Printf("decision tab: write debounce timestamp: %v", err)
	}

	kittyEnv := kittyLaunchEnv()
	tabTitle := "决策清单 — " + project
	if runes := []rune(tabTitle); len(runes) > 60 {
		tabTitle = string(runes[:57]) + "..."
	}

	lsCmd := exec.Command("kitty", "@", "ls")
	lsCmd.Env = kittyEnv
	lsOutput, lsErr := lsCmd.Output()
	if lsErr == nil {
		if exists, _ := kittyTabExists(lsOutput, "decisions", tabTitle); exists {
			log.Printf("decision tab: existing tab for %s, skipping", project)
			return true
		}
	} else {
		log.Printf("decision tab: kitty @ ls failed for %s: %v", project, lsErr)
	}

	prompt := decisionTabPrompt(listPath)
	// 决策 tab 的 prompt 必须在被启动的 bash 脚本内导出，不能依赖
	// `kitty @ launch` 客户端进程的环境变量：kitty 远程启动的子进程继承
	// 的是 kitty 服务端的环境，而不是 `kitty @` 客户端进程的 env
	// （2026-09-01 事故：KITTY_GRILL_PROMPT 丢失 → kitty-grill 回退到
	// 无目标泛化问卷 → 模型自行选中已 done 的 REQ-025 写回，误触发
	// done 任务重开）。heredoc 带引号定界符（'PROMPT_EOF'）保证 prompt
	// 中的反引号/引号原样传入，不做任何 shell 展开。
	script := decisionTabScript(project, listPath, prompt, addr, provider, model)
	cmd := exec.Command("kitty", "@", "launch",
		"--type=tab",
		"--title", tabTitle,
		"bash", "-c", script,
	)
	cmd.Env = append(kittyEnv, "OBSIDIAN_VAULT="+vaultPath)
	if err := cmd.Run(); err != nil {
		log.Printf("decision tab: kitty @ launch failed for %s: %v", project, err)
		return false
	}
	return true
}

// decisionTabPrompt builds the questionnaire prompt for the project-level
// decision list tab. Kept as a function so the launch script can embed it
// and tests can assert its shape.
func decisionTabPrompt(listPath string) string {
	return fmt.Sprintf(`请审阅项目级决策清单：%s 的待答决策点（「决策:」为空或占位符的 D-n）。

输出一个 JSON 问卷（放在一个 `+"```json"+` 代码块里，代码块外不要任何文字），结构：
{"decisions":[
  {"id":"D-14","question":"一句话问题（冲突核心）","options":[{"id":"A","label":"选项说明（从建议提炼）"},{"id":"B","label":"..."}],"recommended":"A","reason":"推荐理由（一句话）"},
  ...
]}
每个待答决策点一个条目：id 用清单的 D-n；options 从该点的「建议」提炼 2-4 个选项，recommended 用建议里标「推荐」的那项。

用户会一轮回复所有决策（每行一个 D-n=<答案>；答案通常是选项 id，也可能是用户的自填文本——自填文本可跨多行，直到下一个 D-n= 行）。你收到后：
1. 把每个决策写回清单对应「- 决策: {答案}」行（直接编辑文件正文，保持格式与顺序）；选项 id 写该选项对应内容，自填文本原样写入。「决策:」行必须保持单行：若自填文本含换行，把换行替换为空格并保持内容语义（curl 请保持单行原文）；
2. 全部填完设 frontmatter grill_continue: true（daemon 据此自动分发）；
3. 输出「决策总结」逐项列出已填项。`, listPath)
}

// decisionTabScript renders the bash script the decision tab executes. The
// prompt is exported INSIDE the script via a quoted heredoc so it survives
// `kitty @ launch` regardless of the client process environment.
func decisionTabScript(project, listPath, prompt, addr, provider, model string) string {
	return fmt.Sprintf(`cat <<'GRILLING_EOF'

╔══════════════════════════════════════════════════════════════╗
║  📋 项目级决策清单 — %s
║
║  %s
║
║  DSH 正在逐项向你提问待答决策点…
╚══════════════════════════════════════════════════════════════╝

GRILLING_EOF
export KITTY_GRILL_PROMPT=$(cat <<'PROMPT_EOF'
%s
PROMPT_EOF
)
exec %s --prompt-env KITTY_GRILL_PROMPT --addr %s --provider %s --model %s`, project, listPath, prompt, grillExecPath(),
		fmt.Sprintf("%q", addr), fmt.Sprintf("%q", provider), fmt.Sprintf("%q", model))
}

type kittyOSWindow struct {
	Tabs []kittyTab `json:"tabs"`
}

type kittyTab struct {
	Title   string        `json:"title"`
	Windows []kittyWindow `json:"windows"`
}

type kittyWindow struct {
	Title string `json:"title"`
}

func shouldLaunchKittyTab(output []byte, taskID, tabTitle string) (bool, error) {
	exists, err := kittyTabExists(output, taskID, tabTitle)
	if err != nil {
		return false, err
	}
	return !exists, nil
}

func kittyTabExists(output []byte, taskID, tabTitle string) (bool, error) {
	var osWindows []kittyOSWindow
	if err := json.Unmarshal(output, &osWindows); err != nil {
		return false, fmt.Errorf("parse kitty @ ls output: %w", err)
	}

	taskPrefix := "Grilling " + taskID
	for _, osWindow := range osWindows {
		for _, tab := range osWindow.Tabs {
			if kittyTitleMatchesTask(tab.Title, taskPrefix, tabTitle) {
				return true, nil
			}
			for _, window := range tab.Windows {
				if kittyTitleMatchesTask(window.Title, taskPrefix, tabTitle) {
					return true, nil
				}
			}
		}
	}
	return false, nil
}

func kittyTitleMatchesTask(current, taskPrefix, tabTitle string) bool {
	if current == tabTitle {
		return true
	}
	return taskPrefix != "Grilling " &&
		(strings.HasPrefix(current, taskPrefix+" ") || strings.HasPrefix(current, taskPrefix+" —"))
}
