package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// roadmapName is the project milestone file (PM skill: maintained on
// stage-review / staging; the daemon now also appends deterministically at
// delivery events so the document never goes stale between PM sessions).
const roadmapName = "Roadmap.md"

// updateRoadmap deterministically records a milestone event in
// Notes/Roadmap.md — created on first use, appended to afterwards, idempotent
// per (date, title). Pure text assembly: no LLM session, matching the daemon
// doc-maintenance contract (Stage-Plan, decision archive).
func (r *Runner) updateRoadmap(project, title, detail string) {
	projDir := resolveVaultProjectDir(r.cfg.ObsidianVault, project)
	if projDir == "" {
		return
	}
	path := filepath.Join(projDir, "Notes", roadmapName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		r.logger.Printf("roadmap %s: mkdir Notes: %v", project, err)
		return
	}
	now := time.Now()
	date := now.Format("2006-01-02")
	stamp := fmt.Sprintf("### %s · %s", date, title)

	data, err := os.ReadFile(path)
	if err != nil {
		// Create with template skeleton.
		data = []byte(fmt.Sprintf(`---
id: "roadmap"
project: %s
status: active
updated: %s
---

# Roadmap — %s

> 里程碑时间线（daemon 在交付事件点确定性追加；PM 在阶段评审/阶段化时补充语义）。

## 当前状态
- 阶段: （待首次阶段化事件记录）

## 里程碑

`, project, now.Format(time.RFC3339), project))
	}
	content := string(data)
	if strings.Contains(content, stamp) {
		return // idempotent: same-date event already recorded
	}
	entry := stamp + "\n- " + detail + "\n"
	// Append before the "## 历史归档" section if present, else at the end.
	if idx := strings.Index(content, "## 历史归档"); idx >= 0 {
		content = content[:idx] + entry + content[idx:]
	} else {
		content = strings.TrimRight(content, "\n") + "\n\n" + entry
	}
	content = refreshFrontmatterStamp(content, now)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		r.logger.Printf("roadmap %s: append %s: %v", project, stamp, err)
		return
	}
	r.logger.Printf("roadmap %s: milestone recorded %q", project, stamp)
}

// refreshFrontmatterStamp updates the `updated:` frontmatter line (first 10
// lines) of a document.
func refreshFrontmatterStamp(content string, now time.Time) string {
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if i >= 10 {
			break
		}
		if strings.HasPrefix(line, "updated:") {
			lines[i] = "updated: " + now.Format(time.RFC3339)
			break
		}
	}
	return strings.Join(lines, "\n")
}
