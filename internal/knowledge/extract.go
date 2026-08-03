package knowledge

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

// ExtractResult holds the outcome of a project knowledge extraction run.
type ExtractResult struct {
	ADRCount    int
	NewRefs     int
	UpdatedRefs int
	Touched     []string // absolute paths of knowledge files created/updated
	Errors      []string
}

// ExtractProjectKnowledge implements Step 0 of the knowledge-base skill:
// scan project ADRs and extract reusable knowledge into References/.
func ExtractProjectKnowledge(vaultDir, projectName string) (*ExtractResult, error) {
	result := &ExtractResult{}

	projectDir := filepath.Join(vaultDir, "Projects", projectName)
	if _, err := os.Stat(projectDir); os.IsNotExist(err) {
		return nil, fmt.Errorf("project not found: %s", projectName)
	}

	adrDir := filepath.Join(projectDir, "Notes", "adr")
	adrs, err := scanADRs(adrDir)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("scan ADRs: %v", err))
	} else {
		result.ADRCount = len(adrs)
	}

	refsDir := filepath.Join(vaultDir, "References")
	for _, adr := range adrs {
		written, updated, touchedPath, err := extractADRKnowledge(adr, refsDir, projectName)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("extract %s: %v", adr.ID, err))
			continue
		}
		result.NewRefs += written
		result.UpdatedRefs += updated
		if touchedPath != "" {
			result.Touched = append(result.Touched, touchedPath)
		}
	}

	return result, nil
}

type adrInfo struct {
	ID       string
	Title    string
	Status   string
	Decision string
	FilePath string
}

func scanADRs(adrDir string) ([]adrInfo, error) {
	entries, err := os.ReadDir(adrDir)
	if err != nil {
		return nil, err
	}
	var adrs []adrInfo
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, "ADR-") || !strings.HasSuffix(name, ".md") {
			continue
		}
		if name == "ADR-INDEX.md" || name == "ADR-COVERAGE.md" {
			continue
		}
		fullPath := filepath.Join(adrDir, name)
		data, err := os.ReadFile(fullPath)
		if err != nil {
			continue
		}
		fm, body, err := parseFrontmatter(data)
		if err != nil {
			continue
		}
		id := strings.TrimSuffix(name, ".md")
		status := "accepted"
		if v, ok := fm["status"]; ok {
			status = fmt.Sprint(v)
		}
		if status != "accepted" && status != "superseded" {
			continue
		}
		title := ""
		if v, ok := fm["title"]; ok {
			title = fmt.Sprint(v)
		}
		decision := extractSection(body, "Decision")
		if idx := strings.Index(decision, "。"); idx > 0 {
			decision = decision[:idx]
		}
		if idx := strings.Index(decision, ".\n"); idx > 0 {
			decision = decision[:idx]
		}

		adrs = append(adrs, adrInfo{
			ID:       id,
			Title:    title,
			Status:   status,
			Decision: strings.TrimSpace(decision),
			FilePath: fullPath,
		})
	}
	return adrs, nil
}

func extractADRKnowledge(adr adrInfo, refsDir, projectName string) (int, int, string, error) {
	newCount, updateCount := 0, 0
	target := classifyADR(adr)
	if target == "" {
		return 0, 0, "", nil
	}

	targetPath := filepath.Join(refsDir, target)
	if _, err := os.Stat(targetPath); os.IsNotExist(err) {
		if err := createRefScaffold(targetPath, adr, projectName); err != nil {
			return 0, 0, "", err
		}
		newCount++
	} else {
		if err := appendPracticeNote(targetPath, adr, projectName); err != nil {
			return 0, 0, "", err
		}
		updateCount++
	}

	return newCount, updateCount, targetPath, nil
}

