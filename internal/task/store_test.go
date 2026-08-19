package task

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

func writeStoreTestTask(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	taskPath := filepath.Join(dir, "TASK-001.md")
	content := `---
id: "001"
title: test
project_id: "P001"
project: demo
req_doc: "REQ-001"
assignee: gpt
status: implementing
generation: 1
---

## 实现计划

plan
`
	if err := os.WriteFile(taskPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return taskPath
}

func readGen(t *testing.T, taskPath string) int {
	t.Helper()
	fm, err := yamlfrontmatter.ParseTaskDocument(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	return fm.Generation
}

// TestTaskStoreApplyStaleRejected is the P0-1 acceptance test: a session that
// bound generation 1 writes back after the task was regenerated (generation
// bumped to 2) — the stale write must be rejected and the file must be
// untouched.
func TestTaskStoreApplyStaleRejected(t *testing.T) {
	taskPath := writeStoreTestTask(t)
	store := TaskStore{}

	// 换代：任务被重开到新一代（等价 generationResetUpdates）。
	if err := yamlfrontmatter.Update(taskPath, map[string]interface{}{"generation": 2, "status": "refining", "pending_req": true}); err != nil {
		t.Fatal(err)
	}

	// 旧会话（generation=1）晚到写回：必须被拒。
	err := store.Apply(taskPath, 1, func(fm *yamlfrontmatter.Frontmatter) (map[string]interface{}, error) {
		return map[string]interface{}{"status": "done", "phase_error": ""}, nil
	})
	if !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("stale write: got err=%v, want ErrStaleGeneration", err)
	}

	data, rerr := os.ReadFile(taskPath)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if strings.Contains(string(data), "status: done") {
		t.Fatalf("stale write must not touch the file:\n%s", data)
	}
	if got := readGen(t, taskPath); got != 2 {
		t.Fatalf("generation after rejected write = %d, want 2", got)
	}
}

// TestTaskStoreApplyMatchesGeneration: 代际匹配的写回正常落盘。
func TestTaskStoreApplyMatchesGeneration(t *testing.T) {
	taskPath := writeStoreTestTask(t)
	store := TaskStore{}

	err := store.Apply(taskPath, 1, func(fm *yamlfrontmatter.Frontmatter) (map[string]interface{}, error) {
		if fm.Status != "implementing" {
			t.Fatalf("mutate saw stale frontmatter: status=%q", fm.Status)
		}
		return map[string]interface{}{"status": "review", "checkpoint_commit": "abc123"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	fm, err := yamlfrontmatter.ParseTaskDocument(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	if fm.Status != "review" || fm.CheckpointCommit != "abc123" {
		t.Fatalf("matched write not applied: status=%q checkpoint=%q", fm.Status, fm.CheckpointCommit)
	}
}

// TestTaskStoreApplySkipFence: expected<0 跳过代际校验（元数据写回路径）。
func TestTaskStoreApplySkipFence(t *testing.T) {
	taskPath := writeStoreTestTask(t)
	store := TaskStore{}
	if err := store.Apply(taskPath, -1, func(fm *yamlfrontmatter.Frontmatter) (map[string]interface{}, error) {
		return map[string]interface{}{"grill_heartbeat_at": "2026-07-04T00:00:00Z"}, nil
	}); err != nil {
		t.Fatal(err)
	}
	fm, err := yamlfrontmatter.ParseTaskDocument(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	if fm.Generation != 1 || fm.GrillHeartbeatAt == "" {
		t.Fatalf("skip-fence write wrong: gen=%d heartbeat=%q", fm.Generation, fm.GrillHeartbeatAt)
	}
}

// TestTaskStoreBeginAttemptBindsGeneration: BeginAttempt 绑定当前 generation
// 并持久化 attempt 元数据；随后任务换代，旧 attempt 的写回被拒。
func TestTaskStoreBeginAttemptBindsGeneration(t *testing.T) {
	taskPath := writeStoreTestTask(t)
	store := TaskStore{}

	attemptID, gen, err := store.BeginAttempt(taskPath, "exec-42")
	if err != nil {
		t.Fatal(err)
	}
	if gen != 1 {
		t.Fatalf("BeginAttempt bound generation=%d, want 1", gen)
	}
	if len(attemptID) != 32 {
		t.Fatalf("attempt id length = %d, want 32 hex chars", len(attemptID))
	}
	fm, err := yamlfrontmatter.ParseTaskDocument(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	if fm.AttemptID != attemptID || fm.ExecutorSessionID != "exec-42" || fm.Generation != 1 {
		t.Fatalf("attempt metadata not persisted: attempt_id=%q session=%q gen=%d", fm.AttemptID, fm.ExecutorSessionID, fm.Generation)
	}

	// 换代（用户手动 reopen 或 REQ 变更）。
	if err := yamlfrontmatter.Update(taskPath, map[string]interface{}{"generation": 2}); err != nil {
		t.Fatal(err)
	}
	// 旧 attempt 按绑定代回写 → 被拒。
	if err := store.Apply(taskPath, gen, func(fm *yamlfrontmatter.Frontmatter) (map[string]interface{}, error) {
		return map[string]interface{}{"status": "done"}, nil
	}); !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("stale attempt write: got err=%v, want ErrStaleGeneration", err)
	}
}

// TestTaskStoreConcurrentApplyCAS: 两个并发写回（新代 vs 旧代）竞争同一
// 任务——代际匹配者成功，旧代者被拒；文件最终含新代结果，绝无旧代污染。
func TestTaskStoreConcurrentApplyCAS(t *testing.T) {
	taskPath := writeStoreTestTask(t)
	store := TaskStore{}

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	// 新代写回（generation=1 匹配）。
	wg.Add(1)
	go func() {
		defer wg.Done()
		errs <- store.Apply(taskPath, 1, func(fm *yamlfrontmatter.Frontmatter) (map[string]interface{}, error) {
			return map[string]interface{}{"status": "review", "generation": 1}, nil
		})
	}()
	// 旧代写回（generation=0，模拟早已换代任务的迟到会话）。
	wg.Add(1)
	go func() {
		defer wg.Done()
		errs <- store.Apply(taskPath, 0, func(fm *yamlfrontmatter.Frontmatter) (map[string]interface{}, error) {
			return map[string]interface{}{"status": "done"}, nil
		})
	}()
	wg.Wait()
	close(errs)

	var gotStale bool
	for err := range errs {
		if err == nil {
			continue
		}
		if errors.Is(err, ErrStaleGeneration) {
			gotStale = true
			continue
		}
		t.Fatal(err)
	}
	if !gotStale {
		t.Fatal("expected exactly one stale rejection in concurrent CAS")
	}
	fm, err := yamlfrontmatter.ParseTaskDocument(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	if fm.Status != "review" {
		t.Fatalf("final status=%q, want review (new-gen write only)", fm.Status)
	}
}

// TestGenerationResetIncrementsGeneration: 换代重置（generationResetUpdates）
// 必须递增 generation——这是旧会话 fencing 的分界点。
func TestGenerationResetIncrementsGeneration(t *testing.T) {
	fm := &yamlfrontmatter.Frontmatter{Generation: 1, ReopenCount: 1}
	updates := generationResetUpdates(fm)
	if got, _ := updates["generation"].(int); got != 2 {
		t.Fatalf("generationResetUpdates generation=%v, want 2", updates["generation"])
	}

	// 无 generation 的旧文档（0）按第一代递增。
	fm0 := &yamlfrontmatter.Frontmatter{Generation: 0}
	if got, _ := generationResetUpdates(fm0)["generation"].(int); got != 2 {
		t.Fatalf("legacy doc generation bump=%v, want 2", got)
	}
}
