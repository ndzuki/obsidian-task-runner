package task

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

func writeTask(dir, name, frontmatter string) string {
	path := filepath.Join(dir, name)
	content := "---\n" + strings.TrimSpace(frontmatter) + "\n---\n# Task\n"
	os.WriteFile(path, []byte(content), 0644)
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

func TestIsAutoUnblockable(t *testing.T) {
	dir := t.TempDir()
	projDir := filepath.Join(dir, "Projects", "001-test")
	tasksDir := filepath.Join(projDir, "Tasks")
	os.MkdirAll(tasksDir, 0755)

	path := writeTask(tasksDir, "TASK-001.md", `
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

	if !IsAutoUnblockable(fm) {
		t.Error("should be auto-unblockable")
	}

	// Test with missing assignee
	path2 := writeTask(tasksDir, "TASK-002.md", `
id: "002"
title: "Test"
project: my-project
status: blocked
assignee: ""
blocked_by: []
`)
	data2, _ := os.ReadFile(path2)
	fm2, _ := yamlfrontmatter.Parse(data2)
	if IsAutoUnblockable(fm2) {
		t.Error("should NOT be auto-unblockable without assignee")
	}
}

func TestFindReadyTasks(t *testing.T) {
	dir := t.TempDir()
	tasksDir := filepath.Join(dir, "Projects", "001-test", "Tasks")

	os.MkdirAll(tasksDir, 0755)
	// Ready task
	writeTask(tasksDir, "TASK-001-ready.md", `
id: "001"
title: "Ready Task"
project: my-app
status: ready
assignee: deepseek
priority: P1
`)

	// Blocked task with valid assignee → should be auto-unblocked
	writeTask(tasksDir, "TASK-002-blocked.md", `
id: "002"
title: "Blocked but Fillable"
project: my-app
status: blocked
assignee: deepseek
blocked_by: []
priority: P0
`)

	// Blocked with no assignee → not ready
	writeTask(tasksDir, "TASK-003-no-assignee.md", `
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

func TestAppendToMemory(t *testing.T) {
	dir := t.TempDir()
	vaultPath := filepath.Join(dir, "vault")

	// New structure: REQ under Projects/001-demo/Requirements/
	projectDir := "001-demo"
	reqDir := filepath.Join(vaultPath, "Projects", projectDir, "Requirements")
	os.MkdirAll(reqDir, 0755)

	reqRelPath := filepath.Join("Projects", projectDir, "Requirements", "REQ-001-test.md")
	targetName := "TASK-001-test.md"

	// First call: creates memory.md
	appendToMemory(vaultPath, projectDir, "001", "001", "My Feature", "ndzuki", "", reqRelPath, targetName, "2026-07-13T10:00:00+08:00")

	memoryPath := filepath.Join(vaultPath, "Projects", projectDir, "Notes", "memory.md")
	if _, err := os.Stat(memoryPath); os.IsNotExist(err) {
		t.Fatal("memory.md was not created")
	}

	content, err := os.ReadFile(memoryPath)
	if err != nil {
		t.Fatal(err)
	}
	contentStr := string(content)

	if !strings.Contains(contentStr, "# 项目记忆: 001-demo") {
		t.Error("memory.md missing project header")
	}
	if !strings.Contains(contentStr, "### REQ-001 · My Feature") {
		t.Error("memory.md missing REQ entry heading")
	}
	if !strings.Contains(contentStr, "[[Requirements/REQ-001-test.md]]") {
		t.Error("memory.md missing requirement reference")
	}
	if !strings.Contains(contentStr, "[[Tasks/TASK-001-test.md]]") {
		t.Error("memory.md missing task reference")
	}

	// Second call: appends to existing memory.md
	appendToMemory(vaultPath, projectDir, "001", "002", "Another Feature", "ndzuki", "", reqRelPath, targetName, "2026-07-13T11:00:00+08:00")

	content2, _ := os.ReadFile(memoryPath)
	if strings.Count(string(content2), "### REQ-") != 2 {
		t.Errorf("expected 2 REQ entries, got %d", strings.Count(string(content2), "### REQ-"))
	}
}

func TestCreateTaskForReqNewStructure(t *testing.T) {
	dir := t.TempDir()
	vaultPath := filepath.Join(dir, "vault")
	// Isolate from real vault-map
	t.Setenv("HOME", dir)

	projectName := "001-release-manager"
	reqDir := filepath.Join(vaultPath, "Projects", projectName, "Requirements")
	os.MkdirAll(reqDir, 0755)

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
	os.WriteFile(reqPath, []byte(reqContent), 0644)

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

	// Verify memory.md was created
	memoryPath := filepath.Join(vaultPath, "Projects", projectName, "Notes", "memory.md")
	if _, err := os.Stat(memoryPath); os.IsNotExist(err) {
		t.Error("memory.md was not created")
	}
}

func TestCreateTaskForReqWithVaultMap(t *testing.T) {
	dir := t.TempDir()
	vaultPath := filepath.Join(dir, "vault")
	t.Setenv("HOME", dir)

	// Set up vault-map with "release-manager" project
	ompDir := filepath.Join(dir, ".omp", "skills", "obsidian-task-runner", "config")
	os.MkdirAll(ompDir, 0755)
	vaultMap := `{"projects":[{"name":"release-manager","path":"/tmp/release-manager"}],"new_project_root":"/tmp"}`
	os.WriteFile(filepath.Join(ompDir, "vault-map.json"), []byte(vaultMap), 0644)

	projectName := "001-release-manager"
	reqDir := filepath.Join(vaultPath, "Projects", projectName, "Requirements")
	os.MkdirAll(reqDir, 0755)

	reqContent := `---
id: "003"
title: "Vault Map Feature"
---

# Vault Map Feature

## 要做什么
Test vault-map project matching.
`
	reqPath := filepath.Join(reqDir, "REQ-003-vault-map.md")
	os.WriteFile(reqPath, []byte(reqContent), 0644)

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
	os.MkdirAll(reqDir, 0755)

	reqContent := `---
id: "001"
title: "Legacy Feature"
---

# Legacy Feature

## 要做什么
Legacy flat structure.
`
	reqPath := filepath.Join(reqDir, "REQ-001-legacy.md")
	os.WriteFile(reqPath, []byte(reqContent), 0644)

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