func classifyADR(adr adrInfo) string {
	decision := strings.ToLower(adr.Decision)
	title := strings.ToLower(adr.Title)

	switch {
	case strings.Contains(decision, "connect") || strings.Contains(decision, "protobuf") ||
		strings.Contains(decision, "grpc") || strings.Contains(title, "connect"):
		return "core/go/connect-rpc.md"
	case strings.Contains(decision, "outbox") || strings.Contains(decision, "at-least-once") ||
		strings.Contains(decision, "replay") || strings.Contains(title, "outbox"):
		return "core/go/outbox-reliable-delivery.md"
	case strings.Contains(decision, "状态机") || strings.Contains(decision, "state machine") ||
		strings.Contains(title, "状态机") || strings.Contains(title, "state machine"):
		return "core/go/state-machine-pattern.md"
	case strings.Contains(decision, "wire") || strings.Contains(decision, "依赖注入") ||
		strings.Contains(title, "wire"):
		return "core/go/wire-di.md"
	case strings.Contains(decision, "operator") || strings.Contains(decision, "k8s") ||
		strings.Contains(title, "operator") || strings.Contains(title, "sdk"):
		return "core/kubernetes/operator-development-guide.md"
	}
	return ""
}

func createRefScaffold(path string, adr adrInfo, projectName string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	now := time.Now().Format("2006-01-02")
	relPath, _ := filepath.Rel(filepath.Dir(filepath.Dir(path)), path)
	parts := strings.Split(relPath, "/")
	topics := parts[len(parts)-2:]
	if len(topics) > 1 {
		topics = topics[:1]
	}
	topicStr := strings.TrimSuffix(strings.Join(topics, ", "), ".md")

	content := fmt.Sprintf(`---
topics: [%s]
level: intermediate
updated: "%s"
source: "local"
verified: false
aliases: []
---

# %s

> 自动从项目 ADR 提取。来源：[%s](Projects/%s/Notes/adr/%s.md)

## 决策摘要

%s

## 更新记录

- %s — 从 %s 项目 %s 自动提取
`, topicStr, now, adr.Title, adr.ID, projectName, adr.ID,
		adr.Decision, now, projectName, adr.ID)

	return os.WriteFile(path, []byte(content), 0o644)
}

func appendPracticeNote(path string, adr adrInfo, projectName string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	content := string(data)
	now := time.Now().Format("2006-01-02")

	note := fmt.Sprintf("\n### %s 项目实践\n\n**来源**：[%s](Projects/%s/Notes/adr/%s.md)（%s）\n\n**决策**：%s\n",
		projectName, adr.ID, projectName, adr.ID, now, adr.Decision)

	if idx := strings.LastIndex(content, "## 更新记录"); idx >= 0 {
		content = content[:idx] + note + content[idx:]
	} else {
		content += "\n" + note + "\n## 更新记录\n\n- `" + now + "` — 从 " + projectName + " 项目 " + adr.ID + " 自动提取实践经验\n"
	}

	return os.WriteFile(path, []byte(content), 0o644)
}

func extractSection(content, heading string) string {
	header := "## " + heading
	idx := strings.Index(content, header)
	if idx < 0 {
		header = "## " + heading + "\n"
		idx = strings.Index(content, header)
	}
	if idx < 0 {
		return ""
	}
	start := idx + len(header)
	if start >= len(content) {
		return ""
	}
	rest := content[start:]
	nextHeader := regexp.MustCompile(`(?m)^## `).FindStringIndex(rest)
	if nextHeader != nil {
		return rest[:nextHeader[0]]
	}
	return rest
}

// MarkVerified sets verified: true on the given knowledge files. Called after
// a merge→done delivery: the ADR decisions extracted from that delivery have
// been validated by real project practice, so the file-level verified flag
// flips. Content added later (unverified ADRs) keeps the per-section notes
// honest — the flag is file-level, practice notes stay section-level.
// All errors are collected and reported together; files that succeeded stay
// flipped (partial success is not rolled back).
func MarkVerified(paths []string) error {
	var errs []string
	for _, p := range paths {
		if err := yamlfrontmatter.Update(p, map[string]interface{}{"verified": true}); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", p, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("mark verified: %s", strings.Join(errs, "; "))
	}
	return nil
}

