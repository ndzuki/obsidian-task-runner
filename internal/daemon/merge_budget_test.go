package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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

	runner := newTestRunner(t.TempDir(), "omp", filepath.Join(dir, "logs"), 1)

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
