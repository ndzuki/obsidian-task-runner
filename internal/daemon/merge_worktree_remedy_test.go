package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ndzuki/obsidian-task-runner/internal/task"
	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

// TestMergeRetryCooling pins the human-repair retry cooldown: while the
// worktree conflict needs a human fix, the daemon must NOT re-attempt the
// merge every scan (TASK-080: ~10s log spam + 5min desktop toast loop).
// Empty/corrupt timestamps must not block — legacy tasks retry immediately.
func TestMergeRetryCooling(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name      string
		notBefore string
		want      bool
	}{
		{"future blocks", now.Add(time.Hour).Format(time.RFC3339), true},
		{"past does not block", now.Add(-time.Hour).Format(time.RFC3339), false},
		{"empty does not block", "", false},
		{"corrupt does not block", "not-a-timestamp", false},
	}
	for _, tt := range tests {
		if got := mergeRetryCooling(tt.notBefore, now); got != tt.want {
			t.Fatalf("mergeRetryCooling(%q) = %v, want %v", tt.notBefore, got, tt.want)
		}
	}
}

// TestMergeWorktreeRemedy pins the human-repair instructions written into the
// task document (phase_error): they must carry an actionable command for the
// exact conflict shape — never a destructive one for the primary checkout.
func TestMergeWorktreeRemedy(t *testing.T) {
	repoDir := "/home/user/src/repos/github.com/ndzuki/release-manager"

	// 主 checkout 占用任务分支：指引切走，绝不建议删除。
	primary := mergeWorktreeRemedy(repoDir, repoDir)
	if !strings.Contains(primary, "switch") {
		t.Fatalf("primary-checkout remedy must suggest git switch: %q", primary)
	}
	if strings.Contains(primary, "worktree remove") {
		t.Fatalf("primary-checkout remedy must never suggest worktree remove: %q", primary)
	}

	// 其它 worktree 占用：给出带确认前提的删除命令（用户自己的目录）。
	occupied := "/home/user/src/repos/github.com/ndzuki/release-manager-t080"
	other := mergeWorktreeRemedy(repoDir, occupied)
	if !strings.Contains(other, "worktree remove --force "+occupied) {
		t.Fatalf("occupied remedy must name the exact command: %q", other)
	}
	if !strings.Contains(other, "确认") {
		t.Fatalf("occupied remedy must carry a confirmation caveat: %q", other)
	}

	// 无占用路径可解析：给出诊断命令。
	generic := mergeWorktreeRemedy(repoDir, "")
	if !strings.Contains(generic, "worktree list") {
		t.Fatalf("generic remedy must suggest git worktree list: %q", generic)
	}
}

// TestBatchMergeCoolingSkipsMergeDispatch pins the scan-level behavior: a
// mergeable task inside the human-repair cooldown must NOT be consumed by the
// merge path (no dispatch, no log spam) — the batch leaves it for the next
// scan. Without the cooling gate the task would be consumed and the merge
// attempted (and fail) every scan.
func TestBatchMergeCoolingSkipsMergeDispatch(t *testing.T) {
	dir := t.TempDir()
	repoDir := filepath.Join(dir, "repo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatalf("create repo dir: %v", err)
	}
	skillDir := writeVaultMap(t, dir, map[string]string{"demo": repoDir})
	runner := newAuditRunner(t, skillDir, filepath.Join(dir, "no-such-dsh"), filepath.Join(dir, "logs"))
	taskPath := writeTaskFile(t, dir, "TASK-COOL.md", "review")
	if err := yamlfrontmatter.Update(taskPath, map[string]interface{}{
		"merge_retry_not_before": time.Now().Add(time.Hour).Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("persist merge cooldown: %v", err)
	}
	done := runBatch(runner, []task.ReadyTask{{
		ID:                  "COOL",
		Title:               "Cooling task",
		Project:             "demo",
		FilePath:            taskPath,
		Status:              "review",
		AutoMerge:           false,
		MergeApproved:       true,
		TargetBranch:        "task/cool",
		MergeRetryNotBefore: time.Now().Add(time.Hour).Format(time.RFC3339),
	}})
	if processed := waitForBatch(t, done); processed != 0 {
		t.Fatalf("processed = %d, want 0 (merge cooling must skip dispatch)", processed)
	}
}

func TestMergeWorktreeFailureUpdates(t *testing.T) {
	now := time.Date(2026, 9, 2, 15, 0, 0, 0, time.UTC)
	taskPath := "/home/user/my vault/Projects/demo/Tasks/TASK-080.md"
	repoDir := "/home/user/src/repos/github.com/ndzuki/release-manager"
	occupied := repoDir
	cause := errors.New("branch is already used by the primary checkout")

	updates := mergeWorktreeFailureUpdates(taskPath, "task/080-operator-inventory-sync-tls", repoDir, occupied, cause, now)
	if got, want := updates["phase_error_code"], string(ErrBranchOwnershipConflict); got != want {
		t.Fatalf("phase_error_code = %v, want %v", got, want)
	}
	message, ok := updates["phase_error"].(string)
	if !ok {
		t.Fatalf("phase_error type = %T, want string", updates["phase_error"])
	}
	for _, want := range []string{
		"task/080-operator-inventory-sync-tls",
		"git -C " + repoDir + " switch main",
		"otg update-status '" + taskPath + "' merge_retry_not_before=",
	} {
		if !strings.Contains(message, want) {
			t.Fatalf("phase_error missing %q: %s", want, message)
		}
	}
	notBefore, ok := updates["merge_retry_not_before"].(string)
	if !ok || !mergeRetryCooling(notBefore, now) {
		t.Fatalf("merge_retry_not_before = %v, want a future RFC3339 deadline", updates["merge_retry_not_before"])
	}
}
