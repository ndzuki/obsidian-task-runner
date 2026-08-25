package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ndzuki/obsidian-task-runner/internal/config"
	"github.com/ndzuki/obsidian-task-runner/internal/designlib"
)

type fakeDesignExecutor struct {
	result  *ExecutionResult
	write   func(TaskSnapshot) error
	spec    PhaseSpec
	started bool
}

func (f *fakeDesignExecutor) Name() string { return "fake-dsh-design" }

func (f *fakeDesignExecutor) Cancel(context.Context, string) error { return nil }

func (f *fakeDesignExecutor) Start(_ context.Context, spec PhaseSpec, snap TaskSnapshot) (ExecutionHandle, error) {
	f.started = true
	f.spec = spec
	if f.write != nil {
		if err := f.write(snap); err != nil {
			return nil, err
		}
	}
	return fakeDesignHandle{result: f.result}, nil
}

func (f *fakeDesignExecutor) Resume(context.Context, PhaseSpec, string, time.Duration) (ExecutionHandle, error) {
	return nil, ErrResumeUnsupported
}

type fakeDesignHandle struct {
	result *ExecutionResult
}

func (h fakeDesignHandle) Wait() (*ExecutionResult, error) { return h.result, nil }
func (fakeDesignHandle) PID() int                          { return 0 }

func newDesignTestRunner(t *testing.T) (*Runner, string, string) {
	t.Helper()
	vault := t.TempDir()
	projectDir := filepath.Join(vault, "Projects", "001-demo")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runner := New(&config.Config{
		ObsidianVault: vault,
		DSHCmd:        "dsh",
		DSHProfile:    "headless",
		PhaseTimeoutMinutes: map[string]int{
			"design": 1,
		},
	})
	return runner, vault, projectDir
}

