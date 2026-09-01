package daemon

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

// stageReviewDecisionRE matches the user's review decision line in a
// Stage-Review.md ("评审决策: continue / supplement:{建议} / end").
var stageReviewDecisionRE = regexp.MustCompile(`(?m)^- 评审决策:\s*(.+)$`)

// stageSupplementRE extracts a supplement decision ("supplement:{建议}").
var stageSupplementRE = regexp.MustCompile(`(?i)^supplement:\s*(.+)$`)

// flipStageReviewDecision deterministically applies an answered Stage-Review
// to the Stage-Plan state machine (daemon-side, no PM session required):
//
//	continue        → current phase delivered; next phase planned→in-progress
//	                 (no next phase → Stage-Plan status: completed)
//	supplement:{s}  → continue + append "- 补充: {s}" to the next phase block
//	end             → current phase delivered; later phases ended; their
//	                 tasks closed (cancelled)
//
// The "current phase" is the in-progress phase when one exists, otherwise
// the review-pending phase named by the Stage-Review frontmatter `stage`
// field (the PM stage-review session flips the phase to review-pending in
// Mode 3 Step 4, so an answered review must locate it there — 2026-09-01:
// Phase 1 sat review-pending for 18 days and every answered review would
// have no-op'ed under the old in-progress-only lookup).
//
// The Stage-Review is then marked answered (grill_continue=false,
// status=answered) so neither the daemon nor the PM session re-processes it.
// The PM distribute session still runs afterwards for REQ annotations and
// knowledge sink — the state flip no longer depends on it.
func (r *Runner) flipStageReviewDecision(ctx context.Context) bool {
	if ctx.Err() != nil {
		return false
	}
	projectsDir := filepath.Join(r.cfg.ObsidianVault, "Projects")
	projects, err := os.ReadDir(projectsDir)
	if err != nil {
		return false
	}
	for _, projectEntry := range projects {
		if !projectEntry.IsDir() {
			continue
		}
		projDir := filepath.Join(projectsDir, projectEntry.Name())
		revPath := stageReviewPath(r.cfg.ObsidianVault, projectEntry.Name())
		if revPath == "" || !grillingListAnswered(revPath) || grillingListDistributed(revPath) {
			continue
		}
		revData, err := os.ReadFile(revPath)
		if err != nil {
			continue
		}
		m := stageReviewDecisionRE.FindSubmatch(revData)
		if m == nil {
			r.logger.Printf("project %s: stage-review answered but 评审决策 line missing, skipping flip", projectEntry.Name())
			continue
		}
		decision := strings.TrimSpace(string(m[1]))

		// The review's own stage (e.g. "Phase 1") locates the phase when the
		// plan no longer has an in-progress phase (PM flipped it to
		// review-pending when the review was produced).
		reviewStage := ""
		if rfm, err := yamlfrontmatter.Parse(revData); err == nil && rfm != nil {
			reviewStage = rfm.Stage
		}

		// Idempotency: the flip must not re-apply the same decision — a
		// re-run after a failed PM session would otherwise advance the
		// state machine to the NEXT phase (Phase 2 becomes in-progress on
		// the first flip; re-applying "continue" would deliver it too).
		// Keying on the decision content lets a revised decision re-flip.
		flipKey := "flipapplied|" + projectEntry.Name() + "|" + decision
		if r.diagNotified(flipKey) {
			continue
		}

		planPath := filepath.Join(projDir, "Notes", stagePlanName)
		planData, err := os.ReadFile(planPath)
		if err != nil {
			r.logger.Printf("project %s: stage-review flip: read stage plan: %v", projectEntry.Name(), err)
			continue
		}
		content, flipped, summary := r.applyStageDecision(string(planData), decision, reviewStage, projDir, projectEntry.Name())
		if !flipped {
			continue
		}
		if err := os.WriteFile(planPath, []byte(content), 0o644); err != nil {
			r.logger.Printf("project %s: stage-review flip: write stage plan: %v", projectEntry.Name(), err)
			continue
		}
		// The Stage-Review status=answered marker is left to the PM
		// distribute session (Mode 2.5 Step 5) so a failed PM session
		// retries the annotations instead of losing them. The daemon flip
		// itself is idempotent: a re-run finds no in-progress or
		// review-pending phase and no-ops.
		r.logger.Printf("project %s: stage-review decision %q applied: %s", projectEntry.Name(), decision, summary)
		r.updateRoadmap(projectEntry.Name(), "阶段决策", summary)
		return true
	}
	return false
}

