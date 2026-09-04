package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndzuki/obsidian-task-runner/internal/task"
	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

// TestClearMergeRepairBudget guards the budget-reset contract: a successful
// planning round clears merge_retry_count (fresh delivery intent, TASK-067:
// replan must not inherit the previous delivery's exhausted repair budget),
// while other phases leave it untouched.
func TestClearMergeRepairBudget(t *testing.T) {
	dir := t.TempDir()
	taskPath := filepath.Join(dir, "TASK-067.md")
	content := "---\nid: \"067\"\ntitle: Op\nstatus: planning\nmerge_retry_count: 3\n---\n# TASK-067\n"
	if err := os.WriteFile(taskPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	runner := newTestRunner(t.TempDir(), "dsh", filepath.Join(dir, "logs"), 1)

	// Planning round completes → budget cleared.
	runner.clearMergeRepairBudget(taskPath, "planning")
	if got := readFrontmatterField(t, taskPath, "merge_retry_count"); got != "0" {
		t.Fatalf("planning completion: merge_retry_count = %q, want 0", got)
	}

	// Non-planning phase completion leaves the budget alone.
	if err := os.WriteFile(taskPath, []byte("---\nid: \"067\"\ntitle: Op\nstatus: implementing\nmerge_retry_count: 2\n---\n# TASK-067\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner.clearMergeRepairBudget(taskPath, "round2")
	if got := readFrontmatterField(t, taskPath, "merge_retry_count"); got != "2" {
		t.Fatalf("round2 completion must not touch budget: merge_retry_count = %q, want 2", got)
	}
}

// TestAutoResolveMergeConflictBudgetExhaustedDebounces pins the notification
// contract: budget-exhausted conflict handoff must go through notifyFailure
// (per-task 5min window) instead of a bare SendTaskAction. The user can clear
// merge_retry_count to continue AI repair, which re-authorizes and re-runs
// the merge every scan — without the window each round re-toasts
// (TASK-067 notification storm).
func TestAutoResolveMergeConflictBudgetExhaustedDebounces(t *testing.T) {
	dir := t.TempDir()
	taskPath := filepath.Join(dir, "TASK-067.md")
	content := "---\nid: \"067\"\ntitle: Op\nstatus: review\nmerge_approved: true\nmerge_retry_count: 3\n---\n# TASK-067\n"
	if err := os.WriteFile(taskPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	runner := newTestRunner(t.TempDir(), "dsh", filepath.Join(dir, "logs"), 1)
	candidate := task.ReadyTask{
		ID: "067", Title: "Op", FilePath: taskPath,
		Status: "review", MergeApproved: true,
	}
	fm, err := yamlfrontmatter.Parse([]byte(content))
	if err != nil || fm == nil {
		t.Fatalf("parse task: %v", err)
	}
	fm.MergeRetryCount = 3 // exhausted: MaxAutoMergeFixes default is 3

	// First handoff toasts and records the debounce entry.
	if err := runner.autoResolveMergeConflict(candidate, "", fm, "PR has merge conflicts"); err != nil {
		t.Fatalf("autoResolveMergeConflict: %v", err)
	}
	entry1, ok := runner.failNotifyAt.Load(taskPath)
	if !ok {
		t.Fatal("budget-exhausted handoff must record a notifyFailure entry")
	}
	// Task must be conflict + unauthorized + conflict-resolve-attempted.
	if got := readFrontmatterField(t, taskPath, "status"); got != "conflict" {
		t.Fatalf("status = %q, want conflict", got)
	}
	if got := readFrontmatterField(t, taskPath, "merge_status"); got != "conflict-resolve-attempted" {
		t.Fatalf("merge_status = %q, want conflict-resolve-attempted", got)
	}

	// Second handoff inside the window (same task, same priority) must NOT
	// re-toast: the entry timestamp stays unchanged.
	if err := runner.autoResolveMergeConflict(candidate, "", fm, "PR has merge conflicts"); err != nil {
		t.Fatalf("second autoResolveMergeConflict: %v", err)
	}
	entry2, _ := runner.failNotifyAt.Load(taskPath)
	if !entry2.(failNotifyEntry).at.Equal(entry1.(failNotifyEntry).at) {
		t.Fatal("second handoff inside the debounce window re-toasted (entry timestamp changed)")
	}
}

func readFrontmatterField(t *testing.T, path, key string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, key+":") {
			return strings.TrimSpace(strings.TrimPrefix(line, key+":"))
		}
	}
	t.Fatalf("field %q not found in %s", key, path)
	return ""
}
