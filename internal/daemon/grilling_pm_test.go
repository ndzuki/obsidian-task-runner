package daemon

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ndzuki/obsidian-task-runner/internal/config"
	"github.com/ndzuki/obsidian-task-runner/internal/task"
)

// writeArgsOMP writes a fake OMP that dumps its argv into argsPath and exits 0.
func writeArgsOMP(t *testing.T, argsPath string) string {
	t.Helper()
	omp := filepath.Join(filepath.Dir(argsPath), "fake-omp")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" > '" + argsPath + "'\n"
	if err := os.WriteFile(omp, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake omp: %v", err)
	}
	return omp
}

// writeSlowArgsOMP writes a fake OMP that sleeps before dumping its argv —
// enough to hold the PM session in flight across the next scan, mimicking
// the real 3-10 minute distribute sessions.
func writeSlowArgsOMP(t *testing.T, argsPath string) string {
	t.Helper()
	omp := filepath.Join(filepath.Dir(argsPath), "fake-omp-slow")
	script := "#!/bin/sh\nsleep 2\nprintf '%s\\n' \"$*\" > '" + argsPath + "'\n"
	if err := os.WriteFile(omp, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake omp: %v", err)
	}
	return omp
}

func withAPIKey(t *testing.T) {
	t.Helper()
	withAPIKeyValue(t, true)
}

// withAPIKeyValue pins the apiKeyProbe for the test. Tests that must NOT
// dispatch PM sessions (consolidate/stage-review fire on unstaged tasks
// even without a Stage-Plan now) pin false so the scan loop short-circuits
// instead of invoking the fake OMP as a PM session.
func withAPIKeyValue(t *testing.T, value bool) {
	t.Helper()
	oldProbe, _ := apiKeyProbe.Load().(func() bool)
	apiKeyProbe.Store(func() bool { return value })
	t.Cleanup(func() { apiKeyProbe.Store(oldProbe) })
}

