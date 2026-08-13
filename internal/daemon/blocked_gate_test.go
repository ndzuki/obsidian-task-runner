package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

// TestFixBlockedGateErrorCodes guards the empty-error-code backfill: an
// entry-gate block whose round2 write-back lost the error code (TASK-019,
// 8/11 — blocked + implementing + no code + blocked_by, then the resolver
// auto-resumed it into a completed→blocked→resume loop) must be re-stamped
// PREREQUISITE_SMOKE_FAILED so the fact-based recovery branch owns it.
// Legacy phase-failure blocks (empty code, no blocked_by) and tasks that
// already carry a code stay untouched.
func TestFixBlockedGateErrorCodes(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	tasksDir := filepath.Join(vault, "Projects", "001-test", "Tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) string {
		t.Helper()
		path := filepath.Join(tasksDir, name)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	gate := write("TASK-001-gate.md", "---\nid: \"001\"\nstatus: blocked\nblocked_phase: implementing\nphase_error_code: \"\"\nblocked_by: [\"TASK-009\"]\n---\n# G\n")
	legacy := write("TASK-002-legacy.md", "---\nid: \"002\"\nstatus: blocked\nblocked_phase: refining\nphase_error_code: \"\"\n---\n# L\n")
	coded := write("TASK-003-coded.md", "---\nid: \"003\"\nstatus: blocked\nblocked_phase: implementing\nphase_error_code: MODEL_FAILED\nblocked_by: [\"TASK-009\"]\n---\n# C\n")
	prereq := write("TASK-004-prereq.md", "---\nid: \"004\"\nstatus: blocked\nblocked_phase: implementing\nphase_error_code: PREREQUISITE_SMOKE_FAILED\nblocked_by: [\"TASK-009\"]\n---\n# P\n")

	runner := healthRunner(t, vault)
	runner.fixBlockedGateErrorCodes()

	if got := codeOf(t, gate); got != "PREREQUISITE_SMOKE_FAILED" {
		t.Fatalf("gate task code = %q, want PREREQUISITE_SMOKE_FAILED", got)
	}
	if got := codeOf(t, legacy); got != "" {
		t.Fatalf("legacy block code = %q, want unchanged empty", got)
	}
	if got := codeOf(t, coded); got != "MODEL_FAILED" {
		t.Fatalf("coded task code = %q, want unchanged MODEL_FAILED", got)
	}
	if got := codeOf(t, prereq); got != "PREREQUISITE_SMOKE_FAILED" {
		t.Fatalf("prereq task code = %q, want unchanged", got)
	}
}

// TestAutoResumeSkipsEmptyCodeGate guards the resolver side: an upstream
// blocked with an empty error code AND a non-empty blocked_by is an entry
// gate (its own dependencies must converge first), so the generic
// upstream-unblock path must NOT approve it — that was the TASK-019 loop
// (resolver kept re-resuming the gate while PR #51 was still open). Legacy
// phase-failure blocks without blocked_by keep auto-resume.
func TestAutoResumeSkipsEmptyCodeGate(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	projDir := filepath.Join(vault, "Projects", "001-test")
	tasksDir := filepath.Join(projDir, "Tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Downstream referencing the gated upstream.
	if err := os.WriteFile(filepath.Join(tasksDir, "TASK-001-down.md"),
		[]byte("---\nid: \"001\"\nstatus: refining\nblocked_by: [\"TASK-002\"]\n---\n# D\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Gated upstream: empty code + own blocked_by (entry gate by shape).
	if err := os.WriteFile(filepath.Join(tasksDir, "TASK-002-gate.md"),
		[]byte("---\nid: \"002\"\nstatus: blocked\nblocked_phase: implementing\nphase_error_code: \"\"\nblocked_by: [\"TASK-009\"]\n---\n# G\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Legacy upstream: empty code, no blocked_by (auto-resumable).
	if err := os.WriteFile(filepath.Join(tasksDir, "TASK-003-legacy.md"),
		[]byte("---\nid: \"003\"\nstatus: blocked\nblocked_phase: refining\nphase_error_code: \"\"\n---\n# L\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	runner := healthRunner(t, vault)
	runner.autoResumeInProject(projDir, projDir, "001", "", "002")
	fm := mustParse(t, filepath.Join(tasksDir, "TASK-002-gate.md"))
	if fm.ResumeApproved {
		t.Fatal("gated upstream was auto-resumed, want false")
	}
	runner.autoResumeInProject(projDir, projDir, "001", "", "003")
	fm = mustParse(t, filepath.Join(tasksDir, "TASK-003-legacy.md"))
	if !fm.ResumeApproved {
		t.Fatal("legacy upstream not auto-resumed, want true")
	}
}

func codeOf(t *testing.T, path string) string {
	t.Helper()
	return mustParse(t, path).PhaseErrorCode
}
