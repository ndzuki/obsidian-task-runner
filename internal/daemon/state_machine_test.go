package daemon

import (
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
