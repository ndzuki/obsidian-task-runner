package daemon

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ndzuki/obsidian-task-runner/internal/config"
	"github.com/ndzuki/obsidian-task-runner/internal/task"
)

// writeArgsOMP writes a fake OMP that dumps its argv into argsPath and exits 0.
func writeArgsOMP(t *testing.T, argsPath string) string {
	t.Helper()
	omp := filepath.Join(filepath.Dir(argsPath), "fake-omp")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" > '" + argsPath + "'\n"
	if err := os.WriteFile(omp, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake omp: %v", err)
	}
	return omp
}

func withAPIKey(t *testing.T) {
	t.Helper()
	oldProbe, _ := apiKeyProbe.Load().(func() bool)
	apiKeyProbe.Store(func() bool { return true })
	t.Cleanup(func() { apiKeyProbe.Store(oldProbe) })
}

func writeGrillingTask(t *testing.T, path, id, reqDoc, project string, parked bool, repeat int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create task dir: %v", err)
	}
	content := "---\n" +
		"id: \"" + id + "\"\n" +
		"title: T" + id + "\n" +
		"project: " + project + "\n" +
		"req_doc: " + reqDoc + "\n" +
		"status: needs-grilling\n" +
		"grill_done: false\n" +
		"grill_continue: false\n" +
		"grill_parked: " + boolStr(parked) + "\n" +
		"grill_repeat: " + strconv.Itoa(repeat) + "\n" +
		"---\n# T" + id + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write task %s: %v", path, err)
	}
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func writeDecisionList(t *testing.T, path string, answered bool) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir list dir: %v", err)
	}
	content := "---\n" +
		"id: \"grilling-decisions\"\n" +
		"project: test\n" +
		"status: open\n" +
		"grill_continue: " + boolStr(answered) + "\n" +
		"---\n# Grilling Decisions\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write decision list: %v", err)
	}
}

