// Command kitty-grill is the interactive grilling client the daemon launches
// inside a new Kitty tab. It connects to the long-lived agent-server
// (/agent/chat) and drives a one-question-at-a-time requirements interview:
// it injects the requirement-elaborator prompt, shows each model question,
// reads the human answer from stdin, and relays it back — preserving the
// conversation through the returned sessionId.
//
// The daemon is responsible for launching it via `kitty @ launch --type=tab`
// with --task/--title/--req/--vault and the resolved --provider/--model.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

type chatRequest struct {
	Message         string `json:"message"`
	Provider        string `json:"provider"`
	Model           string `json:"model"`
	ReasoningEffort string `json:"reasoningEffort,omitempty"`
	SessionID       string `json:"sessionId,omitempty"`
}

type chatResponse struct {
	Text      string `json:"text"`
	Outcome   string `json:"outcome"`
	SessionID string `json:"sessionId"`
	ErrorCode string `json:"errorCode,omitempty"`
	Error     string `json:"error,omitempty"`
}

func main() {
	var (
		taskID    = flag.String("task", "", "task ID (e.g. 057)")
		taskTitle = flag.String("title", "", "task title")
		reqDoc    = flag.String("req", "", "requirement doc path (relative to vault)")
		vaultPath = flag.String("vault", "", "obsidian vault absolute path")
		addr      = flag.String("addr", "127.0.0.1:8799", "agent-server address")
		provider  = flag.String("provider", "deepseek_magic", "DSH provider")
		model     = flag.String("model", "deepseek-v4-pro", "DSH model")
		effort    = flag.String("effort", "low", "reasoning effort for questionnaire generation (off/low/high/max)")
		custom    = flag.String("prompt", "", "custom initial prompt (overrides requirement-elaborator)")
		promptEnv = flag.String("prompt-env", "", "read the prompt from this env var (avoids shell quoting)")
		// 内部模式：异步写回。问卷提交后主进程立即退出（tab 关闭），由
		// detached 子进程携带 --writeback 完成写回，避免 tab 卡在「写回中」。
		writeback = flag.Bool("writeback", false, "internal: submit answers to an existing session and exit")
		sessionID = flag.String("session", "", "agent-server session id (writeback mode)")
		answers   = flag.String("answers", "", "user answers to submit (writeback mode)")
		ctxPath   = flag.String("ctx", "", "writeback context file (fresh-session fallback; writeback mode)")
	)
	flag.Parse()

	if *addr == "" {
		fmt.Fprintln(os.Stderr, "kitty-grill: --addr is required")
		os.Exit(2)
	}

	if *writeback {
		if err := runWriteback(*addr, *provider, *model, *effort, *sessionID, *answers, loadWritebackContext(*ctxPath)); err != nil {
			fmt.Fprintf(os.Stderr, "kitty-grill writeback: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// 启动守卫：任务已离开 needs-grilling（被关闭/完成/推进）时直接退出，
	// 不生成问卷——避免向已归档需求写回决策（观测：TASK-005 关闭后其
	// grilling tab 仍停留在交互状态）。
	if *taskID != "" {
		if left := taskLeftGrilling(*taskID, *vaultPath, *reqDoc); left {
			return
		}
	}

	prompt := *custom
	if *promptEnv != "" {
		if v := os.Getenv(*promptEnv); v != "" {
			prompt = v
		}
	}
	if prompt == "" {
		prompt = buildGrillingPrompt(*taskID, *taskTitle, *reqDoc, *vaultPath)
		// 预取需求文档 + 引用 ADR 全文注入，模型不再逐文件 read（省 60-70% 时间）。
		if ctx := prefetchContext(*vaultPath, *reqDoc); ctx != "" {
			prompt += "\n\n" + ctx
		}
	}
	if err := repl(*addr, *provider, *model, *effort, prompt, *taskID, *vaultPath, *reqDoc); err != nil {
		fmt.Fprintf(os.Stderr, "kitty-grill: %v\n", err)
		os.Exit(1)
	}
}

// adrRefRe matches ADR-<digits> references in requirement text.
var adrRefRe = regexp.MustCompile(`ADR-\d+`)

// prefetchContext reads the requirement doc and any ADRs it explicitly
// references, returning them as a context block the model can consume directly
// instead of issuing read tool calls one file at a time. Returns "" when there
// is nothing to prefetch.
func prefetchContext(vaultPath, reqDoc string) string {
	if vaultPath == "" || reqDoc == "" {
		return ""
	}
	reqContent, err := os.ReadFile(filepath.Join(vaultPath, reqDoc))
	if err != nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("以下是需求文档与相关 ADR 的全文（已提供，无需再 read）：\n\n")
	b.WriteString("=== " + reqDoc + " ===\n")
	b.WriteString(string(reqContent))
	b.WriteString("\n")

	seen := map[string]bool{}
	for _, m := range adrRefRe.FindAllString(string(reqContent), -1) {
		if seen[m] {
			continue
		}
		seen[m] = true
		if p := findADRFile(vaultPath, m); p != "" {
			if c, err := os.ReadFile(p); err == nil {
				b.WriteString("\n=== " + m + " ===\n")
				b.WriteString(string(c))
				b.WriteString("\n")
			}
		}
	}
	return b.String()
}

// findADRFile locates an ADR markdown file by its ID (e.g. ADR-005) under the
// vault's Notes/adr or any Projects/*/Notes/adr directory.
func findADRFile(vaultPath, adrID string) string {
	dirs := []string{filepath.Join(vaultPath, "Notes", "adr")}
	if entries, err := os.ReadDir(filepath.Join(vaultPath, "Projects")); err == nil {
		for _, p := range entries {
			dirs = append(dirs, filepath.Join(vaultPath, "Projects", p.Name(), "Notes", "adr"))
		}
	}
	prefixes := []string{adrID + "-", strings.ToLower(adrID) + "-"}
	for _, d := range dirs {
		entries, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range entries {
			for _, pre := range prefixes {
				if strings.HasPrefix(e.Name(), pre) {
					return filepath.Join(d, e.Name())
				}
			}
		}
	}
	return ""
}

// buildGrillingPrompt constructs the batch-questionnaire prompt: the model does
// a deep survey first, then emits every decision point with options and a
// recommendation in one structured questionnaire, so the human answers all of
// them in a single round instead of question-by-question.
func buildGrillingPrompt(taskID, taskTitle, reqDoc, vaultPath string) string {
	req := reqDoc
	if req != "" && vaultPath != "" {
		req = filepath.Join(vaultPath, reqDoc)
	}
	target := "帮我进行需求详细化"
	switch {
	case req != "":
		target = fmt.Sprintf("对 %s 进行需求详细化", req)
	case taskTitle != "":
		target = fmt.Sprintf("我要实现「%s」，请补充技术细节", taskTitle)
	}
	body := fmt.Sprintf(`%s（遵循 skill://requirement-elaborator 的方法论：事实从环境查，决策由用户定）。

交互方式改为「批量问卷」，不要逐问：
1. 勘察（精简）：读需求文档正文；仅当需求正文明确引用某 ADR / 契约 / 上游 REQ 时，再读那些被引用的文件。不要遍历 CONTEXT.md 全量术语、不要读代码实现——它们会在实现阶段按需加载。
2. 一次性提炼所有需要决策的技术点，合并同类项，控制在 5-12 个
3. 输出一个 JSON 对象（放在一个 %c%c%cjson 代码块里，代码块外不要任何文字），结构：
{
  "decisions": [
    {"id":"D1","question":"一句话问题","options":[{"id":"A","label":"一句话选项说明"},{"id":"B","label":"..."}],"recommended":"A","reason":"推荐理由（基于环境事实）"},
    ...
  ]
}
每个决策点 2-4 个选项；recommended 必须是 options 里存在的 id。

用户会一轮回复所有决策（格式 D1=A D2=B …，可附简短补充）。你收到后：
- 把每个决策写回需求文档（更新/补充 REQ 的技术规格、状态与数据、验收标准等章节）
- 输出「决策总结」：逐项列出用户选择 + 已写回位置
- 如有剩余歧义，总结末尾列「待澄清」，但不要阻塞本轮写回`, target, '`', '`', '`')
	// 真实任务 ID 前置：agent-server 监控面板按 prompt 里第一个 TASK-xxx
	// 打标签，前置可避免启发式抓到 REQ 正文/预取上下文里提到的其他任务
	// （观测：TASK-005 的 grilling 会话被误标为 TASK-058）。
	if taskID != "" {
		return fmt.Sprintf("任务 TASK-%s — %s\n\n%s", taskID, target, body)
	}
	return body
}

// questionnaire is the structured questionnaire the model emits as JSON.
type questionnaire struct {
	Decisions []decision `json:"decisions"`
}

type decision struct {
	ID          string   `json:"id"`
	Question    string   `json:"question"`
	Options     []option `json:"options"`
	Recommended string   `json:"recommended"`
	Reason      string   `json:"reason"`
}

type option struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// extractJSON pulls the first ```json fenced block out of the model reply.
// It returns the trimmed inner text and whether a block was found.
func extractJSON(text string) (string, bool) {
	start := strings.Index(text, "```json")
	if start < 0 {
		return "", false
	}
	start += len("```json")
	if i := strings.IndexByte(text[start:], '\n'); i >= 0 {
		start += i + 1
	}
	end := strings.Index(text[start:], "```")
	if end < 0 {
		return "", false
	}
	return strings.TrimSpace(text[start : start+end]), true
}

// grillingStillActive resolves the task file for taskID (project derived from
// the requirement path) and reports whether its frontmatter status is still
// needs-grilling. Unresolvable/unparseable tasks report active — the guard must
// never block legacy flows that lack a vault/task/status.
func grillingStillActive(vaultPath, reqDoc, taskID string) (bool, string) {
	if vaultPath == "" || taskID == "" {
		return true, ""
	}
	taskPath := resolveTaskFile(vaultPath, reqDoc, taskID)
	if taskPath == "" {
		return true, ""
	}
	data, err := os.ReadFile(taskPath)
	if err != nil {
		return true, ""
	}
	fm, err := yamlfrontmatter.Parse(data)
	if err != nil || fm == nil || fm.Status == "" {
		return true, ""
	}
	return fm.Status == "needs-grilling", fm.Status
}

// resolveTaskFile locates TASK-<id>[-slug].md. It first derives the project
// directory from the requirement path (Projects/<proj>/Requirements/...), then
// falls back to scanning every project's Tasks directory.
func resolveTaskFile(vaultPath, reqDoc, taskID string) string {
	if reqDoc != "" {
		rel := strings.TrimPrefix(reqDoc, "Projects/")
		if i := strings.IndexByte(rel, '/'); i > 0 {
			if p := findTaskInDir(filepath.Join(vaultPath, "Projects", rel[:i], "Tasks"), taskID); p != "" {
				return p
			}
		}
	}
	entries, err := os.ReadDir(filepath.Join(vaultPath, "Projects"))
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if p := findTaskInDir(filepath.Join(vaultPath, "Projects", e.Name(), "Tasks"), taskID); p != "" {
			return p
		}
	}
	return ""
}

func findTaskInDir(tasksDir, taskID string) string {
	entries, err := os.ReadDir(tasksDir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if name == "TASK-"+taskID+".md" || strings.HasPrefix(name, "TASK-"+taskID+"-") {
			return filepath.Join(tasksDir, name)
		}
	}
	return ""
}

// taskLeftGrilling reports whether the task moved out of needs-grilling and
// must not receive any write-back; it prints the notice and returns true when
// blocked.
func taskLeftGrilling(taskID, vaultPath, reqDoc string) bool {
	if taskID == "" {
		return false
	}
	active, status := grillingStillActive(vaultPath, reqDoc, taskID)
	if active {
		return false
	}
	fmt.Printf("\n⚠️  任务 TASK-%s 已离开 grilling 状态（当前 status=%s），决策不会写回。请直接关闭本标签页。\n", taskID, status)
	return true
}

func repl(addr, provider, model, effort, prompt, taskID, vaultPath, reqDoc string) error {
	sessionID := ""

	fmt.Printf("\n╔══════════════════════════════════════════════════════════════╗\n")
	fmt.Printf("║  🟡 需求对齐 — grilling（光标问卷）\n")
	fmt.Printf("║\n")
	fmt.Printf("║  模型列出所有决策点，你用 ↑↓ 选择、Enter 确认，一轮完成。\n")
	fmt.Printf("║  ←/h →/l 切题 · ↓/j ↑/k 选选项 · Enter 确认 · Tab 跳未答\n")
	fmt.Printf("║  q 提交写回 · Ctrl+C 退出\n")
	fmt.Printf("╚══════════════════════════════════════════════════════════════╝\n\n")

	// 阶段 1：生成问卷。
	fmt.Print("⏳ 正在深度勘察并生成问卷（可能需要 1-3 分钟）…\n\n")
	resp, err := chat(addr, provider, model, effort, sessionID, prompt, 0)
	if err != nil {
		return err
	}
	sessionID = resp.SessionID

	// 解析结构化 JSON 问卷；失败则回退打印原始文本 + 手动填写。
	raw, ok := extractJSON(resp.Text)
	if !ok {
		fmt.Println(resp.Text)
		return manualFill(addr, provider, model, effort, sessionID, taskID, vaultPath, reqDoc, prompt)
	}
	var q questionnaire
	if err := json.Unmarshal([]byte(raw), &q); err != nil || len(q.Decisions) == 0 {
		fmt.Println(resp.Text)
		return manualFill(addr, provider, model, effort, sessionID, taskID, vaultPath, reqDoc, prompt)
	}

	// 阶段 2：TUI 光标交互问卷，一轮完成所有选择。
	answers, aborted := runQuestionnaire(q.Decisions)
	if aborted {
		// 用户放弃：回收 agent-server 里的交互会话，避免僵尸条目滞留监控面板。
		closeChatSession(addr, sessionID)
		return nil
	}

	// 组装 D1=A D2=B … 提交。
	var parts []string
	for i, d := range q.Decisions {
		if i < len(answers) && answers[i] != "" {
			parts = append(parts, fmt.Sprintf("%s=%s", d.ID, answers[i]))
		}
	}
	if len(parts) == 0 {
		return nil
	}

	// 写回前复查任务状态：问卷生成 + 作答期间任务可能已被关闭/推进，
	// 写回会落到已离开 grilling 的任务上（观测：TASK-005 关闭后其问卷
	// tab 仍可提交）。此处阻断写回并提示用户。
	if taskLeftGrilling(taskID, vaultPath, reqDoc) {
		return nil
	}

	// 异步提交：detached 子进程完成写回，本进程立即退出并关 tab——不再
	// 同步等待模型写回（观测：TASK-058 提交后卡在「写回需求文档中」，
	// tab 不关闭，还连发重复的「需求变更」桌面提醒）。
	reqPath := reqDoc
	if reqPath != "" && vaultPath != "" {
		reqPath = filepath.Join(vaultPath, reqDoc)
	}
	submitAnswers(addr, provider, model, effort, sessionID, strings.Join(parts, " "), taskID, writebackContext{
		Kind:          "req",
		Target:        reqPath,
		TaskID:        taskID,
		ReqDoc:        reqDoc,
		VaultPath:     vaultPath,
		SkillPrompt:   prompt,
		Questionnaire: q,
	})
	return nil
}

// submitAnswers fires the write-back asynchronously and closes the tab. The
// write-back context (original questionnaire prompt + questionnaire JSON) is
// persisted alongside so a fresh-session fallback can rebuild context if the
// agent-server lost the session (restart between questionnaire and write-back).
// When the detached spawn fails (rare: missing executable), it falls back to a
// bounded synchronous write-back so the answers are never lost.
func submitAnswers(addr, provider, model, effort, sessionID, message, taskID string, ctx writebackContext) {
	ctx.Answers = message
	ctxPath, ctxErr := writeWritebackContext(ctx, taskID)
	if ctxErr == nil {
		if err := spawnAsyncWriteback(addr, provider, model, effort, sessionID, message, taskID, ctxPath); err == nil {
			closeOwnTab()
			return
		}
	}
	fmt.Printf("\n⏳ 后台进程启动失败，改为同步写回（最多等待 %v）…\n\n", writebackTimeout)
	if err := runWriteback(addr, provider, model, effort, sessionID, message, &ctx); err != nil {
		fmt.Printf("\n⚠️  写回失败：%v\n", err)
	}
	closeOwnTab()
}

// manualFill is the fallback when the model does not emit a parseable JSON
// questionnaire: print the raw reply and read one multi-line round of answers.
func manualFill(addr, provider, model, effort, sessionID, taskID, vaultPath, reqDoc, prompt string) error {
	fmt.Println()
	in := bufio.NewReader(os.Stdin)
	var answers []string
	for {
		fmt.Print("✍️  你的决策 > ")
		line, err := in.ReadString('\n')
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		msg := strings.TrimSpace(line)
		if msg == "" {
			if len(answers) > 0 {
				break
			}
			continue
		}
		if msg == "/quit" || msg == "/exit" {
			closeChatSession(addr, sessionID)
			return nil
		}
		answers = append(answers, msg)
	}
	if len(answers) == 0 {
		closeChatSession(addr, sessionID)
		return nil
	}
	// 写回前复查任务状态（同问卷路径：作答期间任务可能已离开 grilling）。
	if taskLeftGrilling(taskID, vaultPath, reqDoc) {
		return nil
	}
	submitAnswers(addr, provider, model, effort, sessionID, strings.Join(answers, "\n"), taskID, writebackContext{
		Kind:        "req",
		TaskID:      taskID,
		ReqDoc:      reqDoc,
		VaultPath:   vaultPath,
		SkillPrompt: prompt,
	})
	return nil
}

// writebackTimeout bounds one write-back round-trip. The interactive
// questionnaire generation stays unbounded (the user is watching), but the
// detached write-back must not run forever: it writes to a log and the user
// has already left.
const writebackTimeout = 10 * time.Minute

// writebackMaxAttempts bounds model-level retries for the write-back turn.
// 免费渠道偶发 SERVER/outcome error 时（观测：TASK-058 决策写回一次失败即
// 丢弃答案），重试同一会话同一答案（幂等）显著提高写回成功率。
const writebackMaxAttempts = 3

// writebackRetryBackoff 是写回重试间隔；测试可覆盖为 0。
var writebackRetryBackoff = 20 * time.Second

// notifyWritebackFailure 是写回最终失败时的桌面提醒；测试可覆盖。
var notifyWritebackFailure = func(err error) {
	if _, e := exec.LookPath("notify-send"); e != nil {
		return
	}
	_ = exec.Command("notify-send", "-u", "critical", "⚠️ Grilling 决策写回失败",
		fmt.Sprintf("已自动重试 %d 次仍失败（%v）。请稍后重新提交决策问卷，或直接手动编辑 Grilling-Decisions.md。", writebackMaxAttempts, err)).Run()
}

// writebackContext carries everything a fresh-session fallback needs to
// rebuild the write-back prompt when the original chat session is gone
// (agent-server restarted between questionnaire and write-back — 观测：
// TASK-058 决策写回 3 次重试全撞「session not found」)。
type writebackContext struct {
	Kind          string        `json:"kind"`             // "req" | "decision"
	Target        string        `json:"target,omitempty"` // REQ / 决策清单绝对路径
	TaskID        string        `json:"taskID,omitempty"`
	ReqDoc        string        `json:"reqDoc,omitempty"`
	VaultPath     string        `json:"vaultPath,omitempty"`
	SkillPrompt   string        `json:"skillPrompt"` // 原始问卷 prompt（含写回指令）
	Questionnaire questionnaire `json:"questionnaire,omitempty"`
	Answers       string        `json:"answers"`
}

// FreshPrompt rebuilds a self-contained write-back prompt for a new session:
// the original questionnaire instructions + the user's answers + the
// questionnaire JSON, so the model can write back without the old context.
func (c writebackContext) FreshPrompt() string {
	var b strings.Builder
	b.WriteString(c.SkillPrompt)
	b.WriteString("\n\n【无需再次生成问卷】用户已经完成问卷并提交答复：\n")
	b.WriteString(c.Answers)
	if len(c.Questionnaire.Decisions) > 0 {
		if data, err := json.Marshal(c.Questionnaire); err == nil {
			b.WriteString("\n\n问卷全文（供参考，含问题与选项）：\n```json\n")
			b.WriteString(string(data))
			b.WriteString("\n```")
		}
	}
	b.WriteString("\n\n请跳过问卷生成与提问，直接执行上面的写回指令（把答复写回需求文档/决策清单）。写回完成后输出「决策总结」即可。")
	return b.String()
}

// writeWritebackContext persists the context JSON next to the write-back logs.
func writeWritebackContext(ctx writebackContext, taskID string) (string, error) {
	logDir := writebackLogDir()
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return "", err
	}
	name := taskID
	if name == "" {
		name = "decision"
	}
	path := filepath.Join(logDir, fmt.Sprintf("writeback-ctx-%s-%s.json", name, time.Now().Format("20060102-150405")))
	data, err := json.MarshalIndent(ctx, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// loadWritebackContext reads a context file; nil when absent/broken/empty.
func loadWritebackContext(path string) *writebackContext {
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var ctx writebackContext
	if err := json.Unmarshal(data, &ctx); err != nil || ctx.Answers == "" {
		return nil
	}
	return &ctx
}

// sessionGoneEvidence reports whether an error proves the target chat session
// no longer exists. Only permanent-loss evidence stops same-session retries
// and switches to the fresh-session fallback; transient errors keep retrying.
func sessionGoneEvidence(errText string) bool {
	lower := strings.ToLower(errText)
	hasSession := strings.Contains(lower, "session")
	hasNotFound := strings.Contains(lower, "not found") ||
		strings.Contains(lower, "no such session") ||
		strings.Contains(lower, "does not exist")
	return hasSession && hasNotFound
}

// runWriteback is the --writeback mode body: re-attach to the session the
// questionnaire left behind and submit the answers. It is executed by the
// detached child process; stdout/stderr go to a log file. On success the chat
// session is closed so the agent-server monitor does not accumulate zombies.
//
// Failure classification:
//   - transient (SERVER / unreachable / unknown)：同一会话重试
//     writebackMaxAttempts 次（幂等）；
//   - session gone（session not found 等）：重试无意义，改用 ctx 重建的
//     全新会话完成写回（fresh-session fallback）；
//   - 全部路径失败：桌面提醒，不再静默丢答案。
func runWriteback(addr, provider, model, effort, sessionID, answers string, ctx *writebackContext) error {
	if answers == "" {
		return fmt.Errorf("writeback mode requires --answers")
	}
	if sessionID == "" {
		// 罕见：问卷会话从未建立（agent-server 启动失败等）→ 直接 fresh。
		if ctx == nil {
			return fmt.Errorf("writeback mode requires --session")
		}
		if err := freshWriteback(addr, provider, model, effort, ctx); err != nil {
			notifyWritebackFailure(err)
			return err
		}
		return nil
	}
	var lastErr error
	for attempt := 1; attempt <= writebackMaxAttempts; attempt++ {
		resp, err := chat(addr, provider, model, effort, sessionID, answers, writebackTimeout)
		if err == nil {
			fmt.Printf("writeback ok (attempt %d): session=%s\n%s\n", attempt, sessionID, resp.Text)
			closeChatSession(addr, sessionID)
			return nil
		}
		lastErr = err
		if sessionGoneEvidence(err.Error()) {
			fmt.Printf("writeback: session %s gone (%v) — retries are futile, falling back to a fresh session\n", sessionID, err)
			break
		}
		fmt.Printf("writeback attempt %d/%d failed: %v\n", attempt, writebackMaxAttempts, err)
		if attempt < writebackMaxAttempts {
			time.Sleep(writebackRetryBackoff)
		}
	}
	if ctx != nil {
		if err := freshWriteback(addr, provider, model, effort, ctx); err != nil {
			lastErr = fmt.Errorf("session lost and fresh fallback failed: %w", err)
		} else {
			return nil
		}
	}
	notifyWritebackFailure(lastErr)
	return lastErr
}

// freshWriteback starts a brand-new chat session with the rebuilt self-
// contained prompt (original questionnaire instructions + answers + questions)
// and closes it afterwards. No resume dependency on the lost session.
func freshWriteback(addr, provider, model, effort string, ctx *writebackContext) error {
	prompt := ctx.FreshPrompt()
	resp, err := chat(addr, provider, model, effort, "", prompt, writebackTimeout)
	if err != nil {
		return err
	}
	fmt.Printf("writeback ok via fresh session: session=%s\n%s\n", resp.SessionID, resp.Text)
	closeChatSession(addr, resp.SessionID)
	return nil
}

// closeChatSession tells the agent-server to cancel a chat session (POST
// /agent/close). Best-effort and silent: the server also lazily recycles
// idle chat sessions, this is just the prompt path. run sessions are
// unaffected (the server tracks chat sessions separately).
func closeChatSession(addr, sessionID string) {
	if addr == "" || sessionID == "" {
		return
	}
	body, err := json.Marshal(struct {
		SessionID string `json:"sessionId"`
	}{sessionID})
	if err != nil {
		return
	}
	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequest(http.MethodPost, "http://"+addr+"/agent/close", bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("content-type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
}

// writebackLogDir returns the directory for detached write-back logs.
func writebackLogDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(os.TempDir(), "kitty-grill-logs")
	}
	return filepath.Join(home, ".dsh", "logs", "kitty-grill")
}

// writebackArgs builds the argv for the detached --writeback child.
func writebackArgs(exe, addr, provider, model, effort, sessionID, message, ctxPath string) []string {
	return []string{
		exe,
		"--writeback",
		"--addr", addr,
		"--provider", provider,
		"--model", model,
		"--effort", effort,
		"--session", sessionID,
		"--answers", message,
		"--ctx", ctxPath,
	}
}

// spawnAsyncWriteback starts a detached --writeback child (setsid) that
// re-attaches to the session and performs the write-back after this process
// exits. Returns an error only when the child cannot be started.
func spawnAsyncWriteback(addr, provider, model, effort, sessionID, message, taskID, ctxPath string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	logDir := writebackLogDir()
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return err
	}
	name := taskID
	if name == "" {
		name = "decision"
	}
	logPath := filepath.Join(logDir, fmt.Sprintf("writeback-%s-%s.log", name, time.Now().Format("20060102-150405")))
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, writebackArgs(exe, addr, provider, model, effort, sessionID, message, ctxPath)[1:]...)
	cmd.Stdin = nil
	cmd.Stdout = f
	cmd.Stderr = f
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		f.Close()
		return err
	}
	fmt.Printf("\n✅ 决策已提交，后台异步写回需求文档（进度见 %s），本标签页即将关闭。\n", logPath)
	// 关闭日志句柄：子进程继承 fd，父进程无需等待。
	f.Close()
	return nil
}

