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

// TestSyncStageInheritanceFollowsReqStage guards the two modes:
//  1. an empty task stage inherits the REQ stage and records stage_source=req;
//  2. a task with stage_source=req follows a REQ stage change;
//  3. a task staged by daemon/PM (stage_source empty) never follows REQ.
func TestSyncStageInheritanceFollowsReqStage(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	projDir := filepath.Join(vault, "Projects", "001-test")
	tasksDir := filepath.Join(projDir, "Tasks")
	reqDir := filepath.Join(projDir, "Requirements")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(reqDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeReq := func(stage string) {
		t.Helper()
		content := "---\nid: \"001\"\ntitle: Req\nstatus: defined\nstage: \"" + stage + "\"\n---\n# Req\n"
		if err := os.WriteFile(filepath.Join(reqDir, "REQ-001-r.md"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeTask := func(id, stage, source string) string {
		t.Helper()
		content := "---\nid: \"" + id + "\"\nstatus: ready\nreq_doc: Projects/001-test/Requirements/REQ-001-r.md\nstage: \"" + stage + "\"\nstage_source: " + source + "\n---\n# T\n"
		path := filepath.Join(tasksDir, "TASK-"+id+"-t.md")
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	stageOf := func(path string) string {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		fm, err := yamlfrontmatter.Parse(data)
		if err != nil || fm == nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		return fm.Stage
	}

	runner := New(&config.Config{ObsidianVault: vault})
	runner.logger = log.New(io.Discard, "", 0)

	writeReq("P1")
	inherited := writeTask("001", "", "")        // empty stage → inherit
	manual := writeTask("002", "P7", `""`)       // explicitly assigned → never follow
	following := writeTask("003", "P1", `"req"`) // REQ-inherited → follows

	runner.syncStageInheritance()
	if got := stageOf(inherited); got != "P1" {
		t.Fatalf("empty-stage task = %q, want P1 (inherited)", got)
	}

	// PM re-stages the REQ to P3.
	writeReq("P3")
	runner.syncStageInheritance()
	if got := stageOf(following); got != "P3" {
		t.Fatalf("REQ-inherited task = %q, want P3 (follows REQ change)", got)
	}
	if got := stageOf(manual); got != "P7" {
		t.Fatalf("explicitly-assigned task = %q, want P7 (never follows)", got)
	}
}
