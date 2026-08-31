package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndzuki/obsidian-task-runner/internal/config"
	"github.com/ndzuki/obsidian-task-runner/internal/task"
	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

// newDispatchFixture builds a valid task file and a Runner wired to a stub
// phaseExecutor returning the given result.
func newDispatchFixture(t *testing.T, result *ExecutionResult) (*Runner, task.ReadyTask, string) {
	t.Helper()
	dir := t.TempDir()
	taskPath := filepath.Join(dir, "TASK-001.md")
	content := "---\nid: \"001\"\ntitle: Test\nproject: demo\nproject_id: \"001\"\nreq_doc: REQ-001.md\nassignee: default\nstatus: refining\ngeneration: 1\nplan_version: 0\n---\n# Task\n"
	if err := os.WriteFile(taskPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	r := New(&config.Config{
		Executor:            "dsh",
		ObsidianVault:       dir,
		PhaseTimeoutMinutes: map[string]int{"refining": 1},
	})
	r.logger = log.New(io.Discard, "", 0)
	r.phaseExecutor = phaseExecutorStub{result: result}
	candidate := task.ReadyTask{ID: "001", Title: "Test", Project: "demo", FilePath: taskPath, Status: "refining", Assignee: "default"}
	return r, candidate, taskPath
}

func TestRunDSHPhaseDispatchSuccess(t *testing.T) {
	r, candidate, taskPath := newDispatchFixture(t, &ExecutionResult{Code: OutcomeSuccess})

	handled := r.runDSHPhaseDispatch(candidate, taskPath, filepath.Dir(taskPath), "refining", "gateway/gpt-5.4-mini", "/skill", "/tmp/task.log")
	if !handled {
		t.Fatal("success dispatch must be handled")
	}
	// Success tail clears phase error but must not flip status.
	fm, err := yamlfrontmatter.ParseTaskDocument(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	if fm.Status != "refining" {
		t.Fatalf("status=%q, want refining (success tail must not change status)", fm.Status)
	}
}

// TestRunDSHPhaseDispatchRefiningRestampsReqHash pins the daemon-side
// safety net for the refining early-out. The pre-dispatch ensureReqHash
// stamps the pre-session REQ bytes; a refining session that rewrites the
// REQ during its own write-back changes the hash. If the success tail does
// not re-stamp the post-session bytes, every later scan sees a stale
// refine_req_hash and re-runs the maturity gate forever instead of routing
// to planning (TASK-058 loop observed after TASK-079 merged).
func TestRunDSHPhaseDispatchRefiningRestampsReqHash(t *testing.T) {
	dir := t.TempDir()
	reqContent := "## 目标\nrefined requirement\n"
	if err := os.WriteFile(filepath.Join(dir, "REQ-001.md"), []byte(reqContent), 0o644); err != nil {
		t.Fatal(err)
	}
	taskPath := filepath.Join(dir, "TASK-001.md")
	content := "---\nid: \"001\"\ntitle: Test\nproject: demo\nproject_id: \"001\"\nreq_doc: REQ-001.md\nassignee: default\nstatus: refining\nmaturity: fully_mature\nrefine_req_hash: sha256:stale\nplan_req_hash: sha256:stale\npending_req: true\ngeneration: 1\nplan_version: 1\n---\n# Task\n"
	if err := os.WriteFile(taskPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	r := New(&config.Config{
		Executor:            "dsh",
		ObsidianVault:       dir,
		PhaseTimeoutMinutes: map[string]int{"refining": 1},
	})
	r.logger = log.New(io.Discard, "", 0)
	r.phaseExecutor = phaseExecutorStub{result: &ExecutionResult{Code: OutcomeSuccess}}
	candidate := task.ReadyTask{
		ID: "001", Title: "Test", Project: "demo", FilePath: taskPath,
		Status: "refining", Assignee: "default", ReqDoc: "REQ-001.md",
		Maturity: "fully_mature", RefineReqHash: "sha256:stale",
		PlanReqHash: "sha256:stale", PendingReq: true,
	}

	handled := r.runDSHPhaseDispatch(candidate, taskPath, dir, "refining", "gateway/gpt-5.4-mini", "/skill", "/tmp/task.log")
	if !handled {
		t.Fatal("success dispatch must be handled")
	}
	fm, err := yamlfrontmatter.ParseTaskDocument(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(reqContent))
	want := "sha256:" + hex.EncodeToString(sum[:])
	if fm.RefineReqHash != want {
		t.Fatalf("refine_req_hash=%q, want %q (stale hash must be re-stamped to post-session REQ bytes)", fm.RefineReqHash, want)
	}
	// Invariant: the success tail re-stamps the hash but must not clear
	// pending_req or flip the status — the planning round owns that.
	if fm.PendingReq != true {
		t.Fatalf("pending_req=%v, want true (only a successful plan clears it)", fm.PendingReq)
	}
	if fm.Status != "refining" {
		t.Fatalf("status=%q, want refining", fm.Status)
	}
}

func TestRunDSHPhaseDispatchFailure(t *testing.T) {
	r, candidate, taskPath := newDispatchFixture(t, &ExecutionResult{Code: OutcomeFailed, Error: "provider down"})

	handled := r.runDSHPhaseDispatch(candidate, taskPath, filepath.Dir(taskPath), "refining", "gateway/gpt-5.4-mini", "/skill", "/tmp/task.log")
	if !handled {
		t.Fatal("failure dispatch must be handled")
	}
	fm, err := yamlfrontmatter.ParseTaskDocument(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	// refining + MODEL_FAILED → retry-then-block: handlePhaseFailure records
	// phase_error_code, not an immediate blocked status.
	if fm.PhaseErrorCode != string(ErrModelFailed) {
		t.Fatalf("phase_error_code=%q, want MODEL_FAILED", fm.PhaseErrorCode)
	}
}

func TestRunDSHPhaseDispatchInterrupted(t *testing.T) {
	r, candidate, taskPath := newDispatchFixture(t, &ExecutionResult{Code: OutcomeInterrupted})

	handled := r.runDSHPhaseDispatch(candidate, taskPath, filepath.Dir(taskPath), "refining", "gateway/gpt-5.4-mini", "/skill", "/tmp/task.log")
	if !handled {
		t.Fatal("interrupted dispatch must be handled")
	}
	fm, err := yamlfrontmatter.ParseTaskDocument(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	if fm.PhaseErrorCode != string(ErrPhaseInterrupted) {
		t.Fatalf("phase_error_code=%q, want PHASE_INTERRUPTED", fm.PhaseErrorCode)
	}
}

func TestRunDSHPhaseDispatchTimeoutMaps(t *testing.T) {
	r, candidate, taskPath := newDispatchFixture(t, &ExecutionResult{Code: OutcomeTimedOut})

	r.runDSHPhaseDispatch(candidate, taskPath, filepath.Dir(taskPath), "refining", "gateway/gpt-5.4-mini", "/skill", "/tmp/task.log")
	fm, err := yamlfrontmatter.ParseTaskDocument(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	if fm.PhaseErrorCode != string(ErrPhaseTimeout) {
		t.Fatalf("phase_error_code=%q, want PHASE_TIMEOUT", fm.PhaseErrorCode)
	}
}

// Ensure the seam is wired: a dsh-configured runner routes through the stub
// (never spawns OMP), while an omp-configured runner ignores it.
func TestExecutorSelectionIsRespected(t *testing.T) {
	dir := t.TempDir()
	taskPath := filepath.Join(dir, "TASK-001.md")
	if err := os.WriteFile(taskPath, []byte("---\nid: \"001\"\ntitle: T\nproject: demo\nproject_id: \"001\"\nreq_doc: R.md\nassignee: default\nstatus: refining\ngeneration: 1\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := New(&config.Config{Executor: "dsh", ObsidianVault: dir})
	if r.phaseExecutor == nil || r.phaseExecutor.Name() != "dsh" {
		t.Fatalf("dsh runner phaseExecutor=%v, want dsh adapter", r.phaseExecutor)
	}
	r2 := New(&config.Config{Executor: "dsh-embed", ObsidianVault: dir})
	if r2.phaseExecutor == nil || r2.phaseExecutor.Name() != "dsh-embed" {
		t.Fatalf("dsh-embed runner phaseExecutor=%v, want dsh-embed adapter", r2.phaseExecutor)
	}
	_ = context.Background()
}

// TestSelectModelPhaseRouting 守护相位感知模型路由：显式 assignee 覆盖一切；
// default assignee 下重型阶段（planning/round2/merge）用 deepseek_magic 旗舰，
// 轻量阶段用 default（mini）。背景：config.DefaultModels 注释声称重型阶段用
// 旗舰，但旧实现按 assignee 全阶段统一路由，planning/round2 一直跑 V4 Flash
// 级 mini（TASK-058/079 复盘）。
func TestSelectModelPhaseRouting(t *testing.T) {
	cfg := config.Defaults()
	cfg.Models = map[string]string{
		"default":        "deepseek_magic/gpt-5.4-mini",
		"deepseek_magic": "deepseek_magic/deepseek-v4-pro",
		"gpt":            "openai/gpt-5.6-sol",
	}
	r := New(cfg)

	for _, phase := range []string{"planning", "round2", "merge"} {
		if got := r.selectModel("default", phase); got != "deepseek_magic/deepseek-v4-pro" {
			t.Fatalf("selectModel(default, %s) = %q, want deepseek_magic/deepseek-v4-pro", phase, got)
		}
	}
	for _, phase := range []string{"refining", "priority", "pm", "audit", "conventions", "design"} {
		if got := r.selectModel("default", phase); got != "deepseek_magic/gpt-5.4-mini" {
			t.Fatalf("selectModel(default, %s) = %q, want mini default", phase, got)
		}
	}
	// 显式 assignee 覆盖一切（含重型阶段）。
	if got := r.selectModel("gpt", "round2"); got != "openai/gpt-5.6-sol" {
		t.Fatalf("explicit assignee not honored: %q", got)
	}
	// 空 assignee 视同 default。
	if got := r.selectModel("", "planning"); got != "deepseek_magic/deepseek-v4-pro" {
		t.Fatalf("empty assignee planning = %q, want flagship", got)
	}
}

// TestOmpPhaseThinkingRefiningMedium 守护 spec 作者阶段的强度提升：
// refining/design 从 low 提到 medium（TASK-079 D5 字段名推断类失误复盘）。
func TestOmpPhaseThinkingRefiningMedium(t *testing.T) {
	cases := map[string]string{
		"refining": "medium",
		"design":   "medium",
		"priority": "medium",
		"planning": "high",
		"round2":   "max",
		"pm":       "low",
		"merge":    "low",
		"audit":    "low",
	}
	for phase, want := range cases {
		if got := ompPhaseThinking(phase); got != want {
			t.Errorf("ompPhaseThinking(%s) = %q, want %q", phase, got, want)
		}
	}
}

// specCaptureExecutor records the PhaseSpec of every dispatched phase so
// tests can assert daemon-layer tool policies.
type specCaptureExecutor struct {
	phaseExecutorStub
	spec PhaseSpec
}

func (e *specCaptureExecutor) Start(_ context.Context, spec PhaseSpec, _ TaskSnapshot) (ExecutionHandle, error) {
	e.spec = spec
	return phaseHandleStub{result: e.result}, nil
}

// mustContain / mustExclude are sets of tool names for assertToolPolicy.
type mustContain []string
type mustExclude []string

// assertToolPolicy verifies a comma-separated tool policy includes every
// mustContain tool and excludes every mustExclude tool. It guards the
// review-session whitelists against regressing to a list that omits the
// harness's benign operating tools (skill/todo_write/job_*) or lets the
// worktree-mutating edit/str_replace_editor back in.
func assertToolPolicy(t *testing.T, policy string, include mustContain, exclude mustExclude) {
	t.Helper()
	set := make(map[string]bool)
	for _, name := range strings.Split(policy, ",") {
		name = strings.TrimSpace(name)
		if name != "" {
			set[name] = true
		}
	}
	for _, name := range include {
		if !set[name] {
			t.Errorf("tool policy %q missing required tool %q", policy, name)
		}
	}
	for _, name := range exclude {
		if set[name] {
			t.Errorf("tool policy %q must exclude %q", policy, name)
		}
	}
}

// TestRunDSHPhaseDispatchConventionsRestrictsTools guards the gap-7 fix: the
// conventions baseline-review session must carry the restricted tool policy at
// the daemon layer (parity with the audit session), not rely on the skill's
// prompt self-restraint. The policy must include the harness's benign tools
// (skill/todo_write/job_output/job_kill/read_image) and write (the review
// artifact), but must exclude the worktree-mutating edit/str_replace_editor —
// otherwise the session fails with TOOL_POLICY_VIOLATION before it can write
// PROJECT-CONVENTIONS.md (TASK-080 2026-08-31 CONVENTIONS_REVIEW_FAILED).
func TestRunDSHPhaseDispatchConventionsRestrictsTools(t *testing.T) {
	r, candidate, taskPath := newDispatchFixture(t, &ExecutionResult{Code: OutcomeSuccess})
	captor := &specCaptureExecutor{phaseExecutorStub: phaseExecutorStub{result: &ExecutionResult{Code: OutcomeSuccess}}}
	r.phaseExecutor = captor

	if handled := r.runDSHPhaseDispatch(candidate, taskPath, filepath.Dir(taskPath), "conventions", "gateway/gpt-5.4-mini", "/obsidian-task-runner-conventions "+taskPath, "/tmp/task.log"); !handled {
		t.Fatal("conventions dispatch must be handled")
	}
	if captor.spec.ToolPolicy != conventionsToolPolicy {
		t.Fatalf("conventions ToolPolicy = %q, want %q", captor.spec.ToolPolicy, conventionsToolPolicy)
	}
	assertToolPolicy(t, captor.spec.ToolPolicy, mustContain{"read", "grep", "glob", "bash", "skill", "todo_write", "job_output", "job_kill", "read_image", "write"}, mustExclude{"edit", "str_replace_editor"})

	// 对照组：普通阶段（refining）无工具限制。
	captor2 := &specCaptureExecutor{phaseExecutorStub: phaseExecutorStub{result: &ExecutionResult{Code: OutcomeSuccess}}}
	r.phaseExecutor = captor2
	if handled := r.runDSHPhaseDispatch(candidate, taskPath, filepath.Dir(taskPath), "refining", "gateway/gpt-5.4-mini", "/obsidian-task-runner-refining "+taskPath, "/tmp/task.log"); !handled {
		t.Fatal("refining dispatch must be handled")
	}
	if captor2.spec.ToolPolicy != "" {
		t.Fatalf("refining ToolPolicy = %q, want empty", captor2.spec.ToolPolicy)
	}
}
