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
