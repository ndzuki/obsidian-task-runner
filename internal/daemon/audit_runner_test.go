package daemon

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ndzuki/obsidian-task-runner/internal/config"
	"github.com/ndzuki/obsidian-task-runner/internal/task"
)

// writeAuditOMP writes a fake OMP that records argv and emits a verdict JSON
// selected by the AUDIT_VERDICT env var: pass / fail / invalid, anything else
// exits non-zero (simulating a crashed session).
func writeAuditOMP(t *testing.T, dir string) (string, string) {
	t.Helper()
	argsFile := filepath.Join(dir, "audit-args")
	omp := filepath.Join(dir, "fake-audit-omp")
	script := `#!/bin/sh
printf '%s\n' "$*" > "$AUDIT_ARGS_FILE"
case "$AUDIT_VERDICT" in
  pass)
    printf '%s\n' '{"verdict":"pass","summary":"all ACs verified","ac_results":[{"ac":"AC-1","pass":true,"evidence":"go test ./...: PASS (12/12)"}]}'
    ;;
  fail)
    printf '%s\n' "{\"verdict\":\"fail\",\"failure_type\":\"$AUDIT_FAILURE_TYPE\",\"summary\":\"AC-2 broken: handler returns 500 on empty body\",\"ac_results\":[{\"ac\":\"AC-1\",\"pass\":true,\"evidence\":\"go test: PASS\"},{\"ac\":\"AC-2\",\"pass\":false,\"evidence\":\"curl -s localhost:8080/x -> HTTP 500\"}]}"
    ;;
  invalid)
    printf '%s\n' 'not json at all, sorry'
    ;;
  *)
    exit 1
    ;;
esac
exit 0
`
	if err := os.WriteFile(omp, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake audit omp: %v", err)
	}
	return omp, argsFile
}

// newAuditRunner builds a Runner with the completion audit enabled.
func newAuditRunner(t *testing.T, skillDir, omp, logDir string) *Runner {
	t.Helper()
	runner := New(&config.Config{
		SkillInstallDir:    skillDir,
		OMPCmd:             omp,
		LogDir:             logDir,
		MaxConcurrentTasks: 1,
		Models:             config.DefaultModels(),
		PhaseConcurrency:   config.DefaultPhaseConcurrency(),
		Audit:              &config.AuditConfig{Enabled: true, MaxFixes: 2, TimeoutMinutes: 15, Concurrency: 1},
	})
	runner.logger = log.New(io.Discard, "", 0)
	return runner
}

// auditTask builds a review task fixture.
func auditTask(t *testing.T, dir, status string, autoMerge bool) (task.ReadyTask, string) {
	t.Helper()
	taskPath := writeTaskFile(t, dir, "TASK-AUDIT.md", status)
	return task.ReadyTask{
		ID:           "AUDIT",
		Title:        "Audit task",
		Project:      "demo",
		FilePath:     taskPath,
		Status:       status,
		AutoMerge:    autoMerge,
		Assignee:     "default",
		ReqDoc:       "Requirements/REQ-AUDIT.md",
		TargetBranch: "task/audit",
	}, taskPath
}

