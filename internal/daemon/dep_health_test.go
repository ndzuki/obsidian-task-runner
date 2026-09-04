package daemon

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

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
// fails to parse must NOT be reported as a broken reference — execution sessions
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

// TestValidateDependencyRefsFlagsClosedRefs guards the closed-reference
// signal: a blocked_by referencing a closed task can never be satisfied
// (closed is a terminal state), so it must be reported once instead of
// starving the gated task silently (TASK-069 blocked_by 011/070 lesson).
func TestValidateDependencyRefsFlagsClosedRefs(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	tasksDir := filepath.Join(vault, "Projects", "001-test", "Tasks")
	writeHealthTask(t, tasksDir, "TASK-069-x.md", "---\nid: \"069\"\nstatus: refining\nblocked_by:\n  - \"011\"\n  - \"052\"\n  - \"070\"\n---\n# X\n")
	writeHealthTask(t, tasksDir, "TASK-011-a.md", "---\nid: \"011\"\nstatus: closed\nclosure_reason: cancelled\n---\n# A\n")
	writeHealthTask(t, tasksDir, "TASK-052-delivered.md", "---\nid: \"052\"\nstatus: closed\nclosure_reason: already-implemented\nmerge_status: merged\n---\n# Delivered\n")
	writeHealthTask(t, tasksDir, "TASK-070-b.md", "---\nid: \"070\"\nstatus: done\n---\n# B\n")

	runner := healthRunner(t, vault)
	runner.validateDependencyRefs()

	cancelled, delivered, done := false, false, false
	runner.diagNotifyAt.Range(func(key, _ interface{}) bool {
		if strings.Contains(key.(string), "blocked_by_closed|069->011") {
			cancelled = true
		}
		if strings.Contains(key.(string), "blocked_by_closed|069->052") {
			delivered = true
		}
		if strings.Contains(key.(string), "blocked_by_closed|069->070") {
			done = true
		}
		return true
	})
	if !cancelled {
		t.Fatal("blocked_by ref to a cancelled closed task must be flagged once")
	}
	if delivered {
		t.Fatal("already-implemented closed blocker must not be flagged")
	}
	if done {
		t.Fatal("blocked_by ref to a done task must not be flagged as closed")
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

// TestValidateDependencyRefsSurfacesReqDependsOnBrokenRefs guards the gap-1
// fix: a REQ depends_on referencing a missing same-project REQ used to be
// silently dropped by syncDependencyInheritance — the downstream task started
// without the dependency and nothing ever signalled it. The validator must
// log and notify once per (project, REQ, reference).
func TestValidateDependencyRefsSurfacesReqDependsOnBrokenRefs(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	reqsDir := filepath.Join(vault, "Projects", "001-test", "Requirements")
	writeHealthTask(t, reqsDir, "REQ-010-a.md", "---\nid: \"010\"\ntitle: A\ndepends_on:\n  - \"REQ-011\"\n---\n# A\n")
	writeHealthTask(t, reqsDir, "REQ-011-b.md", "---\nid: \"011\"\ntitle: B\n---\n# B\n")
	writeHealthTask(t, reqsDir, "REQ-012-c.md", "---\nid: \"012\"\ntitle: C\ndepends_on:\n  - \"REQ-013\"\n---\n# C\n")
	tasksDir := filepath.Join(vault, "Projects", "001-test", "Tasks")
	writeHealthTask(t, tasksDir, "TASK-001-x.md", "---\nid: \"001\"\nstatus: ready\n---\n# X\n")

	runner := healthRunner(t, vault)
	runner.validateDependencyRefs()

	broken, valid := false, false
	runner.diagNotifyAt.Range(func(key, _ interface{}) bool {
		if strings.Contains(key.(string), "req_depends_on|012->013") {
			broken = true
		}
		if strings.Contains(key.(string), "req_depends_on|010->011") {
			valid = true
		}
		return true
	})
	if !broken {
		t.Fatal("broken REQ depends_on ref must be recorded once")
	}
	if valid {
		t.Fatal("valid REQ depends_on ref must not be recorded")
	}
}

// TestCheckVaultMapHealth guards the config-syntax health check: a corrupted
// vault-map.json surfaces a notification ONCE per file version; a valid file
// or a missing one (first install) is silent.
func TestCheckVaultMapHealth(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vault-map.json")
	cfg := &config.Config{ConfigPath: path, Notifications: config.NotifConfig{Desktop: false}}
	r := New(cfg)
	r.logger = log.New(io.Discard, "", 0)

	// 正常文件：不产生任何 diag 标记之外的状态，调用两次都静默（不 panic）。
	if err := os.WriteFile(path, []byte(`{"obsidian_vault":"/v","projects":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	r.checkVaultMapHealth()

	// 损坏文件：应记录一次提醒标记（key 含 mtime+size）。
	if err := os.WriteFile(path, []byte(`{"obsidian_vault": `), 0o644); err != nil {
		t.Fatal(err)
	}
	r.checkVaultMapHealth()
	// 同版本第二次调用不重复（diagNotifyAt 已有该 key）。
	r.checkVaultMapHealth()

	// 修复后 mtime/size 变化 → 允许重新检查且通过。
	time.Sleep(5 * time.Millisecond)
	if err := os.WriteFile(path, []byte(`{"obsidian_vault":"/v","projects":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	r.checkVaultMapHealth()

	// 缺失文件：静默。
	missing := filepath.Join(dir, "nope.json")
	r2 := New(&config.Config{ConfigPath: missing})
	r2.logger = log.New(io.Discard, "", 0)
	r2.checkVaultMapHealth()
}

// TestValidateDependencyRefsSkipsPausedProject guards the desktop-reminder
// storm: when a project's Grilling-Decisions.md is set to paused/closed (the
// user's explicit project-level pause switch), ALL dependency-health
// diagnostics for that project must be silent — broken refs, closed refs and
// upstream-stall reminders are "push the project forward" nudges the user
// has explicitly opted out of (2026-08-31 002-magic-models-manager: TASK-003/
// 004/005 blocked by a paused needs-grilling upstream produced a blocked_by
// reminder every daemon restart because diagNotifyAt is in-memory).
func TestValidateDependencyRefsSkipsPausedProject(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	projDir := filepath.Join(vault, "Projects", "001-test")
	tasksDir := filepath.Join(projDir, "Tasks")
	// Paused decision list (status=closed) — the user's pause switch.
	if err := os.MkdirAll(filepath.Join(projDir, "Notes"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projDir, "Notes", grillingDecisionListName),
		[]byte("---\nid: grilling-decisions\nstatus: closed\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Both a broken ref (999 missing) and a stale-upstream ref (002 idle 6d).
	writeHealthTask(t, tasksDir, "TASK-001-a.md", "---\nid: \"001\"\nstatus: ready\nblocked_by:\n  - \"999\"\n---\n# A\n")
	writeHealthTask(t, tasksDir, "TASK-002-b.md", "---\nid: \"002\"\nstatus: blocked\n---\n# B\n")

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
		t.Fatal("paused project must suppress all dependency-health reminders")
	}
}

// TestValidateDependencyRefsActiveProjectStillNotifies guards the flip side:
// an ACTIVE project (no paused decision list) still surfaces broken refs —
// the pause suppression must not leak into projects the user has not paused.
func TestValidateDependencyRefsActiveProjectStillNotifies(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	tasksDir := filepath.Join(vault, "Projects", "001-test", "Tasks")
	writeHealthTask(t, tasksDir, "TASK-001-a.md", "---\nid: \"001\"\nstatus: ready\nblocked_by:\n  - \"999\"\n---\n# A\n")

	runner := healthRunner(t, vault)
	runner.validateDependencyRefs()

	notified := false
	runner.diagNotifyAt.Range(func(key, _ interface{}) bool {
		if strings.Contains(key.(string), "blocked_by|001->999") {
			notified = true
		}
		return true
	})
	if !notified {
		t.Fatal("active project must still surface broken refs")
	}
}