// applyStageDecision rewrites the Stage-Plan content per the review decision
// and closes later-phase tasks for "end". Returns the new content, whether
// anything changed, and a one-line summary.
//
// The current phase is located in priority order:
//  1. the in-progress phase (pre-review shape);
//  2. the review-pending phase named by reviewStage (post-review shape —
//     PM Mode 3 Step 4 flips the phase to review-pending, so the answer
//     must be able to locate it there);
//  3. the first review-pending phase (reviewStage unknown).
func (r *Runner) applyStageDecision(content, decision, reviewStage, projDir, project string) (string, bool, string) {
	phases := parseStagePlanContent(content)
	if len(phases) == 0 {
		return content, false, ""
	}
	currentIdx := -1
	for i, p := range phases {
		if p.Status == "in-progress" {
			currentIdx = i
			break
		}
	}
	if currentIdx == -1 && reviewStage != "" {
		want := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(reviewStage), "###"))
		for i, p := range phases {
			name := strings.TrimSpace(p.Name)
			if (strings.EqualFold(name, want) || strings.HasPrefix(name, want+":")) && p.Status == "review-pending" {
				currentIdx = i
				break
			}
		}
	}
	if currentIdx == -1 && reviewStage == "" {
		// reviewStage unknown: fall back to the first review-pending phase.
		// A non-empty reviewStage that matches nothing must NOT fall back —
		// the answer belongs to a different phase and flipping the wrong
		// one would deliver it silently.
		for i, p := range phases {
			if p.Status == "review-pending" {
				currentIdx = i
				break
			}
		}
	}
	if currentIdx == -1 {
		return content, false, "no in-progress or review-pending phase"
	}
	current := phases[currentIdx]
	nextIdx := currentIdx + 1
	hasNext := nextIdx < len(phases)

	if strings.EqualFold(decision, "end") {
		if blockers := r.stageEndBlockers(projDir, phases, nextIdx); len(blockers) > 0 {
			return content, false, "end blocked by active later-stage tasks: " + strings.Join(blockers, ", ")
		}
	}

	// continue | supplement | end all deliver the current phase.
	out, ok := flipStageStatus(content, current.Name, "delivered")
	if !ok {
		return content, false, "phase block not found"
	}
	summary := current.Name + " delivered"

	switch {
	case strings.EqualFold(decision, "continue"), strings.EqualFold(decision, "end"):
	case stageSupplementRE.MatchString(decision):
		s := stageSupplementRE.FindStringSubmatch(decision)[1]
		if hasNext {
			out = appendStageSupplement(out, phases[nextIdx].Name, strings.TrimSpace(s))
		}
		summary += " (supplement)"
	default:
		// Unknown decision: leave everything as-is (only flip on valid input).
		return content, false, "unknown decision"
	}

	if strings.EqualFold(decision, "end") {
		// Deliver current; end every later phase and close their tasks.
		for i := nextIdx; i < len(phases); i++ {
			out, _ = flipStageStatus(out, phases[i].Name, "ended")
			summary += "; " + phases[i].Name + " ended"
		}
		r.closePhaseTasks(projDir, phases, nextIdx)
	} else if hasNext {
		out, _ = flipStageStatus(out, phases[nextIdx].Name, "in-progress")
		summary += "; " + phases[nextIdx].Name + " in-progress"
	} else {
		// No next phase: project complete.
		out = setStagePlanStatus(out, "completed")
		summary += "; project completed"
	}
	return out, true, summary
}

