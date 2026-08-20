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

// buildGrillingPrompt constructs the initial requirement-elaborator prompt the
// model receives when the tab opens.
func buildGrillingPrompt(taskID, taskTitle, reqDoc, vaultPath string) string {
	req := reqDoc
	if req != "" && vaultPath != "" {
		req = filepath.Join(vaultPath, reqDoc)
	}
	switch {
	case req != "":
		return fmt.Sprintf("对 %s 进行需求详细化。请使用 skill://requirement-elaborator 加载需求文档，识别其中的模糊点和未明确的技术决策，逐一向我提问以达成共识。", req)
	case taskTitle != "":
		return fmt.Sprintf("我要实现「%s」。请使用 skill://requirement-elaborator 逐一向我提问，补充技术细节以达成共识。", taskTitle)
	default:
		return "请使用 skill://requirement-elaborator 帮我进行需求详细化。先询问我要实现什么功能，然后逐一向我追问技术细节以达成共识。"
	}
}

// repl runs the interactive loop against the agent-server /agent/chat
// endpoint. It prints each model reply, prompts for the human answer, and
// relays it back with the sessionId so the conversation carries across turns.
func repl(addr, provider, model, prompt string) error {
	sessionID := ""

	fmt.Printf("\n╔══════════════════════════════════════════════════════════════╗\n")
	fmt.Printf("║  🟡 需求对齐 — grilling（DSH 交互）\n")
	fmt.Printf("║\n")
	fmt.Printf("║  逐一向你提问，达成共识后模型会写回需求文档。\n")
	fmt.Printf("║  直接输入回答后回车；Ctrl-D 结束。\n")
	fmt.Printf("╚══════════════════════════════════════════════════════════════╝\n\n")

	// 首轮：注入 requirement-elaborator prompt。
	resp, err := chat(addr, provider, model, sessionID, prompt)
	if err != nil {
		return err
	}
	sessionID = resp.SessionID
	fmt.Println(resp.Text)
	fmt.Println()

	in := bufio.NewReader(os.Stdin)
	for {
		fmt.Print("> ")
		line, err := in.ReadString('\n')
		if err == io.EOF {
			fmt.Println()
			break
		}
		if err != nil {
			return err
		}
		msg := strings.TrimSpace(line)
		if msg == "" {
			continue
		}
		if msg == "/quit" || msg == "/exit" {
			break
		}

		resp, err := chat(addr, provider, model, sessionID, msg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "grill: %v\n", err)
			continue
		}
		fmt.Println()
		fmt.Println(resp.Text)
		fmt.Println()
	}
	return nil
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
