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

// fakeOmpBody echoes argv to $FAKE_LOG and exits 0.
const fakeOmpBody = `echo "$@" >> "$FAKE_LOG"`

func TestOMPExecutorStartArgs(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "args.log")
	fake := writeFakeExecutable(t, dir, "omp", fakeOmpBody)

	e := newOMPExecutor(fake)
	spec := PhaseSpec{
		Phase:           "round2",
		Model:           "gateway/gpt-5.4-mini",
		ReasoningEffort: "", // should map via ompPhaseThinking → "max"
		SkillPrompt:     "/obsidian-task-runner-round2 /vault/Tasks/TASK-001.md",
		Timeout:         30 * time.Second,
		WorkingDir:      dir,
		ExtraEnv:        []string{"FAKE_LOG=" + log},
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
	for _, want := range []string{
		"--model", "gateway/gpt-5.4-mini",
		"--auto-approve",
		"-p", "/obsidian-task-runner-round2 /vault/Tasks/TASK-001.md",
		"--thinking", "max",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("args missing %q in %q", want, got)
		}
	}
}

func TestOMPExecutorPhaseThinkingMapping(t *testing.T) {
	cases := map[string]string{
		"priority": "off",
		"round2":   "max",
		"planning": "high",
		"refining": "low",
		"merge":    "low",
	}
	for phase, want := range cases {
		if got := ompPhaseThinking(phase); got != want {
			t.Errorf("ompPhaseThinking(%q) = %q, want %q", phase, got, want)
		}
	}
}

func TestOMPExecutorToolPolicy(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "args.log")
	fake := writeFakeExecutable(t, dir, "omp", fakeOmpBody)

	e := newOMPExecutor(fake)
	spec := PhaseSpec{
		Phase:       "audit",
		Model:       "gateway/gpt-5.4-mini",
		ToolPolicy:  "read,grep,bash",
		SkillPrompt: "/obsidian-task-runner-merge /vault/Tasks/TASK-077.md",
		Timeout:     15 * time.Second,
		WorkingDir:  dir,
		ExtraEnv:    []string{"FAKE_LOG=" + log},
	}
	h, err := e.Start(context.Background(), spec, TaskSnapshot{})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := h.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	data, _ := os.ReadFile(log)
	if !strings.Contains(string(data), "--tools read,grep,bash") {
		t.Errorf("audit tool policy not passed: %q", string(data))
	}
}

func TestOMPExecutorResumeUnsupported(t *testing.T) {
	e := newOMPExecutor("omp")
	if _, err := e.Resume(context.Background(), "tok"); err != ErrResumeUnsupported {
		t.Fatalf("Resume err = %v, want ErrResumeUnsupported", err)
	}
}

func TestOMPExecutorTimeout(t *testing.T) {
	dir := t.TempDir()
	// sleep 10s but timeout 200ms → must settle as timed out quickly.
	fake := writeFakeExecutable(t, dir, "omp", `sleep 10`)
	e := newOMPExecutor(fake)
	spec := PhaseSpec{
		Phase:      "refining",
		Model:      "m",
		Timeout:    200 * time.Millisecond,
		WorkingDir: dir,
	}
	h, err := e.Start(context.Background(), spec, TaskSnapshot{})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	done := make(chan *ExecutionResult, 1)
	errCh := make(chan error, 1)
	go func() {
		r, err := h.Wait()
		if err != nil {
			errCh <- err
			return
		}
		done <- r
	}()
	select {
	case r := <-done:
		if r.Code != OutcomeTimedOut {
			t.Fatalf("Code = %q, want timed_out", r.Code)
		}
	case err := <-errCh:
		t.Fatalf("Wait err: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timeout test hung")
	}
}
