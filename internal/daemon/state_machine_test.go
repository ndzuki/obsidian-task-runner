package daemon

import (
	"testing"

	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

func TestNextLocalTransition_PendingRequirementOverridesResume(t *testing.T) {
	fm := &yamlfrontmatter.Frontmatter{
		Status:          "needs-grilling",
		PendingReq:      true,
		GrillDone:       true,
		GrillResolution: "resume",
		GrillPrevStatus: "implementing",
	}

	transition, ok := nextLocalTransition(fm)
	if !ok {
		t.Fatal("expected local transition")
	}
	if transition.Status != "refining" {
		t.Fatalf("status = %q, want refining", transition.Status)
	}
	if transition.Updates["grill_resolution"] != "" {
		t.Fatalf("grill_resolution = %#v, want cleared", transition.Updates["grill_resolution"])
	}
}

func TestNextLocalTransition_PlanApprovalStartsImplementation(t *testing.T) {
	fm := &yamlfrontmatter.Frontmatter{
		Status:       "plan-review",
		PlanApproved: true,
		AdrProposed:  []interface{}{"ADR-001"},
	}

	transition, ok := nextLocalTransition(fm)
	if !ok || !transition.Dispatch {
		t.Fatal("expected dispatching local transition")
	}
	if transition.Status != "implementing" {
		t.Fatalf("status = %q, want implementing", transition.Status)
	}
	if transition.Updates["plan_approved"] != false || transition.Updates["adr_approved"] != true {
		t.Fatalf("updates = %#v, want consumed plan gate and approved ADRs", transition.Updates)
	}
}

func TestNextLocalTransition_CloseGateCreatesClosedTerminalState(t *testing.T) {
	fm := &yamlfrontmatter.Frontmatter{
		Status:           "review",
		ReworkResolution: "close",
		CloseApproved:    true,
		ClosureReason:    "cancelled",
	}

	transition, ok := nextLocalTransition(fm)
	if !ok {
		t.Fatal("expected close transition")
	}
	if transition.Status != "closed" || transition.Dispatch {
		t.Fatalf("transition = %#v, want non-dispatching closed state", transition)
	}
	if transition.Updates["completed"] == "" {
		t.Fatal("closed transition must record completion time")
	}
}
