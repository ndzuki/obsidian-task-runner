package task

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

// REQFilenameRE matches REQ-<id>-<slug>.md
var reqFilenameRE = regexp.MustCompile(`^REQ-(?P<id>\d+)-(?P<slug>.+)\.md$`)

// ParseReqFilename parses the filename and returns (id, slug) or empty strings.
func ParseReqFilename(path string) (id, slug string) {
	name := filepath.Base(path)
	m := reqFilenameRE.FindStringSubmatch(name)
	if m == nil {
		return "", ""
	}
	return m[1], m[2]
}

// TaskFilenameForReq derives the task filename from a requirement path.
func TaskFilenameForReq(reqRelPath string) string {
	id, slug := ParseReqFilename(reqRelPath)
	if id == "" {
		return ""
	}
	return fmt.Sprintf("TASK-%s-%s.md", id, slug)
}

// AffectedResult describes what happened to a task during on-req-changed.
type AffectedResult struct {
	TaskID    string `json:"task_id"`
	File      string `json:"file"`
	Action    string `json:"action"` // "reset_to_ready", "pending_req", "create_task", "warn_only"
	OldStatus string `json:"old_status,omitempty"`
}

// OnReqChanged processes a requirement file change and updates affected tasks.
func OnReqChanged(vaultPath, reqRelPath string) []AffectedResult {
	tasksDir := filepath.Join(vaultPath, "Tasks")
	if _, err := os.Stat(tasksDir); os.IsNotExist(err) {
		return nil
	}

	var affected []AffectedResult

	entries, _ := os.ReadDir(tasksDir)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		taskPath := filepath.Join(tasksDir, entry.Name())
		data, err := os.ReadFile(taskPath)
		if err != nil {
			continue
		}
		fm, err := yamlfrontmatter.Parse(data)
		if err != nil || fm == nil {
			continue
		}
		if fm.ReqDoc == "" {
			continue
		}

		// Normalize paths for comparison
		taskReq := normalizePath(fm.ReqDoc)
		reqPath := normalizePath(reqRelPath)
		if !pathsMatch(taskReq, reqPath) {
			continue
		}

		switch fm.Status {
		case "ready", "plan-review":
			yamlfrontmatter.Update(taskPath, map[string]interface{}{
				"status":        "ready",
				"plan_approved": false,
			})
			affected = append(affected, AffectedResult{
				TaskID: fm.ID, File: entry.Name(),
				Action: "reset_to_ready", OldStatus: fm.Status,
			})
			fmt.Printf("  %s (%s): %s → ready（需求已更新，重新出计划）\n", fm.ID, entry.Name(), fm.Status)

		case "implementing", "review", "conflict", "done":
			yamlfrontmatter.Update(taskPath, map[string]interface{}{
				"pending_req":    true,
				"merge_approved": false,
			})
			affected = append(affected, AffectedResult{
				TaskID: fm.ID, File: entry.Name(),
				Action: "pending_req", OldStatus: fm.Status,
			})
			fmt.Printf("  %s (%s): status=%s，已标记 pending_req（自动重新出计划）\n", fm.ID, entry.Name(), fm.Status)

		default:
			affected = append(affected, AffectedResult{
				TaskID: fm.ID, File: entry.Name(),
				Action: "warn_only", OldStatus: fm.Status,
			})
			fmt.Fprintf(os.Stderr, "  %s (%s): status=%s，已跳过（请手动评估）\n", fm.ID, entry.Name(), fm.Status)
		}
	}

	// Fallback: auto-create task if no existing task matched
	if len(affected) == 0 {
		created := createTaskForReq(vaultPath, reqRelPath)
		if created != nil {
			affected = append(affected, *created)
		}
	}

	return affected
}

func normalizePath(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	return strings.TrimSuffix(p, ".md")
}

func pathsMatch(a, b string) bool {
	if a == b {
		return true
	}
	an := filepath.Base(a)
	bn := filepath.Base(b)
	if an == bn {
		return true
	}
	return strings.HasSuffix(a, "/"+bn)
}

