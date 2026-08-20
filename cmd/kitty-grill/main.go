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
	"path/filepath"
	"strings"
)

type chatRequest struct {
	Message   string `json:"message"`
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	SessionID string `json:"sessionId,omitempty"`
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
		custom    = flag.String("prompt", "", "custom initial prompt (overrides requirement-elaborator)")
	)
	flag.Parse()

	if *addr == "" {
		fmt.Fprintln(os.Stderr, "kitty-grill: --addr is required")
		os.Exit(2)
	}

	prompt := *custom
	if prompt == "" {
		prompt = buildGrillingPrompt(*taskID, *taskTitle, *reqDoc, *vaultPath)
	}
	if err := repl(*addr, *provider, *model, prompt); err != nil {
		fmt.Fprintf(os.Stderr, "kitty-grill: %v\n", err)
		os.Exit(1)
	}
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
	return fmt.Sprintf(`%s（遵循 skill://requirement-elaborator 的方法论：事实从环境查，决策由用户定）。

交互方式改为「批量问卷」，不要逐问：
1. 深度勘察：读需求文档 + 项目上下文（CONTEXT.md、现有 REQ、ADRs、PROJECT-CONVENTIONS.md、相关代码/协议）
2. 一次性提炼所有需要决策的技术点，合并同类项，控制在 5-15 个
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

// ansi helpers for rendering the questionnaire in the terminal.
const (
	ansiReset  = "\x1b[0m"
	ansiBold   = "\x1b[1m"
	ansiDim    = "\x1b[2m"
	ansiCyan   = "\x1b[36m"
	ansiGreen  = "\x1b[32m"
	ansiYellow = "\x1b[33m"
)

func bold(s string) string   { return ansiBold + s + ansiReset }
func dim(s string) string    { return ansiDim + s + ansiReset }
func cyan(s string) string   { return ansiCyan + s + ansiReset }
func green(s string) string  { return ansiGreen + s + ansiReset }
func yellow(s string) string { return ansiYellow + s + ansiReset }

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

// renderQuestionnaire formats the structured questionnaire as a scannable
// option list: each decision gets a highlighted header, its options with
// letter keys, and a dimmed recommendation line.
func renderQuestionnaire(q questionnaire) string {
	var b strings.Builder
	for _, d := range q.Decisions {
		b.WriteString("\n" + bold(yellow("═══ "+d.ID+": "+d.Question+" ═══")) + "\n")
		for _, o := range d.Options {
			mark := "  "
			if o.ID == d.Recommended && d.Recommended != "" {
				mark = " ⭐推荐"
			}
			b.WriteString(fmt.Sprintf("   %s  %s%s\n", bold(cyan("["+o.ID+"]")), o.Label, green(mark)))
		}
		if d.Reason != "" {
			b.WriteString(dim("    理由: "+d.Reason) + "\n")
		}
	}
	return b.String()
}
func repl(addr, provider, model, prompt string) error {
	sessionID := ""

	fmt.Printf("\n╔══════════════════════════════════════════════════════════════╗\n")
	fmt.Printf("║  🟡 需求对齐 — grilling（批量问卷）\n")
	fmt.Printf("║\n")
	fmt.Printf("║  模型会一次性列出所有决策点与选项，你一轮填写完即可。\n")
	fmt.Printf("║  填写格式：D1=A D2=B …（可多行，空行提交；/quit 结束）\n")
	fmt.Printf("╚══════════════════════════════════════════════════════════════╝\n\n")

	// 阶段 1：生成问卷。
	fmt.Print("⏳ 正在深度勘察并生成问卷（可能需要 1-3 分钟）…\n\n")
	resp, err := chat(addr, provider, model, sessionID, prompt)
	if err != nil {
		return err
	}
	sessionID = resp.SessionID

	// 优先解析结构化 JSON 问卷并渲染；解析失败则回退打印原始文本。
	if raw, ok := extractJSON(resp.Text); ok {
		var q questionnaire
		if err := json.Unmarshal([]byte(raw), &q); err == nil && len(q.Decisions) > 0 {
			fmt.Println(renderQuestionnaire(q))
			fmt.Println()
			fmt.Println(bold("请一轮填写所有决策，格式：") + cyan("D1=A D2=B …") + dim("（可多行，空行提交）"))
		} else {
			fmt.Println(resp.Text)
		}
	} else {
		fmt.Println(resp.Text)
	}
	fmt.Println()

	in := bufio.NewReader(os.Stdin)
	for {
		// 阶段 2：收集一轮答案（多行，空行提交）。
		var answers []string
		for {
			fmt.Print("✍️  你的决策 > ")
			line, err := in.ReadString('\n')
			if err == io.EOF {
				fmt.Println()
				if len(answers) == 0 {
					return nil
				}
				break
			}
			if err != nil {
				return err
			}
			msg := strings.TrimSpace(line)
			if msg == "" {
				if len(answers) > 0 {
					break // 空行 = 提交本轮
				}
				continue
			}
			if msg == "/quit" || msg == "/exit" {
				return nil
			}
			answers = append(answers, msg)
		}

		if len(answers) == 0 {
			return nil
		}

		// 提交本轮答案。
		fmt.Print("\n⏳ 正在根据你的决策写回需求文档…\n\n")
		resp, err := chat(addr, provider, model, sessionID, strings.Join(answers, "\n"))
		if err != nil {
			fmt.Fprintf(os.Stderr, "grill: %v\n", err)
			continue
		}
		fmt.Println(resp.Text)
		fmt.Println()
		// 一轮完成即退出（如有待澄清，模型已在总结里列出，用户可另开 tab）。
		return nil
	}
}

// chat sends one message to the agent-server and returns the model reply.
func chat(addr, provider, model, sessionID, message string) (*chatResponse, error) {
	body, err := json.Marshal(chatRequest{
		Message:   message,
		Provider:  provider,
		Model:     model,
		SessionID: sessionID,
	})
	if err != nil {
		return nil, err
	}

	client := &http.Client{Timeout: 0} // 交互会话不设超时，模型可能长考
	req, err := http.NewRequest(http.MethodPost, "http://"+addr+"/agent/chat", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("content-type", "application/json")

	res, err := client.Do(req)
	if err != nil {
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
