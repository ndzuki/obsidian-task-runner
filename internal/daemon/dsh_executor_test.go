package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeDshBody echoes argv to $FAKE_LOG and exits 0.
const fakeDshBody = `echo "$@" >> "$FAKE_LOG"`

func TestDSHExecutorStartArgs(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "args.log")
	fake := writeFakeExecutable(t, dir, "dsh", fakeDshBody)

	e := newDSHExecutor(fake)
	spec := PhaseSpec{
		Phase:       "round2",
		Model:       "magic/deepseek-v4-pro",
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
	got := dshTaskText("/obsidian-task-runner-priority /vault/REQ.md")
	if !strings.Contains(got, "obsidian-task-runner") {
		t.Errorf("task text missing skill name: %q", got)
	}
	empty := dshTaskText("   ")
	if empty == "" {
		t.Error("empty skill prompt produced empty task text")
	}
}

func TestDSHExecutorResumeUnsupported(t *testing.T) {
	e := newDSHExecutor("dsh")
	if _, err := e.Resume(context.Background(), "tok"); err != ErrResumeUnsupported {
		t.Fatalf("Resume err = %v, want ErrResumeUnsupported", err)
	}
}

func TestDSHExecutorTimeout(t *testing.T) {
	dir := t.TempDir()
	fake := writeFakeExecutable(t, dir, "dsh", `sleep 10`)
	e := newDSHExecutor(fake)
	spec := PhaseSpec{
		Phase:      "refining",
		Model:      "magic/deepseek-v4-pro",
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
