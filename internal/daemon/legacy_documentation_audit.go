package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

const documentationAuditName = "Documentation-Audit.md"

// processLegacyDocumentationAudits retrofits projects whose Stage-Review was
// written before documentation_gate_version existed. It asks PM to audit the
// existing delivery once, without guessing gaps in daemon code. The audit
// result is then copied into the project's Stage-Review so the normal closure
// task and decision gates apply.
func (r *Runner) processLegacyDocumentationAudits(ctx context.Context) int {
	projectsDir := filepath.Join(r.cfg.ObsidianVault, "Projects")
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		return 0
	}
	for _, entry := range entries {
		if ctx.Err() != nil || !entry.IsDir() {
			continue
		}
		projDir := filepath.Join(projectsDir, entry.Name())
		planPath := filepath.Join(projDir, "Notes", stagePlanName)
		if _, err := os.Stat(planPath); err != nil {
			continue
		}
		reviewPath := filepath.Join(projDir, "Notes", stageReviewName)
		reviewData, reviewErr := os.ReadFile(reviewPath)
		if reviewErr == nil {
			fm, parseErr := yamlfrontmatter.Parse(reviewData)
			if parseErr == nil && fm != nil && extraInt(fm.Extra["documentation_gate_version"]) == documentationGateVersion {
				continue
			}
		}
		auditPath := filepath.Join(projDir, "Notes", documentationAuditName)
		if _, err := os.Stat(auditPath); os.IsNotExist(err) {
			if err := createDocumentationAudit(projDir, auditPath, reviewPath); err != nil {
				r.logger.Printf("project %s: create documentation audit: %v", entry.Name(), err)
				continue
			}
			// The marker is written before dispatch. A restart cannot repeatedly
			// create audit files or bypass this retrofit pass.
		}
		gate, gateErr := readDocumentationGate(auditPath)
		if gateErr != nil {
			continue
		}
		if gate.Status == "" || gate.Status == "pending" {
			if err := r.runGrillingPM(ctx, "documentation-audit", auditPath); err != nil {
				if err == errAPIKeyUnavailable {
					return 0
				}
				r.logger.Printf("project %s: documentation audit: %v", entry.Name(), err)
				continue
			}
			r.logger.Printf("project %s: legacy documentation audit dispatched", entry.Name())
			return 1
		}
		if gate.Status != "pass" && gate.Status != "gap" && gate.Status != "not_applicable" {
			r.logger.Printf("project %s: invalid documentation audit gate %q", entry.Name(), gate.Status)
			continue
		}
		if reviewErr := syncDocumentationAuditToReview(auditPath, reviewPath); reviewErr != nil {
			r.logger.Printf("project %s: sync documentation audit: %v", entry.Name(), reviewErr)
			continue
		}
	}
	return 0
}

func createDocumentationAudit(projDir, auditPath, reviewPath string) error {
	data, err := os.ReadFile(reviewPath)
	if err != nil {
		return err
	}
	fm, err := yamlfrontmatter.Parse(data)
	if err != nil || fm == nil {
		return fmt.Errorf("parse legacy Stage-Review: %w", err)
	}
	stage := strings.TrimSpace(fm.Stage)
	if stage == "" {
		return fmt.Errorf("legacy Stage-Review has no stage")
	}
	now := time.Now().Format(time.RFC3339)
	content := fmt.Sprintf(`---
id: "documentation-audit"
project: "%s"
stage: "%s"
status: pending
documentation_gate_version: 1
documentation_gate: pending
documentation_gaps: []
documentation_batch: ""
documentation_task: ""
source_stage_review: "%s"
created: "%s"
updated: "%s"
---
# Documentation Audit — %s

> 这是对既有项目的兼容回补审计，不预设缺口。PM 必须先盘点用户可见能力，再裁定 pass、gap 或 not_applicable。

## 审计范围

- 项目现有 README、docs、配置参考、安装/部署/运维说明
- 已完成阶段的用户可见能力与验证入口
- 命令、路径、配置键、凭据边界、故障恢复和回滚说明

## PM 输出要求

写回 frontmatter：

- documentation_gate: pass | gap | not_applicable
- documentation_gaps: [...]（仅 gap 时逐条填写）
- 不要填写用户的 Stage-Review「评审决策:」
`, fm.Project, stage, filepath.Base(reviewPath), now, now, stage)
	if _, err := os.Stat(auditPath); err == nil {
		return fmt.Errorf("documentation audit already exists: %s", auditPath)
	} else if !os.IsNotExist(err) {
		return err
	}
	return os.WriteFile(auditPath, []byte(content), 0o644)
}

func syncDocumentationAuditToReview(auditPath, reviewPath string) error {
	if _, err := os.Stat(reviewPath); err != nil {
		return err
	}
	gate, err := readDocumentationGate(auditPath)
	if err != nil {
		return err
	}
	updates := map[string]any{
		"documentation_gate_version": documentationGateVersion,
		"documentation_gate":         gate.Status,
		"documentation_gaps":         gate.Gaps,
		"documentation_batch":        gate.BatchID,
		"documentation_task":         gate.TaskID,
		"documentation_audit_status": "completed",
	}
	return yamlfrontmatter.Update(reviewPath, updates)
}
