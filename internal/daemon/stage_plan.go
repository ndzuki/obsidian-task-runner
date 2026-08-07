package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

// stagePlanName is the project-level stage plan filename (PM-owned).
const stagePlanName = "Stage-Plan.md"

// stageReviewName is the per-stage review file written by PM stage-review.
const stageReviewName = "Stage-Review.md"

// stagePhase is one parsed "### Phase N:" block from Stage-Plan.md.
type stagePhase struct {
	Name   string
	Tasks  []string // TASK numeric ids, e.g. ["018", "071"]
	Status string   // planned|in-progress|review-pending|delivered|ended
}

// stagePhaseRE matches a phase block heading; the block body lines follow
// the fixed PM format ("- tasks: ..." / "- status: ..."), which is the
// contract the daemon parses (documented in obsidian-task-runner-pm).
var (
	stagePhaseRE  = regexp.MustCompile(`(?m)^### Phase \d+[^\n]*$`)
	stageTasksRE  = regexp.MustCompile(`(?m)^- tasks:\s*(.+)$`)
	stageStatusRE = regexp.MustCompile(`(?m)^- status:\s*(\S+)$`)
)

// stageReviewPath resolves the project stage-review file, or "" when the
// project has no vault directory.
func stageReviewPath(vaultPath, project string) string {
	projDir := resolveVaultProjectDir(vaultPath, project)
	if projDir == "" {
		return ""
	}
	return filepath.Join(projDir, "Notes", stageReviewName)
}

// parseStagePlan reads Notes/Stage-Plan.md and returns its phase blocks in
// document order. A malformed block (missing tasks/status lines) is skipped.
func parseStagePlan(planPath string) []stagePhase {
	data, err := os.ReadFile(planPath)
	if err != nil {
		return nil
	}
	return parseStagePlanContent(string(data))
}

// parseStagePlanContent parses phase blocks from in-memory Stage-Plan text.
func parseStagePlanContent(content string) []stagePhase {
	headings := stagePhaseRE.FindAllStringIndex(content, -1)
	phases := make([]stagePhase, 0, len(headings))
	for i, loc := range headings {
		end := len(content)
		if i+1 < len(headings) {
			end = headings[i+1][0]
		}
		block := content[loc[0]:end]
		heading := block
		if idx := strings.Index(block, "\n"); idx >= 0 {
			heading = block[:idx]
		}
		phase := stagePhase{Name: strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(heading), "###"))}
		if m := stageTasksRE.FindStringSubmatch(block); m != nil {
			for _, id := range strings.FieldsFunc(m[1], func(r rune) bool { return r == ',' || r == ' ' }) {
				id = strings.TrimSpace(id)
				if id != "" {
					phase.Tasks = append(phase.Tasks, strings.TrimPrefix(id, "TASK-"))
				}
			}
		}
		if m := stageStatusRE.FindStringSubmatch(block); m != nil {
			phase.Status = m[1]
		}
		if len(phase.Tasks) > 0 && phase.Status != "" {
			phases = append(phases, phase)
		}
	}
	return phases
}

// stageTasksState reports the convergence state of a phase's tasks, resolved
// by their frontmatter `stage` field (inherited from the REQ at creation),
// not by the Stage-Plan task list — the field follows the task when it
// moves between stages and never goes stale.
//
// landed: every task is done AND merged (merge_status=merged — a done task
// with a stale PR does not count; the PR-closure loop must finish it first).
// reviewable: landed, OR every remaining task is blocked/closed — nothing in
// the phase is still dispatchable (a phase stuck on blockers must not stay
// silent forever; the PM stage-review then advises wait/narrow/split).
// blockers: ids of the remaining blocked tasks (review input reference).
// A phase with no tasks is neither landed nor reviewable (nothing to review).
func (r *Runner) stageTasksState(projDir, stageID string) (landed, reviewable bool, blockers []string) {
	tasksDir := filepath.Join(projDir, "Tasks")
	entries, err := os.ReadDir(tasksDir)
	if err != nil {
		return false, false, nil
	}
	foundAny := false
	onlyBlocked := true
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "TASK-") || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		path := filepath.Join(tasksDir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		fm, err := yamlfrontmatter.Parse(data)
		if err != nil || fm == nil || fm.Stage != stageID {
			continue
		}
		foundAny = true
		if fm.Status == "done" && fm.MergeStatus == "merged" {
			continue
		}
		if fm.Status == "closed" {
			continue // closed tasks never block a phase review
		}
		if fm.Status == "blocked" {
			blockers = append(blockers, fm.ID)
			continue
		}
		onlyBlocked = false // a dispatchable task remains: phase not reviewable
	}
	if !foundAny {
		return false, false, nil
	}
	landed = onlyBlocked && len(blockers) == 0
	reviewable = onlyBlocked
	return landed, reviewable, blockers
}

// stageIDFor returns the short stage id ("P1") for a parsed phase heading
// ("### Phase 1: 核心链路" → "P1"), or "" when the heading has no number.
var stageIDFor = func(name string) string {
	m := regexp.MustCompile(`Phase\s+(\d+)`).FindStringSubmatch(name)
	if m == nil {
		return ""
	}
	return "P" + m[1]
}

// processStageReviews detects a completed in-progress stage (all its tasks
// done+merged) and dispatches one PM stage-review session. The stage is
// flipped to review-pending by the PM skill so this never re-triggers for
// the same stage. One session per scan, matching the consolidate budget.
func (r *Runner) processStageReviews(ctx context.Context) int {
	if ctx.Err() != nil {
		return 0
	}
	projectsDir := filepath.Join(r.cfg.ObsidianVault, "Projects")
	projects, err := os.ReadDir(projectsDir)
	if err != nil {
		return 0
	}
	for _, projectEntry := range projects {
		if !projectEntry.IsDir() {
			continue
		}
		projDir := filepath.Join(projectsDir, projectEntry.Name())
		planPath := filepath.Join(projDir, "Notes", stagePlanName)
		if _, err := os.Stat(planPath); err != nil {
			continue
		}
		for _, phase := range parseStagePlan(planPath) {
			if phase.Status != "in-progress" {
				continue
			}
			stageID := stageIDFor(phase.Name)
			if stageID == "" {
				continue
			}
			landed, reviewable, blockers := r.stageTasksState(projDir, stageID)
			if !reviewable {
				continue
			}
			if !landed {
				r.logger.Printf("project %s: stage-review dispatched for %q (blocked-only remainder: %v)", projectEntry.Name(), phase.Name, blockers)
			}
			if err := r.runGrillingPM(ctx, "stage-review", planPath); err != nil {
				if errors.Is(err, errAPIKeyUnavailable) {
					return 0 // retry next scan
				}
				r.logger.Printf("project %s: stage-review %q: %v", projectEntry.Name(), phase.Name, err)
				continue
			}
			r.logger.Printf("project %s: stage-review dispatched for %q (stage tasks landed)", projectEntry.Name(), phase.Name)
			r.updateRoadmap(projectEntry.Name(), "阶段评审触发", phase.Name+" 评审已触发（任务全部合入或阻塞收敛），等待用户评审决策")
			return 1
		}
	}
	return 0
}
