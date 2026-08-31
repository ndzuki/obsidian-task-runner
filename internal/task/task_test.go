package task

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndzuki/obsidian-task-runner/internal/config"
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
		autoMerge     bool
		phaseError    string
		want          bool
	}{
		{name: "review approved", status: "review", mergeApproved: true, want: true},
		{name: "conflict approved", status: "conflict", mergeApproved: true, want: true},
		{name: "review awaiting approval", status: "review", want: false},
		{name: "conflict awaiting approval", status: "conflict", want: false},
		{name: "review pending requirement", status: "review", pendingReq: true, want: true},
		{name: "review auto-merge", status: "review", autoMerge: true, want: true},
		{name: "review auto-merge disabled", status: "review", autoMerge: false, want: false},
		{name: "review auto-merge with re-attemptable failure", status: "review", autoMerge: true, phaseError: "GIT_CONFLICT", want: true},
		{name: "review auto-merge with transient gh failure still ready", status: "review", autoMerge: true, phaseError: "GITHUB_UNAVAILABLE", want: true},
		{name: "review auto-merge with permanent repo defect", status: "review", autoMerge: true, phaseError: "REPO_MISMATCH", want: false},
		{name: "review auto-merge with pending req", status: "review", autoMerge: true, pendingReq: true, want: true},
		{name: "conflict auto-merge re-attemptable", status: "conflict", autoMerge: true, phaseError: "BASE_COMMIT_MISMATCH", want: true},
		{name: "conflict auto-merge transient gh failure still ready", status: "conflict", autoMerge: true, phaseError: "GITHUB_UNAVAILABLE", want: true},
		{name: "conflict manual stays manual", status: "conflict", autoMerge: false, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fm := &yamlfrontmatter.Frontmatter{
				Assignee:       "gpt",
				Status:         tt.status,
				MergeApproved:  tt.mergeApproved,
				PendingReq:     tt.pendingReq,
				AutoMerge:      tt.autoMerge,
				PhaseErrorCode: tt.phaseError,
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

func TestIsOffPeakWith(t *testing.T) {
	windows := []config.TimeWindow{{Start: "00:00", End: "09:00"}, {Start: "12:00", End: "14:00"}, {Start: "18:00", End: "24:00"}}
	// These assertions depend on the current time; verify window parsing and
	// cross-midnight handling structurally instead.
	if _, ok := parseHM("09:30"); !ok {
		t.Fatal("parseHM must accept HH:MM")
	}
	if _, ok := parseHM("25:00"); ok {
		t.Fatal("parseHM must reject invalid hour")
	}
	if _, ok := parseHM("9:30"); !ok {
		t.Fatal("parseHM must accept single-digit hour")
	}
	// Cross-midnight window: 22:00-02:00.
	cross := []config.TimeWindow{{Start: "22:00", End: "02:00"}}
	// Structural check: the helper evaluates without panic for any tz.
	_ = IsOffPeakWith(cross, "Asia/Shanghai")
	_ = IsOffPeakWith(windows, "invalid/tz") // falls back to CST
	// Default fn remains the legacy window, and the nil-window path must
	// agree with IsOffPeak() (regression: it used to return false always).
	if OffPeakFn == nil {
		t.Fatal("OffPeakFn must default to IsOffPeak")
	}
	if got := IsOffPeakWith(nil, ""); got != IsOffPeak() {
		t.Fatalf("IsOffPeakWith(nil) = %v, IsOffPeak() = %v — nil must fall back to legacy window", got, IsOffPeak())
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
	result := createTaskForReq(vaultPath, reqRelPath, "")

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

// TestCreateTaskForReqDefaultAssignee 钉住 vault-map default_assignee 契约：
// 非空默认值预写 TASK frontmatter（任务直接可调度）且提醒区显示委派；
// 空默认值保持旧的人工门禁（assignee 空 + blocked 提醒）。
func TestCreateTaskForReqDefaultAssignee(t *testing.T) {
	dir := t.TempDir()
	vaultPath := filepath.Join(dir, "vault")
	projectName := "001-release-manager"
	reqDir := filepath.Join(vaultPath, "Projects", projectName, "Requirements")
	if err := os.MkdirAll(reqDir, 0755); err != nil {
		t.Fatal(err)
	}
	reqContent := `---
id: "005"
title: "Delegated Feature"
---

# Delegated Feature

## 要做什么
Do the thing.
`
	reqRelPath := filepath.Join("Projects", projectName, "Requirements", "REQ-005-delegated.md")
	if err := os.WriteFile(filepath.Join(reqDir, "REQ-005-delegated.md"), []byte(reqContent), 0644); err != nil {
		t.Fatal(err)
	}

	// 非空默认值 → 预写 assignee，无 blocked 提醒。
	result := createTaskForReq(vaultPath, reqRelPath, "default")
	if result == nil {
		t.Fatal("createTaskForReq returned nil")
	}
	taskPath := filepath.Join(vaultPath, "Projects", projectName, "Tasks", "TASK-005-delegated.md")
	data, err := os.ReadFile(taskPath)
	if err != nil {
		t.Fatalf("read created task: %v", err)
	}
	taskStr := string(data)
	if !strings.Contains(taskStr, `assignee: "default"`) {
		t.Errorf("seeded task must carry assignee: \"default\", got:\n%s", taskStr)
	}
	if strings.Contains(taskStr, "任务已暂停在 blocked") {
		t.Error("seeded task must not show the manual-assignee blocked notice")
	}
	if !strings.Contains(taskStr, "默认委派 `default`") {
		t.Error("seeded task must show the delegation notice")
	}

	// 空默认值 → 保持旧门禁（assignee 空 + blocked 提醒）。
	reqRelPath2 := filepath.Join("Projects", projectName, "Requirements", "REQ-006-ungated.md")
	if err := os.WriteFile(filepath.Join(reqDir, "REQ-006-ungated.md"), []byte(reqContent), 0644); err != nil {
		t.Fatal(err)
	}
	if result := createTaskForReq(vaultPath, reqRelPath2, ""); result == nil {
		t.Fatal("createTaskForReq returned nil")
	}
	data2, err := os.ReadFile(filepath.Join(vaultPath, "Projects", projectName, "Tasks", "TASK-006-ungated.md"))
	if err != nil {
		t.Fatalf("read created task: %v", err)
	}
	taskStr2 := string(data2)
	if !strings.Contains(taskStr2, `assignee: ""`) {
		t.Errorf("empty default must keep assignee empty, got:\n%s", taskStr2)
	}
	if !strings.Contains(taskStr2, "任务已暂停在 blocked") {
		t.Error("empty default must keep the manual-assignee blocked notice")
	}
}

func TestCreateTaskForReqWithVaultMap(t *testing.T) {
	dir := t.TempDir()
	vaultPath := filepath.Join(dir, "vault")
	t.Setenv("HOME", dir)

	// Set up vault-map with "release-manager" project
	dshDir := filepath.Join(dir, ".dsh", "skills", "obsidian-task-runner", "config")
	if err := os.MkdirAll(dshDir, 0755); err != nil {
		t.Fatalf("create config directory: %v", err)
	}
	vaultMap := `{"projects":[{"name":"release-manager","path":"/tmp/release-manager"}],"new_project_root":"/tmp"}`
	if err := os.WriteFile(filepath.Join(dshDir, "vault-map.json"), []byte(vaultMap), 0644); err != nil {
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
	result := createTaskForReq(vaultPath, reqRelPath, "")

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
	result := createTaskForReq(vaultPath, reqRelPath, "")

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
		{name: "plan review auto-approve is ready", fm: yamlfrontmatter.Frontmatter{Status: "plan-review", Assignee: "gpt", AutoApprove: true}, want: true},
		{name: "plan review awaiting manual approval not ready", fm: yamlfrontmatter.Frontmatter{Status: "plan-review", Assignee: "gpt", AutoApprove: false}, want: false},
		{name: "close gate waits for approval", fm: yamlfrontmatter.Frontmatter{Status: "review", Assignee: "gpt", ReworkResolution: "close"}, want: false},
		{name: "close gate approved", fm: yamlfrontmatter.Frontmatter{Status: "review", Assignee: "gpt", ReworkResolution: "close", CloseApproved: true}, want: true},
		{name: "done remains terminal without change", fm: yamlfrontmatter.Frontmatter{Status: "done", Assignee: "gpt"}, want: false},
		{name: "done merged stays terminal", fm: yamlfrontmatter.Frontmatter{Status: "done", Assignee: "gpt", MergeStatus: "merged", PRURL: "https://x/pull/1"}, want: false},
		{name: "done with unmerged PR reopens merge", fm: yamlfrontmatter.Frontmatter{Status: "done", Assignee: "gpt", MergeStatus: "conflict-resolve-attempted", PRURL: "https://x/pull/1", TargetBranch: "task/001"}, want: true},
		{name: "done with bare branch stays terminal", fm: yamlfrontmatter.Frontmatter{Status: "done", Assignee: "gpt", TargetBranch: "task/002"}, want: false},
		{name: "legacy needs-refining is schedulable", fm: yamlfrontmatter.Frontmatter{Status: "needs-refining", Assignee: "gpt"}, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsReady(&tt.fm, t.TempDir()); got != tt.want {
				t.Fatalf("IsReady() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFindUnstagedTasks(t *testing.T) {
	vault := t.TempDir()
	projDir := filepath.Join(vault, "Projects", "001-test")
	tasksDir := filepath.Join(projDir, "Tasks")
	notesDir := filepath.Join(projDir, "Notes")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(notesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Active stage plan.
	plan := "---\nid: \"stage-plan\"\nproject: test\nstatus: active\n---\n# Plan\n"
	if err := os.WriteFile(filepath.Join(notesDir, "Stage-Plan.md"), []byte(plan), 0o644); err != nil {
		t.Fatal(err)
	}
	writeTask := func(name, id, status, stage string) {
		t.Helper()
		content := "---\nid: \"" + id + "\"\nproject: test\nstatus: " + status + "\nstage: \"" + stage + "\"\n---\n# " + id + "\n"
		if err := os.WriteFile(filepath.Join(tasksDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeTask("TASK-001-inflight.md", "001", "implementing", "") // unstaged → included
	writeTask("TASK-002-staged.md", "002", "implementing", "P1") // staged → excluded
	writeTask("TASK-003-done.md", "003", "done", "")             // done → excluded
	writeTask("TASK-004-blocked.md", "004", "blocked", "")       // blocked in-flight → included

	got, err := FindUnstagedTasks(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("unstaged count = %d, want 2 (got %+v)", len(got), got)
	}
	if !got[0].Unstaged || !got[1].Unstaged {
		t.Fatal("all returned tasks must carry Unstaged=true")
	}
	ids := map[string]bool{}
	for _, g := range got {
		ids[g.ID] = true
	}
	if !ids["001"] || !ids["004"] {
		t.Fatalf("unstaged ids = %v, want 001 and 004", ids)
	}
}

func TestFindUnstagedTasksSkipsCompletedPlan(t *testing.T) {
	vault := t.TempDir()
	projDir := filepath.Join(vault, "Projects", "001-test")
	tasksDir := filepath.Join(projDir, "Tasks")
	notesDir := filepath.Join(projDir, "Notes")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(notesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	plan := "---\nid: \"stage-plan\"\nproject: test\nstatus: completed\n---\n# Plan\n"
	if err := os.WriteFile(filepath.Join(notesDir, "Stage-Plan.md"), []byte(plan), 0o644); err != nil {
		t.Fatal(err)
	}
	content := "---\nid: \"001\"\nproject: test\nstatus: implementing\nstage: \"\"\n---\n# 001\n"
	if err := os.WriteFile(filepath.Join(tasksDir, "TASK-001-a.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := FindUnstagedTasks(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("completed stage plan must not auto-attach, got %d tasks", len(got))
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

// TestFindReadyTasksSortsByStageThenPriority guards the stage-ordered
// dispatch: within a project, an earlier stage outranks a higher priority
// in a later stage ("P2 P0" waits behind "P1 P3"), and stage ids compare
// numerically ("P10" after "P2"). Unstaged tasks sort last.
func TestFindReadyTasksSortsByStageThenPriority(t *testing.T) {
	vault := t.TempDir()
	tasksDir := filepath.Join(vault, "Projects", "001-test", "Tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatalf("create tasks: %v", err)
	}

	writeTask(t, tasksDir, "TASK-010-p3-p0-late-stage.md", `
id: "010"
title: P2 P0 late
project: test
status: ready
assignee: gpt
priority: P0
stage: "P2"
created: "2026-07-28T10:00:00+08:00"
`)
	writeTask(t, tasksDir, "TASK-020-p10.md", `
id: "020"
title: P10
project: test
status: ready
assignee: gpt
priority: P0
stage: "P10"
created: "2026-07-28T10:00:00+08:00"
`)
	writeTask(t, tasksDir, "TASK-030-p1-p3.md", `
id: "030"
title: P1 P3
project: test
status: ready
assignee: gpt
priority: P3
stage: "P1"
created: "2026-07-28T10:00:00+08:00"
`)
	writeTask(t, tasksDir, "TASK-040-p1-p0.md", `
id: "040"
title: P1 P0
project: test
status: ready
assignee: gpt
priority: P0
stage: "P1"
created: "2026-07-28T10:00:00+08:00"
`)
	writeTask(t, tasksDir, "TASK-050-unstaged.md", `
id: "050"
title: unstaged
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
	got := make([]string, 0, len(ready))
	for _, r := range ready {
		got = append(got, r.ID)
	}
	want := []string{"040", "030", "010", "020", "050"} // P1 P0 → P1 P3 → P2 P0 → P10 P0 → unstaged last
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ready order = %v, want %v", got, want)
		}
	}
}

// TestFindReadyTasksStageSortIsProjectScoped guards cross-project fairness:
// stages of different projects are incomparable, so ordering falls back to
// created time instead of comparing "P1" of project B against "P3" of A.
func TestFindReadyTasksStageSortIsProjectScoped(t *testing.T) {
	vault := t.TempDir()
	projA := filepath.Join(vault, "Projects", "001-a", "Tasks")
	projB := filepath.Join(vault, "Projects", "002-b", "Tasks")
	if err := os.MkdirAll(projA, 0o755); err != nil {
		t.Fatalf("create tasks: %v", err)
	}
	if err := os.MkdirAll(projB, 0o755); err != nil {
		t.Fatalf("create tasks: %v", err)
	}

	writeTask(t, projA, "TASK-001-a-p3.md", `
id: "001"
title: A P3
project: a
status: ready
assignee: gpt
priority: P0
stage: "P3"
created: "2026-07-02T10:00:00+08:00"
`)
	writeTask(t, projB, "TASK-002-b-p1.md", `
id: "002"
title: B P1
project: b
status: ready
assignee: gpt
priority: P0
stage: "P1"
created: "2026-07-01T10:00:00+08:00"
`)

	ready, err := FindReadyTasks(vault)
	if err != nil {
		t.Fatalf("FindReadyTasks: %v", err)
	}
	got := []string{ready[0].ID, ready[1].ID}
	want := []string{"002", "001"} // B created earlier: cross-project order ignores stage
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ready order = %v, want %v", got, want)
		}
	}
}

// TestFindReadyTasksCrossProjectKeepsPriority guards that cross-project
// ordering still respects priority: a later-created P0 in project A must
// dispatch before an earlier-created P1 in project B (stage never leaks
// across projects, priority always does).
func TestFindReadyTasksCrossProjectKeepsPriority(t *testing.T) {
	vault := t.TempDir()
	projA := filepath.Join(vault, "Projects", "001-a", "Tasks")
	projB := filepath.Join(vault, "Projects", "002-b", "Tasks")
	if err := os.MkdirAll(projA, 0o755); err != nil {
		t.Fatalf("create tasks: %v", err)
	}
	if err := os.MkdirAll(projB, 0o755); err != nil {
		t.Fatalf("create tasks: %v", err)
	}

	writeTask(t, projA, "TASK-001-a-p0.md", `
id: "001"
title: A P0
project: a
status: ready
assignee: gpt
priority: P0
stage: "P3"
created: "2026-07-02T10:00:00+08:00"
`)
	writeTask(t, projB, "TASK-002-b-p1.md", `
id: "002"
title: B P1
project: b
status: ready
assignee: gpt
priority: P1
stage: "P1"
created: "2026-07-01T10:00:00+08:00"
`)

	ready, err := FindReadyTasks(vault)
	if err != nil {
		t.Fatalf("FindReadyTasks: %v", err)
	}
	got := []string{ready[0].ID, ready[1].ID}
	want := []string{"001", "002"} // A's P0 outranks B's earlier-created P1
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

	result := createTaskForReq(vault, "Projects/001-test/Requirements/REQ-004-priority.md", "")
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
closure_reason: already-implemented
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
