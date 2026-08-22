package daemon

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ndzuki/obsidian-task-runner/internal/config"
	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

// TestRound2StallCooldown guards the exponential no-progress cooldown used
// to stop entry-gate re-verification rounds from re-dispatching every scan
// (TASK-071: 20+ identical gate-check LLM sessions per day).
func TestRound2StallCooldown(t *testing.T) {
	if got := round2StallCooldown(0); got != 10*time.Minute {
		t.Fatalf("level 0 cooldown = %v, want 10m", got)
	}
	if got := round2StallCooldown(3); got != 80*time.Minute {
		t.Fatalf("level 3 cooldown = %v, want 80m", got)
	}
	if got := round2StallCooldown(6); got != 640*time.Minute {
		t.Fatalf("level 6 cooldown = %v, want 640m", got)
	}
	// Ceiling: beyond max level stays capped.
	if got := round2StallCooldown(9); got != round2StallCooldown(6) {
		t.Fatalf("level 9 cooldown = %v, want capped at %v", got, round2StallCooldown(6))
	}
	// Negative levels clamp to base.
	if got := round2StallCooldown(-1); got != 10*time.Minute {
		t.Fatalf("negative level cooldown = %v, want 10m", got)
	}
}

// TestRecordRound2Completion guards the stall lifecycle: a no-progress
// completion (still implementing, no checkpoint) raises the level and
// persists the deadline to frontmatter, a progressed completion (checkpoint
// written or status changed) resets both.
func TestRecordRound2Completion(t *testing.T) {
	dir := t.TempDir()
	taskPath := filepath.Join(dir, "TASK-071.md")
	writeTask := func(status, checkpoint string) {
		t.Helper()
		content := "---\nid: \"071\"\ntitle: T\nstatus: " + status + "\ncheckpoint_commit: \"" + checkpoint + "\"\n---\n"
		if err := os.WriteFile(taskPath, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	readStallUntil := func() string {
		t.Helper()
		data, err := os.ReadFile(taskPath)
		if err != nil {
			t.Fatal(err)
		}
		fm, err := yamlfrontmatter.Parse(data)
		if err != nil || fm == nil {
			t.Fatal("parse task after record")
		}
		return fm.Round2StallUntil
	}

	runner := New(&config.Config{})
	runner.logger = log.New(io.Discard, "", 0)

	// First no-progress completion → level 0, deadline persisted.
	writeTask("implementing", "")
	runner.recordRound2Completion(taskPath, "071")
	if got, ok := runner.round2Stalls.Load(taskPath); !ok {
		t.Fatal("no stall recorded after no-progress completion")
	} else if got.(round2Stall).level != 0 {
		t.Fatalf("first no-progress level = %d, want 0", got.(round2Stall).level)
	}
	if until := readStallUntil(); until == "" {
		t.Fatal("round2_stall_until not persisted")
	} else if dl, err := time.Parse(time.RFC3339, until); err != nil || time.Until(dl) > 11*time.Minute {
		t.Fatalf("persisted deadline %q not ~10m ahead: %v", until, err)
	}

	// Second no-progress completion → level 1 (cooldown doubles).
	writeTask("implementing", "")
	runner.recordRound2Completion(taskPath, "071")
	s, _ := runner.round2Stalls.Load(taskPath)
	if s.(round2Stall).level != 1 {
		t.Fatalf("second no-progress level = %d, want 1", s.(round2Stall).level)
	}

	// Restart simulation: fresh runner (empty in-memory map) must still see
	// the persisted deadline as active.
	fresh := New(&config.Config{})
	fresh.logger = log.New(io.Discard, "", 0)
	if _, ok := fresh.round2StallActive(taskPath); !ok {
		t.Fatal("persisted stall deadline not honored after restart")
	}

	// Progress: checkpoint written → stall reset (memory + frontmatter).
	writeTask("implementing", "abc123")
	runner.recordRound2Completion(taskPath, "071")
	if _, ok := runner.round2Stalls.Load(taskPath); ok {
		t.Fatal("stall not reset after checkpoint progress")
	}
	if until := readStallUntil(); until != "" {
		t.Fatalf("round2_stall_until not cleared after progress: %q", until)
	}

	// Progress: status left implementing → stall reset.
	writeTask("review", "")
	runner.recordRound2Completion(taskPath, "071")
	if _, ok := runner.round2Stalls.Load(taskPath); ok {
		t.Fatal("stall not reset after status change")
	}
}

// TestRecordRound2CompletionCapsAtBlockLevel 守护无进展熔断：连续 3 轮
// no-progress round2（level 0,1,2）后不再派发会话——任务转 blocked +
// PREREQUISITE_SMOKE_FAILED 门禁态，等待 blocked_by 事实恢复（观测：
// TASK-058 同一 gate FAIL 报告空转 8+ 轮，每轮一次全量 LLM 会话）。
func TestRecordRound2CompletionCapsAtBlockLevel(t *testing.T) {
	dir := t.TempDir()
	taskPath := filepath.Join(dir, "TASK-058.md")
	writeTask := func(status, checkpoint string) {
		t.Helper()
		content := "---\nid: \"058\"\ntitle: T\nstatus: " + status + "\ncheckpoint_commit: \"" + checkpoint + "\"\nblocked_by:\n  - \"079\"\n---\n"
		if err := os.WriteFile(taskPath, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	runner := New(&config.Config{})
	runner.logger = log.New(io.Discard, "", 0)

	// 第 1 轮无进展 → level 0（10m 冷却），仍在 implementing。
	writeTask("implementing", "")
	runner.recordRound2Completion(taskPath, "058")
	if s, ok := runner.round2Stalls.Load(taskPath); !ok || s.(round2Stall).level != 0 {
		t.Fatalf("round 1: stall = %v ok=%v, want level 0", s, ok)
	}
	// 第 2 轮 → level 1。
	writeTask("implementing", "")
	runner.recordRound2Completion(taskPath, "058")
	if s, _ := runner.round2Stalls.Load(taskPath); s.(round2Stall).level != 1 {
		t.Fatalf("round 2: level = %d, want 1", s.(round2Stall).level)
	}
	// 第 3 轮 → level 2 ≥ block level：熔断转 blocked，不再排冷却。
	writeTask("implementing", "")
	runner.recordRound2Completion(taskPath, "058")
	if _, ok := runner.round2Stalls.Load(taskPath); ok {
		t.Fatal("熔断后不应再有内存 stall 状态")
	}
	data, err := os.ReadFile(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	fm, err := yamlfrontmatter.Parse(data)
	if err != nil || fm == nil {
		t.Fatal("parse after cap")
	}
	if fm.Status != "blocked" || fm.BlockedPhase != "implementing" {
		t.Fatalf("status/blocked_phase = %q/%q, want blocked/implementing", fm.Status, fm.BlockedPhase)
	}
	if fm.PhaseErrorCode != "PREREQUISITE_SMOKE_FAILED" {
		t.Fatalf("phase_error_code = %q, want PREREQUISITE_SMOKE_FAILED", fm.PhaseErrorCode)
	}
	if fm.Round2StallUntil != "" || fm.Round2StallLevel != 0 {
		t.Fatalf("stall fields not reset: until=%q level=%d", fm.Round2StallUntil, fm.Round2StallLevel)
	}
}

// TestRecordRound2CompletionCapRestartSafe：重启后 level 从 frontmatter
// 恢复——已 2 轮无进展的任务重启后第 3 轮仍触发熔断（不会因重启回到 10m
// 冷却无限空转）。
func TestRecordRound2CompletionCapRestartSafe(t *testing.T) {
	dir := t.TempDir()
	taskPath := filepath.Join(dir, "TASK-058.md")
	content := "---\nid: \"058\"\ntitle: T\nstatus: implementing\ncheckpoint_commit: \"\"\nblocked_by:\n  - \"079\"\nround2_stall_until: \"2030-01-01T00:00:00+08:00\"\nround2_stall_level: 1\n---\n"
	if err := os.WriteFile(taskPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	// 模拟重启：内存空，但 frontmatter 里有上一轮的 level=1。
	runner := New(&config.Config{})
	runner.logger = log.New(io.Discard, "", 0)
	runner.recordRound2Completion(taskPath, "058")
	data, _ := os.ReadFile(taskPath)
	fm, err := yamlfrontmatter.Parse(data)
	if err != nil || fm == nil {
		t.Fatal("parse after cap")
	}
	if fm.Status != "blocked" || fm.PhaseErrorCode != "PREREQUISITE_SMOKE_FAILED" {
		t.Fatalf("重启后应仍触发熔断: status=%q code=%q", fm.Status, fm.PhaseErrorCode)
	}
}
