package daemon

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/ndzuki/obsidian-task-runner/internal/config"
	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

// TestAutoResumeAgedBlocks guards the 24h fallback: a blocked task carrying a
// transient, auto-recoverable error re-arms itself after the window, while
// fresh blocks, entry gates, exhausted budgets, and already-approved resumes
// stay untouched.
func TestAutoResumeAgedBlocks(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	tasksDir := filepath.Join(vault, "Projects", "001-test", "Tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}

	writeBlocked := func(id, code, blockedAt, updated string, count int, resume bool) string {
		t.Helper()
		if blockedAt != "" {
			blockedAt = "blocked_at: " + blockedAt + "\n"
		}
		if updated != "" {
			updated = "updated: " + updated + "\n"
		}
		content := "---\nid: \"" + id + "\"\ntitle: T" + id + "\nproject: test\nassignee: default\nstatus: blocked\nblocked_phase: implementing\nphase_error_code: " + code + "\n" +
			blockedAt + updated +
			"auto_resume_count: " + strconv.Itoa(count) + "\nauto_resume_pending: false\nresume_approved: " + strconv.FormatBool(resume) + "\nblocked_by: []\n---\n# T\n"
		path := filepath.Join(tasksDir, "TASK-"+id+"-t.md")
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	aged := time.Now().Add(-25 * time.Hour).Format(time.RFC3339)
	fresh := time.Now().Add(-time.Hour).Format(time.RFC3339)

	// Aged transient errors → resumed (with and without blocked_at stamp).
	agedTimeout := writeBlocked("001", "PHASE_TIMEOUT", aged, "", 0, false)
	agedTimeoutNoStamp := writeBlocked("002", "MODEL_FAILED", "", aged, 0, false)
	agedDesign := writeBlocked("003", "DESIGN_SESSION_FAILED", aged, "", 0, false)
	// Excluded shapes → untouched.
	freshTimeout := writeBlocked("004", "PHASE_TIMEOUT", fresh, "", 0, false)
	freshNoStamp := writeBlocked("005", "MODEL_FAILED", "", fresh, 0, false)
	gate := writeBlocked("006", "PREREQUISITE_SMOKE_FAILED", aged, "", 0, false)
	budget := writeBlocked("007", "PHASE_TIMEOUT", aged, "", maxAutoResumeAttempts, false)
	already := writeBlocked("008", "PHASE_TIMEOUT", aged, "", 0, true)
	human := writeBlocked("009", "REQ_MISSING", aged, "", 0, false)

	runner := New(&config.Config{ObsidianVault: vault})
	runner.logger = log.New(io.Discard, "", 0)
	runner.autoResumeAgedBlocks()

	resumed := func(path string) bool {
		t.Helper()
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		fm, err := yamlfrontmatter.Parse(raw)
		if err != nil || fm == nil {
			t.Fatalf("parse: %v", err)
		}
		return fm.ResumeApproved && fm.AutoResumePending
	}

	if !resumed(agedTimeout) {
		t.Fatal("aged PHASE_TIMEOUT must auto-resume")
	}
	if !resumed(agedTimeoutNoStamp) {
		t.Fatal("aged MODEL_FAILED without blocked_at must fall back to updated")
	}
	if !resumed(agedDesign) {
		t.Fatal("aged DESIGN_SESSION_FAILED must auto-resume")
	}
	for name, path := range map[string]string{
		"fresh PHASE_TIMEOUT":         freshTimeout,
		"fresh MODEL_FAILED no stamp": freshNoStamp,
		"entry gate":                  gate,
		"budget exhausted":            budget,
		"already approved":            already,
		"human-decision block":        human,
	} {
		if resumed(path) {
			t.Fatalf("%s must stay blocked: %s", name, path)
		}
	}
}

// TestHandlePhaseFailureStampsBlockedAt guards the write side: transitioning
// into blocked records blocked_at, and restoreBlockedPhase clears it.
func TestHandlePhaseFailureStampsBlockedAt(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	tasksDir := filepath.Join(vault, "Projects", "001-test", "Tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(tasksDir, "TASK-001-t.md")
	if err := os.WriteFile(path, []byte("---\nid: \"001\"\ntitle: T\nproject: test\nassignee: default\nstatus: implementing\n---\n# T\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	runner := New(&config.Config{ObsidianVault: vault})
	runner.logger = log.New(io.Discard, "", 0)
	runner.handlePhaseFailure(path, "001", "T", "implementing", "round2", ErrPhaseTimeout, "超时", "")

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	fm, err := yamlfrontmatter.Parse(raw)
	if err != nil || fm == nil {
		t.Fatalf("parse: %v", err)
	}
	if fm.Status != "blocked" || fm.BlockedAt == "" {
		t.Fatalf("block must stamp blocked_at: status=%s blocked_at=%q", fm.Status, fm.BlockedAt)
	}
	if _, err := time.Parse(time.RFC3339, fm.BlockedAt); err != nil {
		t.Fatalf("blocked_at must be RFC3339: %v", err)
	}

	if err := runner.restoreBlockedPhase(path, "implementing", true); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(path)
	fm, _ = yamlfrontmatter.Parse(raw)
	if fm.BlockedAt != "" || fm.Status != "implementing" {
		t.Fatalf("restore must clear blocked_at and restore status: status=%s blocked_at=%q", fm.Status, fm.BlockedAt)
	}
}

// TestAutoResumeAgedBlocksCustomWindow guards the configurable window: with
// auto_resume_aged_after_hours=2 a task blocked 3h resumes while one blocked
// 90min stays blocked.
func TestAutoResumeAgedBlocksCustomWindow(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	tasksDir := filepath.Join(vault, "Projects", "001-test", "Tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(id, blockedAt string) string {
		t.Helper()
		content := "---\nid: \"" + id + "\"\ntitle: T" + id + "\nproject: test\nassignee: default\nstatus: blocked\nblocked_phase: implementing\nphase_error_code: PHASE_TIMEOUT\nblocked_at: " + blockedAt + "\nauto_resume_count: 0\nauto_resume_pending: false\nresume_approved: false\nblocked_by: []\n---\n# T\n"
		path := filepath.Join(tasksDir, "TASK-"+id+"-t.md")
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	aged := write("001", time.Now().Add(-3*time.Hour).Format(time.RFC3339))
	recent := write("002", time.Now().Add(-90*time.Minute).Format(time.RFC3339))

	runner := New(&config.Config{ObsidianVault: vault, AutoResumeAgedAfterHours: 2})
	runner.logger = log.New(io.Discard, "", 0)
	runner.autoResumeAgedBlocks()

	raw, err := os.ReadFile(aged)
	if err != nil {
		t.Fatal(err)
	}
	fm, err := yamlfrontmatter.Parse(raw)
	if err != nil || fm == nil {
		t.Fatalf("parse: %v", err)
	}
	if !fm.ResumeApproved {
		t.Fatal("task blocked 3h must resume under a 2h window")
	}
	raw, err = os.ReadFile(recent)
	if err != nil {
		t.Fatal(err)
	}
	fm, err = yamlfrontmatter.Parse(raw)
	if err != nil || fm == nil {
		t.Fatalf("parse: %v", err)
	}
	if fm.ResumeApproved {
		t.Fatal("task blocked 90min must stay blocked under a 2h window")
	}
}
