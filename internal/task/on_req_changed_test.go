package task

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

// reqContentWithType returns a requirement body ending with the given change
// type annotation (or none), and its sha256:hex hash.
func reqContentWithType(changeType string) (string, string) {
	body := "---\nid: \"099\"\ntitle: Test Requirement\n---\n# Test Requirement\n要做什么: test\n"
	if changeType != "" {
		body += "## 变更记录\n\n> 时间: 2026-08-12T12:00:00+08:00\n> 变更类型: " + changeType + "\n"
	}
	sum := sha256.Sum256([]byte(body))
	return body, "sha256:" + fmt.Sprintf("%x", sum)
}

// writeReqTask creates a one-project vault with REQ-099 and TASK-099 whose
// frontmatter carries the given extra fields (status, refine_req_hash, ...).
func writeReqTask(t *testing.T, reqBody string, taskYAML map[string]string) (vault, taskPath string) {
	t.Helper()
	vault = t.TempDir()
	projDir := filepath.Join(vault, "Projects", "001-test")
	tasksDir := filepath.Join(projDir, "Tasks")
	reqsDir := filepath.Join(projDir, "Requirements")
	if err := os.MkdirAll(tasksDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(reqsDir, 0755); err != nil {
		t.Fatal(err)
	}
	reqPath := filepath.Join(reqsDir, "REQ-099-test-req.md")
	if err := os.WriteFile(reqPath, []byte(reqBody), 0644); err != nil {
		t.Fatal(err)
	}

	fm := "---\nid: \"099\"\ntitle: Test Task\nproject: test\nassignee: default\nreq_doc: Projects/001-test/Requirements/REQ-099-test-req.md\n"
	for k, v := range taskYAML {
		fm += k + ": " + v + "\n"
	}
	fm += "---\n# TASK-099\n"
	// Filename must match the canonical derivation (REQ-099-test-req →
	// TASK-099-test-req.md) so the no-affected fallback does not create one.
	taskPath = filepath.Join(tasksDir, "TASK-099-test-req.md")
	if err := os.WriteFile(taskPath, []byte(fm), 0644); err != nil {
		t.Fatal(err)
	}
	return vault, taskPath
}

// TestOnReqChanged_DoneBreakingReopens guards the breaking (and unannotated)
// path: a delivered task reopens with generation reset so the second delivery
// gets a fresh branch/PR instead of reusing the MERGED one.
func TestOnReqChanged_DoneBreakingReopens(t *testing.T) {
	reqBody, _ := reqContentWithType(ReqChangeBreaking)
	vault, taskPath := writeReqTask(t, reqBody, map[string]string{
		"status":              "done",
		"pending_req":         "false",
		"merge_approved":      "true",
		"reopen_count":        "1",
		"target_branch":       "task/099-values",
		"pr_url":              "https://github.com/x/y/pull/9",
		"merge_status":        "merged",
		"completed":           "2026-07-16T18:20:16+08:00",
		"knowledge_extracted": "true",
	})

	results := OnReqChanged(vault, "Projects/001-test/Requirements/REQ-099-test-req.md", "")
	if len(results) != 1 {
		t.Fatalf("expected 1 affected result, got %d", len(results))
	}
	if results[0].Action != "pending_req" || results[0].OldStatus != "done" {
		t.Fatalf("result = %+v, want pending_req from done", results[0])
	}

	fm := readTaskFM(t, taskPath)
	if fm.Status != "refining" || !fm.PendingReq || fm.MergeApproved {
		t.Fatalf("task not reopened: status=%q pending_req=%v merge_approved=%v", fm.Status, fm.PendingReq, fm.MergeApproved)
	}
	if fm.ReopenCount != 2 {
		t.Fatalf("reopen_count = %d, want 2 (generation reset)", fm.ReopenCount)
	}
	if fm.TargetBranch != "" || fm.PRURL != "" || fm.MergeStatus != "" || fm.Completed != "" {
		t.Fatalf("generation not reset: target_branch=%q pr_url=%q merge_status=%q completed=%q",
			fm.TargetBranch, fm.PRURL, fm.MergeStatus, fm.Completed)
	}
	if fm.KnowledgeExtracted {
		t.Fatal("knowledge_extracted should reset so the second delivery re-extracts")
	}
}

// TestOnReqChanged_DoneUnannotatedIsBreaking guards the conservative default:
// a REQ change without a type annotation reopens the done task.
func TestOnReqChanged_DoneUnannotatedIsBreaking(t *testing.T) {
	reqBody, _ := reqContentWithType("")
	vault, taskPath := writeReqTask(t, reqBody, map[string]string{
		"status":       "done",
		"pending_req":  "false",
		"reopen_count": "0",
	})

	results := OnReqChanged(vault, "Projects/001-test/Requirements/REQ-099-test-req.md", "")
	if len(results) != 1 || results[0].Action != "pending_req" {
		t.Fatalf("unannotated change must be treated as breaking, got %+v", results)
	}
	fm := readTaskFM(t, taskPath)
	if fm.Status != "refining" || fm.ReopenCount != 1 {
		t.Fatalf("task = %q reopen_count=%d, want refining/1", fm.Status, fm.ReopenCount)
	}
}

// TestOnReqChanged_DoneAdditiveStaysTerminal guards the additive path: the
// delivered task keeps done and only a suggestion notification is produced.
func TestOnReqChanged_DoneAdditiveStaysTerminal(t *testing.T) {
	reqBody, _ := reqContentWithType(ReqChangeAdditive)
	vault, taskPath := writeReqTask(t, reqBody, map[string]string{
		"status":       "done",
		"pending_req":  "false",
		"reopen_count": "1",
	})

	results := OnReqChanged(vault, "Projects/001-test/Requirements/REQ-099-test-req.md", "")
	if len(results) != 1 {
		t.Fatalf("expected 1 affected result, got %d", len(results))
	}
	if results[0].Action != ActionReqAdditive {
		t.Fatalf("Action = %q, want %q", results[0].Action, ActionReqAdditive)
	}
	fm := readTaskFM(t, taskPath)
	if fm.Status != "done" || fm.PendingReq || fm.ReopenCount != 1 {
		t.Fatalf("done task must stay terminal: status=%q pending_req=%v reopen_count=%d", fm.Status, fm.PendingReq, fm.ReopenCount)
	}
}

// TestOnReqChanged_DoneCosmeticIgnored guards the cosmetic path: no result,
// no task mutation.
func TestOnReqChanged_DoneCosmeticIgnored(t *testing.T) {
	reqBody, _ := reqContentWithType(ReqChangeCosmetic)
	vault, taskPath := writeReqTask(t, reqBody, map[string]string{
		"status":       "done",
		"pending_req":  "false",
		"reopen_count": "0",
	})

	results := OnReqChanged(vault, "Projects/001-test/Requirements/REQ-099-test-req.md", "")
	if len(results) != 0 {
		t.Fatalf("cosmetic change must be ignored, got %+v", results)
	}
	fm := readTaskFM(t, taskPath)
	if fm.Status != "done" || fm.PendingReq || fm.ReopenCount != 0 {
		t.Fatalf("task mutated by cosmetic change: status=%q pending_req=%v reopen_count=%d", fm.Status, fm.PendingReq, fm.ReopenCount)
	}
}

// TestOnReqChanged_AbsorbedChangeSkipped guards the absorbed-change
// dedup: when the task's refine_req_hash already equals the current REQ
// content, the event is a self-write (refining/PM writing back) and must not
// re-open or re-notify.
func TestOnReqChanged_AbsorbedChangeSkipped(t *testing.T) {
	reqBody, reqHash := reqContentWithType(ReqChangeBreaking)
	vault, taskPath := writeReqTask(t, reqBody, map[string]string{
		"status":              "done",
		"pending_req":         "false",
		"refine_req_hash":     reqHash,
		"target_branch":       "task/099-values",
		"pr_url":              "https://github.com/x/y/pull/9",
		"merge_status":        "merged",
		"completed":           "2026-07-16T18:20:16+08:00",
		"knowledge_extracted": "true",
	})

	results := OnReqChanged(vault, "Projects/001-test/Requirements/REQ-099-test-req.md", "")
	if len(results) != 0 {
		t.Fatalf("absorbed change must be skipped, got %+v", results)
	}
	fm := readTaskFM(t, taskPath)
	if fm.Status != "done" || fm.ReopenCount != 0 || fm.TargetBranch == "" {
		t.Fatalf("task must stay untouched: status=%q reopen_count=%d target_branch=%q", fm.Status, fm.ReopenCount, fm.TargetBranch)
	}
}

// TestOnReqChanged_ReviewAdditiveStillReopens guards that in-flight
// deliveries (review/conflict) absorb ANY change type — merging stale scope
// is forbidden, so additive must also force a replan.
func TestOnReqChanged_ReviewAdditiveStillReopens(t *testing.T) {
	reqBody, _ := reqContentWithType(ReqChangeAdditive)
	vault, taskPath := writeReqTask(t, reqBody, map[string]string{
		"status":         "review",
		"pending_req":    "false",
		"merge_approved": "true",
		"target_branch":  "task/099-values",
		"pr_url":         "https://github.com/x/y/pull/9",
	})

	results := OnReqChanged(vault, "Projects/001-test/Requirements/REQ-099-test-req.md", "")
	if len(results) != 1 || results[0].Action != "pending_req" {
		t.Fatalf("review + additive must reopen, got %+v", results)
	}
	fm := readTaskFM(t, taskPath)
	if fm.Status != "refining" || !fm.PendingReq || fm.MergeApproved {
		t.Fatalf("review not reopened: status=%q pending_req=%v merge_approved=%v", fm.Status, fm.PendingReq, fm.MergeApproved)
	}
	// In-flight redelivery keeps its branch/PR facts (no generation reset).
	if fm.TargetBranch != "task/099-values" || fm.PRURL != "https://github.com/x/y/pull/9" {
		t.Fatalf("review reopen must not reset generation: target_branch=%q pr_url=%q", fm.TargetBranch, fm.PRURL)
	}
}

// TestOnReqChanged_RenameDoneGenerationReset guards the rename path: a REQ
// rename reopens a delivered task with the same generation reset as a
// breaking change — otherwise the MERGED old PR is reused and the second
// delivery never merges.
func TestOnReqChanged_RenameDoneGenerationReset(t *testing.T) {
	vault := t.TempDir()
	projDir := filepath.Join(vault, "Projects", "001-test")
	tasksDir := filepath.Join(projDir, "Tasks")
	reqsDir := filepath.Join(projDir, "Requirements")
	if err := os.MkdirAll(tasksDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(reqsDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Old REQ file + its renamed sibling.
	if err := os.WriteFile(filepath.Join(reqsDir, "REQ-099-test-req.md"), []byte("# old\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(reqsDir, "REQ-099-test-req2.md"), []byte("# new\n"), 0644); err != nil {
		t.Fatal(err)
	}
	taskPath := filepath.Join(tasksDir, "TASK-099-test-req.md")
	taskMD := `---
id: "099"
title: Test Task
project: test
assignee: default
status: done
pending_req: false
merge_approved: true
reopen_count: 1
target_branch: task/099-values
pr_url: https://github.com/x/y/pull/9
merge_status: merged
completed: 2026-07-16T18:20:16+08:00
knowledge_extracted: true
req_doc: Projects/001-test/Requirements/REQ-099-test-req.md
---
# TASK-099
`
	if err := os.WriteFile(taskPath, []byte(taskMD), 0644); err != nil {
		t.Fatal(err)
	}

	results := OnReqChanged(vault, "Projects/001-test/Requirements/REQ-099-test-req2.md", "")
	if len(results) != 1 || results[0].Action != "rename_req" {
		t.Fatalf("expected rename_req result, got %+v", results)
	}
	fm := readTaskFM(t, taskPath)
	if fm.Status != "refining" || !fm.PendingReq {
		t.Fatalf("renamed done task not reopened: status=%q pending_req=%v", fm.Status, fm.PendingReq)
	}
	if fm.ReopenCount != 2 || fm.TargetBranch != "" || fm.PRURL != "" || fm.MergeStatus != "" || fm.Completed != "" || fm.KnowledgeExtracted {
		t.Fatalf("rename reopen skipped generation reset: reopen_count=%d target_branch=%q pr_url=%q merge_status=%q completed=%q extracted=%v",
			fm.ReopenCount, fm.TargetBranch, fm.PRURL, fm.MergeStatus, fm.Completed, fm.KnowledgeExtracted)
	}
	if fm.ReqDoc != "Projects/001-test/Requirements/REQ-099-test-req2.md" {
		t.Fatalf("req_doc not updated: %q", fm.ReqDoc)
	}
}

// TestOnReqChanged_CosmeticLegacyFilenameNoCreate guards the fallback: a
// cosmetic change on a task whose filename deviates from the canonical
// derivation must NOT auto-create a duplicate TASK (matchedAny guard).
func TestOnReqChanged_CosmeticLegacyFilenameNoCreate(t *testing.T) {
	reqBody, _ := reqContentWithType(ReqChangeCosmetic)
	vault := t.TempDir()
	projDir := filepath.Join(vault, "Projects", "001-test")
	tasksDir := filepath.Join(projDir, "Tasks")
	reqsDir := filepath.Join(projDir, "Requirements")
	if err := os.MkdirAll(tasksDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(reqsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(reqsDir, "REQ-099-test-req.md"), []byte(reqBody), 0644); err != nil {
		t.Fatal(err)
	}
	// Legacy filename that does not match TaskFilenameForReq derivation.
	legacyPath := filepath.Join(tasksDir, "TASK-099-legacy-name.md")
	legacyMD := `---
id: "099"
title: Test Task
project: test
assignee: default
status: done
pending_req: false
req_doc: Projects/001-test/Requirements/REQ-099-test-req.md
---
# TASK-099
`
	if err := os.WriteFile(legacyPath, []byte(legacyMD), 0644); err != nil {
		t.Fatal(err)
	}

	results := OnReqChanged(vault, "Projects/001-test/Requirements/REQ-099-test-req.md", "")
	if len(results) != 0 {
		t.Fatalf("cosmetic change must be ignored, got %+v", results)
	}
	entries, err := os.ReadDir(tasksDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "TASK-099-legacy-name.md" {
		files := make([]string, 0, len(entries))
		for _, e := range entries {
			files = append(files, e.Name())
		}
		t.Fatalf("fallback created a duplicate task, Tasks dir = %v", files)
	}
}

func readTaskFM(t *testing.T, taskPath string) *yamlfrontmatter.Frontmatter {
	t.Helper()
	data, err := os.ReadFile(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	fm, err := yamlfrontmatter.Parse(data)
	if err != nil || fm == nil {
		t.Fatalf("parse task: %v", err)
	}
	return fm
}
