package daemon

import (
	"log"
	"os"
	"path/filepath"
	"testing"

	"github.com/ndzuki/obsidian-task-runner/internal/config"
	"github.com/ndzuki/obsidian-task-runner/internal/task"
	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

func TestRunPriorityAssessmentWritesNormalizedResult(t *testing.T) {
	dir := t.TempDir()
	reqPath := filepath.Join(dir, "REQ-001.md")
	if err := os.WriteFile(reqPath, []byte("# Requirement\n"), 0o644); err != nil {
		t.Fatalf("write REQ: %v", err)
	}
	taskPath := filepath.Join(dir, "TASK-001.md")
	if err := os.WriteFile(taskPath, []byte("---\nid: \"001\"\nstatus: blocked\npriority: \"\"\npriority_assessment_status: pending\npriority_assessment_attempts: 0\n---\n# Task\n"), 0o644); err != nil {
		t.Fatalf("write TASK: %v", err)
	}
	omp := filepath.Join(dir, "omp")
	script := "#!/bin/sh\nprintf '%s' '{\"priority\":\"P1\",\"impact\":\"high\",\"urgency\":\"near_term\",\"workaround\":\"partial\",\"score\":999,\"confidence\":\"high\",\"reason\":\"core path\",\"recommendation\":\"\"}'\n"
	if err := os.WriteFile(omp, []byte(script), 0o755); err != nil {
		t.Fatalf("write OMP: %v", err)
	}

	runner := &Runner{cfg: &config.Config{OMPCmd: omp, Models: config.DefaultModels()}, logger: log.New(os.Stderr, "", 0)}
	if err := runner.runPriorityAssessment(task.PriorityTask{ID: "001", FilePath: taskPath, ReqDoc: reqPath}); err != nil {
		t.Fatalf("runPriorityAssessment: %v", err)
	}
	data, err := os.ReadFile(taskPath)
	if err != nil {
		t.Fatalf("read TASK: %v", err)
	}
	fm, err := yamlfrontmatter.Parse(data)
	if err != nil {
		t.Fatalf("parse TASK: %v", err)
	}
	if fm.Priority != "P2" || fm.PriorityScore != 6 || fm.PriorityAssessmentStatus != "completed" {
		t.Fatalf("assessment = priority %q score %d status %q", fm.Priority, fm.PriorityScore, fm.PriorityAssessmentStatus)
	}
}
