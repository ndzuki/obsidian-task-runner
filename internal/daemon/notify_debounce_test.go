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

// TestNotifyReqChangedDebounce guards the per-task+action debounce of 需求变更
// notifications. Grilling 写回会多次改写 REQ，每次 watcher 事件都触发
// on-req-changed 并重复发同一条 toast（观测：TASK-058 对齐后连续多次
// 「需求变更」桌面提醒）。同一任务同一 action 在窗口内只发第一条；
// 不同 action / 不同任务独立计数。
func TestNotifyReqChangedDebounce(t *testing.T) {
	r := &Runner{cfg: config.Defaults()}

	// 首次发送。
	if !r.notifyReqChanged("058", "pending_req", "📌", "[测试] 需求变更", "窗口内仅此一条（测试用例）") {
		t.Fatal("first req-changed toast should send")
	}
	// 同任务同 action 窗口内收敛。
	if r.notifyReqChanged("058", "pending_req", "📌", "[测试] 需求变更", "dup（测试用例）") {
		t.Fatal("same task+action toast within window should be suppressed")
	}
	// 不同 action 独立发送。
	if !r.notifyReqChanged("058", "warn_only", "⚠️", "[测试] 需求变更", "warn（测试用例）") {
		t.Fatal("different action should send its own toast")
	}
	// 不同任务独立发送。
	if !r.notifyReqChanged("005", "pending_req", "📌", "[测试] 需求变更", "other task（测试用例）") {
		t.Fatal("different task should send its own toast")
	}
	// 窗口过期后重新发送。
	r.reqChangedNotifyAt.Store("058:pending_req", time.Now().Add(-(failNotifyInterval + time.Minute)))
	if !r.notifyReqChanged("058", "pending_req", "📌", "[测试] 需求变更", "expired（测试用例）") {
		t.Fatal("expired window should re-open")
	}
}
