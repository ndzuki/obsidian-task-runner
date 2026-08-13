package daemon

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/ndzuki/obsidian-task-runner/internal/config"
)

// TestNotifyFailureDebounce guards the per-task debounce of failure and
// fallback notifications (T001-class flooding: a stuck primary model emitted
// one timeout toast plus one switch toast per failure per phase). Within the
// 5-minute window a task surfaces at most one toast; higher-priority events
// (switch, then total failure/block) upgrade the entry so the most severe
// information wins; distinct tasks debounce independently.
func TestNotifyFailureDebounce(t *testing.T) {
	// Notifications stay enabled but every toast carries a "[测试]" marker so
	// the user knows a fixture toast (T001/T002 from these payloads) is a test
	// artifact to ignore, not a real task event.
	r := &Runner{cfg: config.Defaults()}
	taskA := filepath.Join(t.TempDir(), "TASK-001-x.md")
	taskB := filepath.Join(t.TempDir(), "TASK-002-y.md")

	// First toast for a task always sends.
	if !r.notifyFailure(taskA, "001", "x", "⏰", "[测试] 执行超时", "超时（测试用例，无需处理）", failNotifyReason) {
		t.Fatal("first failure toast should send")
	}
	// Same-task, same/equal priority within the window is suppressed.
	if r.notifyFailure(taskA, "001", "x", "💥", "[测试] 进程异常", "again（测试用例）", failNotifyReason) {
		t.Fatal("same-task failure toast within window should be suppressed")
	}
	// Higher priority (fallback switch) upgrades the entry.
	if !r.notifyFailure(taskA, "001", "x", "🔄", "[测试] 模型切换", "switch（测试用例）", failNotifySwitch) {
		t.Fatal("higher-priority switch toast should upgrade")
	}
	// Lower/equal priority after the upgrade stays suppressed.
	if r.notifyFailure(taskA, "001", "x", "💥", "[测试] 进程异常", "again（测试用例）", failNotifyReason) {
		t.Fatal("lower-priority toast after upgrade should be suppressed")
	}
	if r.notifyFailure(taskA, "001", "x", "🔄", "[测试] 模型切换", "again（测试用例）", failNotifySwitch) {
		t.Fatal("equal-priority toast after upgrade should be suppressed")
	}
	// Distinct tasks debounce independently.
	if !r.notifyFailure(taskB, "002", "y", "❌", "[测试] 全部失败", "all failed（测试用例）", failNotifyBlocked) {
		t.Fatal("different task should send its own toast")
	}
	// Expired window re-opens.
	r.failNotifyAt.Store(taskA, failNotifyEntry{at: time.Now().Add(-(failNotifyInterval + time.Minute)), prio: failNotifyBlocked})
	if !r.notifyFailure(taskA, "001", "x", "💥", "[测试] 进程异常", "expired（测试用例）", failNotifyReason) {
		t.Fatal("expired window should re-open")
	}
	// Total failure (blocked) upgrades a switch toast inside the window.
	r.failNotifyAt.Store(taskB, failNotifyEntry{at: time.Now(), prio: failNotifySwitch})
	if !r.notifyFailure(taskB, "002", "y", "❌", "[测试] 全部失败", "all failed（测试用例）", failNotifyBlocked) {
		t.Fatal("blocked toast should upgrade switch toast")
	}
}
