package daemon

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ndzuki/obsidian-task-runner/internal/config"
	"github.com/ndzuki/obsidian-task-runner/internal/task"
	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

func TestResolveBlockedDependenciesAutoResumesUpstream(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	tasksDir := filepath.Join(vault, "Projects", "001-test", "Tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Upstream: phase-failure blocked, not resumed
	upstream := filepath.Join(tasksDir, "TASK-001-upstream.md")
	if err := os.WriteFile(upstream, []byte(`---
id: "001"
title: Upstream
project: test
status: blocked
blocked_phase: implementing
resume_approved: false
assignee: default
---
# Upstream
`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Downstream: blocked waiting on upstream
	downstream := filepath.Join(tasksDir, "TASK-002-downstream.md")
	if err := os.WriteFile(downstream, []byte(`---
id: "002"
title: Downstream
project: test
status: blocked
blocked_by: ["TASK-001"]
assignee: default
---
# Downstream
`), 0o644); err != nil {
		t.Fatal(err)
	}

	runner := New(&config.Config{ObsidianVault: vault})
	runner.logger = log.New(io.Discard, "", 0)
	runner.resolveBlockedDependencies()

	data, err := os.ReadFile(upstream)
	if err != nil {
		t.Fatal(err)
	}
	fm, err := yamlfrontmatter.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if !fm.ResumeApproved {
		t.Fatal("upstream resume_approved should be true after dependency resolution")
	}
}

// TestParkedFactRecoveryUnparksOnConvergedUpstream guards the D-19 style
// "park until upstream changes" exit: a needs-grilling+parked task whose
// blocked_by upstreams all landed (done + no phase error) is automatically
// un-parked into refining — no distribute round-trip needed.
func TestParkedFactRecoveryUnparksOnConvergedUpstream(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	projDir := filepath.Join(vault, "Projects", "001-test")
	tasksDir := filepath.Join(projDir, "Tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Upstream done + merged (phase error cleared).
	upstream := filepath.Join(tasksDir, "TASK-001-up.md")
	if err := os.WriteFile(upstream, []byte(`---
id: "001"
project: test
status: done
phase_error_code: ""
assignee: default
---
# Up
`), 0o644); err != nil {
		t.Fatal(err)
	}
	// Parked task waiting on upstream facts.
	parked := filepath.Join(tasksDir, "TASK-002-e2e.md")
	if err := os.WriteFile(parked, []byte(`---
id: "002"
project: test
status: needs-grilling
grill_parked: true
grill_done: true
grill_resolution: replan
blocked_by: ["TASK-001"]
assignee: default
---
# E2E
`), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := New(&config.Config{ObsidianVault: vault})
	runner.logger = log.New(io.Discard, "", 0)
	runner.parkedFactRecovery()

	data, err := os.ReadFile(parked)
	if err != nil {
		t.Fatal(err)
	}
	fm, err := yamlfrontmatter.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if fm.Status != "refining" || fm.GrillParked {
		t.Fatalf("parked task should be un-parked to refining, got status=%s parked=%v", fm.Status, fm.GrillParked)
	}
	if fm.GrillResolution != "" {
		t.Fatalf("stale grill_resolution must be cleared, got %q", fm.GrillResolution)
	}

	// Upstream with unresolved error keeps the gate shut.
	_ = os.WriteFile(upstream, []byte(`---
id: "001"
project: test
status: done
phase_error_code: BASE_COMMIT_MISMATCH
assignee: default
---
# Up
`), 0o644)
	parked2 := filepath.Join(tasksDir, "TASK-003-e2e2.md")
	if err := os.WriteFile(parked2, []byte(`---
id: "003"
project: test
status: needs-grilling
grill_parked: true
blocked_by: ["TASK-001"]
assignee: default
---
# E2E2
`), 0o644); err != nil {
		t.Fatal(err)
	}
	runner.parkedFactRecovery()
	data, _ = os.ReadFile(parked2)
	fm, _ = yamlfrontmatter.Parse(data)
	if fm.Status != "needs-grilling" || !fm.GrillParked {
		t.Fatal("parked task must stay parked while upstream carries an unresolved merge error")
	}
}

// TestParkedFactRecoveryKeepsDisputePark guards the TASK-068 loop: a
// needs-grilling+parked task whose park is a DISPUTE (its conflicts escalated
// into the project-level decision list, which still holds an unanswered
// "来源任务: TASK-<id>" entry) must NOT un-park on blocked_by convergence —
// its recovery gate is the list answers, consumed only by PM distribute. The
// D-19 style prerequisite-gate park (no list entry sourcing the task) still
// exits on facts.
func TestParkedFactRecoveryKeepsDisputePark(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	projDir := filepath.Join(vault, "Projects", "001-test")
	tasksDir := filepath.Join(projDir, "Tasks")
	notesDir := filepath.Join(projDir, "Notes")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Upstream done + merged.
	upstream := filepath.Join(tasksDir, "TASK-001-up.md")
	if err := os.WriteFile(upstream, []byte(`---
id: "001"
project: test
status: done
phase_error_code: ""
assignee: default
---
# Up
`), 0o644); err != nil {
		t.Fatal(err)
	}
	// Dispute-parked task whose blocked_by all landed.
	parked := filepath.Join(tasksDir, "TASK-002-dispute.md")
	writeFile(t, parked, `---
id: "002"
project: test
status: needs-grilling
grill_parked: true
grill_done: true
grill_resolution: replan
blocked_by: ["TASK-001"]
assignee: default
---
# DisputePark
`)
	// Decision list still holds an UNANSWERED entry sourced from TASK-002.
	listPath := filepath.Join(notesDir, "Grilling-Decisions.md")
	writeFile(t, listPath, `---
id: "grilling-decisions"
project: test
status: open
grill_continue: false
---
# Grilling Decisions

## 决策点

### D-88: REQ-068 — 幂等 scope 冲突
- 来源任务: TASK-002
- 冲突: 未决
- 建议: org-inclusive
- 决策: {用户填写}
`)

	runner := New(&config.Config{ObsidianVault: vault})
	runner.logger = log.New(io.Discard, "", 0)
	runner.parkedFactRecovery()

	data, err := os.ReadFile(parked)
	if err != nil {
		t.Fatal(err)
	}
	fm, err := yamlfrontmatter.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if fm.Status != "needs-grilling" || !fm.GrillParked {
		t.Fatalf("dispute park must stay parked until list answered, got status=%s parked=%v", fm.Status, fm.GrillParked)
	}

	// Answer the list → recovery re-enters refining on next scan.
	writeFile(t, listPath, `---
id: "grilling-decisions"
project: test
status: answered
grill_continue: true
---
# Grilling Decisions

## 决策点

### D-88: REQ-068 — 幂等 scope 冲突
- 来源任务: TASK-002
- 冲突: 未决
- 建议: org-inclusive
- 决策: 对齐 REQ-010 org:user:def 口径
`)
	runner.parkedFactRecovery()
	data, _ = os.ReadFile(parked)
	fm, _ = yamlfrontmatter.Parse(data)
	if fm.Status != "refining" || fm.GrillParked {
		t.Fatalf("answered dispute park should un-park to refining, got status=%s parked=%v", fm.Status, fm.GrillParked)
	}
}

// writeFile is a small test helper writing a file at path.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestPrerequisiteGateResumesOnFactChange guards the fact-based gate
// recovery: a blocked task with PREREQUISITE_SMOKE_FAILED resumes ONLY when
// every blocked_by upstream is done with a cleared phase error (PR merged).
// A stale done with an unresolved error (PR never merged) keeps the gate
// shut — this is what ends the 17-round replan loop (TASK-066).
func TestPrerequisiteGateResumesOnFactChange(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	tasksDir := filepath.Join(vault, "Projects", "001-test", "Tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Upstream done but with an unresolved merge error — gate must stay shut.
	unmerged := filepath.Join(tasksDir, "TASK-001-unmerged.md")
	if err := os.WriteFile(unmerged, []byte(`---
id: "001"
title: Upstream Unmerged
project: test
status: done
phase_error_code: BASE_COMMIT_MISMATCH
assignee: default
---
# Upstream
`), 0o644); err != nil {
		t.Fatal(err)
	}
	// Upstream done with cleared errors — PR merged, gate may open.
	merged := filepath.Join(tasksDir, "TASK-002-merged.md")
	if err := os.WriteFile(merged, []byte(`---
id: "002"
title: Upstream Merged
project: test
status: done
phase_error_code: ""
assignee: default
---
# Upstream
`), 0o644); err != nil {
		t.Fatal(err)
	}
	// Gated task: blocked on both.
	gated := filepath.Join(tasksDir, "TASK-003-gated.md")
	if err := os.WriteFile(gated, []byte(`---
id: "003"
title: Gated E2E
project: test
status: blocked
blocked_phase: implementing
blocked_by: ["TASK-001", "TASK-002"]
phase_error_code: PREREQUISITE_SMOKE_FAILED
resume_approved: false
assignee: default
---
# Gated
`), 0o644); err != nil {
		t.Fatal(err)
	}

	runner := New(&config.Config{ObsidianVault: vault})
	runner.logger = log.New(io.Discard, "", 0)
	runner.resolveBlockedDependencies()

	data, err := os.ReadFile(gated)
	if err != nil {
		t.Fatal(err)
	}
	fm, err := yamlfrontmatter.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if fm.ResumeApproved {
		t.Fatal("gate resumed while upstream TASK-001 still carries an unresolved merge error")
	}

	// Upstream 001 finally merges: daemon clears its error on the next
	// completeMerge path; simulate the fact change and re-run.
	if err := os.WriteFile(unmerged, []byte(`---
id: "001"
title: Upstream Unmerged
project: test
status: done
phase_error_code: ""
assignee: default
---
# Upstream
`), 0o644); err != nil {
		t.Fatal(err)
	}
	runner.resolveBlockedDependencies()

	data, err = os.ReadFile(gated)
	if err != nil {
		t.Fatal(err)
	}
	fm, err = yamlfrontmatter.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if !fm.ResumeApproved {
		t.Fatal("gate should auto-resume once all upstream facts converged (done + no phase error)")
	}
}

// TestDownstreamDoesNotAutoResumePrereqGate guards the TASK-019 loop: a
// refining/ready downstream referencing a PREREQUISITE_SMOKE_FAILED upstream
// must NOT re-approve its resume through the generic upstream-unblock path —
// the entry gate opens only when the gated task's own blocked_by facts
// converge (upstream done + cleared phase error). Before this guard, 066/069
// re-resumed 019 every scan while PR #51 was still OPEN.
func TestDownstreamDoesNotAutoResumePrereqGate(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	tasksDir := filepath.Join(vault, "Projects", "001-test", "Tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Upstream: entry-gate blocked (PR #51 still OPEN in the real world).
	upstream := filepath.Join(tasksDir, "TASK-019-gated.md")
	if err := os.WriteFile(upstream, []byte(`---
id: "019"
title: Gated Upstream
project: test
status: blocked
blocked_phase: implementing
blocked_by: ["TASK-067"]
phase_error_code: PREREQUISITE_SMOKE_FAILED
resume_approved: false
assignee: default
---
# Gated Upstream
`), 0o644); err != nil {
		t.Fatal(err)
	}
	// Downstream: refining, blocked_by the gated upstream.
	downstream := filepath.Join(tasksDir, "TASK-066-downstream.md")
	if err := os.WriteFile(downstream, []byte(`---
id: "066"
title: Refining Downstream
project: test
status: refining
blocked_by: ["TASK-019"]
assignee: default
---
# Downstream
`), 0o644); err != nil {
		t.Fatal(err)
	}

	runner := New(&config.Config{ObsidianVault: vault})
	runner.logger = log.New(io.Discard, "", 0)
	runner.resolveBlockedDependencies()

	fm := mustParse(t, upstream)
	if fm.ResumeApproved {
		t.Fatal("refining downstream must NOT auto-resume a PREREQUISITE_SMOKE_FAILED upstream")
	}
}

// TestPrerequisiteGateMissingUpstreamStaysBlocked guards the unknown-upstream
// case: a blocked_by reference that resolves to nothing must keep the gate
// shut rather than optimistically resuming.
func TestPrerequisiteGateMissingUpstreamStaysBlocked(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	tasksDir := filepath.Join(vault, "Projects", "001-test", "Tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	gated := filepath.Join(tasksDir, "TASK-001-gated.md")
	if err := os.WriteFile(gated, []byte(`---
id: "001"
title: Gated
project: test
status: blocked
blocked_phase: implementing
blocked_by: ["TASK-099"]
phase_error_code: PREREQUISITE_SMOKE_FAILED
resume_approved: false
assignee: default
---
# Gated
`), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := New(&config.Config{ObsidianVault: vault})
	runner.logger = log.New(io.Discard, "", 0)
	runner.resolveBlockedDependencies()
	data, err := os.ReadFile(gated)
	if err != nil {
		t.Fatal(err)
	}
	fm, err := yamlfrontmatter.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if fm.ResumeApproved {
		t.Fatal("gate resumed despite missing upstream task")
	}
}

func TestResolveBlockedDependenciesSkipsUserDecisionBlocked(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	tasksDir := filepath.Join(vault, "Projects", "001-test", "Tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Upstream: blocked WITHOUT blocked_phase (user-decision block) — must NOT be auto-resumed
	upstream := filepath.Join(tasksDir, "TASK-003-user-block.md")
	if err := os.WriteFile(upstream, []byte(`---
id: "003"
title: User Block
project: test
status: blocked
blocked_phase: ""
resume_approved: false
assignee: default
---
# User Block
`), 0o644); err != nil {
		t.Fatal(err)
	}

	downstream := filepath.Join(tasksDir, "TASK-004-downstream.md")
	if err := os.WriteFile(downstream, []byte(`---
id: "004"
title: Downstream
project: test
status: blocked
blocked_by: ["TASK-003"]
assignee: default
---
# Downstream
`), 0o644); err != nil {
		t.Fatal(err)
	}

	runner := New(&config.Config{ObsidianVault: vault})
	runner.logger = log.New(io.Discard, "", 0)
	runner.resolveBlockedDependencies()

	data, err := os.ReadFile(upstream)
	if err != nil {
		t.Fatal(err)
	}
	fm, err := yamlfrontmatter.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if fm.ResumeApproved {
		t.Fatal("user-decision blocked upstream must NOT be auto-resumed")
	}
}

func TestResolveBlockedDependenciesCrossProjectReference(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	aTasks := filepath.Join(vault, "Projects", "001-alpha", "Tasks")
	bTasks := filepath.Join(vault, "Projects", "002-beta", "Tasks")
	if err := os.MkdirAll(aTasks, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(bTasks, 0o755); err != nil {
		t.Fatal(err)
	}

	// Upstream in alpha, referenced with project prefix
	upstream := filepath.Join(aTasks, "TASK-005-upstream.md")
	if err := os.WriteFile(upstream, []byte(`---
id: "005"
title: Alpha Upstream
project: alpha
status: blocked
blocked_phase: refining
resume_approved: false
assignee: default
---
# Alpha Upstream
`), 0o644); err != nil {
		t.Fatal(err)
	}

	downstream := filepath.Join(bTasks, "TASK-006-downstream.md")
	if err := os.WriteFile(downstream, []byte(`---
id: "006"
title: Beta Downstream
project: beta
status: blocked
blocked_by: ["alpha:TASK-005"]
assignee: default
---
# Beta Downstream
`), 0o644); err != nil {
		t.Fatal(err)
	}

	runner := New(&config.Config{ObsidianVault: vault})
	runner.logger = log.New(io.Discard, "", 0)
	runner.resolveBlockedDependencies()

	data, err := os.ReadFile(upstream)
	if err != nil {
		t.Fatal(err)
	}
	fm, err := yamlfrontmatter.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if !fm.ResumeApproved {
		t.Fatal("cross-project upstream should be auto-resumed")
	}
}

func TestResolveBlockedDependenciesSameIDOtherProjectUntouched(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	curTasks := filepath.Join(vault, "Projects", "001-current", "Tasks")
	otherTasks := filepath.Join(vault, "Projects", "002-other", "Tasks")
	if err := os.MkdirAll(curTasks, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(otherTasks, 0o755); err != nil {
		t.Fatal(err)
	}

	// Same TASK ID in BOTH projects; only current-project one is the blocker.
	other := filepath.Join(otherTasks, "TASK-010-same-id.md")
	if err := os.WriteFile(other, []byte(`---
id: "010"
title: Other Project Same ID
project: other
status: blocked
blocked_phase: implementing
resume_approved: false
assignee: default
---
# Other
`), 0o644); err != nil {
		t.Fatal(err)
	}
	current := filepath.Join(curTasks, "TASK-010-current-blocker.md")
	if err := os.WriteFile(current, []byte(`---
id: "010"
title: Current Blocker
project: current
status: blocked
blocked_phase: implementing
resume_approved: false
assignee: default
---
# Current
`), 0o644); err != nil {
		t.Fatal(err)
	}
	downstream := filepath.Join(curTasks, "TASK-011-downstream.md")
	if err := os.WriteFile(downstream, []byte(`---
id: "011"
title: Downstream
project: current
status: blocked
blocked_by: ["TASK-010"]
assignee: default
---
# Downstream
`), 0o644); err != nil {
		t.Fatal(err)
	}

	runner := New(&config.Config{ObsidianVault: vault})
	runner.logger = log.New(io.Discard, "", 0)
	runner.resolveBlockedDependencies()

	// Current-project blocker (same ID) must be resumed.
	data, err := os.ReadFile(current)
	if err != nil {
		t.Fatal(err)
	}
	fm, err := yamlfrontmatter.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if !fm.ResumeApproved {
		t.Fatal("current-project same-ID blocker should be auto-resumed")
	}

	// Other-project same-ID task must NOT be touched.
	data, err = os.ReadFile(other)
	if err != nil {
		t.Fatal(err)
	}
	fm, err = yamlfrontmatter.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if fm.ResumeApproved {
		t.Fatal("other-project same-ID task must NOT be auto-resumed")
	}
}

func TestResolveBlockedDependenciesTransitiveChain(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	tasksDir := filepath.Join(vault, "Projects", "001-test", "Tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// A (blocked on phase failure) <- B (blocked by A) <- C (blocked by B)
	for _, tc := range []struct{ id, title, blockedBy string }{
		{"001", "A", ""},
		{"002", "B", `["TASK-001"]`},
		{"003", "C", `["TASK-002"]`},
	} {
		content := "---\nid: \"" + tc.id + "\"\ntitle: " + tc.title + "\nproject: test\nstatus: blocked\nassignee: default\n"
		if tc.blockedBy != "" {
			content += "blocked_by: " + tc.blockedBy + "\n"
		}
		if tc.id == "001" {
			content += "blocked_phase: implementing\nresume_approved: false\n"
		}
		content += "---\n# " + tc.title + "\n"
		if err := os.WriteFile(filepath.Join(tasksDir, "TASK-"+tc.id+"-"+tc.title+".md"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	runner := New(&config.Config{ObsidianVault: vault})
	runner.logger = log.New(io.Discard, "", 0)
	runner.resolveBlockedDependencies()

	// One pass: A resumed (B blocked_by A), but B itself is not phase-failure blocked.
	data, err := os.ReadFile(filepath.Join(tasksDir, "TASK-001-A.md"))
	if err != nil {
		t.Fatal(err)
	}
	fm, _ := yamlfrontmatter.Parse(data)
	if !fm.ResumeApproved {
		t.Fatal("chain head A should be auto-resumed")
	}
	data, err = os.ReadFile(filepath.Join(tasksDir, "TASK-002-B.md"))
	if err != nil {
		t.Fatal(err)
	}
	fm, _ = yamlfrontmatter.Parse(data)
	if fm.ResumeApproved {
		t.Fatal("B must not be resumed in same pass — it is not phase-failure blocked")
	}

	// Simulate A completing: next pass finds B auto-unblockable via IsAutoUnblockable,
	// not via phase-failure resume.
	if err := yamlfrontmatter.Update(filepath.Join(tasksDir, "TASK-001-A.md"), map[string]interface{}{"status": "done"}); err != nil {
		t.Fatal(err)
	}
	if !task.IsAutoUnblockable(mustParse(t, filepath.Join(tasksDir, "TASK-002-B.md")), vault) {
		t.Fatal("B should become auto-unblockable after A completes")
	}
}

func TestResolveBlockedDependenciesMalformedRef(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	tasksDir := filepath.Join(vault, "Projects", "001-test", "Tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}

	downstream := filepath.Join(tasksDir, "TASK-030-downstream.md")
	if err := os.WriteFile(downstream, []byte(`---
id: "030"
title: Downstream
project: test
status: blocked
blocked_by: ["TASK-"]
assignee: default
---
# Downstream
`), 0o644); err != nil {
		t.Fatal(err)
	}

	runner := New(&config.Config{ObsidianVault: vault})
	runner.logger = log.New(io.Discard, "", 0)
	runner.resolveBlockedDependencies() // must not panic
}

func TestResolveBlockedDependenciesDownstreamStillGated(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	tasksDir := filepath.Join(vault, "Projects", "001-test", "Tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(tasksDir, "TASK-040-upstream.md"), []byte(`---
id: "040"
title: Upstream
project: test
status: blocked
blocked_phase: implementing
resume_approved: false
assignee: default
---
# Upstream
`), 0o644); err != nil {
		t.Fatal(err)
	}
	downstream := filepath.Join(tasksDir, "TASK-041-downstream.md")
	if err := os.WriteFile(downstream, []byte(`---
id: "041"
title: Downstream
project: test
status: blocked
blocked_by: ["TASK-040"]
assignee: default
---
# Downstream
`), 0o644); err != nil {
		t.Fatal(err)
	}

	runner := New(&config.Config{ObsidianVault: vault})
	runner.logger = log.New(io.Discard, "", 0)
	runner.resolveBlockedDependencies()

	// Downstream must remain NOT ready while upstream is still blocked.
	data, err := os.ReadFile(downstream)
	if err != nil {
		t.Fatal(err)
	}
	fm, err := yamlfrontmatter.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if task.IsReady(fm, vault) {
		t.Fatal("downstream must remain gated while upstream is blocked")
	}
}

// TestResolveBlockedDependenciesRefiningDownstreamResumesUpstream guards the
// TASK-019 lesson: a refining (non-blocked, non-terminal) downstream whose
// upstream is legacy phase-failure blocked must trigger the upstream
// auto-resume — the resolver previously only scanned blocked downstreams,
// so a refining task stalled behind a blocked upstream had no resolver.
func TestResolveBlockedDependenciesRefiningDownstreamResumesUpstream(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	tasksDir := filepath.Join(vault, "Projects", "001-test", "Tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}

	upstream := filepath.Join(tasksDir, "TASK-019-upstream.md")
	if err := os.WriteFile(upstream, []byte(`---
id: "019"
title: Upstream
project: test
status: blocked
blocked_phase: implementing
phase_error_code: ""
resume_approved: false
assignee: default
---
# Upstream
`), 0o644); err != nil {
		t.Fatal(err)
	}
	downstream := filepath.Join(tasksDir, "TASK-066-downstream.md")
	if err := os.WriteFile(downstream, []byte(`---
id: "066"
title: Downstream
project: test
status: refining
blocked_by: ["TASK-019"]
assignee: default
---
# Downstream
`), 0o644); err != nil {
		t.Fatal(err)
	}

	runner := New(&config.Config{ObsidianVault: vault})
	runner.logger = log.New(io.Discard, "", 0)
	runner.resolveBlockedDependencies()

	if !mustParse(t, upstream).ResumeApproved {
		t.Fatal("refining downstream must auto-resume its phase-failure blocked upstream")
	}
}

func TestResolveBlockedDependenciesHyphenatedDirExactMatch(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	// Project dir named exactly "release-manager" (no numeric prefix).
	otherTasks := filepath.Join(vault, "Projects", "release-manager", "Tasks")
	curTasks := filepath.Join(vault, "Projects", "001-current", "Tasks")
	if err := os.MkdirAll(otherTasks, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(curTasks, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(otherTasks, "TASK-050-upstream.md"), []byte(`---
id: "050"
title: Upstream
project: release-manager
status: blocked
blocked_phase: implementing
phase_error_code: MODEL_FAILED
resume_approved: false
assignee: default
---
# Upstream
`), 0o644); err != nil {
		t.Fatal(err)
	}
	downstream := filepath.Join(curTasks, "TASK-051-downstream.md")
	if err := os.WriteFile(downstream, []byte(`---
id: "051"
title: Downstream
project: current
status: blocked
blocked_by: ["release-manager:TASK-050"]
assignee: default
---
# Downstream
`), 0o644); err != nil {
		t.Fatal(err)
	}

	runner := New(&config.Config{ObsidianVault: vault})
	runner.logger = log.New(io.Discard, "", 0)
	runner.resolveBlockedDependencies()

	if !mustParse(t, filepath.Join(otherTasks, "TASK-050-upstream.md")).ResumeApproved {
		t.Fatal("hyphenated project dir must match by exact name")
	}
}

func TestResolveBlockedDependenciesCycleNotResumed(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	tasksDir := filepath.Join(vault, "Projects", "001-test", "Tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// A blocked_by B, B blocked_by A — both phase-failure blocked. Neither may be auto-resumed.
	for _, tc := range []struct{ id, title, blockedBy string }{
		{"060", "A", `["TASK-061"]`},
		{"061", "B", `["TASK-060"]`},
	} {
		content := "---\nid: \"" + tc.id + "\"\ntitle: " + tc.title + "\nproject: test\nstatus: blocked\nblocked_phase: implementing\nphase_error_code: MODEL_FAILED\nresume_approved: false\nassignee: default\nblocked_by: " + tc.blockedBy + "\n---\n# " + tc.title + "\n"
		if err := os.WriteFile(filepath.Join(tasksDir, "TASK-"+tc.id+"-"+tc.title+".md"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	runner := New(&config.Config{ObsidianVault: vault})
	runner.logger = log.New(io.Discard, "", 0)
	runner.resolveBlockedDependencies()

	if mustParse(t, filepath.Join(tasksDir, "TASK-060-A.md")).ResumeApproved {
		t.Fatal("A in a cycle must not be auto-resumed")
	}
	if mustParse(t, filepath.Join(tasksDir, "TASK-061-B.md")).ResumeApproved {
		t.Fatal("B in a cycle must not be auto-resumed")
	}
}

func TestResolveBlockedDependenciesReqMissingNotResumed(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	tasksDir := filepath.Join(vault, "Projects", "001-test", "Tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Upstream has blocked_phase set but REQ_MISSING error — must NOT be auto-resumed.
	if err := os.WriteFile(filepath.Join(tasksDir, "TASK-070-upstream.md"), []byte(`---
id: "070"
title: Upstream
project: test
status: blocked
blocked_phase: implementing
phase_error_code: REQ_MISSING
resume_approved: false
assignee: default
---
# Upstream
`), 0o644); err != nil {
		t.Fatal(err)
	}
	downstream := filepath.Join(tasksDir, "TASK-071-downstream.md")
	if err := os.WriteFile(downstream, []byte(`---
id: "071"
title: Downstream
project: test
status: blocked
blocked_by: ["TASK-070"]
assignee: default
---
# Downstream
`), 0o644); err != nil {
		t.Fatal(err)
	}

	runner := New(&config.Config{ObsidianVault: vault})
	runner.logger = log.New(io.Discard, "", 0)
	runner.resolveBlockedDependencies()

	if mustParse(t, filepath.Join(tasksDir, "TASK-070-upstream.md")).ResumeApproved {
		t.Fatal("REQ_MISSING blocked upstream must NOT be auto-resumed")
	}
}

func TestResolveBlockedDependenciesUnqualifiedRefInCandidateProject(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	alphaTasks := filepath.Join(vault, "Projects", "001-alpha", "Tasks")
	currentTasks := filepath.Join(vault, "Projects", "002-current", "Tasks")
	if err := os.MkdirAll(alphaTasks, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(currentTasks, 0o755); err != nil {
		t.Fatal(err)
	}

	// current TASK-010 -> alpha:TASK-020; alpha TASK-020 blocked_by TASK-010
	// (unqualified, resolves to ALPHA-local TASK-010 which is done). No cycle.
	if err := os.WriteFile(filepath.Join(alphaTasks, "TASK-010-alpha-local.md"), []byte(`---
id: "010"
title: Alpha local done
project: alpha
status: done
assignee: default
---
# Alpha local
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(alphaTasks, "TASK-020-upstream.md"), []byte(`---
id: "020"
title: Alpha upstream
project: alpha
status: blocked
blocked_phase: implementing
phase_error_code: MODEL_FAILED
resume_approved: false
assignee: default
blocked_by: ["TASK-010"]
---
# Alpha upstream
`), 0o644); err != nil {
		t.Fatal(err)
	}
	downstream := filepath.Join(currentTasks, "TASK-010-downstream.md")
	if err := os.WriteFile(downstream, []byte(`---
id: "010"
title: Current downstream
project: current
status: blocked
blocked_by: ["alpha:TASK-020"]
assignee: default
---
# Current downstream
`), 0o644); err != nil {
		t.Fatal(err)
	}

	runner := New(&config.Config{ObsidianVault: vault})
	runner.logger = log.New(io.Discard, "", 0)
	runner.resolveBlockedDependencies()

	// Alpha TASK-020's unqualified TASK-010 is alpha-local (done), not the
	// current project's TASK-010 — so NO cycle; alpha TASK-020 must be resumed.
	if !mustParse(t, filepath.Join(alphaTasks, "TASK-020-upstream.md")).ResumeApproved {
		t.Fatal("alpha TASK-020 should be auto-resumed (no cycle: its TASK-010 is alpha-local done)")
	}
}

func TestResolveBlockedDependenciesExplicitCrossProjectCycleDetected(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	alphaTasks := filepath.Join(vault, "Projects", "001-alpha", "Tasks")
	currentTasks := filepath.Join(vault, "Projects", "002-current", "Tasks")
	if err := os.MkdirAll(alphaTasks, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(currentTasks, 0o755); err != nil {
		t.Fatal(err)
	}

	// current:TASK-010 <-> alpha:TASK-020 explicit cross-project cycle.
	if err := os.WriteFile(filepath.Join(alphaTasks, "TASK-020-upstream.md"), []byte(`---
id: "020"
title: Alpha upstream
project: alpha
status: blocked
blocked_phase: implementing
phase_error_code: MODEL_FAILED
resume_approved: false
assignee: default
blocked_by: ["current:TASK-010"]
---
# Alpha upstream
`), 0o644); err != nil {
		t.Fatal(err)
	}
	downstream := filepath.Join(currentTasks, "TASK-010-downstream.md")
	if err := os.WriteFile(downstream, []byte(`---
id: "010"
title: Current downstream
project: current
status: blocked
blocked_phase: implementing
phase_error_code: MODEL_FAILED
resume_approved: false
assignee: default
blocked_by: ["alpha:TASK-020"]
---
# Current downstream
`), 0o644); err != nil {
		t.Fatal(err)
	}

	runner := New(&config.Config{ObsidianVault: vault})
	runner.logger = log.New(io.Discard, "", 0)
	runner.resolveBlockedDependencies()

	if mustParse(t, filepath.Join(alphaTasks, "TASK-020-upstream.md")).ResumeApproved {
		t.Fatal("alpha TASK-020 in explicit cross-project cycle must NOT be auto-resumed")
	}
	if mustParse(t, downstream).ResumeApproved {
		t.Fatal("current TASK-010 in explicit cross-project cycle must NOT be auto-resumed")
	}
}

func TestResolveBlockedDependenciesFilenameMismatchNotResumed(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	tasksDir := filepath.Join(vault, "Projects", "001-test", "Tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// File named TASK-010-* but frontmatter id=011 — must NOT satisfy a
	// blocked_by reference to TASK-010.
	if err := os.WriteFile(filepath.Join(tasksDir, "TASK-010-wrong-id.md"), []byte(`---
id: "011"
title: Wrong ID
project: test
status: blocked
blocked_phase: implementing
phase_error_code: MODEL_FAILED
resume_approved: false
assignee: default
---
# Wrong ID
`), 0o644); err != nil {
		t.Fatal(err)
	}
	downstream := filepath.Join(tasksDir, "TASK-012-downstream.md")
	if err := os.WriteFile(downstream, []byte(`---
id: "012"
title: Downstream
project: test
status: blocked
blocked_by: ["TASK-010"]
assignee: default
---
# Downstream
`), 0o644); err != nil {
		t.Fatal(err)
	}

	runner := New(&config.Config{ObsidianVault: vault})
	runner.logger = log.New(io.Discard, "", 0)
	runner.resolveBlockedDependencies()

	if mustParse(t, filepath.Join(tasksDir, "TASK-010-wrong-id.md")).ResumeApproved {
		t.Fatal("file with mismatched frontmatter id must NOT be auto-resumed")
	}
}

func TestResolveBlockedDependenciesDuplicateDirsPreferExact(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	// Both "release-manager" and "001-release-manager" exist.
	exactTasks := filepath.Join(vault, "Projects", "release-manager", "Tasks")
	prefixedTasks := filepath.Join(vault, "Projects", "001-release-manager", "Tasks")
	curTasks := filepath.Join(vault, "Projects", "002-current", "Tasks")
	for _, d := range []string{exactTasks, prefixedTasks, curTasks} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// Only the exact dir has the upstream blocker.
	if err := os.WriteFile(filepath.Join(exactTasks, "TASK-100-upstream.md"), []byte(`---
id: "100"
title: Exact Upstream
project: release-manager
status: blocked
blocked_phase: implementing
phase_error_code: MODEL_FAILED
resume_approved: false
assignee: default
---
# Exact Upstream
`), 0o644); err != nil {
		t.Fatal(err)
	}
	downstream := filepath.Join(curTasks, "TASK-101-downstream.md")
	if err := os.WriteFile(downstream, []byte(`---
id: "101"
title: Downstream
project: current
status: blocked
blocked_by: ["release-manager:TASK-100"]
assignee: default
---
# Downstream
`), 0o644); err != nil {
		t.Fatal(err)
	}

	runner := New(&config.Config{ObsidianVault: vault})
	runner.logger = log.New(io.Discard, "", 0)
	runner.resolveBlockedDependencies()

	if !mustParse(t, filepath.Join(exactTasks, "TASK-100-upstream.md")).ResumeApproved {
		t.Fatal("exact-name dir must be preferred over numeric-prefix dir")
	}
}

func TestResolveBlockedDependenciesFrontmatterProjectFallback(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	// Directory 001-storage-v2, but task frontmatter project=alpha.
	otherTasks := filepath.Join(vault, "Projects", "001-storage-v2", "Tasks")
	curTasks := filepath.Join(vault, "Projects", "002-current", "Tasks")
	if err := os.MkdirAll(otherTasks, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(curTasks, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(otherTasks, "TASK-090-upstream.md"), []byte(`---
id: "090"
title: Upstream
project: alpha
status: blocked
blocked_phase: implementing
phase_error_code: MODEL_FAILED
resume_approved: false
assignee: default
---
# Upstream
`), 0o644); err != nil {
		t.Fatal(err)
	}
	downstream := filepath.Join(curTasks, "TASK-091-downstream.md")
	if err := os.WriteFile(downstream, []byte(`---
id: "091"
title: Downstream
project: current
status: blocked
blocked_by: ["alpha:TASK-090"]
assignee: default
---
# Downstream
`), 0o644); err != nil {
		t.Fatal(err)
	}

	runner := New(&config.Config{ObsidianVault: vault})
	runner.logger = log.New(io.Discard, "", 0)
	runner.resolveBlockedDependencies()

	// Directory name storage-v2 doesn't match key alpha, but frontmatter does.
	if !mustParse(t, filepath.Join(otherTasks, "TASK-090-upstream.md")).ResumeApproved {
		t.Fatal("upstream in dir 001-storage-v2 with project=alpha should be auto-resumed via frontmatter fallback")
	}
}

func TestResolveBlockedDependenciesAutoResumeBudgetExhausted(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	tasksDir := filepath.Join(vault, "Projects", "001-test", "Tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Upstream already auto-resumed twice — budget exhausted.
	if err := os.WriteFile(filepath.Join(tasksDir, "TASK-110-upstream.md"), []byte(`---
id: "110"
title: Upstream
project: test
status: blocked
blocked_phase: implementing
phase_error_code: MODEL_FAILED
resume_approved: false
auto_resume_count: 2
assignee: default
---
# Upstream
`), 0o644); err != nil {
		t.Fatal(err)
	}
	downstream := filepath.Join(tasksDir, "TASK-111-downstream.md")
	if err := os.WriteFile(downstream, []byte(`---
id: "111"
title: Downstream
project: test
status: blocked
blocked_by: ["TASK-110"]
assignee: default
---
# Downstream
`), 0o644); err != nil {
		t.Fatal(err)
	}

	runner := New(&config.Config{ObsidianVault: vault})
	runner.logger = log.New(io.Discard, "", 0)
	runner.resolveBlockedDependencies()

	if mustParse(t, filepath.Join(tasksDir, "TASK-110-upstream.md")).ResumeApproved {
		t.Fatal("upstream beyond auto-resume budget must NOT be auto-resumed")
	}
}

func TestResolveBlockedDependenciesAutoResumeIncrementsBudget(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	tasksDir := filepath.Join(vault, "Projects", "001-test", "Tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(tasksDir, "TASK-120-upstream.md"), []byte(`---
id: "120"
title: Upstream
project: test
status: blocked
blocked_phase: implementing
phase_error_code: MODEL_FAILED
resume_approved: false
auto_resume_count: 0
assignee: default
---
# Upstream
`), 0o644); err != nil {
		t.Fatal(err)
	}
	downstream := filepath.Join(tasksDir, "TASK-121-downstream.md")
	if err := os.WriteFile(downstream, []byte(`---
id: "121"
title: Downstream
project: test
status: blocked
blocked_by: ["TASK-120"]
assignee: default
---
# Downstream
`), 0o644); err != nil {
		t.Fatal(err)
	}

	runner := New(&config.Config{ObsidianVault: vault})
	runner.logger = log.New(io.Discard, "", 0)
	runner.resolveBlockedDependencies()

	fm := mustParse(t, filepath.Join(tasksDir, "TASK-120-upstream.md"))
	if !fm.ResumeApproved {
		t.Fatal("upstream within budget should be auto-resumed")
	}
	if !fm.AutoResumePending {
		t.Fatal("auto-resume must set auto_resume_pending marker")
	}
	if fm.AutoResumeCount != 0 {
		t.Fatalf("auto_resume_count = %d, want 0 — count only increments on failure", fm.AutoResumeCount)
	}
}

func TestHandlePhaseFailureIncrementsBudget(t *testing.T) {
	dir := t.TempDir()
	tasksDir := filepath.Join(dir, "Tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(tasksDir, "TASK-130-fail.md")
	if err := os.WriteFile(path, []byte(`---
id: "130"
title: Failing
project: test
status: implementing
blocked_phase: ""
auto_resume_count: 1
auto_resume_pending: true
assignee: default
---
# Failing
`), 0o644); err != nil {
		t.Fatal(err)
	}

	runner := New(&config.Config{})
	runner.logger = log.New(io.Discard, "", 0)
	runner.handlePhaseFailure(path, "130", "Failing", "implementing", "round2", ErrModelFailed, "boom", "")

	fm := mustParse(t, path)
	if fm.Status != "blocked" {
		t.Fatalf("status = %q, want blocked", fm.Status)
	}
	if fm.AutoResumeCount != 2 {
		t.Fatalf("auto_resume_count = %d, want 2 after failure", fm.AutoResumeCount)
	}
	if fm.AutoResumePending {
		t.Fatal("auto_resume_pending must be cleared after failure")
	}
}

func TestHandlePhaseFailureNonPendingKeepsBudget(t *testing.T) {
	dir := t.TempDir()
	tasksDir := filepath.Join(dir, "Tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(tasksDir, "TASK-131-first-fail.md")
	if err := os.WriteFile(path, []byte(`---
id: "131"
title: First Fail
project: test
status: implementing
blocked_phase: ""
auto_resume_count: 0
auto_resume_pending: false
assignee: default
---
# First Fail
`), 0o644); err != nil {
		t.Fatal(err)
	}

	runner := New(&config.Config{})
	runner.logger = log.New(io.Discard, "", 0)
	runner.handlePhaseFailure(path, "131", "First Fail", "implementing", "round2", ErrModelFailed, "boom", "")

	fm := mustParse(t, path)
	if fm.AutoResumeCount != 0 {
		t.Fatalf("auto_resume_count = %d, want 0 — initial failure must not consume budget", fm.AutoResumeCount)
	}

	// Manual-resume-then-fail also leaves count untouched (manual resets budget).
	if err := yamlfrontmatter.Update(path, map[string]interface{}{
		"status": "implementing", "blocked_phase": "", "resume_approved": false,
	}); err != nil {
		t.Fatal(err)
	}
	runner.handlePhaseFailure(path, "131", "First Fail", "implementing", "round2", ErrModelFailed, "boom2", "")
	fm = mustParse(t, path)
	if fm.AutoResumeCount != 0 {
		t.Fatalf("auto_resume_count = %d, want 0 after manual-resume failure", fm.AutoResumeCount)
	}
}

func TestScanAndProcessResumesAndDispatchesResolvedUpstream(t *testing.T) {
	dir := t.TempDir()
	omp, startDir, releaseFile := writeBarrierOMP(t, dir)
	t.Setenv("START_DIR", startDir)
	t.Setenv("RELEASE_FILE", releaseFile)

	vault := filepath.Join(dir, "vault")
	tasksDir := filepath.Join(vault, "Projects", "001-test", "Tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}

	repo := filepath.Join(dir, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	// Re-write vault-map with repo mapping (first write used nil projects).
	skillDir := writeVaultMap(t, dir, map[string]string{"test": repo})

	// Upstream blocked on phase failure; downstream blocked_by it.
	upstream := filepath.Join(tasksDir, "TASK-080-upstream.md")
	if err := os.WriteFile(upstream, []byte(`---
id: "080"
title: Upstream
project: test
status: blocked
blocked_phase: implementing
phase_error_code: MODEL_FAILED
resume_approved: false
assignee: default
req_doc: Projects/001-test/Requirements/REQ-080.md
---
# Upstream
`), 0o644); err != nil {
		t.Fatal(err)
	}
	downstream := filepath.Join(tasksDir, "TASK-081-downstream.md")
	// priority: P2 keeps the task out of FindPriorityTasks: the unblock in
	// processBatch turns it "ready" mid-scan, and a priority assessment OMP
	// would race this test's barrier-OMP start-count assertion (observed
	// flake: "start count did not reach 1; got 2").
	if err := os.WriteFile(downstream, []byte(`---
id: "081"
title: Downstream
project: test
status: blocked
blocked_by: ["TASK-080"]
priority: P2
assignee: default
---
# Downstream
`), 0o644); err != nil {
		t.Fatal(err)
	}

	runner := newTestRunner(skillDir, omp, filepath.Join(dir, "logs"), 2)
	runner.cfg.ObsidianVault = vault
	runner.cfg.SkillInstallDir = skillDir

	// resolveBlockedDependencies auto-resumes upstream; scanAndProcess restores
	// it to implementing and dispatches OMP (runs async: barrier OMP blocks until
	// release, so scanAndProcess won't return until then).
	runner.resolveBlockedDependencies()
	done := make(chan error, 1)
	go func() { done <- runner.scanAndProcess() }()

	waitForStartCount(t, startDir, 1)
	data, err := os.ReadFile(upstream)
	if err != nil {
		t.Fatal(err)
	}
	fm, err := yamlfrontmatter.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if fm.Status != "implementing" {
		t.Fatalf("upstream status = %q, want implementing", fm.Status)
	}

	releaseBarrier(t, releaseFile)
	// Async dispatch: scanAndProcess returned long ago; wait for the OMP
	// task AND its follow-up scan to unwind, or the leaked goroutines race
	// the next test's global state (apiKeyProbe).
	waitForScanIdle(t, runner)
}

func mustParse(t *testing.T, path string) *yamlfrontmatter.Frontmatter {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	fm, err := yamlfrontmatter.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	return fm
}

// TestScanAndProcessAutoResumesKeyBlockedTask verifies that a task blocked on
// API_KEY_UNAVAILABLE is restored to blocked_phase and dispatched in the SAME
// scan round once the key probe succeeds (fall-through, no extra round wait).
func TestScanAndProcessAutoResumesKeyBlockedTask(t *testing.T) {
	dir := t.TempDir()
	omp, startDir, releaseFile := writeBarrierOMP(t, dir)
	t.Setenv("START_DIR", startDir)
	t.Setenv("RELEASE_FILE", releaseFile)

	vault := filepath.Join(dir, "vault")
	tasksDir := filepath.Join(vault, "Projects", "001-test", "Tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(dir, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	skillDir := writeVaultMap(t, dir, map[string]string{"test": repo})

	taskPath := filepath.Join(tasksDir, "TASK-082-keyblocked.md")
	if err := os.WriteFile(taskPath, []byte(`---
id: "082"
title: Key Blocked
project: test
status: blocked
blocked_phase: implementing
phase_error_code: API_KEY_UNAVAILABLE
phase_error: OMP 无法获取模型 API Key
resume_approved: false
priority: P2
assignee: default
req_doc: Projects/001-test/Requirements/REQ-082.md
stage: "P1"
---
# Key Blocked
`), 0o644); err != nil {
		t.Fatal(err)
	}

	oldProbe, _ := apiKeyProbe.Load().(func() bool)
	apiKeyProbe.Store(func() bool { return true })
	t.Cleanup(func() { apiKeyProbe.Store(oldProbe) })

	runner := newTestRunner(skillDir, omp, filepath.Join(dir, "logs"), 2)
	runner.cfg.ObsidianVault = vault
	runner.cfg.SkillInstallDir = skillDir

	done := make(chan error, 1)
	go func() { done <- runner.scanAndProcess() }()

	waitForStartCount(t, startDir, 1)

	fm := mustParse(t, taskPath)
	if fm.Status != "implementing" {
		t.Fatalf("status = %q, want implementing", fm.Status)
	}
	if fm.BlockedPhase != "" {
		t.Fatalf("blocked_phase = %q, want empty", fm.BlockedPhase)
	}
	if fm.PhaseErrorCode != "" {
		t.Fatalf("phase_error_code = %q, want empty", fm.PhaseErrorCode)
	}
	if fm.PhaseError != "" {
		t.Fatalf("phase_error = %q, want empty", fm.PhaseError)
	}

	releaseBarrier(t, releaseFile)
	if err := <-done; err != nil {
		t.Fatalf("scanAndProcess: %v", err)
	}
	// Async dispatch: scanAndProcess returned before the OMP task finished;
	// wait for the task and its follow-up scan to unwind before ending.
	waitForScanIdle(t, runner)
}

// TestScanAndProcessAutoResumesInterruptedBlockedTask guards the
// PHASE_INTERRUPTED self-heal: legacy daemons wrote shutdown-interrupted
// phases as blocked (observed: TASK-015, 8/5 era), while the skill contract
// (docs/workflow.md 3.2) promises automatic re-scheduling on restart with NO
// manual resume_approved. The scan must restore the blocked_phase exactly
// like the API-key probe does.
func TestScanAndProcessAutoResumesInterruptedBlockedTask(t *testing.T) {
	dir := t.TempDir()
	omp, startDir, releaseFile := writeBarrierOMP(t, dir)
	t.Setenv("START_DIR", startDir)
	t.Setenv("RELEASE_FILE", releaseFile)

	vault := filepath.Join(dir, "vault")
	tasksDir := filepath.Join(vault, "Projects", "001-test", "Tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(dir, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	skillDir := writeVaultMap(t, dir, map[string]string{"test": repo})

	taskPath := filepath.Join(tasksDir, "TASK-083-interrupted.md")
	if err := os.WriteFile(taskPath, []byte(`---
id: "083"
title: Interrupted Refining
project: test
status: blocked
blocked_phase: refining
phase_error_code: PHASE_INTERRUPTED
phase_error: daemon 重启中断，等待自动恢复
resume_approved: false
priority: P2
assignee: default
req_doc: Projects/001-test/Requirements/REQ-083.md
stage: "P1"
---
# Interrupted
`), 0o644); err != nil {
		t.Fatal(err)
	}

	runner := newTestRunner(skillDir, omp, filepath.Join(dir, "logs"), 2)
	runner.cfg.ObsidianVault = vault
	runner.cfg.SkillInstallDir = skillDir

	done := make(chan error, 1)
	go func() { done <- runner.scanAndProcess() }()

	waitForStartCount(t, startDir, 1)

	fm := mustParse(t, taskPath)
	if fm.Status != "refining" {
		t.Fatalf("status = %q, want refining (auto-restored, no manual resume)", fm.Status)
	}
	if fm.BlockedPhase != "" {
		t.Fatalf("blocked_phase = %q, want empty", fm.BlockedPhase)
	}
	if fm.PhaseErrorCode != "" {
		t.Fatalf("phase_error_code = %q, want empty", fm.PhaseErrorCode)
	}
	if fm.ResumeApproved {
		t.Fatal("resume_approved = true, want false (self-heal, not manual resume)")
	}

	// Stop the re-dispatch loop before releasing the barrier: the fake OMP
	// leaves status=refining, so the follow-up scan would re-dispatch it
	// forever. A done marker ends the loop so waitForScanIdle can unwind.
	if err := yamlfrontmatter.Update(taskPath, map[string]interface{}{"status": "done"}); err != nil {
		t.Fatal(err)
	}
	releaseBarrier(t, releaseFile)
	if err := <-done; err != nil {
		t.Fatalf("scanAndProcess: %v", err)
	}
	waitForScanIdle(t, runner)
}

// TestScanAndProcessKeepsKeyBlockedWhenUnavailable verifies that a key-blocked
// task stays blocked and OMP is NOT launched while the key probe fails.
func TestScanAndProcessKeepsKeyBlockedWhenUnavailable(t *testing.T) {
	dir := t.TempDir()
	omp, startDir, releaseFile := writeBarrierOMP(t, dir)
	t.Setenv("START_DIR", startDir)
	t.Setenv("RELEASE_FILE", releaseFile)

	vault := filepath.Join(dir, "vault")
	tasksDir := filepath.Join(vault, "Projects", "001-test", "Tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(dir, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	skillDir := writeVaultMap(t, dir, map[string]string{"test": repo})

	taskPath := filepath.Join(tasksDir, "TASK-083-keyblocked.md")
	if err := os.WriteFile(taskPath, []byte(`---
id: "083"
title: Key Blocked
project: test
status: blocked
blocked_phase: implementing
phase_error_code: API_KEY_UNAVAILABLE
phase_error: OMP 无法获取模型 API Key
resume_approved: false
priority: P2
assignee: default
req_doc: Projects/001-test/Requirements/REQ-083.md
---
# Key Blocked
`), 0o644); err != nil {
		t.Fatal(err)
	}

	oldProbe, _ := apiKeyProbe.Load().(func() bool)
	apiKeyProbe.Store(func() bool { return false })
	t.Cleanup(func() { apiKeyProbe.Store(oldProbe) })

	runner := newTestRunner(skillDir, omp, filepath.Join(dir, "logs"), 2)
	runner.cfg.ObsidianVault = vault
	runner.cfg.SkillInstallDir = skillDir

	if err := runner.scanAndProcess(); err != nil {
		t.Fatalf("scanAndProcess: %v", err)
	}

	if n := countStartFiles(t, startDir); n != 0 {
		t.Fatalf("OMP launched %d time(s) while key unavailable, want 0", n)
	}
	fm := mustParse(t, taskPath)
	if fm.Status != "blocked" {
		t.Fatalf("status = %q, want blocked", fm.Status)
	}
	if fm.BlockedPhase != "implementing" {
		t.Fatalf("blocked_phase = %q, want implementing", fm.BlockedPhase)
	}
}

// TestDaemonShutdownSignalsRunningOMP verifies that cancelling the daemon
// context (systemd stop / shutdown) sends SIGTERM to the running execution session
// (graceful exit) instead of leaving it until a hard kill.
func TestDaemonShutdownSignalsRunningOMP(t *testing.T) {
	dir := t.TempDir()
	startDir := filepath.Join(dir, "starts")
	releaseFile := filepath.Join(dir, "release")
	omp := filepath.Join(dir, "fake-omp")
	script := `#!/bin/sh
mkdir -p "$START_DIR"
printf '%s\n' "$$" > "$START_DIR/started"
trap 'echo term > "$START_DIR/term"; exit 143' TERM
while [ ! -e "$RELEASE_FILE" ]; do sleep 0.2; done
`
	if err := os.WriteFile(omp, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("START_DIR", startDir)
	t.Setenv("RELEASE_FILE", releaseFile)

	repo := filepath.Join(dir, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	taskPath := filepath.Join(dir, "TASK-090-shutdown.md")
	if err := os.WriteFile(taskPath, []byte(`---
id: "090"
title: Shutdown
project: test
status: implementing
assignee: default
req_doc: Projects/001-test/Requirements/REQ-090.md
---
# Shutdown
`), 0o644); err != nil {
		t.Fatal(err)
	}

	runner := newTestRunner(filepath.Join(dir, "skill"), omp, filepath.Join(dir, "logs"), 2)
	ctx, cancel := context.WithCancel(context.Background())
	runner.daemonCtx = ctx

	done := make(chan int, 1)
	go func() {
		tasks, err := task.FindReadyTaskForFile("", taskPath)
		if err != nil || tasks == nil {
			done <- -1
			return
		}
		done <- runner.processBatchSequential([]task.ReadyTask{*tasks}, repo)
	}()

	// Wait for OMP to start, then cancel the daemon context.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(startDir, "started")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("OMP did not start")
		}
		time.Sleep(20 * time.Millisecond)
	}

	cancel()

	select {
	case processed := <-done:
		if processed < 0 {
			t.Fatal("task not ready")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("processBatchSequential did not return after daemon cancel")
	}
	if _, err := os.Stat(filepath.Join(startDir, "term")); err != nil {
		t.Fatalf("OMP did not receive SIGTERM on daemon shutdown: %v", err)
	}

	// Shutdown interruption must NOT be treated as a failure: task stays in
	// implementing with PHASE_INTERRUPTED so the next scan auto-resumes it.
	fm := mustParse(t, taskPath)
	if fm.Status != "implementing" {
		t.Fatalf("status = %q after shutdown, want implementing (not blocked)", fm.Status)
	}
	if fm.BlockedPhase != "" {
		t.Fatalf("blocked_phase = %q, want empty after shutdown", fm.BlockedPhase)
	}
	if fm.PhaseErrorCode != string(ErrPhaseInterrupted) {
		t.Fatalf("phase_error_code = %q, want %q", fm.PhaseErrorCode, ErrPhaseInterrupted)
	}

	_ = os.WriteFile(releaseFile, nil, 0o644)
}

func TestClearPhaseErrorClearsFields(t *testing.T) {
	dir := t.TempDir()
	taskPath := filepath.Join(dir, "TASK-091-interrupted.md")
	if err := os.WriteFile(taskPath, []byte(`---
id: "091"
title: Interrupted
status: implementing
phase_error_code: PHASE_INTERRUPTED
phase_error: daemon 重启中断，等待自动恢复
phase_log: /tmp/some-round2.log
assignee: default
---
# Interrupted
`), 0o644); err != nil {
		t.Fatal(err)
	}

	runner := New(&config.Config{})
	runner.logger = log.New(io.Discard, "", 0)
	runner.clearPhaseError(taskPath, "091")

	fm := mustParse(t, taskPath)
	if fm.PhaseErrorCode != "" || fm.PhaseError != "" || fm.PhaseLog != "" {
		t.Fatalf("phase error fields not cleared: code=%q err=%q log=%q", fm.PhaseErrorCode, fm.PhaseError, fm.PhaseLog)
	}
	if fm.Status != "implementing" {
		t.Fatalf("status = %q, want implementing (untouched)", fm.Status)
	}
}
