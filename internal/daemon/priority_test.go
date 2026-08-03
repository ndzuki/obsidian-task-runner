package daemon

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
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
	oldProbe := apiKeyProbe
	apiKeyProbe = func() bool { return true }
	t.Cleanup(func() { apiKeyProbe = oldProbe })
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

func TestProcessPriorityAssessmentsUsesIndependentBatchLimit(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	tasksDir := filepath.Join(vault, "Projects", "test", "Tasks")
	reqsDir := filepath.Join(vault, "Projects", "test", "Requirements")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatalf("create tasks directory: %v", err)
	}
	if err := os.MkdirAll(reqsDir, 0o755); err != nil {
		t.Fatalf("create requirements directory: %v", err)
	}
	for i := 1; i <= 3; i++ {
		id := fmt.Sprintf("%03d", i)
		reqRel := filepath.Join("Projects", "test", "Requirements", "REQ-"+id+".md")
		if err := os.WriteFile(filepath.Join(vault, reqRel), []byte("# Requirement\n"), 0o644); err != nil {
			t.Fatalf("write requirement: %v", err)
		}
		content := fmt.Sprintf(`---
id: %q
title: Priority %s
project: test
status: blocked
priority: ""
priority_assessment_status: pending
priority_assessment_attempts: 0
req_doc: %s
---
# TASK-%s
`, id, id, reqRel, id)
		if err := os.WriteFile(filepath.Join(tasksDir, "TASK-"+id+".md"), []byte(content), 0o644); err != nil {
			t.Fatalf("write task: %v", err)
		}
	}

	calls := filepath.Join(dir, "calls")
	omp := filepath.Join(dir, "omp")
	script := "#!/bin/sh\nprintf 'call\\n' >> \"$CALLS\"\nprintf '%s' '{\"priority\":\"P1\",\"impact\":\"high\",\"urgency\":\"near_term\",\"workaround\":\"partial\",\"score\":6,\"confidence\":\"high\",\"reason\":\"core path\",\"recommendation\":\"\"}'\n"
	if err := os.WriteFile(omp, []byte(script), 0o755); err != nil {
		t.Fatalf("write OMP: %v", err)
	}
	t.Setenv("CALLS", calls)
	runner := New(&config.Config{
		ObsidianVault: vault,
		OMPCmd:        omp,
		Models:        config.DefaultModels(),
	})
	runner.logger = log.New(os.Stderr, "", 0)
	oldProbe := apiKeyProbe
	apiKeyProbe = func() bool { return true }
	t.Cleanup(func() { apiKeyProbe = oldProbe })

	if processed := runner.processPriorityAssessments(context.Background()); processed != priorityAssessmentBatchLimit {
		t.Fatalf("processed = %d, want %d", processed, priorityAssessmentBatchLimit)
	}
	data, err := os.ReadFile(calls)
	if err != nil {
		t.Fatalf("read calls: %v", err)
	}
	if got := strings.Count(string(data), "call\n"); got != priorityAssessmentBatchLimit {
		t.Fatalf("OMP calls = %d, want %d", got, priorityAssessmentBatchLimit)
	}

	thirdData, err := os.ReadFile(filepath.Join(tasksDir, "TASK-003.md"))
	if err != nil {
		t.Fatalf("read third task: %v", err)
	}
	third, err := yamlfrontmatter.Parse(thirdData)
	if err != nil {
		t.Fatalf("parse third task: %v", err)
	}
	if third.PriorityAssessmentStatus != "pending" {
		t.Fatalf("third assessment status = %q, want pending", third.PriorityAssessmentStatus)
	}
}