// closeTabArgs builds the kitty remote-control argv for closing this window.
func closeTabArgs(windowID string) []string {
	return []string{"kitty", "@", "close-window", "--match", "id:" + windowID}
}

// closeOwnTab closes the kitty tab this questionnaire runs in via remote
// control, using KITTY_WINDOW_ID injected by kitty into launched windows.
// Silent no-op when not running inside kitty (manual runs, tests) or when
// kitty is unavailable — the daemon's debounce re-opens a tab when needed.
func closeOwnTab() {
	windowID := os.Getenv("KITTY_WINDOW_ID")
	if windowID == "" {
		return
	}
	if _, err := exec.LookPath("kitty"); err != nil {
		return
	}
	env := os.Environ()
	if os.Getenv("KITTY_LISTEN_ON") == "" {
		if entries, err := os.ReadDir("/tmp"); err == nil {
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), "kitty-") {
					env = append(env, "KITTY_LISTEN_ON=unix:/tmp/"+e.Name())
					break
				}
			}
		}
	}
	cmd := exec.Command("kitty", closeTabArgs(windowID)[1:]...)
	cmd.Env = env
	if err := cmd.Run(); err != nil {
		fmt.Printf("\n⚠️  自动关闭标签页失败（%v），请手动关闭。\n", err)
	}
}

