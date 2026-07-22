package task

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	project_pkg "github.com/ndzuki/obsidian-task-runner/internal/project"
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
	Action    string `json:"action"`
	OldStatus string `json:"old_status,omitempty"`
}

// OnReqChanged processes a requirement file change and updates affected tasks.
func OnReqChanged(vaultPath, reqRelPath string) []AffectedResult {
	projectsDir := filepath.Join(vaultPath, "Projects")
	if _, err := os.Stat(projectsDir); os.IsNotExist(err) {
		return nil
	}

	var affected []AffectedResult
	projEntries, _ := os.ReadDir(projectsDir)
	for _, proj := range projEntries {
		if !proj.IsDir() { continue }
		tasksDir := filepath.Join(projectsDir, proj.Name(), "Tasks")
		entries, err := os.ReadDir(tasksDir)
		if err != nil { continue }
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") { continue }
			taskPath := filepath.Join(tasksDir, entry.Name())
			data, err := os.ReadFile(taskPath)
			if err != nil { continue }
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
		case "blocked":
			if err := yamlfrontmatter.Update(taskPath, map[string]interface{}{
				"pending_req": true,
			}); err != nil {
				fmt.Fprintf(os.Stderr, "Error updating blocked task %s: %v\n", taskPath, err)
				continue
			}
			affected = append(affected, AffectedResult{
				TaskID: fm.ID, File: entry.Name(),
				Action: "pending_req", OldStatus: fm.Status,
			})
		case "ready":
			if err := yamlfrontmatter.Update(taskPath, map[string]interface{}{
				"pending_req": true,
			}); err != nil {
				fmt.Fprintf(os.Stderr, "Error updating ready task %s: %v\n", taskPath, err)
				continue
			}
			affected = append(affected, AffectedResult{
				TaskID: fm.ID, File: entry.Name(),
				Action: "pending_req", OldStatus: fm.Status,
			})
		case "refining", "planning":
			if err := yamlfrontmatter.Update(taskPath, map[string]interface{}{
				"pending_req": true,
			}); err != nil {
				fmt.Fprintf(os.Stderr, "Error marking pending_req on %s: %v\n", taskPath, err)
				continue
			}
			affected = append(affected, AffectedResult{
				TaskID: fm.ID, File: entry.Name(),
				Action: "pending_req", OldStatus: fm.Status,
			})
		case "needs-grilling":
			if err := yamlfrontmatter.Update(taskPath, map[string]interface{}{
				"pending_req": true,
			}); err != nil {
				fmt.Fprintf(os.Stderr, "Error marking pending_req on %s: %v\n", taskPath, err)
				continue
			}
			affected = append(affected, AffectedResult{
				TaskID: fm.ID, File: entry.Name(),
				Action: "pending_req", OldStatus: fm.Status,
			})
		case "plan-review":
			if err := yamlfrontmatter.Update(taskPath, map[string]interface{}{
				"status":            "refining",
				"pending_req":       true,
				"plan_approved":     false,
				"grill_done":        false,
				"grill_context":     "",
				"grill_prev_status": "",
			}); err != nil {
				fmt.Fprintf(os.Stderr, "Error resetting plan-review task %s: %v\n", taskPath, err)
				continue
			}
			affected = append(affected, AffectedResult{
				TaskID: fm.ID, File: entry.Name(),
				Action: "reset_to_ready", OldStatus: fm.Status,
			})
		case "implementing":
			if err := yamlfrontmatter.Update(taskPath, map[string]interface{}{
				"pending_req":    true,
				"merge_approved": false,
			}); err != nil {
				fmt.Fprintf(os.Stderr, "Error marking pending_req on %s: %v\n", taskPath, err)
				continue
			}
			affected = append(affected, AffectedResult{
				TaskID: fm.ID, File: entry.Name(),
				Action: "pending_req", OldStatus: fm.Status,
			})
		case "review", "conflict", "done":
			if err := yamlfrontmatter.Update(taskPath, map[string]interface{}{
				"status":         "refining",
				"pending_req":    true,
				"merge_approved": false,
			}); err != nil {
				fmt.Fprintf(os.Stderr, "Error setting %s to refining on %s: %v\n", fm.Status, taskPath, err)
				continue
			}
			affected = append(affected, AffectedResult{
				TaskID: fm.ID, File: entry.Name(),
				Action: "pending_req", OldStatus: fm.Status,
			})
		default:
			affected = append(affected, AffectedResult{
				TaskID: fm.ID, File: entry.Name(),
				Action: "warn_only", OldStatus: fm.Status,
			})
		}
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

// pathsMatch returns true only when both normalized paths are identical.
// Basename fallback is explicitly prohibited to avoid cross-project collisions.
func pathsMatch(a, b string) bool {
	return a == b
}

// createTaskForReq auto-creates a TASK file from a new requirement.
func createTaskForReq(vaultPath, reqRelPath string) *AffectedResult {
	id, slug := ParseReqFilename(reqRelPath)
	if id == "" || slug == "" {
		return nil
	}

	// Derive project directory from the requirement's path.
	// New structure: Projects/<project>/Requirements/REQ-xxx.md → project = <project>
	// Old structure:   Requirements/REQ-xxx.md → project = <id>-<slug> (backward compat)
	projectDir := deriveProjectDir(reqRelPath, id, slug)
	tasksDir := filepath.Join(vaultPath, "Projects", projectDir, "Tasks")
	reqDir := filepath.Join(vaultPath, "Projects", projectDir, "Requirements")
	targetName := TaskFilenameForReq(reqRelPath)
	if targetName == "" {
		return nil
	}
	os.MkdirAll(tasksDir, 0755)
	os.MkdirAll(reqDir, 0755)

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

	projName := ""
	projectID := project_pkg.ExtractProjectID(projectDir)
	priority := "P2"
	epic := ""
	reviewer := ""
	author := ""
	tagsList := []string{}
	if reqFM != nil {
		projName = reqFM.Project
		if reqFM.ProjectID != "" {
			projectID = reqFM.ProjectID // override from REQ frontmatter if set
		}
		priority = reqFM.Priority
		epic = reqFM.Epic
		reviewer = reqFM.Reviewer
		author = reqFM.Author
		tagsList = reqFM.Tags
	}
	if priority == "" {
		priority = "P2"
	}

	// Resolve project field for vault-map matching.
	// Priority: REQ frontmatter → vault-map match on projectDir → projectDir fallback.
	if projName == "" {
		projName = resolveProjectField(projectDir)
	}

	title := firstHeading(reqContent)
	if title == "" {
		title = strings.ReplaceAll(slug, "-", " ")
	}

	now := time.Now().Format("2006-01-02T15:04:05-07:00")

	// Build task markdown
	tags := ""
	if len(tagsList) > 0 {
		tags = "  - " + strings.Join(tagsList, "\n  - ")
	} else {
		tags = "  - "
	}

	summary := extractSection(reqContent, "要做什么")
	ac := extractSection(reqContent, "完成标准", "验收标准")

	taskMD := fmt.Sprintf(`---
id: "%s"
title: "%s"
project: "%s"
project_id: "%s"
template: ""
status: blocked
plan_approved: false
merge_approved: false
adr_approved: false
resume_approved: false
pending_req: false
maturity: ""
refine_version: 0
refine_req_hash: ""
plan_req_hash: ""
plan_version: 0
checkpoint_commit: ""
refine_retry_count: 0
refine_error: ""
planning_retry_count: 0
blocked_phase: ""
phase_error: ""
phase_log: ""
grill_owner: ""
grill_started_at: ""
grill_timeout_minutes: 30
grill_done: false
grill_resolution: ""
grill_context: ""
grill_prev_status: ""
req_refine_count: 0
auto_approve: false
off_peak_only: false
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
author: "%s"
tags:
%s
epic: "%s"
parent: ""
blocks: []
blocked_by: []
target_branch: ""
pr_url: ""
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

> ⚠️ **任务已暂停在 blocked。** 请在 frontmatter 中补齐必填字段后保存，daemon 自动进入 refining → maturity gate。

---

## 需求成熟度评估
<!-- 🤖 refining Skill 写入 -->

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
1. %s — 任务创建，status=blocked
`, id, title, projName, projectID, now, now, priority, reviewer, reqRelPath, author, tags, epic,
		id, title, summary, ac, reqRelPath,
		projName, map[bool]string{true: "✅", false: "🔴 必填"}[projName != ""],
		"`"+now+"`")

	targetPath := filepath.Join(tasksDir, targetName)
	if err := os.WriteFile(targetPath, []byte(taskMD), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Error creating task: %v\n", err)
		return nil
	}

	return &AffectedResult{
		TaskID: id, File: targetName, Action: "create_task",
	}
}

// deriveProjectDir extracts the project directory name from a requirement path.
// New structure: "Projects/001-release-manager/Requirements/REQ-002-demo.md" → "001-release-manager"
// Old structure: "Requirements/REQ-001-demo.md" → "001-demo" (backward compatible)
func deriveProjectDir(reqRelPath, id, slug string) string {
	// Require "Projects/" prefix for the new structure
	projPrefix := "Projects/"
	if strings.HasPrefix(reqRelPath, projPrefix) {
		rest := strings.TrimPrefix(reqRelPath, projPrefix)
		// rest = "001-release-manager/Requirements/REQ-002-demo.md"
		idx := strings.Index(rest, string(filepath.Separator))
		if idx > 0 {
			return rest[:idx]
		}
	}
	// Old flat structure: use id-slug as project directory
	return fmt.Sprintf("%s-%s", id, slug)
}

// resolveProjectField maps a Vault project directory name to a vault-map project key.
// Falls back to projectDir if vault-map is unavailable or no match found.
func resolveProjectField(projectDir string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return projectDir
	}
	mapFile := filepath.Join(home, ".omp", "skills", "obsidian-task-runner", "config", "vault-map.json")
	if mapped := project_pkg.MatchVaultDir(mapFile, projectDir); mapped != "" {
		return mapped
	}
	return projectDir
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