func (r *Runner) stageEndBlockers(projDir string, phases []stagePhase, startIdx int) []string {
	if projDir == "" || startIdx >= len(phases) {
		return nil
	}
	stageIDs := make(map[string]bool)
	for i := startIdx; i < len(phases); i++ {
		if id := stageIDFor(phases[i].Name); id != "" {
			stageIDs[id] = true
		}
	}
	entries, err := os.ReadDir(filepath.Join(projDir, "Tasks"))
	if err != nil {
		return []string{"task scan failed"}
	}
	var blockers []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "TASK-") || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(projDir, "Tasks", entry.Name()))
		if err != nil {
			blockers = append(blockers, entry.Name()+" (unreadable)")
			continue
		}
		fm, err := yamlfrontmatter.Parse(data)
		if err != nil || fm == nil {
			blockers = append(blockers, entry.Name()+" (unparsable)")
			continue
		}
		if !stageIDs[fm.Stage] || fm.Status == "done" || fm.Status == "closed" {
			continue
		}
		if fm.PlanVersion > 0 || fm.TargetBranch != "" || fm.PRURL != "" || fm.MergeStatus != "" || fm.CheckpointCommit != "" {
			blockers = append(blockers, "TASK-"+fm.ID+" ("+fm.Status+")")
			continue
		}
		switch fm.Status {
		case "ready", "blocked", "needs-grilling", "needs-refining":
			// No delivery has started: a user-approved stage end may close it.
			continue
		default:
			blockers = append(blockers, "TASK-"+fm.ID+" ("+fm.Status+")")
		}
	}
	return blockers
}

// closePhaseTasks closes every task belonging to phases from startIdx on.
func (r *Runner) closePhaseTasks(projDir string, phases []stagePhase, startIdx int) {
	tasksDir := filepath.Join(projDir, "Tasks")
	entries, err := os.ReadDir(tasksDir)
	if err != nil {
		return
	}
	stageIDs := make(map[string]bool)
	for i := startIdx; i < len(phases); i++ {
		if id := stageIDFor(phases[i].Name); id != "" {
			stageIDs[id] = true
		}
	}
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
		if err != nil || fm == nil || !stageIDs[fm.Stage] {
			continue
		}
		if fm.Status == "closed" || fm.Status == "done" {
			continue
		}
		if err := yamlfrontmatter.Update(path, map[string]interface{}{
			"status":         "closed",
			"closure_reason": "cancelled",
			"closure_note":   "阶段化交付提前结束（用户评估满意）",
		}); err != nil {
			r.logger.Printf("project close phase task %s: %v", entry.Name(), err)
			continue
		}
		r.cleanupTaskArtifacts(path, r.repoDirForTask(fm.Project))
	}
}

// flipStageStatus rewrites the "- status:" line inside the phase block whose
// heading matches phaseName.
func flipStageStatus(content, phaseName, newStatus string) (string, bool) {
	headings := stagePhaseRE.FindAllStringIndex(content, -1)
	for i, loc := range headings {
		end := len(content)
		if i+1 < len(headings) {
			end = headings[i+1][0]
		}
		// Normalize the "### " prefix before comparing with the parsed name.
		firstLine := strings.TrimSpace(strings.SplitN(content[loc[0]:end], "\n", 2)[0])
		firstLine = strings.TrimSpace(strings.TrimPrefix(firstLine, "###"))
		if !strings.HasPrefix(firstLine, phaseName) {
			continue
		}
		block := content[loc[0]:end]
		sm := stageStatusRE.FindStringSubmatchIndex(block)
		if sm == nil {
			return content, false
		}
		replacement := "- status: " + newStatus
		out := content[:loc[0]+sm[0]] + replacement + content[loc[0]+sm[1]:]
		return out, true
	}
	return content, false
}

// appendStageSupplement adds a "- 补充:" line to the phase block.
func appendStageSupplement(content, phaseName, supplement string) string {
	headings := stagePhaseRE.FindAllStringIndex(content, -1)
	for i, loc := range headings {
		end := len(content)
		if i+1 < len(headings) {
			end = headings[i+1][0]
		}
		firstLine := strings.SplitN(content[loc[0]:end], "\n", 2)[0]
		firstLine = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(firstLine), "###"))
		if !strings.HasPrefix(firstLine, phaseName) {
			continue
		}
		// Insert after the status line inside the block.
		block := content[loc[0]:end]
		sm := stageStatusRE.FindStringSubmatchIndex(block)
		if sm == nil {
			return content
		}
		insertAt := loc[0] + sm[1]
		return content[:insertAt] + "\n- 补充: " + supplement + content[insertAt:]
	}
	return content
}

// setStagePlanStatus updates the Stage-Plan frontmatter `status:` value.
func setStagePlanStatus(content, status string) string {
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "status:") && i < 10 { // frontmatter region
			lines[i] = "status: " + status
			break
		}
	}
	return strings.Join(lines, "\n")
}
