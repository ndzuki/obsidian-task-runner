package daemon

import (
	"strings"
	"testing"

	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

// TestGrillingReleaseUpdatesClearsGrillContinue guards the async-grilling
// field lifecycle: consuming a grilling result (resume/replan) must clear
// grill_continue so a stale true cannot re-trigger the async-resume path
// the next time the task enters needs-grilling.
func TestGrillingReleaseUpdatesClearsGrillContinue(t *testing.T) {
	for _, status := range []string{"refining", "implementing", "plan-review"} {
		updates := grillingReleaseUpdates(status)
		if got, ok := updates["grill_continue"]; !ok || got != false {
			t.Fatalf("grillingReleaseUpdates(%q) missing grill_continue=false: %+v", status, updates)
		}
		if updates["status"] != status {
			t.Fatalf("grillingReleaseUpdates(%q) status = %v", status, updates["status"])
		}
		if updates["grill_done"] != false {
			t.Fatalf("grillingReleaseUpdates(%q) must keep clearing grill_done", status)
		}
		if got, ok := updates["grill_parked"]; !ok || got != false {
			t.Fatalf("grillingReleaseUpdates(%q) missing grill_parked=false: %+v", status, updates)
		}
	}
}

// TestTransitionToRefiningClearsGrillParked guards the PM-consolidation
// lifecycle: any route back into refining (replan, REQ change) must release
// the parked state. grill_repeat is deliberately kept — it survives across
// refine rounds so repeat disputes can still escalate to park again.
func TestTransitionToRefiningClearsGrillParked(t *testing.T) {
	transition := transitionToRefining("test")
	if got, ok := transition.Updates["grill_parked"]; !ok || got != false {
		t.Fatalf("transitionToRefining missing grill_parked=false: %+v", transition.Updates)
	}
	if got := transition.Updates["status"]; got != "refining" {
		t.Fatalf("transitionToRefining status = %v, want refining", got)
	}
	if _, cleared := transition.Updates["grill_repeat"]; cleared {
		t.Fatal("transitionToRefining must NOT clear grill_repeat (repeat counter survives)")
	}
}

// TestPendingReqLeavesParkedTasksAlone guards the park lifecycle: a
// needs-grilling task with grill_parked=true must NOT be yanked back to
// refining by the pending-req precedence rule — park means the dispute
// waits for the project-level decision list (PM distribute resets to
// refining after the user answers). Non-parked tasks keep the legacy
// pending-req precedence behavior.
func TestPendingReqLeavesParkedTasksAlone(t *testing.T) {
	parked := &yamlfrontmatter.Frontmatter{
		Status:      "needs-grilling",
		PendingReq:  true,
		GrillParked: true,
		GrillDone:   false,
	}
	if transition, ok := nextLocalTransition(parked); ok {
		t.Fatalf("parked + pending_req produced transition %q (%s), want none", transition.Status, transition.Reason)
	}

	active := &yamlfrontmatter.Frontmatter{
		Status:     "needs-grilling",
		PendingReq: true,
		GrillDone:  false,
	}
	transition, ok := nextLocalTransition(active)
	if !ok {
		t.Fatal("pending_req + non-parked needs-grilling produced no transition, want refining")
	}
	if transition.Status != "refining" {
		t.Fatalf("pending_req + non-parked needs-grilling = %q, want refining", transition.Status)
	}
}

// TestAutoApproveSkipsPlanReviewGate guards the opt-in plan automation: a
// plan-review task with auto_approve=true moves straight to implementing
// (plan_approved set), symmetric with auto_merge. Manual tasks still wait.
func TestAutoApproveSkipsPlanReviewGate(t *testing.T) {
	auto := &yamlfrontmatter.Frontmatter{
		Status:      "plan-review",
		AutoApprove: true,
	}
	transition, ok := nextLocalTransition(auto)
	if !ok {
		t.Fatal("auto_approve plan-review produced no transition")
	}
	if transition.Status != "implementing" {
		t.Fatalf("auto_approve plan-review = %q, want implementing", transition.Status)
	}
	if got := transition.Updates["plan_approved"]; got != true {
		t.Fatalf("auto_approve must set plan_approved=true, got %v", got)
	}
	if !strings.Contains(transition.Reason, "auto_approve") {
		t.Fatalf("transition reason = %q, want auto_approve marker", transition.Reason)
	}

	manual := &yamlfrontmatter.Frontmatter{
		Status:      "plan-review",
		AutoApprove: false,
	}
	if transition, ok := nextLocalTransition(manual); ok {
		t.Fatalf("manual plan-review produced transition %q, want none", transition.Status)
	}
}

// TestParkedBlocksStaleReplan guards the no-op replan loop (TASK-066: 17
// rounds zero convergence): a parked task carrying a stale
// grill_done+grill_resolution=replan from before parking must NEVER
// auto-transition back to refining — that re-opens the cycle on an
// unchanged REQ. Only PM distribute explicitly resets parked tasks.
func TestParkedBlocksStaleReplan(t *testing.T) {
	stale := &yamlfrontmatter.Frontmatter{
		Status:         "needs-grilling",
		GrillParked:    true,
		GrillDone:      true,
		GrillResolution: "replan",
		PlanVersion:    17,
	}
	if transition, ok := nextLocalTransition(stale); ok {
		t.Fatalf("parked task with stale replan produced transition %q (%s), want none", transition.Status, transition.Reason)
	}
}

// TestDoneWithUnmergedPRReopensMerge guards the PR lifecycle closure: a
// task in done whose PR never merged (merge_status != merged, PR or branch
// exists) must reopen as review so the merge flow runs again (stale
// CONFLICTING PRs from done tasks previously stalled forever —
// "precondition: status done is not mergeable"). auto_merge re-authorizes
// automatically; manual-gate tasks get merge_approved=false.
func TestDoneWithUnmergedPRReopensMerge(t *testing.T) {
	autoMerge := &yamlfrontmatter.Frontmatter{
		Status:        "done",
		MergeStatus:   "conflict-resolve-attempted",
		PRURL:         "https://github.com/ndzuki/release-manager/pull/51",
		TargetBranch:  "task/067-operation-creation-workflow",
		AutoMerge:     true,
		PhaseErrorCode: "BASE_COMMIT_MISMATCH",
	}
	transition, ok := nextLocalTransition(autoMerge)
	if !ok {
		t.Fatal("done with unmerged PR produced no transition, want review")
	}
	if transition.Status != "review" {
		t.Fatalf("done with unmerged PR = %q, want review", transition.Status)
	}
	if got := transition.Updates["merge_approved"]; got != true {
		t.Fatalf("auto_merge done-reopen merge_approved = %v, want true", got)
	}
	if got := transition.Updates["merge_status"]; got != "" {
		t.Fatalf("reopened merge_status = %v, want cleared", got)
	}
	if got := transition.Updates["phase_error_code"]; got != "" {
		t.Fatalf("reopened phase_error_code = %v, want cleared", got)
	}

	manualGate := &yamlfrontmatter.Frontmatter{
		Status:       "done",
		MergeStatus:  "checks-pending",
		TargetBranch: "task/051-web-customer-management",
		AutoMerge:    false,
	}
	transition, ok = nextLocalTransition(manualGate)
	if !ok {
		t.Fatal("done (manual gate) with unmerged PR produced no transition, want review")
	}
	if got := transition.Updates["merge_approved"]; got != false {
		t.Fatalf("manual-gate done-reopen merge_approved = %v, want false", got)
	}
}

// TestDoneMergedStaysTerminal guards that a genuinely merged task (or a
// done task with no PR/branch at all) stays terminal.
func TestDoneMergedStaysTerminal(t *testing.T) {
	merged := &yamlfrontmatter.Frontmatter{
		Status:      "done",
		MergeStatus: "merged",
		PRURL:       "https://example.com/pull/1",
	}
	if transition, ok := nextLocalTransition(merged); ok {
		t.Fatalf("merged done produced transition %q (%s), want none", transition.Status, transition.Reason)
	}
	noPR := &yamlfrontmatter.Frontmatter{Status: "done"}
	if transition, ok := nextLocalTransition(noPR); ok {
		t.Fatalf("done without PR/branch produced transition %q (%s), want none", transition.Status, transition.Reason)
	}
}
