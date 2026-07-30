package task

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

func writeTask(t *testing.T, dir, name, frontmatter string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	content := "---\n" + strings.TrimSpace(frontmatter) + "\n---\n# Task\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write task %s: %v", path, err)
	}
	return path
}

func TestIsValidAssignee(t *testing.T) {
	if !IsValidAssignee("deepseek") {
		t.Error("deepseek should be valid")
	}
	if !IsValidAssignee("gemini") {
		t.Error("gemini should be valid (any non-empty)")
	}
	if IsValidAssignee("") {
		t.Error("empty should NOT be valid")
	}
}

func TestIsReadyForMerge(t *testing.T) {
	tests := []struct {
		name          string
		status        string
		mergeApproved bool
		pendingReq    bool
		want          bool
	}{
		{name: "review approved", status: "review", mergeApproved: true, want: true},
		{name: "conflict approved", status: "conflict", mergeApproved: true, want: true},
		{name: "review awaiting approval", status: "review", want: false},
		{name: "conflict awaiting approval", status: "conflict", want: false},
		{name: "review pending requirement", status: "review", pendingReq: true, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fm := &yamlfrontmatter.Frontmatter{
				Assignee:      "gpt",
				Status:        tt.status,
				MergeApproved: tt.mergeApproved,
				PendingReq:    tt.pendingReq,
			}
			if got := IsReady(fm, t.TempDir()); got != tt.want {
				t.Fatalf("IsReady() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsAutoUnblockable(t *testing.T) {
	dir := t.TempDir()
	projDir := filepath.Join(dir, "Projects", "001-test")
	tasksDir := filepath.Join(projDir, "Tasks")
	if err := os.MkdirAll(tasksDir, 0755); err != nil {
		t.Fatalf("create tasks directory: %v", err)
	}

	path := writeTask(t, tasksDir, "TASK-001.md", `
id: "001"
title: "Test"
project: my-project
status: blocked
assignee: deepseek
blocked_by: []
`)
	data, _ := os.ReadFile(path)
	fm, err := yamlfrontmatter.Parse(data)
	if err != nil || fm == nil {
		t.Fatal("parse failed")
	}

	if !IsAutoUnblockable(fm, dir) {
		t.Error("should be auto-unblockable with empty blocked_by")
	}

	// Test with missing assignee
	path2 := writeTask(t, tasksDir, "TASK-002.md", `
id: "002"
title: "Test"
project: my-project
status: blocked
assignee: ""
blocked_by: []
`)
	data2, _ := os.ReadFile(path2)
	fm2, _ := yamlfrontmatter.Parse(data2)
	if IsAutoUnblockable(fm2, dir) {
		t.Error("should NOT be auto-unblockable without assignee")
	}
}

func TestBlockedByDependencyResolution(t *testing.T) {
	dir := t.TempDir()
	projDir := filepath.Join(dir, "Projects", "001-test")
	tasksDir := filepath.Join(projDir, "Tasks")
	if err := os.MkdirAll(tasksDir, 0755); err != nil {
		t.Fatalf("create tasks directory: %v", err)
	}

	writeTask(t, tasksDir, "TASK-010-done.md", `
id: "010"
title: "Dependency Done"
project: "001-test"
status: done
assignee: deepseek
`)

	blockedPath := writeTask(t, tasksDir, "TASK-020-blocked.md", `
id: "020"
title: "Blocked Task"
project: "001-test"
status: blocked
assignee: deepseek
blocked_by:
  - "TASK-010"
priority: P0
`)

	data, _ := os.ReadFile(blockedPath)
	fm, _ := yamlfrontmatter.Parse(data)

	if !IsAutoUnblockable(fm, dir) {
		t.Error("should be auto-unblockable; blocker TASK-010 is done")
	}

	tasks, err := FindReadyTasks(dir)
	if err != nil {
		t.Fatalf("FindReadyTasks: %v", err)
	}
	found := false
	for _, rt := range tasks {
		if rt.ID == "020" {
			found = true
			break
		}
	}
	if !found {
		t.Error("blocked task with done dependencies should appear in ready tasks")
	}
}

func TestBlockedByUnresolvedDependency(t *testing.T) {
	dir := t.TempDir()
	projDir := filepath.Join(dir, "Projects", "001-test")
	tasksDir := filepath.Join(projDir, "Tasks")
	if err := os.MkdirAll(tasksDir, 0755); err != nil {
		t.Fatalf("create tasks directory: %v", err)
	}

	// Create a dependency task that is NOT done
	writeTask(t, tasksDir, "TASK-011-planning.md", `
id: "011"
title: "Still Planning"
project: my-project
status: plan-review
assignee: deepseek
`)

	// Create a task blocked by the non-done dependency
	blockedPath := writeTask(t, tasksDir, "TASK-021-blocked.md", `
id: "021"
title: "Still Blocked"
project: my-project
status: blocked
assignee: deepseek
blocked_by:
  - "TASK-011"
`)

	data, _ := os.ReadFile(blockedPath)
	fm, _ := yamlfrontmatter.Parse(data)

	// Should NOT be unblockable because TASK-011 is not done
	if IsAutoUnblockable(fm, dir) {
		t.Error("should NOT be auto-unblockable; blocker TASK-011 is not done")
	}

	// Verify FindReadyTasks does not pick it up
	tasks, _ := FindReadyTasks(dir)
	for _, rt := range tasks {
		if rt.ID == "021" {
			t.Error("blocked task with unresolved dependencies should NOT appear in ready tasks")
		}
	}
}

func TestFindReadyTasks(t *testing.T) {
	dir := t.TempDir()
	tasksDir := filepath.Join(dir, "Projects", "001-test", "Tasks")

	if err := os.MkdirAll(tasksDir, 0755); err != nil {
		t.Fatalf("create tasks directory: %v", err)
	}
	// Ready task
	writeTask(t, tasksDir, "TASK-001-ready.md", `
id: "001"
title: "Ready Task"
project: my-app
status: ready
assignee: deepseek
priority: P1
`)

	// Blocked task with valid assignee → should be auto-unblocked
	writeTask(t, tasksDir, "TASK-002-blocked.md", `
id: "002"
title: "Blocked but Fillable"
project: my-app
status: blocked
assignee: deepseek
blocked_by: []
priority: P0
`)

	// Blocked with no assignee → not ready
	writeTask(t, tasksDir, "TASK-003-no-assignee.md", `
id: "003"
title: "No Assignee"
project: my-app
status: blocked
assignee: ""
blocked_by: []
`)

	tasks, err := FindReadyTasks(dir)
	if err != nil {
		t.Fatalf("FindReadyTasks: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(tasks))
	}
	// P0 should come first
	if tasks[0].ID != "002" {
		t.Errorf("first task id = %q, want 002 (P0 first)", tasks[0].ID)
	}
	if tasks[1].ID != "001" {
		t.Errorf("second task id = %q, want 001", tasks[1].ID)
	}
	if tasks[1].TargetBranch != "" {
		t.Errorf("target branch = %q, want empty", tasks[1].TargetBranch)
	}
}

func TestDeriveProjectDir(t *testing.T) {
	tests := []struct {
		name       string
		reqRelPath string
		id         string
		slug       string
		want       string
	}{
		{
			name:       "new structure under Projects",
			reqRelPath: "Projects/001-release-manager/Requirements/REQ-002-demo2.md",
			id:         "002",
			slug:       "demo2",
			want:       "001-release-manager",
		},
		{
			name:       "new structure deep nested",
			reqRelPath: "Projects/my-project/Requirements/REQ-005-feature.md",
			id:         "005",
			slug:       "feature",
			want:       "my-project",
		},
		{
			name:       "old structure flat",
			reqRelPath: "Requirements/REQ-001-demo.md",
			id:         "001",
			slug:       "demo",
			want:       "001-demo",
		},
		{
			name:       "old structure with subdirs",
			reqRelPath: "subdir/Requirements/REQ-003-test.md",
			id:         "003",
			slug:       "test",
			want:       "003-test",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := deriveProjectDir(tt.reqRelPath, tt.id, tt.slug)
			if got != tt.want {
				t.Errorf("deriveProjectDir(%q, %q, %q) = %q, want %q",
					tt.reqRelPath, tt.id, tt.slug, got, tt.want)
			}
		})
	}
}

func TestCreateTaskForReqNewStructure(t *testing.T) {
	dir := t.TempDir()
	vaultPath := filepath.Join(dir, "vault")
	// Isolate from real vault-map
	t.Setenv("HOME", dir)

	projectName := "001-release-manager"
	reqDir := filepath.Join(vaultPath, "Projects", projectName, "Requirements")
	if err := os.MkdirAll(reqDir, 0755); err != nil {
		t.Fatalf("create requirements directory: %v", err)
	}

	reqContent := `---
id: "002"
title: "Test Feature"
priority: P1
author: test-user
---

# Test Feature

## 要做什么
Add a test feature.
`
	reqPath := filepath.Join(reqDir, "REQ-002-test-feature.md")
	if err := os.WriteFile(reqPath, []byte(reqContent), 0644); err != nil {
		t.Fatalf("write requirement: %v", err)
	}

	reqRelPath := filepath.Join("Projects", projectName, "Requirements", "REQ-002-test-feature.md")
	result := createTaskForReq(vaultPath, reqRelPath)

	if result == nil {
		t.Fatal("createTaskForReq returned nil")
	}
	if result.Action != "create_task" {
		t.Errorf("expected create_task, got %s", result.Action)
	}

	// Verify TASK was created in the correct project directory
	taskPath := filepath.Join(vaultPath, "Projects", projectName, "Tasks", "TASK-002-test-feature.md")
	if _, err := os.Stat(taskPath); os.IsNotExist(err) {
		t.Fatalf("TASK not created at expected path: %s", taskPath)
	}

	taskData, _ := os.ReadFile(taskPath)
	taskStr := string(taskData)
	// No vault-map in isolated HOME → falls back to projectDir
	if !strings.Contains(taskStr, `"001-release-manager"`) {
		t.Error("TASK frontmatter project should fall back to 001-release-manager when no vault-map")
	}
	// Verify req_doc contains the requirement path, not the author
	wantReqDoc := filepath.Join("Projects", projectName, "Requirements", "REQ-002-test-feature.md")
	if !strings.Contains(taskStr, "\nreq_doc: "+wantReqDoc) {
		t.Errorf("TASK req_doc should be the REQ path %q", wantReqDoc)
	}
	// Verify author contains the author name, not the REQ path
	if !strings.Contains(taskStr, "\nauthor: \"test-user\"") {
		t.Error("TASK author should be \"test-user\"")
	}

	// Also verify req_doc does NOT contain author value
	if strings.Contains(taskStr, "\nreq_doc: test-user") || strings.Contains(taskStr, "\nreq_doc: \"test-user\"") {
		t.Error("TASK req_doc should NOT contain author value 'test-user'")
	}
	// Also verify author does NOT contain req_doc path
	if strings.Contains(taskStr, "\nauthor: \""+wantReqDoc+"\"") {
		t.Error("TASK author should NOT contain the REQ path")
	}

}

func TestCreateTaskForReqWithVaultMap(t *testing.T) {
	dir := t.TempDir()
	vaultPath := filepath.Join(dir, "vault")
	t.Setenv("HOME", dir)

	// Set up vault-map with "release-manager" project
	ompDir := filepath.Join(dir, ".omp", "skills", "obsidian-task-runner", "config")
	if err := os.MkdirAll(ompDir, 0755); err != nil {
		t.Fatalf("create config directory: %v", err)
	}
	vaultMap := `{"projects":[{"name":"release-manager","path":"/tmp/release-manager"}],"new_project_root":"/tmp"}`
	if err := os.WriteFile(filepath.Join(ompDir, "vault-map.json"), []byte(vaultMap), 0644); err != nil {
		t.Fatalf("write vault map: %v", err)
	}

	projectName := "001-release-manager"
	reqDir := filepath.Join(vaultPath, "Projects", projectName, "Requirements")
	if err := os.MkdirAll(reqDir, 0755); err != nil {
		t.Fatalf("create requirements directory: %v", err)
	}

	reqContent := `---
id: "003"
title: "Vault Map Feature"
---

# Vault Map Feature

## 要做什么
Test vault-map project matching.
`
	reqPath := filepath.Join(reqDir, "REQ-003-vault-map.md")
	if err := os.WriteFile(reqPath, []byte(reqContent), 0644); err != nil {
		t.Fatalf("write requirement: %v", err)
	}

	reqRelPath := filepath.Join("Projects", projectName, "Requirements", "REQ-003-vault-map.md")
	result := createTaskForReq(vaultPath, reqRelPath)

	if result == nil {
		t.Fatal("createTaskForReq returned nil")
	}

	taskPath := filepath.Join(vaultPath, "Projects", projectName, "Tasks", "TASK-003-vault-map.md")
	taskData, _ := os.ReadFile(taskPath)
	taskStr := string(taskData)

	// Should match vault-map "release-manager" not "001-release-manager"
	if !strings.Contains(taskStr, `project: "release-manager"`) {
		t.Error("TASK frontmatter project should match vault-map key 'release-manager', got something else")
		t.Logf("Frontmatter excerpt: %s", taskStr[:300])
	}
}

func TestCreateTaskForReqOldStructure(t *testing.T) {
	dir := t.TempDir()
	vaultPath := filepath.Join(dir, "vault")

	reqDir := filepath.Join(vaultPath, "Requirements")
	if err := os.MkdirAll(reqDir, 0755); err != nil {
		t.Fatalf("create requirements directory: %v", err)
	}

	reqContent := `---
id: "001"
title: "Legacy Feature"
---

# Legacy Feature

## 要做什么
Legacy flat structure.
`
	reqPath := filepath.Join(reqDir, "REQ-001-legacy.md")
	if err := os.WriteFile(reqPath, []byte(reqContent), 0644); err != nil {
		t.Fatalf("write requirement: %v", err)
	}

	reqRelPath := "Requirements/REQ-001-legacy.md"
	result := createTaskForReq(vaultPath, reqRelPath)

	if result == nil {
		t.Fatal("createTaskForReq returned nil for old structure")
	}

	// Old structure creates project dir as "id-slug"
	projDir := "001-legacy"
	taskPath := filepath.Join(vaultPath, "Projects", projDir, "Tasks", "TASK-001-legacy.md")
	if _, err := os.Stat(taskPath); os.IsNotExist(err) {
		t.Fatalf("TASK not created for old structure at: %s", taskPath)
	}
}

func TestIsAutoUnblockable_BlockedPhaseGate(t *testing.T) {
	dir := t.TempDir()
	projDir := filepath.Join(dir, "Projects", "001-test")
	tasksDir := filepath.Join(projDir, "Tasks")
	if err := os.MkdirAll(tasksDir, 0755); err != nil {
		t.Fatalf("create tasks directory: %v", err)
	}

	tests := []struct {
		name           string
		blockedPhase   string
		resumeApproved bool
		want           bool
	}{
		{
			name:           "no blocked_phase → auto-unblock",
			blockedPhase:   "",
			resumeApproved: false,
			want:           true,
		},
		{
			name:           "blocked_phase set, not approved → stays blocked",
			blockedPhase:   "refining",
			resumeApproved: false,
			want:           false,
		},
		{
			name:           "blocked_phase set, approved → auto-unblock",
			blockedPhase:   "planning",
			resumeApproved: true,
			want:           true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeTask(t, tasksDir, "TASK-"+tt.name+".md", fmt.Sprintf(`
id: "%s"
title: "%s"
project: my-project
status: blocked
assignee: deepseek
blocked_by: []
blocked_phase: "%s"
resume_approved: %v
`, tt.name, tt.name, tt.blockedPhase, tt.resumeApproved))
			data, _ := os.ReadFile(path)
			fm, _ := yamlfrontmatter.Parse(data)
			if fm == nil {
				t.Fatal("parse failed")
			}
			if got := IsAutoUnblockable(fm, dir); got != tt.want {
				t.Errorf("IsAutoUnblockable() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBlockedBy_CrossProjectFallback(t *testing.T) {
	// Verify cross-project dependency resolves when directory name (002-b)
	// differs from the frontmatter project field (b).
	dir := t.TempDir()
	aDir := filepath.Join(dir, "Projects", "002-a")
	bDir := filepath.Join(dir, "Projects", "001-b")
	aTasks := filepath.Join(aDir, "Tasks")
	bTasks := filepath.Join(bDir, "Tasks")
	if err := os.MkdirAll(aTasks, 0755); err != nil {
		t.Fatalf("create project a tasks directory: %v", err)
	}
	if err := os.MkdirAll(bTasks, 0755); err != nil {
		t.Fatalf("create project b tasks directory: %v", err)
	}

	// Dependency in project "b" (directory 001-b)
	writeTask(t, bTasks, "TASK-010-done.md", `
id: "010"
title: "Dependency in b"
project: b
status: done
assignee: deepseek
`)

	// Task in project "a" (directory 002-a) blocked by b:TASK-010
	blockedPath := writeTask(t, aTasks, "TASK-020-blocked.md", `
id: "020"
title: "Cross project blocked"
project: a
status: blocked
assignee: deepseek
blocked_by:
  - b:TASK-010
`)

	data, _ := os.ReadFile(blockedPath)
	fm, _ := yamlfrontmatter.Parse(data)
	if fm == nil {
		t.Fatal("parse failed")
	}

	if !IsAutoUnblockable(fm, dir) {
		t.Error("should be auto-unblockable; cross-project dependency b:TASK-010 is done")
	}

	tasks, err := FindReadyTasks(dir)
	if err != nil {
		t.Fatalf("FindReadyTasks: %v", err)
	}
	found := false
	for _, rt := range tasks {
		if rt.ID == "020" {
			found = true
			break
		}
	}
	if !found {
		t.Error("cross-project blocked task with resolved dependency should appear in ready tasks")
	}
}

func TestIsReadyCompleteStateMachine(t *testing.T) {
	tests := []struct {
		name string
		fm   yamlfrontmatter.Frontmatter
		want bool
	}{
		{name: "closed is terminal", fm: yamlfrontmatter.Frontmatter{Status: "closed", Assignee: "gpt"}, want: false},
		{name: "review feedback resumes", fm: yamlfrontmatter.Frontmatter{Status: "review", Assignee: "gpt", ReworkResolution: "resume"}, want: true},
		{name: "plan review replans", fm: yamlfrontmatter.Frontmatter{Status: "plan-review", Assignee: "gpt", ReworkResolution: "replan"}, want: true},
		{name: "close gate waits for approval", fm: yamlfrontmatter.Frontmatter{Status: "review", Assignee: "gpt", ReworkResolution: "close"}, want: false},
		{name: "close gate approved", fm: yamlfrontmatter.Frontmatter{Status: "review", Assignee: "gpt", ReworkResolution: "close", CloseApproved: true}, want: true},
		{name: "done remains terminal without change", fm: yamlfrontmatter.Frontmatter{Status: "done", Assignee: "gpt"}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsReady(&tt.fm, t.TempDir()); got != tt.want {
				t.Fatalf("IsReady() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFindReadyTasksSortsByPriorityThenCreated(t *testing.T) {
	vault := t.TempDir()
	tasksDir := filepath.Join(vault, "Projects", "001-test", "Tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatalf("create tasks: %v", err)
	}

	writeTask(t, tasksDir, "TASK-003-new-p0.md", `
id: "003"
title: New P0
project: test
status: ready
assignee: gpt
priority: P0
created: "2026-07-28T10:00:00+08:00"
`)
	writeTask(t, tasksDir, "TASK-002-old-p1.md", `
id: "002"
title: Old P1
project: test
status: ready
assignee: gpt
priority: P1
created: "2026-07-01T10:00:00+08:00"
`)
	writeTask(t, tasksDir, "TASK-001-old-p0.md", `
id: "001"
title: Old P0
project: test
status: ready
assignee: gpt
priority: P0
created: "2026-07-01T10:00:00+08:00"
`)

	ready, err := FindReadyTasks(vault)
	if err != nil {
		t.Fatalf("FindReadyTasks: %v", err)
	}
	got := []string{ready[0].ID, ready[1].ID, ready[2].ID}
	want := []string{"001", "003", "002"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ready order = %v, want %v", got, want)
		}
	}
}

func TestCreateTaskForReqWithoutPriorityStartsAssessment(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	t.Setenv("HOME", dir)
	reqDir := filepath.Join(vault, "Projects", "001-test", "Requirements")
	if err := os.MkdirAll(reqDir, 0o755); err != nil {
		t.Fatalf("create requirements: %v", err)
	}
	reqPath := filepath.Join(reqDir, "REQ-004-priority.md")
	if err := os.WriteFile(reqPath, []byte("---\nid: \"004\"\ntitle: Priority\npriority: \"\"\n---\n# Priority\n"), 0o644); err != nil {
		t.Fatalf("write requirement: %v", err)
	}

	result := createTaskForReq(vault, "Projects/001-test/Requirements/REQ-004-priority.md")
	if result == nil {
		t.Fatal("expected TASK creation")
	}
	data, err := os.ReadFile(filepath.Join(vault, "Projects", "001-test", "Tasks", "TASK-004-priority.md"))
	if err != nil {
		t.Fatalf("read TASK: %v", err)
	}
	fm, err := yamlfrontmatter.Parse(data)
	if err != nil {
		t.Fatalf("parse TASK: %v", err)
	}
	if fm.Priority != "" || fm.PriorityAssessmentStatus != "pending" || fm.Status != "blocked" {
		t.Fatalf("new TASK priority state = priority %q assessment %q status %q", fm.Priority, fm.PriorityAssessmentStatus, fm.Status)
	}
}

func TestClosedBlockerSatisfaction(t *testing.T) {
	vault := t.TempDir()
	tasksDir := filepath.Join(vault, "Projects", "001-test", "Tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatalf("create tasks: %v", err)
	}

	writeTask(t, tasksDir, "TASK-010-implemented.md", `
id: "010"
project: 001-test
status: closed
closure_reason: already_implemented
`)
	if !AreBlockersDone(vault, "001-test", []string{"TASK-010"}) {
		t.Fatal("already_implemented closed blocker should satisfy dependency")
	}

	writeTask(t, tasksDir, "TASK-011-cancelled.md", `
id: "011"
project: 001-test
status: closed
closure_reason: cancelled
`)
	if AreBlockersDone(vault, "001-test", []string{"TASK-011"}) {
		t.Fatal("cancelled closed blocker must not satisfy dependency")
	}

	writeTask(t, tasksDir, "TASK-012-replacement.md", `
id: "012"
project: 001-test
status: done
`)
	writeTask(t, tasksDir, "TASK-013-duplicate.md", `
id: "013"
project: 001-test
status: closed
closure_reason: duplicate
replacement_task: TASK-012
`)
	if !AreBlockersDone(vault, "001-test", []string{"TASK-013"}) {
		t.Fatal("duplicate closed blocker should satisfy dependency when replacement is done")
	}
}
