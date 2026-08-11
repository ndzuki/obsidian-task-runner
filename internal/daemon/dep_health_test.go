package daemon

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ndzuki/obsidian-task-runner/internal/config"
)

func healthRunner(t *testing.T, vault string) *Runner {
	t.Helper()
	runner := New(&config.Config{ObsidianVault: vault})
	runner.logger = log.New(io.Discard, "", 0)
	return runner
}

func writeHealthTask(t *testing.T, tasksDir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tasksDir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestValidateDependencyRefsSurfacesBrokenRefs guards the silent-starvation
// signal: a blocked_by referencing a missing task must log and notify once,
// and a valid reference must stay quiet.
func TestValidateDependencyRefsSurfacesBrokenRefs(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	tasksDir := filepath.Join(vault, "Projects", "001-test", "Tasks")
	writeHealthTask(t, tasksDir, "TASK-001-a.md", "---\nid: \"001\"\nstatus: ready\nblocked_by:\n  - \"999\"\n---\n# A\n")
	writeHealthTask(t, tasksDir, "TASK-002-b.md", "---\nid: \"002\"\nstatus: ready\nblocked_by:\n  - \"001\"\n---\n# B\n")

	runner := healthRunner(t, vault)
	runner.validateDependencyRefs()

	// 001's broken ref is reported; 002's valid ref is not.
	broken, valid := false, false
	runner.diagNotifyAt.Range(func(key, _ interface{}) bool {
		if strings.Contains(key.(string), "blocked_by|001->999") {
			broken = true
		}
		if strings.Contains(key.(string), "blocked_by|002->001") {
			valid = true
		}
		return true
	})
	if !broken {
		t.Fatal("broken blocked_by ref must be recorded once")
	}
	if valid {
		t.Fatal("valid blocked_by ref must not be recorded")
	}
}

// TestValidateDependencyRefsSkipsUnparsableRefs guards the transient-write
// window: a blocked_by referencing a task whose file exists but currently
// fails to parse must NOT be reported as a broken reference — OMP sessions
// rewrite frontmatter in place, and a partial write can briefly produce
// duplicate keys / invalid YAML (observed with refine_version). The check
// defers to the next scan instead of firing a false "missing task" toast.
func TestValidateDependencyRefsSkipsUnparsableRefs(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	tasksDir := filepath.Join(vault, "Projects", "001-test", "Tasks")
	writeHealthTask(t, tasksDir, "TASK-001-a.md", "---\nid: \"001\"\nstatus: ready\nblocked_by:\n  - \"068\"\n---\n# A\n")
	// TASK-068 exists but its frontmatter is currently unparsable (duplicate
	// mapping key) — the exact failure mode of an interrupted write-back.
	writeHealthTask(t, tasksDir, "TASK-068-x.md", "---\nid: \"068\"\nrefine_version: 4\nrefine_version: 7\nstatus: ready\n---\n# X\n")

	runner := healthRunner(t, vault)
	runner.validateDependencyRefs()

	notified := false
	runner.diagNotifyAt.Range(func(key, _ interface{}) bool {
		if strings.Contains(key.(string), "blocked_by") {
			notified = true
		}
		return true
	})
	if notified {
		t.Fatal("ref to an unparsable-but-existing task must not be reported as broken")
	}
}

// TestDetectPlanFileOverlapsWarnsOnce guards the merge-conflict early signal:
// two implementing tasks planning the same file trigger one notification;
// a second run stays quiet (one-shot per key).
func TestDetectPlanFileOverlapsWarnsOnce(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	tasksDir := filepath.Join(vault, "Projects", "001-test", "Tasks")
	writeHealthTask(t, tasksDir, "TASK-001-a.md", "---\nid: \"001\"\nstatus: implementing\nplan_files:\n  - internal/foo.go\n---\n# A\n")
	writeHealthTask(t, tasksDir, "TASK-002-b.md", "---\nid: \"002\"\nstatus: implementing\nplan_files:\n  - internal/foo.go\n  - internal/bar.go\n---\n# B\n")
	writeHealthTask(t, tasksDir, "TASK-003-c.md", "---\nid: \"003\"\nstatus: implementing\nplan_files:\n  - internal/bar.go\n---\n# C\n")

	runner := healthRunner(t, vault)
	runner.detectPlanFileOverlaps()
	runner.detectPlanFileOverlaps() // second run must not re-notify

	overlapKeys := 0
	runner.diagNotifyAt.Range(func(key, _ interface{}) bool {
		if strings.Contains(key.(string), "overlap|") {
			overlapKeys++
		}
		return true
	})
	if overlapKeys != 2 { // foo.go (001+002), bar.go (002+003)
		t.Fatalf("overlap keys = %d, want 2 (one per file, not per run)", overlapKeys)
	}
}

// TestProjectHealthDiagnosticsFlagsStuckQueue guards the rebaseline trigger:
// a project with many merged-but-not-done tasks and a large in-flight queue
// fires a daily warning; a healthy project stays silent.
func TestProjectHealthDiagnosticsFlagsStuckQueue(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	tasksDir := filepath.Join(vault, "Projects", "001-test", "Tasks")
	// 22 in-flight, 6 of them merged-but-never-done (stale closure signature).
	for i := 1; i <= 22; i++ {
		status := "implementing"
		merge := ""
		if i <= 6 {
			status = "plan-review"
			merge = "merged"
		}
		writeHealthTask(t, tasksDir, "TASK-"+pad(i)+"-x.md",
			"---\nid: \""+pad(i)+"\"\nstatus: "+status+"\nmerge_status: "+merge+"\nstage: \"P1\"\n---\n# X\n")
	}

	runner := healthRunner(t, vault)
	runner.projectHealthDiagnostics()

	fired := false
	runner.diagNotifyAt.Range(func(key, _ interface{}) bool {
		if strings.Contains(key.(string), "rebaseline|") {
			fired = true
		}
		return true
	})
	if !fired {
		t.Fatal("stuck-queue signature must fire the rebaseline warning")
	}
}

// TestProjectHealthDiagnosticsIgnoresClosedMerged guards the stale-closure
// accounting: a closed task that carries merge_status=merged is a normal
// terminal state (user decision), NOT an unreconciled delivery — it must
// neither inflate merged-not-done nor trip the rebaseline warning.
func TestProjectHealthDiagnosticsIgnoresClosedMerged(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	tasksDir := filepath.Join(vault, "Projects", "001-test", "Tasks")
	// 22 in-flight (under in-flight threshold would not fire; here only
	// closed tasks carry merged — a wrong count of 17+ would trip it).
	for i := 1; i <= 22; i++ {
		writeHealthTask(t, tasksDir, "TASK-"+pad(i)+"-x.md",
			"---\nid: \""+pad(i)+"\"\nstatus: implementing\nmerge_status: \nstage: \"P1\"\n---\n# X\n")
	}
	for i := 1; i <= 17; i++ {
		writeHealthTask(t, tasksDir, "TASK-"+pad(100+i)+"-c.md",
			"---\nid: \""+pad(100+i)+"\"\nstatus: closed\nmerge_status: merged\nclosure_reason: cancelled\n---\n# C\n")
	}

	runner := healthRunner(t, vault)
	runner.projectHealthDiagnostics()

	// merged-not-done must be 0 (all closed) — a diagnostic log line with
	// nonzero count means the closed tasks leaked into the statistic.
	fired := false
	runner.diagNotifyAt.Range(func(key, _ interface{}) bool {
		if strings.Contains(key.(string), "rebaseline|") {
			fired = true
		}
		return true
	})
	if fired {
		t.Fatal("closed+merged tasks must not trip the rebaseline warning")
	}
}

func pad(n int) string {
	s := strconv.Itoa(n)
	for len(s) < 3 {
		s = "0" + s
	}
	return s
}