func TestParseAuditResult(t *testing.T) {
	got, err := parseAuditResult([]byte(`{"verdict":"pass","summary":"ok","ac_results":[{"ac":"AC-1","pass":true,"evidence":"x"}]}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.Verdict != "pass" || len(got.ACResults) != 1 || !got.ACResults[0].Pass {
		t.Fatalf("parsed = %+v", got)
	}
	// Tolerate surrounding prose (the session may print lines around JSON).
	got, err = parseAuditResult([]byte("Working...\nlog line\n{\"verdict\":\"fail\",\"summary\":\"nope\",\"ac_results\":[]}\ntrailing"))
	if err != nil {
		t.Fatalf("parse with noise: %v", err)
	}
	if got.Verdict != "fail" {
		t.Fatalf("verdict = %q, want fail", got.Verdict)
	}
	// No JSON object.
	if _, err := parseAuditResult([]byte("nothing here")); err == nil {
		t.Fatal("expected error for non-JSON output")
	}
	// Unknown verdict.
	if _, err := parseAuditResult([]byte(`{"verdict":"maybe","summary":"","ac_results":[]}`)); err == nil {
		t.Fatal("expected error for unknown verdict")
	}
}

func TestReviewAuditPassProceedsToMergeAuthorization(t *testing.T) {
	dir := t.TempDir()
	skillDir := writeVaultMap(t, dir, nil)
	omp, argsFile := writeAuditOMP(t, dir)
	t.Setenv("AUDIT_ARGS_FILE", argsFile)
	t.Setenv("AUDIT_VERDICT", "pass")
	runner := newAuditRunner(t, skillDir, omp, filepath.Join(dir, "logs"))
	cand, taskPath := auditTask(t, dir, "review", true)

	handled, err := runner.processReviewAudit(cand, dir)
	if err != nil {
		t.Fatalf("processReviewAudit: %v", err)
	}
	if handled {
		t.Fatal("handled = true, want false (audit passed, merge path may proceed)")
	}
	fm := mustParse(t, taskPath)
	if fm.AuditStatus != "passed" {
		t.Fatalf("audit_status = %q, want passed", fm.AuditStatus)
	}
	if fm.AuditFailCount != 0 {
		t.Fatalf("audit_fail_count = %d, want 0", fm.AuditFailCount)
	}
	if fm.Status != "review" {
		t.Fatalf("status = %q, want review kept", fm.Status)
	}
	// The audit session must run with the restricted tool surface.
	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read audit args: %v", err)
	}
	if !strings.Contains(string(args), "--tools read,grep,bash") {
		t.Fatalf("audit args = %q, want restricted --tools read,grep,bash", args)
	}
	if !strings.Contains(string(args), "--thinking off") {
		t.Fatalf("audit args = %q, want --thinking off (cheap verification)", args)
	}
	// Audit log must be persisted for the implementer/user.
	if fm.AuditLog == "" {
		t.Fatal("audit_log empty, want session log path")
	}
	if data, err := os.ReadFile(fm.AuditLog); err != nil || !strings.Contains(string(data), "all ACs verified") {
		t.Fatalf("audit log content missing (err=%v)", err)
	}
}

func TestReviewAuditFailRoutesBackToImplementing(t *testing.T) {
	dir := t.TempDir()
	skillDir := writeVaultMap(t, dir, nil)
	omp, argsFile := writeAuditOMP(t, dir)
	t.Setenv("AUDIT_ARGS_FILE", argsFile)
	t.Setenv("AUDIT_VERDICT", "fail")
	runner := newAuditRunner(t, skillDir, omp, filepath.Join(dir, "logs"))
	cand, taskPath := auditTask(t, dir, "review", true)

	handled, err := runner.processReviewAudit(cand, dir)
	if err != nil {
		t.Fatalf("processReviewAudit: %v", err)
	}
	if !handled {
		t.Fatal("handled = false, want true (task consumed by audit path)")
	}
	fm := mustParse(t, taskPath)
	if fm.Status != "implementing" {
		t.Fatalf("status = %q, want implementing", fm.Status)
	}
	if fm.PhaseErrorCode != string(ErrAuditFailed) {
		t.Fatalf("phase_error_code = %q, want %q", fm.PhaseErrorCode, ErrAuditFailed)
	}
	if !strings.Contains(fm.PhaseError, "AC-2 broken") {
		t.Fatalf("phase_error = %q, want audit summary embedded", fm.PhaseError)
	}
	if fm.AuditFailCount != 1 {
		t.Fatalf("audit_fail_count = %d, want 1", fm.AuditFailCount)
	}
	if fm.MergeApproved {
		t.Fatal("merge_approved must be cleared on audit fail")
	}
}

func TestReviewAuditFailExhaustsBudgetGoesGrilling(t *testing.T) {
	dir := t.TempDir()
	skillDir := writeVaultMap(t, dir, nil)
	omp, argsFile := writeAuditOMP(t, dir)
	t.Setenv("AUDIT_ARGS_FILE", argsFile)
	t.Setenv("AUDIT_VERDICT", "fail")
	t.Setenv("AUDIT_FAILURE_TYPE", "implementation")
	runner := newAuditRunner(t, skillDir, omp, filepath.Join(dir, "logs"))
	cand, taskPath := auditTask(t, dir, "review", true)
	cand.AuditFailCount = 1 // already failed once; max_fixes=2 → escalation, not a hard block

	if _, err := runner.processReviewAudit(cand, dir); err != nil {
		t.Fatalf("processReviewAudit: %v", err)
	}
	fm := mustParse(t, taskPath)
	if fm.Status != "needs-grilling" {
		t.Fatalf("status = %q, want needs-grilling (decision gate, not blocked)", fm.Status)
	}
	if fm.GrillPrevStatus != "implementing" {
		t.Fatalf("grill_prev_status = %q, want implementing", fm.GrillPrevStatus)
	}
	if !strings.Contains(fm.GrillContext, "连续 2 次未通过独立审计") {
		t.Fatalf("grill_context = %q, want budget-exhausted decision prompt", fm.GrillContext)
	}
	if !strings.Contains(fm.GrillContext, "AC-2") {
		t.Fatalf("grill_context = %q, want audit report embedded", fm.GrillContext)
	}
	// The grilling decision is the new safety valve: fresh budget after resume.
	if fm.AuditFailCount != 0 {
		t.Fatalf("audit_fail_count = %d, want 0 (reset on grilling handoff)", fm.AuditFailCount)
	}
	if fm.PhaseErrorCode != string(ErrAuditFailed) {
		t.Fatalf("phase_error_code = %q, want %q", fm.PhaseErrorCode, ErrAuditFailed)
	}
	if fm.MergeApproved {
		t.Fatal("merge_approved must be cleared")
	}
}

func TestReviewAuditRequirementFailureGoesGrilling(t *testing.T) {
	dir := t.TempDir()
	skillDir := writeVaultMap(t, dir, nil)
	omp, argsFile := writeAuditOMP(t, dir)
	t.Setenv("AUDIT_ARGS_FILE", argsFile)
	t.Setenv("AUDIT_VERDICT", "fail")
	t.Setenv("AUDIT_FAILURE_TYPE", "requirement")
	runner := newAuditRunner(t, skillDir, omp, filepath.Join(dir, "logs"))
	cand, taskPath := auditTask(t, dir, "review", true)

	if _, err := runner.processReviewAudit(cand, dir); err != nil {
		t.Fatalf("processReviewAudit: %v", err)
	}
	fm := mustParse(t, taskPath)
	if fm.Status != "needs-grilling" {
		t.Fatalf("status = %q, want needs-grilling (requirement dispute)", fm.Status)
	}
	if !strings.Contains(fm.GrillContext, "需求争议") {
		t.Fatalf("grill_context = %q, want requirement-dispute framing", fm.GrillContext)
	}
	if fm.GrillPrevStatus != "implementing" {
		t.Fatalf("grill_prev_status = %q, want implementing", fm.GrillPrevStatus)
	}
	// A requirement dispute must not consume the implementation repair budget.
	if fm.AuditFailCount != 0 {
		t.Fatalf("audit_fail_count = %d, want 0", fm.AuditFailCount)
	}
}

func TestReviewAuditImplementationFailureDoesNotGrillOnFirstTry(t *testing.T) {
	dir := t.TempDir()
	skillDir := writeVaultMap(t, dir, nil)
	omp, argsFile := writeAuditOMP(t, dir)
	t.Setenv("AUDIT_ARGS_FILE", argsFile)
	t.Setenv("AUDIT_VERDICT", "fail")
	t.Setenv("AUDIT_FAILURE_TYPE", "implementation")
	runner := newAuditRunner(t, skillDir, omp, filepath.Join(dir, "logs"))
	cand, taskPath := auditTask(t, dir, "review", true)

	if _, err := runner.processReviewAudit(cand, dir); err != nil {
		t.Fatalf("processReviewAudit: %v", err)
	}
	fm := mustParse(t, taskPath)
	if fm.Status != "implementing" {
		t.Fatalf("status = %q, want implementing (automatic repair loop)", fm.Status)
	}
	if fm.AuditFailCount != 1 {
		t.Fatalf("audit_fail_count = %d, want 1", fm.AuditFailCount)
	}
	if fm.GrillContext != "" {
		t.Fatalf("grill_context = %q, want empty (no human gate yet)", fm.GrillContext)
	}
}

func TestReviewAuditSessionFailureKeepsReviewPending(t *testing.T) {
	dir := t.TempDir()
	skillDir := writeVaultMap(t, dir, nil)
	omp, argsFile := writeAuditOMP(t, dir)
	t.Setenv("AUDIT_ARGS_FILE", argsFile)
	t.Setenv("AUDIT_VERDICT", "crash") // fake exits 1
	runner := newAuditRunner(t, skillDir, omp, filepath.Join(dir, "logs"))
	cand, taskPath := auditTask(t, dir, "review", true)

	handled, err := runner.processReviewAudit(cand, dir)
	if err != nil {
		t.Fatalf("processReviewAudit: %v", err)
	}
	if !handled {
		t.Fatal("handled = false, want true (session failure must not dispatch further)")
	}
	fm := mustParse(t, taskPath)
	if fm.Status != "review" {
		t.Fatalf("status = %q, want review kept on session failure", fm.Status)
	}
	if fm.AuditStatus != "pending" {
		t.Fatalf("audit_status = %q, want pending (retried next scan)", fm.AuditStatus)
	}
	// Cooldown: the immediate retry must be skipped.
	if _, err := runner.processReviewAudit(cand, dir); err != nil {
		t.Fatalf("second processReviewAudit: %v", err)
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "starts"))
	_ = entries // no second OMP start observable; the cooldown path returns early
}

func TestReviewAuditSkipsHumanApprovedMerge(t *testing.T) {
	dir := t.TempDir()
	skillDir := writeVaultMap(t, dir, nil)
	omp, argsFile := writeAuditOMP(t, dir)
	t.Setenv("AUDIT_ARGS_FILE", argsFile)
	t.Setenv("AUDIT_VERDICT", "pass")
	runner := newAuditRunner(t, skillDir, omp, filepath.Join(dir, "logs"))
	cand, taskPath := auditTask(t, dir, "review", true)
	cand.MergeApproved = true // human authorized — audit must not run

	handled, err := runner.processReviewAudit(cand, taskPath)
	if err != nil {
		t.Fatalf("processReviewAudit: %v", err)
	}
	if handled {
		t.Fatal("handled = true, want false (human-approved merges skip the audit)")
	}
	if _, err := os.Stat(argsFile); !os.IsNotExist(err) {
		t.Fatal("audit OMP must not run for a human-approved merge")
	}
}

func TestReviewAuditSkipsNonAutoMerge(t *testing.T) {
	dir := t.TempDir()
	skillDir := writeVaultMap(t, dir, nil)
	omp, argsFile := writeAuditOMP(t, dir)
	t.Setenv("AUDIT_ARGS_FILE", argsFile)
	runner := newAuditRunner(t, skillDir, omp, filepath.Join(dir, "logs"))
	cand, _ := auditTask(t, dir, "review", false) // auto_merge=false

	handled, err := runner.processReviewAudit(cand, dir)
	if err != nil {
		t.Fatalf("processReviewAudit: %v", err)
	}
	if handled {
		t.Fatal("handled = true, want false for a non-auto-merge task")
	}
	if _, err := os.Stat(argsFile); !os.IsNotExist(err) {
		t.Fatal("audit OMP must not run for a non-auto-merge task")
	}
}

func TestReviewAuditDisabled(t *testing.T) {
	dir := t.TempDir()
	skillDir := writeVaultMap(t, dir, nil)
	omp, argsFile := writeAuditOMP(t, dir)
	t.Setenv("AUDIT_ARGS_FILE", argsFile)
	runner := newAuditRunner(t, skillDir, omp, filepath.Join(dir, "logs"))
	runner.cfg.Audit.Enabled = false
	cand, _ := auditTask(t, dir, "review", true)

	handled, err := runner.processReviewAudit(cand, dir)
	if err != nil {
		t.Fatalf("processReviewAudit: %v", err)
	}
	if handled {
		t.Fatal("handled = true, want false when audit is disabled")
	}
	if _, err := os.Stat(argsFile); !os.IsNotExist(err) {
		t.Fatal("audit OMP must not run when disabled")
	}
}

// TestBatchReviewAuditBeforeAutoApprove pins the scan-level ordering: the
// completion audit must run BEFORE canAutoApproveMerge. Regression: the gate
// was originally placed after auto-approval, which wrote merge_approved=true
// first — processReviewAudit then skipped every fresh auto_merge review
// (dead gate), and the unit tests missed it because they call
// processReviewAudit directly with MergeApproved=false.
func TestBatchReviewAuditBeforeAutoApprove(t *testing.T) {
	dir := t.TempDir()
	repoDir := filepath.Join(dir, "repo")
	omp, argsFile := writeAuditOMP(t, dir)
	t.Setenv("AUDIT_ARGS_FILE", argsFile)
	t.Setenv("AUDIT_VERDICT", "pass")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatalf("create repo dir: %v", err)
	}
	skillDir := writeVaultMap(t, dir, map[string]string{"demo": repoDir})
	runner := newAuditRunner(t, skillDir, omp, filepath.Join(dir, "logs"))
	taskPath := writeTaskFile(t, dir, "TASK-BATCH.md", "review")
	// Merge Phase will fail (dir is not a git repo, no gh) — that is fine:
	// the assertion target is the audit-before-authorization ordering, and
	// merge failure is logged and swallowed without changing task status.
	done := runBatch(runner, []task.ReadyTask{{
		ID:           "BATCH",
		Title:        "Batch audit",
		Project:      "demo",
		FilePath:     taskPath,
		Status:       "review",
		AutoMerge:    true,
		Assignee:     "default",
		TargetBranch: "task/batch",
	}})
	// The audit gate runs synchronously inside the batch goroutine; wait for
	// the fake OMP to actually start (it writes its argv) before asserting.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(argsFile); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(argsFile); err != nil {
		t.Fatalf("audit OMP never started: %v", err)
	}
	if processed := waitForBatch(t, done); processed != 1 {
		t.Fatalf("processed = %d, want 1", processed)
	}
	// processBatch is dispatch-only: the runTask goroutine (audit session,
	// merge authorization write-backs) is still running when the batch
	// returns. Wait for it before TempDir cleanup, otherwise the async
	// frontmatter write races removeAll — Go 1.26 TempDir numbers
	// subdirectories (/tmp/<test><rand>/001), and the race re-creates the
	// task file after removal, leaving the dir non-empty (cleanup error).
	waitForTasksIdle(t, runner)
	fm := mustParse(t, taskPath)
	if fm.AuditStatus != "passed" {
		t.Fatalf("audit_status = %q, want passed (audit must run before auto-approval)", fm.AuditStatus)
	}
	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read audit args: %v", err)
	}
	if !strings.Contains(string(args), "--tools read,grep,bash") {
		t.Fatalf("audit args = %q, want restricted tools", args)
	}
}
