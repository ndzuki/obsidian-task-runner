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
