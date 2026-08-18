package daemon

import (
	"path/filepath"
	"testing"
)

// TestRecoverBlockedPendingReqRoutesPhaseFailure guards the TASK-001-deployd
// stall: a phase-failure blocked task whose REQ changed (pending_req) has no
// downstream to unwind it via resolveBlockedDependencies, and a manual resume
// would re-implement the stale requirement. It must route back to refining
// with all grill/plan/merge/stall residuals cleared.
func TestRecoverBlockedPendingReqRoutesPhaseFailure(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	tasksDir := filepath.Join(vault, "Projects", "001-test", "Tasks")
	path := filepath.Join(tasksDir, "TASK-001-direct.md")
	writeHealthTask(t, tasksDir, "TASK-001-direct.md", `---
id: "001"
title: Direct
project: test
assignee: default
status: blocked
blocked_phase: implementing
phase_error_code: PHASE_TIMEOUT
phase_error: "超时"
phase_log: /tmp/x.log
resume_approved: false
auto_resume_pending: false
pending_req: true
blocked_by: []
plan_approved: true
merge_approved: true
grill_done: true
grill_resolution: replan
grill_prev_status: implementing
round2_stall_until: "2026-08-20T00:00:00+08:00"
---
# Direct
`)

	runner := healthRunner(t, vault)
	runner.recoverBlockedPendingReq()

	fm := mustParse(t, path)
	if fm.Status != "refining" {
		t.Fatalf("status = %q, want refining", fm.Status)
	}
	if !fm.PendingReq {
		t.Fatal("pending_req must stay true until the new plan succeeds (invariant: pending_req MUST NOT be cleared until new plan succeeds)")
	}
	for field, val := range map[string]string{
		"blocked_phase":    fm.BlockedPhase,
		"phase_error":      fm.PhaseError,
		"phase_error_code": fm.PhaseErrorCode,
		"phase_log":        fm.PhaseLog,
	} {
		if val != "" {
			t.Fatalf("%s = %q, want empty", field, val)
		}
	}
	if fm.ResumeApproved || fm.AutoResumePending {
		t.Fatalf("resume markers not cleared: resume_approved=%v auto_resume_pending=%v", fm.ResumeApproved, fm.AutoResumePending)
	}
	if fm.GrillDone || fm.GrillResolution != "" || fm.GrillPrevStatus != "" {
		t.Fatalf("grill residual not cleared: done=%v resolution=%q prev=%q", fm.GrillDone, fm.GrillResolution, fm.GrillPrevStatus)
	}
	if fm.PlanApproved || fm.MergeApproved {
		t.Fatalf("approval residual not cleared: plan=%v merge=%v", fm.PlanApproved, fm.MergeApproved)
	}
	if fm.Round2StallUntil != "" {
		t.Fatalf("round2_stall_until = %q, want empty", fm.Round2StallUntil)
	}
}

// TestRecoverBlockedPendingReqSkipsEntryGateAndUserBlocks guards the
// exclusions: entry gates (PREREQUISITE_SMOKE_FAILED) and non-transient user
// decisions (REQ_MISSING) keep their own recovery paths and must not be routed
// to refining by the pending_req override.
func TestRecoverBlockedPendingReqSkipsEntryGateAndUserBlocks(t *testing.T) {
	cases := []struct {
		name string
		code string
		by   string
	}{
		{name: "entry-gate", code: "PREREQUISITE_SMOKE_FAILED", by: `["TASK-010"]`},
		{name: "req-missing", code: "REQ_MISSING", by: "[]"},
		{name: "empty-code-gate", code: "", by: `["TASK-010"]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			vault := filepath.Join(dir, "vault")
			tasksDir := filepath.Join(vault, "Projects", "001-test", "Tasks")
			path := filepath.Join(tasksDir, "TASK-002-skip.md")
			writeHealthTask(t, tasksDir, "TASK-002-skip.md", `---
id: "002"
title: Skip
project: test
assignee: default
status: blocked
blocked_phase: implementing
phase_error_code: `+tc.code+`
pending_req: true
blocked_by: `+tc.by+`
---
# Skip
`)
			runner := healthRunner(t, vault)
			runner.recoverBlockedPendingReq()

			fm := mustParse(t, path)
			if fm.Status != "blocked" {
				t.Fatalf("status = %q, want blocked (excluded from override)", fm.Status)
			}
		})
	}
}

// TestRecoverBlockedPendingReqSkipsWithoutPendingReq guards the no-op: a
// phase-failure blocked task with no REQ change stays blocked — manual resume
// is still the contract for that path.
func TestRecoverBlockedPendingReqSkipsWithoutPendingReq(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	tasksDir := filepath.Join(vault, "Projects", "001-test", "Tasks")
	path := filepath.Join(tasksDir, "TASK-003-nopending.md")
	writeHealthTask(t, tasksDir, "TASK-003-nopending.md", `---
id: "003"
title: NoPending
project: test
assignee: default
status: blocked
blocked_phase: implementing
phase_error_code: PHASE_TIMEOUT
pending_req: false
blocked_by: []
---
# NoPending
`)
	runner := healthRunner(t, vault)
	runner.recoverBlockedPendingReq()

	fm := mustParse(t, path)
	if fm.Status != "blocked" {
		t.Fatalf("status = %q, want blocked (no pending_req)", fm.Status)
	}
}