func writeValidDesignLibrary(t *testing.T, projectDir string) error {
	t.Helper()
	layout, err := designlib.Ensure(projectDir)
	if err != nil {
		return err
	}
	files := map[string]string{
		layout.GlossaryPath(): "---\nschema: glossary-v1\n---\n# Glossary\nOrder = 订单\n",
		filepath.Join(layout.ContractsPath(), "order-api.md"): "---\nschema: contract-v1\nid: order-api\ntitle: Order API\n---\n# Contract\n",
		filepath.Join(layout.DecisionsPath(), "ADR-001.md"):   "---\nschema: decision-v1\nid: ADR-001\ntitle: Storage\nstatus: accepted\n---\n# Decision\n",
		filepath.Join(layout.WavesPath(), "wave-0.md"):        "---\nschema: wave-v1\nid: wave-0\ntitle: Contract first\n---\n# Wave 0\n",
	}
	for path, content := range files {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func TestRunGlobalDesignSessionSuccess(t *testing.T) {
	runner, vault, projectDir := newDesignTestRunner(t)
	layout := designlib.ForProject(projectDir)
	fake := &fakeDesignExecutor{
		result: &ExecutionResult{Phase: "design", Code: OutcomeSuccess, ResumeToken: "dsh-session-1"},
		write:  func(TaskSnapshot) error { return writeValidDesignLibrary(t, projectDir) },
	}
	runner.designExecutor = fake

	err := runner.runGlobalDesignSession(context.Background(), "demo", "001", "/vault/TASK-001.md", "Projects/001-demo/Requirements/REQ-001.md", projectDir)
	if err != nil {
		t.Fatal(err)
	}
	rev, err := layout.ReadRevision()
	if err != nil {
		t.Fatal(err)
	}
	if rev.Number != 1 || rev.SessionID != "dsh-session-1" {
		t.Fatalf("revision=%+v, want revision 1/session dsh-session-1", rev)
	}
	if fake.spec.Phase != "design" || fake.spec.ReasoningEffort != "max" {
		t.Fatalf("unexpected design spec: %+v", fake.spec)
	}
	// The design session's working directory is the vault Design dir itself —
	// the workspace-write sandbox scope is exactly the artifact tree, and the
	// repo is only passed as a read-only evidence path (TASK-065 lesson).
	if fake.spec.WorkingDir != layout.Root {
		t.Fatalf("design WorkingDir=%q, want Design dir %q", fake.spec.WorkingDir, layout.Root)
	}
	if fake.spec.Timeout != time.Minute {
		t.Fatalf("design timeout=%v, want 1m", fake.spec.Timeout)
	}
	if fake.spec.SkillPrompt == "" || !containsAll(fake.spec.SkillPrompt, "obsidian-task-runner-design", "design_dir", vault, "repo_dir=") {
		t.Fatalf("design prompt missing required inputs: %q", fake.spec.SkillPrompt)
	}
}

// TestRunGlobalDesignSessionRepoIsEvidenceOnly guards the TASK-065 fix: even
// when a repo directory is provided, the session runs inside the vault Design
// dir (its write scope) and receives the repo as a prompt argument.
func TestRunGlobalDesignSessionRepoIsEvidenceOnly(t *testing.T) {
	runner, _, projectDir := newDesignTestRunner(t)
	layout := designlib.ForProject(projectDir)
	repoDir := t.TempDir()
	fake := &fakeDesignExecutor{
		result: &ExecutionResult{Phase: "design", Code: OutcomeSuccess},
		write:  func(TaskSnapshot) error { return writeValidDesignLibrary(t, projectDir) },
	}
	runner.designExecutor = fake

	if err := runner.runGlobalDesignSession(context.Background(), "demo", "001", "/vault/TASK-001.md", "Projects/001-demo/Requirements/REQ-001.md", repoDir); err != nil {
		t.Fatal(err)
	}
	if fake.spec.WorkingDir != layout.Root {
		t.Fatalf("design WorkingDir=%q, want Design dir %q (repo must not be the working dir)", fake.spec.WorkingDir, layout.Root)
	}
	if !strings.Contains(fake.spec.SkillPrompt, "repo_dir="+repoDir) {
		t.Fatalf("design prompt must pass repo_dir=%s, got %q", repoDir, fake.spec.SkillPrompt)
	}
}

// TestRunGlobalDesignSessionImportsStagedLibrary covers the legacy fallback:
// sessions from before the Design-dir contract staged artifacts under
// <repo>/.design-stage/ and the daemon must validate+import them instead of
// failing forever on an empty real library (TASK-065: valid staging sat in
// the repo while the gate failed three times on the vault).
func TestRunGlobalDesignSessionImportsStagedLibrary(t *testing.T) {
	runner, _, projectDir := newDesignTestRunner(t)
	repoDir := t.TempDir()
	stagedProject := filepath.Join(repoDir, ".design-stage", "Projects", filepath.Base(projectDir))
	if err := writeValidDesignLibrary(t, stagedProject); err != nil {
		t.Fatal(err)
	}
	fake := &fakeDesignExecutor{
		result: &ExecutionResult{Phase: "design", Code: OutcomeSuccess, Stdout: "staged; vault read-only in session sandbox"},
		// The session writes nothing to the REAL library — legacy behavior.
	}
	runner.designExecutor = fake

	if err := runner.runGlobalDesignSession(context.Background(), "demo", "001", "/vault/TASK-001.md", "Projects/001-demo/Requirements/REQ-001.md", repoDir); err != nil {
		t.Fatal(err)
	}
	layout := designlib.ForProject(projectDir)
	if err := layout.Validate(); err != nil {
		t.Fatalf("imported library invalid: %v", err)
	}
	rev, err := layout.ReadRevision()
	if err != nil {
		t.Fatal(err)
	}
	if rev.Number != 1 {
		t.Fatalf("revision=%d, want 1 (imported staged library must bump)", rev.Number)
	}
}

// TestRunGlobalDesignSessionDoesNotBumpOnFailure guards the invariant that an
// invalid library never bumps the revision, and that the error carries the
// session's own conclusion tail for diagnosis.
func TestRunGlobalDesignSessionDoesNotBumpOnFailure(t *testing.T) {
	tests := []struct {
		name   string
		result *ExecutionResult
		write  func(*testing.T, string) error
	}{
		{
			name:   "executor failure",
			result: &ExecutionResult{Phase: "design", Code: OutcomeFailed, Error: "provider unavailable"},
		},
		{
			name:   "invalid artifacts",
			result: &ExecutionResult{Phase: "design", Code: OutcomeSuccess, Stdout: "conclusion: vault read-only"},
			write: func(t *testing.T, projectDir string) error {
				layout, err := designlib.Ensure(projectDir)
				if err != nil {
					return err
				}
				return os.WriteFile(layout.GlossaryPath(), []byte("---\nschema: glossary-v1\n---\n# only glossary\n"), 0o644)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner, _, projectDir := newDesignTestRunner(t)
			repoDir := t.TempDir() // no valid staging: import must not invent artifacts
			fake := &fakeDesignExecutor{result: tt.result}
			if tt.write != nil {
				fake.write = func(TaskSnapshot) error { return tt.write(t, projectDir) }
			}
			runner.designExecutor = fake
			err := runner.runGlobalDesignSession(context.Background(), "demo", "001", "TASK-001.md", "REQ-001.md", repoDir)
			if err == nil {
				t.Fatal("invalid design session must fail")
			}
			if tt.name == "invalid artifacts" && !strings.Contains(err.Error(), "conclusion: vault read-only") {
				t.Fatalf("error must carry the session tail for diagnosis, got: %v", err)
			}
			rev, readErr := designlib.ForProject(projectDir).ReadRevision()
			if readErr != nil {
				t.Fatal(readErr)
			}
			if rev.Number != 0 {
				t.Fatalf("revision=%d after failed session, want 0", rev.Number)
			}
		})
	}
}

// TestRunGlobalDesignSessionFailsFastOnUnwritableTarget guards the daemon-side
// write probe: an unwritable Design dir must fail in milliseconds with
// errDesignTargetUnwritable, NOT burn a 10-90 minute session that then reports
// success against a directory it could never write.
func TestRunGlobalDesignSessionFailsFastOnUnwritableTarget(t *testing.T) {
	runner, _, projectDir := newDesignTestRunner(t)
	layout, err := designlib.Ensure(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	// A directory at the probe path makes OpenFile fail (EISDIR) on any OS,
	// root included — a portable "unwritable" simulation.
	if err := os.MkdirAll(filepath.Join(layout.Root, designProbeName), 0o755); err != nil {
		t.Fatal(err)
	}
	fake := &fakeDesignExecutor{result: &ExecutionResult{Phase: "design", Code: OutcomeSuccess}}
	runner.designExecutor = fake

	err = runner.runGlobalDesignSession(context.Background(), "demo", "001", "TASK-001.md", "REQ-001.md", projectDir)
	if err == nil {
		t.Fatal("unwritable design target must fail")
	}
	if !errors.Is(err, errDesignTargetUnwritable) {
		t.Fatalf("error = %v, want errDesignTargetUnwritable", err)
	}
	if fake.started {
		t.Fatal("design session must not be dispatched when the target is unwritable")
	}
	if code := designGateErrorCode(err); code != ErrDesignTargetUnwritable {
		t.Fatalf("designGateErrorCode=%s, want %s", code, ErrDesignTargetUnwritable)
	}
}

func containsAll(s string, needles ...string) bool {
	for _, needle := range needles {
		if !strings.Contains(s, needle) {
			return false
		}
	}
	return true
}