func TestGrillingListPaused(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	notes := filepath.Join(vault, "Projects", "test", "Notes")
	if err := os.MkdirAll(notes, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(notes, "Grilling-Decisions.md")
	open := `---
id: "grilling-decisions"
project: test
status: open
---
# Decisions
- 决策: <用户填写>
`
	if err := os.WriteFile(path, []byte(open), 0o644); err != nil {
		t.Fatal(err)
	}
	if grillingListPaused(path) {
		t.Fatal("status=open should not be paused")
	}
	paused := `---
id: "grilling-decisions"
project: test
status: paused
---
# Decisions
- 决策: <用户填写>
`
	if err := os.WriteFile(path, []byte(paused), 0o644); err != nil {
		t.Fatal(err)
	}
	if !grillingListPaused(path) {
		t.Fatal("status=paused should be paused")
	}
	closed := `---
id: "grilling-decisions"
project: test
status: closed
---
# Decisions
`
	if err := os.WriteFile(path, []byte(closed), 0o644); err != nil {
		t.Fatal(err)
	}
	if !grillingListPaused(path) {
		t.Fatal("status=closed should be paused")
	}
	// User typo variant "pause" (seen in the field) must also suppress.
	typo := `---
id: "grilling-decisions"
project: test
status: pause
---
# Decisions
`
	if err := os.WriteFile(path, []byte(typo), 0o644); err != nil {
		t.Fatal(err)
	}
	if !grillingListPaused(path) {
		t.Fatal("status=pause (typo) should be paused")
	}
	if ok, err := activatePausedDecisionList(vault, "test"); !ok || err != nil {
		t.Fatalf("activate pause-typo list: ok=%v err=%v, want true,nil", ok, err)
	}
	if fm := mustParse(t, path); fm.Status != "open" {
		t.Fatalf("status after activation = %q, want open", fm.Status)
	}
	if grillingListPaused(filepath.Join(dir, "missing.md")) {
		t.Fatal("missing list should not be paused")
	}
}

func TestActivatePausedDecisionList(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	notes := filepath.Join(vault, "Projects", "002-magic-models-manager", "Notes")
	if err := os.MkdirAll(notes, 0o755); err != nil {
		t.Fatal(err)
	}
	listPath := filepath.Join(notes, "Grilling-Decisions.md")
	write := func(status string) {
		content := "---\nid: \"grilling-decisions\"\nproject: 002-magic-models-manager\nstatus: " + status + "\ngrill_continue: false\n---\n# Decisions\n- 决策: <用户填写>\n"
		if err := os.WriteFile(listPath, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// No list → false, nil.
	if ok, err := activatePausedDecisionList(vault, "no-such-project"); ok || err != nil {
		t.Fatalf("missing project: ok=%v err=%v, want false,nil", ok, err)
	}
	// open list → untouched.
	write("open")
	if ok, err := activatePausedDecisionList(vault, "002-magic-models-manager"); ok || err != nil {
		t.Fatalf("open list: ok=%v err=%v, want false,nil", ok, err)
	}
	if fm := mustParse(t, listPath); fm.Status != "open" {
		t.Fatalf("open list status changed to %q", fm.Status)
	}
	// paused list → reactivated (REQ update = user actively supplementing).
	write("paused")
	if ok, err := activatePausedDecisionList(vault, "002-magic-models-manager"); !ok || err != nil {
		t.Fatalf("paused list: ok=%v err=%v, want true,nil", ok, err)
	}
	if fm := mustParse(t, listPath); fm.Status != "open" {
		t.Fatalf("paused list status = %q after activation, want open", fm.Status)
	}
}

func TestProjectFromReqPath(t *testing.T) {
	cases := map[string]string{
		"Projects/002-magic-models-manager/Requirements/REQ-001.md": "002-magic-models-manager",
		"Requirements/REQ-001.md":                                   "",
		"Projects/001-release-manager/Requirements/REQ-009.md":      "001-release-manager",
	}
	for input, want := range cases {
		if got := projectFromReqPath(input); got != want {
			t.Errorf("projectFromReqPath(%q) = %q, want %q", input, got, want)
		}
	}
}

// TestResolveVaultProjectDirAcceptsPrefixedProject guards the decision-list
// distribute/reminder path: task frontmatter historically carries the full
// prefixed directory name ("002-magic-models-manager") while vault-map uses
// the unprefixed name — both must resolve to the same directory, otherwise
// grillingDecisionListPath returns "" and answers never distribute.
func TestResolveVaultProjectDirAcceptsPrefixedProject(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	for _, proj := range []string{"001-release-manager", "002-magic-models-manager", "003-obsidian-task-runner"} {
		if err := os.MkdirAll(filepath.Join(vault, "Projects", proj), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cases := map[string]string{
		"002-magic-models-manager": "002-magic-models-manager", // prefixed (legacy frontmatter)
		"magic-models-manager":     "002-magic-models-manager", // unprefixed vault-map name
		"release-manager":          "001-release-manager",
		"obsidian-task-runner":     "003-obsidian-task-runner",
		"no-such-project":          "",
	}
	for input, wantDir := range cases {
		got := resolveVaultProjectDir(vault, input)
		if wantDir == "" {
			if got != "" {
				t.Errorf("resolveVaultProjectDir(%q) = %q, want empty", input, got)
			}
			continue
		}
		want := filepath.Join(vault, "Projects", wantDir)
		if got != want {
			t.Errorf("resolveVaultProjectDir(%q) = %q, want %q", input, got, want)
		}
	}
}

func writeGrillingTask(t *testing.T, path, id, reqDoc, project string, parked bool, repeat int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create task dir: %v", err)
	}
	content := "---\n" +
		"id: \"" + id + "\"\n" +
		"title: T" + id + "\n" +
		"project: " + project + "\n" +
		"req_doc: " + reqDoc + "\n" +
		"status: needs-grilling\n" +
		"grill_done: false\n" +
		"grill_continue: false\n" +
		"grill_parked: " + boolStr(parked) + "\n" +
		"grill_repeat: " + strconv.Itoa(repeat) + "\n" +
		"---\n# T" + id + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write task %s: %v", path, err)
	}
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func writeDecisionList(t *testing.T, path string, answered bool) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir list dir: %v", err)
	}
	content := "---\n" +
		"id: \"grilling-decisions\"\n" +
		"project: test\n" +
		"status: open\n" +
		"grill_continue: " + boolStr(answered) + "\n" +
		"---\n# Grilling Decisions\n" +
		"\n## 决策点\n\n### D-1: test\n- 决策: <用户填写>\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write decision list: %v", err)
	}
}

