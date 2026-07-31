package daemon

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"

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
	runner.handlePhaseFailure(path, "130", "Failing", "round2", ErrModelFailed, "boom", "")

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
	runner.handlePhaseFailure(path, "131", "First Fail", "round2", ErrModelFailed, "boom", "")

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
	runner.handlePhaseFailure(path, "131", "First Fail", "round2", ErrModelFailed, "boom2", "")
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
	if err := os.WriteFile(downstream, []byte(`---
id: "081"
title: Downstream
project: test
status: blocked
blocked_by: ["TASK-080"]
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
