package daemon

import (
	"strings"
	"time"

	"github.com/ndzuki/obsidian-task-runner/internal/task"
	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

type localTransition struct {
	Status   string
	Updates  map[string]interface{}
	Dispatch bool
	Reason   string
}

func nextLocalTransition(fm *yamlfrontmatter.Frontmatter) (localTransition, bool) {
	if fm == nil || fm.Status == "closed" {
		return localTransition{}, false
	}

	if fm.PendingReq {
		switch fm.Status {
		case "needs-grilling":
			// Parked disputes wait for the project-level Grilling-Decisions.md
			// list (PM distribute resets to refining once answered). The
			// pending-req precedence rule must not yank a parked task back
			// into refining — that re-opens the repeated-dispute loop
			// (TASK-069: 35+ rounds) instead of waiting for the one-time
			// answer.
			if fm.GrillParked {
				break
			}
			return transitionToRefining("pending requirement takes precedence"), true
		case "review", "conflict", "done":
			return transitionToRefining("pending requirement takes precedence"), true
		}
	}

	if fm.ReworkResolution != "" {
		switch fm.ReworkResolution {
		case "resume":
			if fm.Status == "review" {
				return localTransition{
					Status:   "implementing",
					Dispatch: true,
					Reason:   "review feedback resumes implementation",
					Updates: map[string]interface{}{
						"status":            "implementing",
						"plan_approved":     false,
						"merge_approved":    false,
						"rework_resolution": "",
					},
				}, true
			}
		case "replan":
			if fm.Status == "plan-review" || fm.Status == "review" {
				transition := transitionToRefining("review feedback requires replanning")
				transition.Updates["pending_req"] = true
				transition.Updates["rework_resolution"] = ""
				return transition, true
			}
		case "close":
			if (fm.Status == "plan-review" || fm.Status == "review") && fm.CloseApproved {
				return localTransition{
					Status: "closed",
					Reason: "close gate approved",
					Updates: map[string]interface{}{
						"status":            "closed",
						"completed":         time.Now().Format(time.RFC3339),
						"plan_approved":     false,
						"merge_approved":    false,
						"close_approved":    false,
						"rework_resolution": "",
					},
				}, true
			}
		}
	}

	switch fm.Status {
	case "ready":
		return localTransition{
			Status:   "refining",
			Dispatch: true,
			Reason:   "ready task enters maturity gate",
			Updates:  map[string]interface{}{"status": "refining"},
		}, true
	case "needs-refining":
		// Legacy status from an earlier daemon version; the current name for
		// "requirement needs refinement via Grilling" is needs-grilling.
		// Migrating lets the normal needs-grilling path (Kitty tab creation,
		// reminders, lease handling) pick the task up.
		return localTransition{
			Status:   "needs-grilling",
			Dispatch: true,
			Reason:   "legacy needs-refining migrated to needs-grilling",
			Updates:  map[string]interface{}{"status": "needs-grilling"},
		}, true
	case "plan-review":
		if fm.PlanApproved {
			return localTransition{
				Status:   "implementing",
				Dispatch: true,
				Reason:   "plan gate approved",
				Updates: map[string]interface{}{
					"status":       "implementing",
					"adr_approved": hasADRProposal(fm.AdrProposed),
				},
			}, true
		}
		if fm.AutoApprove {
			// User opted in to skip the plan-review gate (symmetry with
			// auto_merge): a freshly produced plan counts as approved and
			// the task moves straight to implementing. Plan quality is the
			// user's trade-off — the flag is explicit per task. The caller
			// (prepareBatch) notifies on the "auto_approve" reason marker so
			// this function stays side-effect free.
			return localTransition{
				Status:   "implementing",
				Dispatch: true,
				Reason:   "auto_approve plan gate (no manual review)",
				Updates: map[string]interface{}{
					"status":        "implementing",
					"plan_approved": true,
					"adr_approved":  hasADRProposal(fm.AdrProposed),
				},
			}, true
		}
	case "done":
		// Done but never merged (PR or branch exists, merge_status != merged):
		// the merge flow was interrupted or the PR went stale after the task
		// was marked done — reopen it so the PR lifecycle closes on its own
		// (TASK-067/019 PRs sat CONFLICTING for weeks because done is not a
		// mergeable status). auto_merge tasks re-authorize automatically;
		// manual-gate tasks wait for merge_approved.
		if task.DoneReopensMerge(fm) {
			return localTransition{
				Status:   "review",
				Dispatch: true,
				Reason:   "done task has unmerged PR, reopening merge flow",
				Updates: map[string]interface{}{
					"status":           "review",
					"merge_approved":   fm.AutoMerge,
					"merge_status":     "",
					"phase_error_code": "",
					"phase_error":      "",
					"phase_log":        "",
					"completed":        "",
				},
			}, true
		}
	case "needs-grilling":
		if fm.GrillParked {
			// Parked disputes live in the project-level Grilling-Decisions.md
			// list. NEVER auto-transition (resume/replan) while parked — a
			// stale grill_resolution would re-open the no-op replan loop
			// (TASK-066: 17 rounds zero convergence on an unchanged REQ).
			// Only PM distribute explicitly resets parked tasks to refining.
			return localTransition{}, false
		}
		if !fm.GrillDone {
			return localTransition{}, false
		}
		switch fm.GrillResolution {
		case "resume":
			status := fm.GrillPrevStatus
			if status == "" {
				status = "implementing"
			}
			return localTransition{
				Status:   status,
				Dispatch: true,
				Reason:   "grilling resolution resumes prior phase",
				Updates:  grillingReleaseUpdates(status),
			}, true
		case "replan":
			transition := transitionToRefining("grilling resolution requires replanning")
			for key, value := range grillingReleaseUpdates("refining") {
				transition.Updates[key] = value
			}
			transition.Updates["pending_req"] = true
			return transition, true
		}
	}

	if fm.PlanApproved && fm.Status != "plan-review" && fm.Status != "implementing" {
		return localTransition{
			Status:   fm.Status,
			Dispatch: true,
			Reason:   "premature plan approval reset",
			Updates:  map[string]interface{}{"plan_approved": false},
		}, true
	}

	return localTransition{}, false
}

func transitionToRefining(reason string) localTransition {
	return localTransition{
		Status: fmStatusRefining,
		Reason: reason,
		Updates: map[string]interface{}{
			"status":            fmStatusRefining,
			"plan_approved":     false,
			"merge_approved":    false,
			"grill_done":        false,
			"grill_resolution":  "",
			"grill_context":     "",
			"grill_prev_status": "",
			"grill_parked":      false,
		},
	}
}

func grillingReleaseUpdates(status string) map[string]interface{} {
	return map[string]interface{}{
		"status":             status,
		"grill_done":         false,
		"grill_owner":        "",
		"grill_started_at":   "",
		"grill_heartbeat_at": "",
		"grill_resolution":   "",
		"grill_context":      "",
		"grill_prev_status":  "",
		"grill_continue":     false,
		"grill_parked":       false,
	}
}

func hasADRProposal(value any) bool {
	switch proposed := value.(type) {
	case nil:
		return false
	case string:
		trimmed := strings.TrimSpace(proposed)
		return trimmed != "" && trimmed != "[]" && trimmed != "null"
	case []interface{}:
		return len(proposed) > 0
	case []string:
		return len(proposed) > 0
	default:
		return true
	}
}

const fmStatusRefining = "refining"