func TestNeedsConsolidationGrouping(t *testing.T) {
	cases := []struct {
		name    string
		members []task.GrillingTask
		want    bool
	}{
		{
			name:    "empty group never consolidates",
			members: nil,
			want:    false,
		},
		{
			name:    "lone task below repeat threshold",
			members: []task.GrillingTask{{GrillRepeat: 1, GrillParked: false}},
			want:    false,
		},
		{
			name:    "lone task with repeat dispute consolidates",
			members: []task.GrillingTask{{GrillRepeat: 2, GrillParked: false}},
			want:    true,
		},
		{
			name:    "lone task with churning replans consolidates",
			members: []task.GrillingTask{{PlanVersion: 3, GrillRepeat: 1, GrillParked: false}},
			want:    true,
		},
		{
			name:    "lone task below both thresholds stays per-task",
			members: []task.GrillingTask{{PlanVersion: 2, GrillRepeat: 1, GrillParked: false}},
			want:    false,
		},
		{
			name:    "lone parked task does not re-consolidate",
			members: []task.GrillingTask{{GrillRepeat: 3, GrillParked: true}},
			want:    false,
		},
		{
			name: "shared req with un-parked member consolidates",
			members: []task.GrillingTask{
				{GrillRepeat: 1, GrillParked: false},
				{GrillRepeat: 1, GrillParked: false},
			},
			want: true,
		},
		{
			name: "fully parked shared group does not re-consolidate",
			members: []task.GrillingTask{
				{GrillRepeat: 2, GrillParked: true},
				{GrillRepeat: 2, GrillParked: true},
			},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := needsConsolidation(tc.members); got != tc.want {
				t.Fatalf("needsConsolidation = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestGrillingDecisionCounts guards the pending-decision parser: unfilled
// "决策:" lines and an unfilled split-confirmation line count as pending;
// answered ones do not. The daemon uses this to skip empty distribute
// round-trips when the user re-sets grill_continue=true on a fully answered
// list.
func TestGrillingDecisionCounts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Grilling-Decisions.md")
	content := `---
id: grilling-decisions
status: open
grill_continue: true
---

## 拆分确认

- 拆分: 确认

## 决策点

### D-1: REQ-001 — 问题
- 决策: 采纳方案 A

### D-2: REQ-002 — 问题
- 决策: <用户填写>

### D-3: REQ-003 — 问题
- 决策: 
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	total, pending := grillingDecisionCounts(path)
	if total != 4 { // 3 decision points + split confirmation
		t.Fatalf("total = %d, want 4", total)
	}
	if pending != 2 { // D-2 placeholder + D-3 empty
		t.Fatalf("pending = %d, want 2", pending)
	}

	// Fully answered: pending must be 0.
	content = `---
id: grilling-decisions
status: open
grill_continue: true
---

## 拆分确认

- 拆分: 不拆分

## 决策点

### D-1: REQ-001 — 问题
- 决策: 采纳方案 A
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	total, pending = grillingDecisionCounts(path)
	if total != 2 || pending != 0 {
		t.Fatalf("fully answered: total=%d pending=%d, want 2/0", total, pending)
	}
}

// TestDecisionAnsweredPlaceholderVariants guards placeholder-lenient
// matching: PM templates write `{用户填写}`, older revisions `<用户填写>`,
// and field copies render `（用户填写）` — all must count as UNANSWERED.
// Otherwise a fully-placeholder list reports pending=0, the decision tab is
// never opened, and every scan auto-distributes an empty batch (observed:
// release-manager Grilling-Decisions.md grew 1800+ no-op distribute logs
// and never opened its decision tab for 24h+).
func TestDecisionAnsweredPlaceholderVariants(t *testing.T) {
	for _, v := range []string{
		"",
		" ",
		"（用户填写）",
		"(用户填写)",
		"<用户填写>",
		"{用户填写}",
		"  用户填写  ",
		"确认 / 修改（列出修改）/ 不拆分",
		"继续 / supplement:{建议} / end",
	} {
		if decisionAnswered(v) {
			t.Errorf("decisionAnswered(%q) = true, want false (placeholder)", v)
		}
	}
	for _, v := range []string{
		"按建议方向采纳：不豁免（2026-08-04 确认）",
		"选 A：TASK-071 缩小为集成任务",
		"continue",
		"不拆分",
	} {
		if !decisionAnswered(v) {
			t.Errorf("decisionAnswered(%q) = false, want true (real answer)", v)
		}
	}
}

// TestFullyAnsweredButUndistributedStillDispatches guards the write-back
// path: a list whose decisions are all filled but whose frontmatter status
// is still open (never distributed) must dispatch once — otherwise the
// user's answers silently never reach the REQs.
func TestFullyAnsweredButUndistributedStillDispatches(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	tasksDir := filepath.Join(vault, "Projects", "001-test", "Tasks")
	writeGrillingTask(t, filepath.Join(tasksDir, "TASK-025.md"), "025", "Projects/001-test/Requirements/REQ-025.md", "test", true, 3)
	listPath := filepath.Join(vault, "Projects", "001-test", "Notes", "Grilling-Decisions.md")
	content := `---
id: "grilling-decisions"
project: test
status: open
grill_continue: true
---

## 决策点

### D-1: REQ-025 — 问题
- 决策: 采纳方案 A
`
	if err := os.MkdirAll(filepath.Dir(listPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(listPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	argsPath := filepath.Join(dir, "pm-args")
	omp := writeArgsOMP(t, argsPath)
	runner := &Runner{
		cfg: &config.Config{
			OMPCmd:              omp,
			ObsidianVault:       vault,
			PhaseTimeoutMinutes: map[string]int{"refining": 1},
			Models:              config.DefaultModels(),
		},
		logger: log.New(io.Discard, "", 0),
	}
	if n := runner.processGrillingConsolidation(context.Background()); n != 1 {
		t.Fatalf("processed = %d, want 1 (fully answered but undistributed must dispatch)", n)
	}
	if args := waitForPmArgs(t, argsPath); !strings.Contains(args, "distribute") {
		t.Fatalf("pm args = %q, want distribute prompt", args)
	}
}

// TestDistributeInFlightDedup guards against concurrent PM distribute
// sessions for the same list. A distribute takes minutes and the
// changed-since-distribute signal stays true until the session records the
// answer hash — without the in-flight dedup every scan re-dispatches and
// stacks concurrent sessions (observed: 5 distribute processes on one
// release-manager list within 4 minutes, 2026-08-07).
func TestDistributeInFlightDedup(t *testing.T) {
	dir := t.TempDir()
	withAPIKey(t)
	vault := filepath.Join(dir, "vault")
	tasksDir := filepath.Join(vault, "Projects", "001-test", "Tasks")
	writeGrillingTask(t, filepath.Join(tasksDir, "TASK-025.md"), "025", "Projects/001-test/Requirements/REQ-025.md", "test", true, 3)
	listPath := filepath.Join(vault, "Projects", "001-test", "Notes", "Grilling-Decisions.md")
	content := `---
id: "grilling-decisions"
project: test
status: open
grill_continue: false
---

## 决策点

### D-1: REQ-025 — 问题
- 决策: 采纳方案 A
`
	if err := os.MkdirAll(filepath.Dir(listPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(listPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	argsPath := filepath.Join(dir, "pm-args")
	omp := writeSlowArgsOMP(t, argsPath)
	runner := &Runner{
		cfg: &config.Config{
			OMPCmd:              omp,
			ObsidianVault:       vault,
			PhaseTimeoutMinutes: map[string]int{"refining": 1},
			Models:              config.DefaultModels(),
		},
		logger: log.New(io.Discard, "", 0),
	}
	// First scan dispatches; the session is still in flight.
	if n := runner.processGrillingConsolidation(context.Background()); n != 1 {
		t.Fatalf("first processed = %d, want 1", n)
	}
	// Second scan while the session runs must NOT stack another dispatch.
	if n := runner.processGrillingConsolidation(context.Background()); n != 0 {
		t.Fatalf("in-flight processed = %d, want 0 (dedup)", n)
	}
	// The single dispatched session completes and its argv lands.
	args := waitForPmArgs(t, argsPath)
	if !strings.Contains(args, "distribute") {
		t.Fatalf("pm args = %q, want distribute prompt", args)
	}
}

// TestConsolidateInFlightDedup guards the consolidate path against the same
// concurrent-session storm as distribute: a fresh dispute (unparked member)
// bypasses the 4h cooldown every scan, so without the in-flight dedup each
// scan stacks another consolidate session while the first runs.
func TestConsolidateInFlightDedup(t *testing.T) {
	dir := t.TempDir()
	withAPIKey(t)
	vault := filepath.Join(dir, "vault")
	tasksDir := filepath.Join(vault, "Projects", "001-test", "Tasks")
	// Single task with a repeat dispute (grill_repeat>=2, unparked) is a
	// fresh dispute → needsConsolidation without relying on a shared req_doc.
	writeGrillingTask(t, filepath.Join(tasksDir, "TASK-030.md"), "030", "Projects/001-test/Requirements/REQ-030.md", "test", false, 3)
	argsPath := filepath.Join(dir, "pm-args")
	omp := writeSlowArgsOMP(t, argsPath)
	runner := &Runner{
		cfg: &config.Config{
			OMPCmd:              omp,
			ObsidianVault:       vault,
			PhaseTimeoutMinutes: map[string]int{"refining": 1},
			Models:              config.DefaultModels(),
		},
		logger: log.New(io.Discard, "", 0),
	}
	// First scan dispatches; the session is still in flight.
	if n := runner.processGrillingConsolidation(context.Background()); n != 1 {
		t.Fatalf("first processed = %d, want 1", n)
	}
	// Second scan while the session runs must NOT stack another dispatch.
	if n := runner.processGrillingConsolidation(context.Background()); n != 0 {
		t.Fatalf("in-flight processed = %d, want 0 (dedup)", n)
	}
	args := waitForPmArgs(t, argsPath)
	if !strings.Contains(args, "consolidate") {
		t.Fatalf("pm args = %q, want consolidate prompt", args)
	}
}

// TestPartialBatchTailAutoDispatches guards the changed-since-distribute
// signal: after a first distribute (status=answered), the user fills the
// remaining decisions — the daemon must auto-dispatch again even though the
// manual flag was never re-set.
func TestPartialBatchTailAutoDispatches(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	tasksDir := filepath.Join(vault, "Projects", "001-test", "Tasks")
	writeGrillingTask(t, filepath.Join(tasksDir, "TASK-025.md"), "025", "Projects/001-test/Requirements/REQ-025.md", "test", true, 3)
	listPath := filepath.Join(vault, "Projects", "001-test", "Notes", "Grilling-Decisions.md")
	// answered (already distributed) + remaining pending decision.
	content := `---
id: "grilling-decisions"
project: test
status: answered
grill_continue: false
last_distributed_at: 2026-08-05T10:00:00+08:00
---

## 决策点

### D-1: REQ-025 — 问题
- 决策: 采纳方案 A

### D-2: REQ-025 — 问题2
- 决策: <用户填写>
`
	if err := os.MkdirAll(filepath.Dir(listPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(listPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	argsPath := filepath.Join(dir, "pm-args")
	omp := writeArgsOMP(t, argsPath)
	runner := &Runner{
		cfg: &config.Config{
			OMPCmd:              omp,
			ObsidianVault:       vault,
			PhaseTimeoutMinutes: map[string]int{"refining": 1},
			Models:              config.DefaultModels(),
		},
		logger: log.New(io.Discard, "", 0),
	}
	// Before the user edits: pending>0, no auto dispatch.
	if n := runner.processGrillingConsolidation(context.Background()); n != 0 {
		t.Fatalf("pre-edit processed = %d, want 0 (pending answers, no manual flag)", n)
	}
	// User fills D-2 → file mtime now after last_distributed_at.
	content = strings.Replace(content, "决策: <用户填写>", "决策: 采纳方案 B", 1)
	if err := os.WriteFile(listPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if n := runner.processGrillingConsolidation(context.Background()); n != 1 {
		t.Fatalf("post-edit processed = %d, want 1 (auto dispatch on fully answered + changed)", n)
	}
	if args := waitForPmArgs(t, argsPath); !strings.Contains(args, "distribute") {
		t.Fatalf("pm args = %q, want distribute prompt", args)
	}
}

// TestGrillingAnswersHashIgnoresNonAnswerEdits guards the precision of the
// change signal: comment/log-line edits and timestamp refreshes must NOT
// change the answer hash (mtime-based detection would re-dispatch); only
// actual answer changes move it.
func TestGrillingAnswersHashIgnoresNonAnswerEdits(t *testing.T) {
	base := `---
id: grilling-decisions
status: open
---

## 决策点

### D-1: REQ-001 — 问题
- 决策: 采纳方案 A

### D-2: REQ-002 — 问题
- 决策: <用户填写>
`
	h1 := grillingAnswersHash(base)

	// Non-answer edits: log line above the decision region, frontmatter
	// timestamp, trailing whitespace on a non-answer line.
	edited := strings.Replace(base, "---\n", "---\nlast_distributed_at: 2026-08-05T10:00:00+08:00\n", 1)
	edited = strings.Replace(edited, "\n## 决策点", "\n> 2026-08-05T10:00:00 追加日志行\n\n## 决策点", 1)
	if grillingAnswersHash(edited) != h1 {
		t.Fatal("non-answer edits must not change the answer hash")
	}

	// Real answer change: D-2 filled.
	answered := strings.Replace(base, "决策: <用户填写>", "决策: 采纳方案 B", 1)
	if grillingAnswersHash(answered) == h1 {
		t.Fatal("filling an answer must change the answer hash")
	}

	// Split-confirmation answer is part of the hash (bounded to the
	// "## 拆分确认" section).
	withSplit := strings.Replace(base, "## 决策点", "## 拆分确认\n\n- 拆分: 确认\n\n## 决策点", 1)
	if grillingAnswersHash(withSplit) == h1 {
		t.Fatal("split-confirmation answer must change the answer hash")
	}
	// A "- 拆分:" line OUTSIDE the 拆分确认 section must NOT leak into the
	// hash (decision-body text stays inert).
	bodyLeak := strings.Replace(base, "- 决策: 采纳方案 A", "- 拆分: 误入正文\n- 决策: 采纳方案 A", 1)
	if grillingAnswersHash(bodyLeak) != h1 {
		t.Fatal("decision-body 拆分 line must not change the answer hash")
	}
}

func TestGrillingListAnsweredDetection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Grilling-Decisions.md")

	writeDecisionList(t, path, false)
	if grillingListAnswered(path) {
		t.Fatal("unanswered list reported as answered")
	}

	writeDecisionList(t, path, true)
	if !grillingListAnswered(path) {
		t.Fatal("answered list reported as unanswered")
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if grillingListAnswered(path) {
		t.Fatal("missing list reported as answered")
	}
}

func TestProcessGrillingConsolidationDispatchesConsolidate(t *testing.T) {
	dir := t.TempDir()
	withAPIKey(t)
	vault := filepath.Join(dir, "vault")
	tasksDir := filepath.Join(vault, "Projects", "001-test", "Tasks")
	writeGrillingTask(t, filepath.Join(tasksDir, "TASK-012.md"), "012", "Projects/001-test/Requirements/REQ-012.md", "test", false, 1)
	writeGrillingTask(t, filepath.Join(tasksDir, "TASK-074.md"), "074", "Projects/001-test/Requirements/REQ-012.md", "test", false, 1)

	argsPath := filepath.Join(dir, "pm-args")
	omp := writeArgsOMP(t, argsPath)
	runner := &Runner{
		cfg: &config.Config{
			OMPCmd:              omp,
			ObsidianVault:       vault,
			PhaseTimeoutMinutes: map[string]int{"refining": 1},
			Models:              config.DefaultModels(),
		},
		logger: log.New(io.Discard, "", 0),
	}

	processed := runner.processGrillingConsolidation(context.Background())
	if processed != 1 {
		t.Fatalf("processed = %d, want 1", processed)
	}
	args := waitForPmArgs(t, argsPath)
	if !strings.Contains(args, "/obsidian-task-runner-pm consolidate") {
		t.Fatalf("pm args = %q, want consolidate prompt", args)
	}
	if !strings.Contains(args, "TASK-012") || !strings.Contains(args, "TASK-074") {
		t.Fatalf("pm args = %q, want both task paths", args)
	}
}

// waitForPmArgs polls for the async PM session to write its argv capture.
func waitForPmArgs(t *testing.T, argsPath string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(argsPath); err == nil {
			return string(data)
		}
		time.Sleep(10 * time.Millisecond)
	}
	data, _ := os.ReadFile(argsPath)
	t.Fatalf("pm args never appeared at %s", argsPath)
	return string(data)
}

func TestProcessGrillingConsolidationDistributesAnsweredList(t *testing.T) {
	dir := t.TempDir()
	withAPIKey(t)
	vault := filepath.Join(dir, "vault")
	tasksDir := filepath.Join(vault, "Projects", "001-test", "Tasks")
	writeGrillingTask(t, filepath.Join(tasksDir, "TASK-025.md"), "025", "Projects/001-test/Requirements/REQ-025.md", "test", true, 3)
	writeDecisionList(t, filepath.Join(vault, "Projects", "001-test", "Notes", "Grilling-Decisions.md"), true)

	argsPath := filepath.Join(dir, "pm-args")
	omp := writeArgsOMP(t, argsPath)
	runner := &Runner{
		cfg: &config.Config{
			OMPCmd:              omp,
			ObsidianVault:       vault,
			PhaseTimeoutMinutes: map[string]int{"refining": 1},
			Models:              config.DefaultModels(),
		},
		logger: log.New(io.Discard, "", 0),
	}

	processed := runner.processGrillingConsolidation(context.Background())
	if processed != 1 {
		t.Fatalf("processed = %d, want 1", processed)
	}
	args := waitForPmArgs(t, argsPath)
	if !strings.Contains(args, "/obsidian-task-runner-pm distribute") {
		t.Fatalf("pm args = %q, want distribute prompt", args)
	}
	if !strings.Contains(args, "Grilling-Decisions.md") {
		t.Fatalf("pm args = %q, want decision list path", args)
	}
}

func TestProcessGrillingConsolidationSkipsFullyParkedGroup(t *testing.T) {
	dir := t.TempDir()
	withAPIKey(t)
	vault := filepath.Join(dir, "vault")
	tasksDir := filepath.Join(vault, "Projects", "001-test", "Tasks")
	writeGrillingTask(t, filepath.Join(tasksDir, "TASK-012.md"), "012", "Projects/001-test/Requirements/REQ-012.md", "test", true, 2)
	writeGrillingTask(t, filepath.Join(tasksDir, "TASK-074.md"), "074", "Projects/001-test/Requirements/REQ-012.md", "test", true, 2)

	argsPath := filepath.Join(dir, "pm-args")
	omp := writeArgsOMP(t, argsPath)
	runner := &Runner{
		cfg: &config.Config{
			OMPCmd:              omp,
			ObsidianVault:       vault,
			PhaseTimeoutMinutes: map[string]int{"refining": 1},
			Models:              config.DefaultModels(),
		},
		logger: log.New(io.Discard, "", 0),
	}

	processed := runner.processGrillingConsolidation(context.Background())
	if processed != 0 {
		t.Fatalf("processed = %d, want 0 (fully parked group waits for answers)", processed)
	}
	if _, err := os.Stat(argsPath); !os.IsNotExist(err) {
		t.Fatal("PM session must not be dispatched for a fully parked group")
	}
}

func TestParkedTaskIsNotDispatched(t *testing.T) {
	dir := t.TempDir()
	omp, _, _ := writeBarrierOMP(t, dir)

	vault := filepath.Join(dir, "vault")
	tasksDir := filepath.Join(vault, "Projects", "001-test", "Tasks")
	writeGrillingTask(t, filepath.Join(tasksDir, "TASK-025.md"), "025", "Projects/001-test/Requirements/REQ-025.md", "test", true, 2)

	runner := &Runner{
		cfg: &config.Config{
			OMPCmd:             omp,
			ObsidianVault:      vault,
			MaxConcurrentTasks: 2,
			Models:             config.DefaultModels(),
		},
		logger: log.New(io.Discard, "", 0),
	}

	pending := runner.prepareBatch([]task.ReadyTask{{ID: "025", Project: "test", ReqDoc: "Projects/001-test/Requirements/REQ-025.md", FilePath: filepath.Join(tasksDir, "TASK-025.md"), Status: "needs-grilling", GrillParked: true, GrillContinue: false, Assignee: "default"}})
	if len(pending) != 0 {
		t.Fatalf("parked task entered dispatch queue: %d pending", len(pending))
	}
}
