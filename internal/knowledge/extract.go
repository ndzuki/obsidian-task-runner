package knowledge

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// ExtractResult holds the outcome of a project knowledge extraction run.
type ExtractResult struct {
	ADRCount    int
	NewRefs     int
	UpdatedRefs int
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
		written, updated, err := extractADRKnowledge(adr, refsDir, projectName)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("extract %s: %v", adr.ID, err))
			continue
		}
		result.NewRefs += written
		result.UpdatedRefs += updated
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

func extractADRKnowledge(adr adrInfo, refsDir, projectName string) (int, int, error) {
	newCount, updateCount := 0, 0
	target := classifyADR(adr)
	if target == "" {
		return 0, 0, nil
	}

	targetPath := filepath.Join(refsDir, target)
	if _, err := os.Stat(targetPath); os.IsNotExist(err) {
		if err := createRefScaffold(targetPath, adr, projectName); err != nil {
			return 0, 0, err
		}
		newCount++
	} else {
		if err := appendPracticeNote(targetPath, adr, projectName); err != nil {
			return 0, 0, err
		}
		updateCount++
	}

	return newCount, updateCount, nil
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