func TestNeedsConsolidationGrouping(t *testing.T) {
	cases := []struct {
		name    string
		members []task.GrillingTask
		want    bool
	}{
		{
			name:    "empty group never consolidates",
			members: nil,
			want:    false,
		},
		{
			name:    "lone task below repeat threshold",
			members: []task.GrillingTask{{GrillRepeat: 1, GrillParked: false}},
			want:    false,
		},
		{
			name:    "lone task with repeat dispute consolidates",
			members: []task.GrillingTask{{GrillRepeat: 2, GrillParked: false}},
			want:    true,
		},
		{
			name:    "lone task with churning replans consolidates",
			members: []task.GrillingTask{{PlanVersion: 3, GrillRepeat: 1, GrillParked: false}},
			want:    true,
		},
		{
			name:    "lone task below both thresholds stays per-task",
			members: []task.GrillingTask{{PlanVersion: 2, GrillRepeat: 1, GrillParked: false}},
			want:    false,
		},
		{
			name:    "lone parked task does not re-consolidate",
			members: []task.GrillingTask{{GrillRepeat: 3, GrillParked: true}},
			want:    false,
		},
		{
			name: "shared req with un-parked member consolidates",
			members: []task.GrillingTask{
				{GrillRepeat: 1, GrillParked: false},
				{GrillRepeat: 1, GrillParked: false},
			},
			want: true,
		},
		{
			name: "fully parked shared group does not re-consolidate",
			members: []task.GrillingTask{
				{GrillRepeat: 2, GrillParked: true},
				{GrillRepeat: 2, GrillParked: true},
			},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := needsConsolidation(tc.members); got != tc.want {
				t.Fatalf("needsConsolidation = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestGrillingListAnsweredDetection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Grilling-Decisions.md")

	writeDecisionList(t, path, false)
	if grillingListAnswered(path) {
		t.Fatal("unanswered list reported as answered")
	}

	writeDecisionList(t, path, true)
	if !grillingListAnswered(path) {
		t.Fatal("answered list reported as unanswered")
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if grillingListAnswered(path) {
		t.Fatal("missing list reported as answered")
	}
}

func TestProcessGrillingConsolidationDispatchesConsolidate(t *testing.T) {
	dir := t.TempDir()
	withAPIKey(t)
	vault := filepath.Join(dir, "vault")
	tasksDir := filepath.Join(vault, "Projects", "001-test", "Tasks")
	writeGrillingTask(t, filepath.Join(tasksDir, "TASK-012.md"), "012", "Projects/001-test/Requirements/REQ-012.md", "test", false, 1)
	writeGrillingTask(t, filepath.Join(tasksDir, "TASK-074.md"), "074", "Projects/001-test/Requirements/REQ-012.md", "test", false, 1)

	argsPath := filepath.Join(dir, "pm-args")
	omp := writeArgsOMP(t, argsPath)
	runner := &Runner{
		cfg: &config.Config{
			OMPCmd:              omp,
			ObsidianVault:       vault,
			PhaseTimeoutMinutes: map[string]int{"refining": 1},
			Models:              config.DefaultModels(),
		},
		logger: log.New(io.Discard, "", 0),
	}

	processed := runner.processGrillingConsolidation(context.Background())
	if processed != 1 {
		t.Fatalf("processed = %d, want 1", processed)
	}
	data, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("read pm args: %v", err)
	}
	args := string(data)
	if !strings.Contains(args, "/obsidian-task-runner-pm consolidate") {
		t.Fatalf("pm args = %q, want consolidate prompt", args)
	}
	if !strings.Contains(args, "TASK-012") || !strings.Contains(args, "TASK-074") {
		t.Fatalf("pm args = %q, want both task paths", args)
	}
}

func TestProcessGrillingConsolidationDistributesAnsweredList(t *testing.T) {
	dir := t.TempDir()
	withAPIKey(t)
	vault := filepath.Join(dir, "vault")
	tasksDir := filepath.Join(vault, "Projects", "001-test", "Tasks")
	writeGrillingTask(t, filepath.Join(tasksDir, "TASK-025.md"), "025", "Projects/001-test/Requirements/REQ-025.md", "test", true, 3)
	writeDecisionList(t, filepath.Join(vault, "Projects", "001-test", "Notes", "Grilling-Decisions.md"), true)

	argsPath := filepath.Join(dir, "pm-args")
	omp := writeArgsOMP(t, argsPath)
	runner := &Runner{
		cfg: &config.Config{
			OMPCmd:              omp,
			ObsidianVault:       vault,
			PhaseTimeoutMinutes: map[string]int{"refining": 1},
			Models:              config.DefaultModels(),
		},
		logger: log.New(io.Discard, "", 0),
	}

	processed := runner.processGrillingConsolidation(context.Background())
	if processed != 1 {
		t.Fatalf("processed = %d, want 1", processed)
	}
	data, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("read pm args: %v", err)
	}
	args := string(data)
	if !strings.Contains(args, "/obsidian-task-runner-pm distribute") {
		t.Fatalf("pm args = %q, want distribute prompt", args)
	}
	if !strings.Contains(args, "Grilling-Decisions.md") {
		t.Fatalf("pm args = %q, want decision list path", args)
	}
}

func TestProcessGrillingConsolidationSkipsFullyParkedGroup(t *testing.T) {
	dir := t.TempDir()
	withAPIKey(t)
	vault := filepath.Join(dir, "vault")
	tasksDir := filepath.Join(vault, "Projects", "001-test", "Tasks")
	writeGrillingTask(t, filepath.Join(tasksDir, "TASK-012.md"), "012", "Projects/001-test/Requirements/REQ-012.md", "test", true, 2)
	writeGrillingTask(t, filepath.Join(tasksDir, "TASK-074.md"), "074", "Projects/001-test/Requirements/REQ-012.md", "test", true, 2)

	argsPath := filepath.Join(dir, "pm-args")
	omp := writeArgsOMP(t, argsPath)
	runner := &Runner{
		cfg: &config.Config{
			OMPCmd:              omp,
			ObsidianVault:       vault,
			PhaseTimeoutMinutes: map[string]int{"refining": 1},
			Models:              config.DefaultModels(),
		},
		logger: log.New(io.Discard, "", 0),
	}

	processed := runner.processGrillingConsolidation(context.Background())
	if processed != 0 {
		t.Fatalf("processed = %d, want 0 (fully parked group waits for answers)", processed)
	}
	if _, err := os.Stat(argsPath); !os.IsNotExist(err) {
		t.Fatal("PM session must not be dispatched for a fully parked group")
	}
}

func TestParkedTaskIsNotDispatched(t *testing.T) {
	dir := t.TempDir()
	omp, _, _ := writeBarrierOMP(t, dir)

	vault := filepath.Join(dir, "vault")
	tasksDir := filepath.Join(vault, "Projects", "001-test", "Tasks")
	writeGrillingTask(t, filepath.Join(tasksDir, "TASK-025.md"), "025", "Projects/001-test/Requirements/REQ-025.md", "test", true, 2)

	runner := &Runner{
		cfg: &config.Config{
			OMPCmd:             omp,
			ObsidianVault:      vault,
			MaxConcurrentTasks: 2,
			Models:             config.DefaultModels(),
		},
		logger: log.New(io.Discard, "", 0),
	}

	pending := runner.prepareBatch([]task.ReadyTask{{ID: "025", Project: "test", ReqDoc: "Projects/001-test/Requirements/REQ-025.md", FilePath: filepath.Join(tasksDir, "TASK-025.md"), Status: "needs-grilling", GrillParked: true, GrillContinue: false, Assignee: "default"}})
	if len(pending) != 0 {
		t.Fatalf("parked task entered dispatch queue: %d pending", len(pending))
	}
}
