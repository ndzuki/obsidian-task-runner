package daemon

import "testing"

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
	}
}
