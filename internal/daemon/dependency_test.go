package daemon

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"

	"github.com/ndzuki/obsidian-task-runner/internal/config"
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