// chat sends one message to the agent-server and returns the model reply.
// timeout == 0 means no timeout (interactive questionnaire generation).
func chat(addr, provider, model, effort, sessionID, message string, timeout time.Duration) (*chatResponse, error) {
	body, err := json.Marshal(chatRequest{
		Message:         message,
		Provider:        provider,
		Model:           model,
		ReasoningEffort: effort,
		SessionID:       sessionID,
	})
	if err != nil {
		return nil, err
	}

	client := &http.Client{Timeout: timeout} // 交互问卷 0=不限；写回用 writebackTimeout
	req, err := http.NewRequest(http.MethodPost, "http://"+addr+"/agent/chat", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("content-type", "application/json")

	res, err := client.Do(req)
	if err != nil {
		if os.IsTimeout(err) {
			return nil, fmt.Errorf("agent-server write-back timed out after %v: %w", timeout, err)
		}
		return nil, fmt.Errorf("agent-server unreachable: %w", err)
	}
	defer res.Body.Close()

	data, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("agent-server HTTP %d: %s", res.StatusCode, strings.TrimSpace(string(data)))
	}

	var out chatResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("agent-server bad response: %w", err)
	}
	if out.Outcome != "completed" {
		return nil, fmt.Errorf("agent-server outcome %s: %s", out.Outcome, out.ErrorCode)
	}
	if out.SessionID == "" {
		return nil, fmt.Errorf("agent-server returned no sessionId")
	}
	return &out, nil
}