// failureRootCause maps stable error codes to known root causes / fixes used
// by the auto-sink of daemon phase failures into the knowledge base.
type failureRootCause struct {
	RootCause string
	Fix       string
	Lesson    string
}

var failureRootCauses = map[string]failureRootCause{
	"API_KEY_UNAVAILABLE": {
		RootCause: "OMP 无法获取模型 API Key（KeePassXC 未解锁或 secret service 不可达；systemd 单元需 XDG_RUNTIME_DIR/DBUS_SESSION_BUS_ADDRESS）",
		Fix:       "解锁 KeePassXC（或配置 DEEPSEEK_API_KEY/CODEX_API_KEY 环境变量）；daemon 探测到 key 后自动恢复",
		Lesson:    "外部依赖不可用是可恢复条件，不应与真失败混为一谈；key 失败不重试不 fallback",
	},
	"PHASE_INTERRUPTED": {
		RootCause: "daemon 停机/重启中断了执行中的 OMP（SIGTERM → 保存 session 退出）",
		Fix:       "无需处理：重启后下一轮 scan 自动重新调度，阶段成功后标记自动清除",
		Lesson:    "部署重启是运维常态，任务状态保持 + PHASE_INTERRUPTED 标记使重启无损",
	},
	"MODEL_FAILED": {
		RootCause: "模型调用失败（进程异常退出、网络、模型服务错误）",
		Fix:       "查看 phase_log 定位具体错误（key/quota/网络）；必要时手动 resume",
		Lesson:    "先看日志再判断失败类别；MODEL_FAILED 是兜底分类",
	},
	"PHASE_TIMEOUT": {
		RootCause: "模型在阶段超时内无响应",
		Fix:       "检查网络与模型服务状态；fallback 模型会自动重试",
		Lesson:    "超时走独立分类，不与其他失败混淆",
	},
	"MODEL_QUOTA_EXHAUSTED": {
		RootCause: "模型 token 配额耗尽",
		Fix:       "前往模型平台充值后续航",
		Lesson:    "配额错误优先识别，避免误判为网络问题",
	},
}

// AppendFailurePattern sinks a daemon phase failure into the knowledge base as
// a bug pattern (现象/根因/修复/教训), deduplicated by error code + phase.
// First occurrence per code+phase is recorded; later occurrences are skipped
// (the file itself is the dedup store, surviving daemon restarts). Missing
// knowledge base is silently skipped.
func AppendFailurePattern(vaultDir, code, phase, taskID, logPath string) error {
	if vaultDir == "" {
		return nil
	}
	path := filepath.Join(vaultDir, "References", "core", "daemon-stuck-task-patterns.md")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // knowledge base not set up
		}
		return err
	}
	content := string(data)

	marker := code + " — " + phase + " 阶段失败"
	if strings.Contains(content, marker) {
		return nil // already recorded
	}

	rc, known := failureRootCauses[code]
	if !known {
		rc = failureRootCause{
			RootCause: "见 phase_log 定位具体原因",
			Fix:       "分析日志后修复，必要时手动 resume_approved",
			Lesson:    "未知错误码先归类再处理",
		}
	}

	patternNo := strings.Count(content, "## 模式") + 1
	now := time.Now().Format("2006-01-02")
	entry := fmt.Sprintf(`
---

## 模式 %d：%s — %s 阶段失败

**现象**：任务 TASK-%s 失败（错误码 %s，阶段 %s，日志：%s）。

**根因**：%s

**修复**：%s

**教训**：%s

**检查项**：出现 %s 错误码 → %s（自动沉淀于 %s）
`, patternNo, code, phase, taskID, code, phase, logPath,
		rc.RootCause, rc.Fix, rc.Lesson, code, rc.Fix, now)

	// 追加到"更新记录"前:把条目插到文件末尾的检查清单/更新记录之前
	if idx := strings.LastIndex(content, "## 检查清单"); idx >= 0 {
		content = content[:idx] + entry + content[idx:]
	} else {
		content += entry
	}
	return os.WriteFile(path, []byte(content), 0o644)
}
