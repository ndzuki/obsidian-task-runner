package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeFakeExecutable writes an executable script that echoes its args to a
// log file and exits with the given code. Used to verify adapter contract
// without spawning a real agent.
func writeFakeExecutable(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatalf("write fake %s: %v", name, err)
	}
	return p
}

// fakeDshBody echoes argv to $FAKE_LOG and exits 0.
const fakeDshBody = `echo "$@" >> "$FAKE_LOG"`

func TestDSHExecutorStartArgs(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "args.log")
	fake := writeFakeExecutable(t, dir, "dsh", fakeDshBody)

	e := newDSHExecutor(fake)
	spec := PhaseSpec{
		Phase:       "round2",
		Model:       "magic/acme-pro",
		SkillPrompt: "/obsidian-task-runner-round2 /vault/Tasks/TASK-001.md",
		Timeout:     30 * time.Second,
		WorkingDir:  dir,
		ExtraEnv:    []string{"FAKE_LOG=" + log},
	}
	h, err := e.Start(context.Background(), spec, TaskSnapshot{TaskID: "001"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if h.PID() == 0 {
		t.Fatal("PID() = 0, want non-zero")
	}
	res, err := h.Wait()
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if res.Code != OutcomeSuccess {
		t.Fatalf("Code = %q, want success", res.Code)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("read args log: %v", err)
	}
	got := string(data)
	for _, want := range []string{"--profile", "headless", "/obsidian-task-runner-round2"} {
		if !strings.Contains(got, want) {
			t.Errorf("args missing %q in %q", want, got)
		}
	}
}

func TestDSHTaskText(t *testing.T) {
	// 空 skillDir：走 slash 兜底（保留 skill 名），不注入正文。
	e := newDSHExecutorWithProfile("dsh", "headless", "")
	got := e.dshTaskText("/obsidian-task-runner-priority /vault/REQ.md")
	if !strings.Contains(got, "obsidian-task-runner") {
		t.Errorf("task text missing skill name: %q", got)
	}
	empty := e.dshTaskText("   ")
	if empty == "" {
		t.Error("empty skill prompt produced empty task text")
	}
}

func TestDSHTaskTextInjectsSkillBody(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "obsidian-task-runner-round1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "obsidian-task-runner-round1", "SKILL.md"), []byte("---\nname: obsidian-task-runner-round1\ndescription: x\n---\n\nStep 1: read TASK\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e := newDSHExecutorWithProfile("dsh", "headless", dir)
	got := e.dshTaskText("/obsidian-task-runner-round1 /vault/TASK-001.md")
	if !strings.Contains(got, "Step 1: read TASK") {
		t.Fatalf("task text missing injected skill body: %q", got)
	}
	if !strings.Contains(got, "/vault/TASK-001.md") {
		t.Fatalf("task text missing args: %q", got)
	}
	// 完整模板（非 slash）不注入，走兜底。
	audit := e.dshTaskText("你是独立审计员……输出 JSON")
	if !strings.Contains(audit, "你是独立审计员") {
		t.Fatalf("full-template prompt must fall through: %q", audit)
	}
}

func TestDSHExecutorResumeUnsupported(t *testing.T) {
	e := newDSHExecutor("dsh")
	if _, err := e.Resume(context.Background(), PhaseSpec{}, "tok", 0); err != ErrResumeUnsupported {
		t.Fatalf("Resume err = %v, want ErrResumeUnsupported", err)
	}
}

func TestDSHExecutorTimeout(t *testing.T) {
	dir := t.TempDir()
	fake := writeFakeExecutable(t, dir, "dsh", `sleep 10`)
	e := newDSHExecutor(fake)
	spec := PhaseSpec{
		Phase:      "refining",
		Model:      "magic/acme-pro",
		Timeout:    200 * time.Millisecond,
		WorkingDir: dir,
	}
	h, err := e.Start(context.Background(), spec, TaskSnapshot{})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	done := make(chan *ExecutionResult, 1)
	go func() {
		r, _ := h.Wait()
		done <- r
	}()
	select {
	case r := <-done:
		if r.Code != OutcomeTimedOut {
			t.Fatalf("Code = %q, want timed_out", r.Code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout test hung")
	}
}
