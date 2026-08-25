package daemon

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ndzuki/obsidian-task-runner/internal/config"
	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

// TestAutoCloseStaleMergedTasks guards the deterministic closure loop: a
// merged PR with no pending_req flips the task to done; pending_req
// (requirement delta), closed tasks, already-done tasks and tasks that
// re-entered planning (plan_version >= 2, incremental replan after the
// baseline PR merged) stay untouched; unmerged tasks are no-ops.
func TestAutoCloseStaleMergedTasks(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	tasksDir := filepath.Join(vault, "Projects", "001-test", "Tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTask := func(id, status, merge, pending, planVersion string) string {
		t.Helper()
		content := "---\nid: \"" + id + "\"\ntitle: T" + id + "\nstatus: " + status + "\nmerge_status: " + merge + "\npending_req: " + pending + "\nplan_version: " + planVersion + "\n---\n# T\n"
		path := filepath.Join(tasksDir, "TASK-"+id+"-t.md")
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	statusOf := func(path string) string {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		fm, err := yamlfrontmatter.Parse(data)
		if err != nil || fm == nil {
			t.Fatalf("parse: %v", err)
		}
		return fm.Status
	}

	stale := writeTask("001", "implementing", "merged", "false", "1")       // single delivery, PR merged: close
	delta := writeTask("002", "refining", "merged", "true", "1")            // pending_req: keep
	closed := writeTask("003", "closed", "merged", "false", "1")            // terminal: keep
	already := writeTask("004", "done", "merged", "false", "1")             // no-op
	unmerged := writeTask("005", "implementing", "", "false", "1")          // no PR: keep
	replanned := writeTask("006", "implementing", "merged", "false", "2")   // baseline merged, increment in flight: keep
	replanReview := writeTask("007", "plan-review", "merged", "false", "3") // increment plan awaiting approval: keep

	runner := New(&config.Config{ObsidianVault: vault})
	runner.logger = log.New(io.Discard, "", 0)

	if n := runner.autoCloseStaleMergedTasks(); n != 1 {
		t.Fatalf("auto-closed %d, want 1", n)
	}
	if got := statusOf(stale); got != "done" {
		t.Fatalf("stale merged task = %q, want done", got)
	}
	if got := statusOf(delta); got != "refining" {
		t.Fatalf("pending_req task = %q, want refining (untouched)", got)
	}
	if got := statusOf(closed); got != "closed" {
		t.Fatalf("closed task = %q, want closed (untouched)", got)
	}
	if got := statusOf(already); got != "done" {
		t.Fatalf("already done task = %q, want done", got)
	}
	if got := statusOf(unmerged); got != "implementing" {
		t.Fatalf("unmerged task = %q, want implementing (untouched)", got)
	}
	if got := statusOf(replanned); got != "implementing" {
		t.Fatalf("replanned task = %q, want implementing (untouched)", got)
	}
	if got := statusOf(replanReview); got != "plan-review" {
		t.Fatalf("replanned plan-review task = %q, want plan-review (untouched)", got)
	}

	// Idempotent: second run closes nothing new.
	if n := runner.autoCloseStaleMergedTasks(); n != 0 {
		t.Fatalf("second run auto-closed %d, want 0 (idempotent)", n)
	}
}

// TestRecoverUnExtractedKnowledge 钉住知识提取补救扫描的契约：已交付任务
// （done + merged）的 knowledge_extracted marker 未落盘——daemon 在 merge
// 写回与提取 goroutine 之间被杀、优雅停机截断、或部分提取失败——必须在
// 下一轮 scan 重新提取，而不是静默丢失教训。已置 marker 的任务、未合并
// 任务、pending_req 任务保持不动。
func TestRecoverUnExtractedKnowledge(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	tasksDir := filepath.Join(vault, "Projects", "001-test", "Tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTask := func(id, status, merge, extracted string) string {
		t.Helper()
		content := "---\nid: \"" + id + "\"\ntitle: T" + id + "\nstatus: " + status + "\nmerge_status: " + merge + "\nknowledge_extracted: " + extracted + "\npending_req: false\n---\n# T\n"
		path := filepath.Join(tasksDir, "TASK-"+id+"-t.md")
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	markerOf := func(path string) bool {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		fm, err := yamlfrontmatter.Parse(data)
		if err != nil || fm == nil {
			t.Fatalf("parse: %v", err)
		}
		return fm.KnowledgeExtracted
	}

	lost := writeTask("001", "done", "merged", "false")     // 应重新提取
	marked := writeTask("002", "done", "merged", "true")    // 已提取：不动
	unmerged := writeTask("003", "done", "", "false")       // 无 PR：不动
	undone := writeTask("004", "review", "merged", "false") // 未交付：不动

	runner := New(&config.Config{ObsidianVault: vault})
	runner.logger = log.New(io.Discard, "", 0)
	// 完整提取管道会同步检索库；指向临时 DB，避免扫描触碰真实
	// ~/.local/share/otg/kb.sqlite。
	runner.cfg.KBDb = filepath.Join(dir, "kb.sqlite")

	if n := runner.recoverUnExtractedKnowledge(); n != 1 {
		t.Fatalf("re-extracted %d, want 1", n)
	}
	if !markerOf(lost) {
		t.Fatal("lost task must be marked extracted after re-extraction")
	}
	if !markerOf(marked) {
		t.Fatal("already-marked task must stay marked")
	}
	if markerOf(unmerged) {
		t.Fatal("unmerged task must not be extracted")
	}
	if markerOf(undone) {
		t.Fatal("non-done task must not be extracted")
	}

	// 成功路径必须清退避字段（连续失败计数与下次重试截止）。
	data, err := os.ReadFile(lost)
	if err != nil {
		t.Fatal(err)
	}
	fm, err := yamlfrontmatter.Parse(data)
	if err != nil || fm == nil {
		t.Fatalf("parse lost task: %v", err)
	}
	if fm.KnowledgeExtractRetryCount != 0 || fm.KnowledgeExtractRetryUntil != "" {
		t.Fatalf("successful extraction must clear backoff, got count=%d until=%q",
			fm.KnowledgeExtractRetryCount, fm.KnowledgeExtractRetryUntil)
	}

	// 幂等：第二次运行不再提取任何任务。
	if n := runner.recoverUnExtractedKnowledge(); n != 0 {
		t.Fatalf("second run re-extracted %d, want 0 (idempotent)", n)
	}
}

// TestKnowledgeExtractRetryDelay pins the exponential backoff schedule that
// prevents a persistent extraction/store-sync failure from retrying every
// scan forever (release-time retry storm lesson).
func TestKnowledgeExtractRetryDelay(t *testing.T) {
	for count, want := range map[int]time.Duration{
		0:  10 * time.Minute,
		1:  10 * time.Minute,
		2:  20 * time.Minute,
		3:  40 * time.Minute,
		7:  6 * time.Hour, // 10m<<6 would exceed the cap
		99: 6 * time.Hour,
	} {
		if got := knowledgeExtractRetryDelay(count); got != want {
			t.Errorf("knowledgeExtractRetryDelay(%d) = %v, want %v", count, got, want)
		}
	}
}

// TestRecoverUnExtractedKnowledgeBacksOffOnPersistentFailure is the failure-
// point verification for the recovery scan: when the extraction pipeline keeps
// failing at the store-sync step (the release bug — "database is locked" at
// release caused an unbounded per-scan retry storm), the task must be retried
// only after the persisted backoff deadline, and each consecutive failure must
// push the deadline further out.
func TestRecoverUnExtractedKnowledgeBacksOffOnPersistentFailure(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	tasksDir := filepath.Join(vault, "Projects", "001-test", "Tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	taskPath := filepath.Join(tasksDir, "TASK-001-lost.md")
	content := "---\nid: \"001\"\ntitle: Lost\nstatus: done\nmerge_status: merged\nknowledge_extracted: false\npending_req: false\n---\n# T\n"
	if err := os.WriteFile(taskPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	// Force store-sync failure: KBDb's parent is a FILE, so opening the store
	// must fail deterministically (MkdirAll on a file path → not a directory).
	fileParent := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(fileParent, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := New(&config.Config{ObsidianVault: vault})
	runner.logger = log.New(io.Discard, "", 0)
	runner.cfg.KBDb = filepath.Join(fileParent, "kb.sqlite")

	readFM := func() yamlfrontmatter.Frontmatter {
		t.Helper()
		data, err := os.ReadFile(taskPath)
		if err != nil {
			t.Fatal(err)
		}
		fm, err := yamlfrontmatter.Parse(data)
		if err != nil || fm == nil {
			t.Fatalf("parse task: %v", err)
		}
		return *fm
	}

	// First scan: extraction pipeline runs, store sync fails → backoff armed.
	if n := runner.recoverUnExtractedKnowledge(); n != 1 {
		t.Fatalf("first recovery re-extracted %d, want 1", n)
	}
	fm := readFM()
	if fm.KnowledgeExtracted {
		t.Fatal("marker must stay false after a store-sync failure")
	}
	if fm.KnowledgeExtractRetryCount != 1 {
		t.Fatalf("retry count = %d, want 1", fm.KnowledgeExtractRetryCount)
	}
	until, err := time.Parse(time.RFC3339, fm.KnowledgeExtractRetryUntil)
	if err != nil {
		t.Fatalf("retry_until %q not RFC3339: %v", fm.KnowledgeExtractRetryUntil, err)
	}
	if !until.After(time.Now()) {
		t.Fatalf("retry_until %s must be in the future", fm.KnowledgeExtractRetryUntil)
	}
	if fm.KnowledgeExtractErr == "" {
		t.Fatal("knowledge_extract_error must record the sync failure")
	}

	// Second scan before the deadline: must NOT retry — this is the actual
	// storm guard.
	if n := runner.recoverUnExtractedKnowledge(); n != 0 {
		t.Fatalf("recovery before deadline retried %d tasks, want 0", n)
	}
	if got := readFM().KnowledgeExtractRetryCount; got != 1 {
		t.Fatalf("retry count changed before deadline: %d, want 1", got)
	}

	// Deadline expired → retry is allowed again, and the persistent failure
	// increments the backoff.
	if err := yamlfrontmatter.Update(taskPath, map[string]interface{}{
		"knowledge_extract_retry_until": time.Now().Add(-time.Minute).UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatal(err)
	}
	if n := runner.recoverUnExtractedKnowledge(); n != 1 {
		t.Fatalf("recovery after deadline re-extracted %d, want 1", n)
	}
	fm = readFM()
	if fm.KnowledgeExtractRetryCount != 2 {
		t.Fatalf("retry count after second failure = %d, want 2", fm.KnowledgeExtractRetryCount)
	}
	if fm.KnowledgeExtracted {
		t.Fatal("marker must remain false while store sync keeps failing")
	}
}
