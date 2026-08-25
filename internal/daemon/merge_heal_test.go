package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndzuki/obsidian-task-runner/internal/task"
	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

// TestHealTargetBranch 覆盖 TASK-079 的自愈路径：round2 会话被 daemon 重启
// 打断后 target_branch 没写回，merge 授权永远卡在 precondition。任务 worktree
// 已 checkout 在 task/{id}-slug 分支上——healTargetBranch 应从它恢复分支名并
// 持久化，且只在分支带 "task/" 前缀时生效（不误写 main 等无关分支）。
func TestHealTargetBranch(t *testing.T) {
	base := t.TempDir()
	repoDir := filepath.Join(base, "repo")
	wtRoot := filepath.Join(base, ".otg-worktrees")
	run := func(dir string, args ...string) string {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	_ = os.MkdirAll(repoDir, 0o755)
	run(repoDir, "init", "-q", "-b", "main")
	run(repoDir, "config", "user.email", "t@t")
	run(repoDir, "config", "user.name", "t")
	// 全局 gpg 签名配置会在无密钥环境下使 commit 失败（见 detect_stale_done_test.go）。
	run(repoDir, "config", "commit.gpgsign", "false")
	run(repoDir, "config", "gpg.format", "")
	run(repoDir, "config", "user.signingkey", "")
	_ = os.WriteFile(filepath.Join(repoDir, "f.txt"), []byte("x"), 0o644)
	run(repoDir, "add", ".")
	run(repoDir, "commit", "-q", "-m", "init")

	taskPath := filepath.Join(base, "TASK-079-heal.md")
	content := "---\nid: \"079\"\nproject: test\nstatus: review\nmerge_approved: true\ntarget_branch: \"\"\n---\n# 079\n"
	if err := os.WriteFile(taskPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	candidate := task.ReadyTask{ID: "079", Project: "test", FilePath: taskPath}

	wtPath := taskWorktreePath(wtRoot, repoDir, taskRunKey(taskPath))
	_ = os.MkdirAll(filepath.Dir(wtPath), 0o755)
	run(repoDir, "worktree", "add", "-b", "task/079-heal", wtPath, "HEAD")

	// 1) 空 target_branch + task/ 分支 → 自愈写回。
	fm := &yamlfrontmatter.Frontmatter{}
	if !healTargetBranch(wtRoot, repoDir, candidate, fm) {
		t.Fatalf("healTargetBranch = false, want true (worktree on task/079-heal)")
	}
	if fm.TargetBranch != "task/079-heal" {
		t.Fatalf("fm.TargetBranch = %q, want task/079-heal", fm.TargetBranch)
	}
	parsed, err := yamlfrontmatter.Parse(mustRead(t, taskPath))
	if err != nil {
		t.Fatalf("reparse task: %v", err)
	}
	if parsed.TargetBranch != "task/079-heal" {
		t.Fatalf("persisted target_branch = %q, want task/079-heal", parsed.TargetBranch)
	}

	// 2) 已有 target_branch → 不覆盖（幂等）。
	if healTargetBranch(wtRoot, repoDir, candidate, fm) {
		t.Fatalf("healTargetBranch = true with target_branch already set, want false")
	}

	// 3) worktree 分支不是 task/ 前缀 → 不写回。
	_ = yamlfrontmatter.Update(taskPath, map[string]interface{}{"target_branch": ""})
	run(repoDir, "branch", "other", "main")
	run(wtPath, "checkout", "-q", "other")
	fm2 := &yamlfrontmatter.Frontmatter{}
	if healTargetBranch(wtRoot, repoDir, candidate, fm2) {
		t.Fatalf("healTargetBranch = true for branch %q, want false (non-task/ branch)", "other")
	}
	if fm2.TargetBranch != "" {
		t.Fatalf("fm.TargetBranch = %q, want empty", fm2.TargetBranch)
	}

	// 4) worktree 不存在 → false。
	run(repoDir, "worktree", "remove", "--force", wtPath)
	run(repoDir, "worktree", "prune")
	fm3 := &yamlfrontmatter.Frontmatter{}
	if healTargetBranch(wtRoot, repoDir, candidate, fm3) {
		t.Fatalf("healTargetBranch = true without worktree, want false")
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
