package daemon

import (
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndzuki/obsidian-task-runner/internal/config"
)

// makeTaskWorktree 在 <repo parent>/.otg-worktrees/<repoHash>/TASK-<runkey>
// 创建真实 git worktree（复用 taskWorktreePath，与 ensureTaskWorktree 的
// 命名/位置约定一致）。
func makeTaskWorktree(t *testing.T, repoDir, taskPath string) string {
	t.Helper()
	wtPath := taskWorktreePath("", repoDir, taskRunKey(taskPath))
	if err := os.MkdirAll(filepath.Dir(wtPath), 0o755); err != nil {
		t.Fatalf("mkdir worktree parent: %v", err)
	}
	if out, err := exec.Command("git", "-C", repoDir, "worktree", "add", "-b", "task/"+filepath.Base(taskPath), wtPath).CombinedOutput(); err != nil {
		t.Fatalf("git worktree add %s: %v: %s", wtPath, err, out)
	}
	return wtPath
}

// writeTaskWithFM 写入带完整 frontmatter 的任务文件（fm 须含 status 行）。
func writeTaskWithFM(t *testing.T, dir, name, fm string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir tasks: %v", err)
	}
	path := filepath.Join(dir, name)
	// ParseTaskDocument 要求 id/status/project/req_doc 齐全，否则解析失败
	// 会被清理逻辑误判为孤儿。
	content := "---\nid: test\nproject: demo\nreq_doc: Projects/demo/REQ-001.md\n" + fm + "---\n# Task\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write task: %v", err)
	}
	return path
}

// TestCleanupOrphanWorktrees 覆盖回收判据：
//   - closed 终态 → 回收
//   - done+merged（已合并交付）→ 回收
//   - 孤儿（任务文件不存在）→ 回收
//   - done 未合并（可能重开 merge）→ 保留
//   - 非终态活跃任务 → 保留
//   - taskRuns 调度保护 → 保留
//
// 回归背景：worktree 从不清理，8/14 累积 1052 个、4GB。
func TestCleanupOrphanWorktrees(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir) // createRepository 等 helper 依赖隔离的 HOME
	vault := filepath.Join(dir, "vault")
	tasksDir := filepath.Join(vault, "Projects", "demo", "Tasks")
	repo := createRepository(t, dir)

	closedPath := writeTaskWithFM(t, tasksDir, "TASK-001-closed.md", "status: closed\n")
	mergedPath := writeTaskWithFM(t, tasksDir, "TASK-002-merged.md", "status: done\nmerge_status: merged\n")
	donePath := writeTaskWithFM(t, tasksDir, "TASK-003-done.md", "status: done\n")         // 未合并 → 保留
	livePath := writeTaskWithFM(t, tasksDir, "TASK-004-live.md", "status: implementing\n") // 活跃 → 保留
	orphanKey := taskRunKey(filepath.Join(vault, "Tasks", "TASK-999-removed.md"))          // 无任务文件 → 回收

	closedWT := makeTaskWorktree(t, repo, closedPath)
	mergedWT := makeTaskWorktree(t, repo, mergedPath)
	doneWT := makeTaskWorktree(t, repo, donePath)
	liveWT := makeTaskWorktree(t, repo, livePath)
	orphanWT := taskWorktreePath("", repo, orphanKey)
	if err := os.MkdirAll(filepath.Dir(orphanWT), 0o755); err != nil {
		t.Fatalf("mkdir orphan parent: %v", err)
	}
	if out, err := exec.Command("git", "-C", repo, "worktree", "add", "-b", "task/orphan", orphanWT).CombinedOutput(); err != nil {
		t.Fatalf("git worktree add orphan: %v: %s", err, out)
	}

	runner := New(&config.Config{
		ObsidianVault: vault,
		// cleanupOrphanWorktrees 只回收当前 vault 配置仓库的 worktree
		//（allowedRepos 白名单）；测试必须登记仓库路径。
		Projects: []config.Project{{Name: "demo", Path: repo}},
	})
	runner.logger = log.New(io.Discard, "", 0)

	// taskRuns 调度保护：closed 任务虽终态，但调度中 → 本轮保留。
	runner.taskRuns.Store(taskRunKey(closedPath), struct{}{})
	runner.cleanupOrphanWorktrees()

	for name, wt := range map[string]string{
		"closed": closedWT,
		"merged": mergedWT,
		"orphan": orphanWT,
		"done":   doneWT,
		"live":   liveWT,
	} {
		_, err := os.Stat(wt)
		switch name {
		case "merged", "orphan":
			if err == nil {
				t.Errorf("%s worktree must be reclaimed, still exists: %s", name, wt)
			}
		default:
			if err != nil {
				t.Errorf("%s worktree must be kept, removed: %s", name, wt)
			}
		}
	}

	// 移除调度保护后，closed 任务 worktree 被回收。
	runner.taskRuns.Delete(taskRunKey(closedPath))
	runner.cleanupOrphanWorktrees()
	if _, err := os.Stat(closedWT); err == nil {
		t.Errorf("closed worktree must be reclaimed after dispatch protection dropped: %s", closedWT)
	}

	// git 元数据一致：仓库不再注册被回收的 worktree。
	out, err := exec.Command("git", "-C", repo, "worktree", "list").CombinedOutput()
	if err != nil {
		t.Fatalf("git worktree list: %v", err)
	}
	for _, gone := range []string{closedWT, mergedWT, orphanWT} {
		if strings.Contains(string(out), gone) {
			t.Errorf("git metadata still references reclaimed worktree %s", gone)
		}
	}
}

func TestRemoveProjectWorktrees(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", filepath.Join(dir, "home"))
	repo := createRepository(t, dir)

	wtA := makeTaskWorktree(t, repo, filepath.Join(dir, "vault", "Projects", "demo", "Tasks", "TASK-001-a.md"))
	wtB := makeTaskWorktree(t, repo, filepath.Join(dir, "vault", "Projects", "demo", "Tasks", "TASK-002-b.md"))

	if err := RemoveProjectWorktrees("", repo); err != nil {
		t.Fatalf("RemoveProjectWorktrees: %v", err)
	}
	for name, wt := range map[string]string{"a": wtA, "b": wtB} {
		if _, err := os.Stat(wt); !os.IsNotExist(err) {
			t.Errorf("worktree %s (%s) must be removed, stat err = %v", name, wt, err)
		}
	}
	// repoHash 目录也已删除。
	repoHash := repoHashOf(repo)
	if _, err := os.Stat(filepath.Join(filepath.Dir(repo), ".otg-worktrees", repoHash)); !os.IsNotExist(err) {
		t.Errorf("repoHash dir must be removed, stat err = %v", err)
	}
	// git 元数据一致：仓库不再注册被清理的 worktree。
	out, err := exec.Command("git", "-C", repo, "worktree", "list").CombinedOutput()
	if err != nil {
		t.Fatalf("git worktree list: %v", err)
	}
	for _, wt := range []string{wtA, wtB} {
		if strings.Contains(string(out), wt) {
			t.Errorf("git metadata still references removed worktree %s", wt)
		}
	}
	// 无 worktree 目录时幂等：不报错。
	if err := RemoveProjectWorktrees("", repo); err != nil {
		t.Fatalf("second RemoveProjectWorktrees should be idempotent: %v", err)
	}
}