// createTaskForReq auto-creates a TASK file from a new requirement.
func createTaskForReq(vaultPath, reqRelPath string) *AffectedResult {
	id, slug := ParseReqFilename(reqRelPath)
	if id == "" || slug == "" {
		return nil
	}

	tasksDir := filepath.Join(vaultPath, "Tasks")
	targetName := TaskFilenameForReq(reqRelPath)
	if targetName == "" {
		return nil
	}

	// Don't overwrite existing
	if _, err := os.Stat(filepath.Join(tasksDir, targetName)); err == nil {
		return nil
	}

	// Read requirement for metadata
	reqPath := filepath.Join(vaultPath, reqRelPath)
	reqData, err := os.ReadFile(reqPath)
	if err != nil {
		return nil
	}
	reqContent := string(reqData)
	reqFM, _ := yamlfrontmatter.Parse(reqData)

	project := ""
	priority := "P2"
	epic := ""
	reviewer := ""
	if reqFM != nil {
		project = reqFM.Project
		priority = reqFM.Priority
		epic = reqFM.Epic
		reviewer = reqFM.Reviewer
	}
	if priority == "" {
		priority = "P2"
	}

	title := firstHeading(reqContent)
	if title == "" {
		title = strings.ReplaceAll(slug, "-", " ")
	}

	now := time.Now().Format("2006-01-02T15:04:05-07:00")

	// Build task markdown
	tags := ""
	if reqFM != nil && len(reqFM.Tags) > 0 {
		tags = "  - " + strings.Join(reqFM.Tags, "\n  - ")
	} else {
		tags = "  - "
	}

	summary := extractSection(reqContent, "要做什么")
	ac := extractSection(reqContent, "完成标准", "验收标准")

	taskMD := fmt.Sprintf(`---
id: "%s"
title: "%s"
project: "%s"
new_project: false
template: ""
status: blocked
plan_approved: false
merge_approved: false
pending_req: false
off_peak_only: false
plan_version: 0
created: "%s"
updated: "%s"
completed: ""
priority: %s
due_date: ""
estimated_hours: 0
actual_hours: 0
assignee: ""
reviewer: "%s"
req_doc: %s
component: ""
tags:
%s
epic: "%s"
parent: ""
blocks: []
blocked_by: []
target_branch: ""
target_env: staging
---

# TASK-%s: %s

## 需求摘要

%s

## 验收标准

%s

## 人工 Review 提醒

自动从 %s 生成。请确认以下字段：

| 字段 | 当前值 | 需要填？ |
|------|--------|---------|
| project | %s | %s |
| assignee | （空） | 🔴 必填（deepseek / gpt） |

> ⚠️ **任务已暂停在 blocked。** 请在 frontmatter 中补齐必填字段后保存，daemon 自动进入 Round 1。

---

## 执行摘要
| 轮次 | 阶段 | 计划版本 | 状态 | 时间戳 |
|------|------|---------|------|--------|
| 1 | — | v0 | ⏳ blocked（等待填字段） | —

---

## 实现计划
### v1 · PENDING

---

## 实现记录
### Round 1 · PENDING

---

## 验收记录
### Round 1 · PENDING

---

## 变更记录
1. %s — 任务创建，等待就绪
`, id, title, project, now, now, priority, reviewer, reqRelPath, tags, epic,
		id, title, summary, ac, reqRelPath,
		project, map[bool]string{true: "✅", false: "🔴 必填"}[project != ""],
		"`"+now+"`")

	targetPath := filepath.Join(tasksDir, targetName)
	if err := os.WriteFile(targetPath, []byte(taskMD), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Error creating task: %v\n", err)
		return nil
	}

	fmt.Printf("  %s (%s): 自动创建任务文档（status=blocked）\n", id, targetName)
	return &AffectedResult{
		TaskID: id, File: targetName, Action: "create_task",
	}
}

func firstHeading(content string) string {
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, "# ") {
			return strings.TrimPrefix(line, "# ")
		}
	}
	return ""
}

func extractSection(content string, headings ...string) string {
	inSection := false
	var lines []string
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "## ") {
			title := strings.TrimPrefix(trimmed, "## ")
			for _, h := range headings {
				if title == h {
					inSection = true
					break
				} else {
					inSection = false
				}
			}
			continue
		}
		if inSection && trimmed != "" && !strings.HasPrefix(trimmed, "<!--") {
			lines = append(lines, line)
		}
	}
	if len(lines) == 0 {
		return "<!-- 请从需求文档补充摘要 -->"
	}
	return strings.Join(lines, "\n")
}

// PrintAffected outputs affected results as JSON.
func PrintAffected(results []AffectedResult) {
	data, _ := json.Marshal(results)
	fmt.Println(string(data))
}
