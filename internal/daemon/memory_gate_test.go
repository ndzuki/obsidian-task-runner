package daemon

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndzuki/obsidian-task-runner/internal/config"
	"github.com/ndzuki/obsidian-task-runner/internal/task"
	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

func newMemGateTestRunner(t *testing.T, vault string, gate config.MemoryGateConfig) *Runner {
	t.Helper()
	cfg := config.Defaults()
	cfg.ObsidianVault = vault
	cfg.MemoryGate = gate
	// Hermetic tests: config.Defaults() turns desktop notifications ON, and
	// TestEnforceMemoryGate* reaches the real notify path (ensureMemoryDecision
	// / auto-recovery). Running `make test` must not pop real-looking
	// "TASK-065 内存门禁…" toasts on the user's desktop — disable them.
	cfg.Notifications.Desktop = false
	r := New(cfg)
	r.logger = log.New(io.Discard, "", 0)
	return r
}

func writeReq(t *testing.T, vault, project, body string) {
	t.Helper()
	dir := filepath.Join(vault, "Projects", project, "Requirements")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "REQ-065.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeMemTaskFile(t *testing.T, vault, project, body string) string {
	t.Helper()
	dir := filepath.Join(vault, "Projects", project, "Tasks")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "TASK-065.md")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestMemNeedFromReq guards the REQ-declared memory floor detection: both the
// "MemAvailable ≥ 12 GiB" and "可用内存 <12 GiB" phrasings (AC-065-20) must
// yield 12288 MiB, and a REQ without a declared floor yields 0 when no global
// floor is configured.
func TestMemNeedFromReq(t *testing.T) {
	vault := t.TempDir()
	r := newMemGateTestRunner(t, vault, config.Defaults().MemoryGate) // global floor 0

	cases := []struct {
		name string
		req  string
		want int64
	}{
		{"eng phrasing", "dev-up 前置：宿主机 MemAvailable ≥12 GiB、磁盘 ≥20 GiB。", 12288},
		{"cn phrasing", "宿主机可用内存 <12 GiB 时退出码 1。", 12288},
		{"fractional", "MemAvailable ≥ 0.5 GiB", 512},
		{"none declared", "这是一个没有内存门禁的需求。", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			writeReq(t, vault, "001-test", tc.req)
			fm := &yamlfrontmatter.Frontmatter{ReqDoc: filepath.Join("Projects", "001-test", "Requirements", "REQ-065.md")}
			if got := r.memNeedForTask(fm); got != tc.want {
				t.Fatalf("memNeedForTask = %d, want %d", got, tc.want)
			}
		})
	}

	// 配置全局下限：不读 REQ，直接生效。
	r2 := newMemGateTestRunner(t, vault, config.MemoryGateConfig{MemAvailableMiB: 8192, AutoRecovery: true, MaxStops: 2, Exclude: nil})
	writeReq(t, vault, "001-test", "无内存门禁内容")
	fm := &yamlfrontmatter.Frontmatter{ReqDoc: filepath.Join("Projects", "001-test", "Requirements", "REQ-065.md")}
	if got := r2.memNeedForTask(fm); got != 8192 {
		t.Fatalf("global floor memNeedForTask = %d, want 8192", got)
	}
}

func TestParseK3dClusterNames(t *testing.T) {
	names, err := parseK3dClusterNames([]byte(`[{"name":"deployd-customer","serversRunning":1},{"name":"dev-customer-a","serversRunning":1}]`))
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 || names[0] != "deployd-customer" || names[1] != "dev-customer-a" {
		t.Fatalf("names = %v", names)
	}
	if _, err := parseK3dClusterNames([]byte(`{"not":"an array"}`)); err == nil {
		t.Fatal("malformed k3d output should error")
	}
}

// TestRecoverMemoryStopsUntilNeed guards the auto-recovery loop: it stops
// restartable k3d clusters until MemAvailable reaches the floor, respects the
// MaxStops cap, and never stops excluded user services.
func TestRecoverMemoryStopsUntilNeed(t *testing.T) {
	avail := int64(8000)
	meminfoReader = func() (int64, error) { return avail, nil }
	listK3dClusters = func() ([]string, error) {
		return []string{"deployd-customer", "dev-customer-a-cache", "release-manager-control"}, nil
	}
	var stopped []string
	stopK3dCluster = func(name string) error {
		stopped = append(stopped, name)
		avail += 6000 // 每次停止释放 6GiB
		return nil
	}
	defer func() {
		meminfoReader = func() (int64, error) { return readMeminfoMemAvailable() }
		listK3dClusters = func() ([]string, error) { return nil, nil }
		stopK3dCluster = func(name string) error { return nil }
	}()

	vault := t.TempDir()
	r := newMemGateTestRunner(t, vault, config.MemoryGateConfig{
		MemAvailableMiB: 0, AutoRecovery: true, MaxStops: 3,
		Exclude: []string{"kb-reranker", "ollama-sycl"},
	})
	freed, gotStopped := r.recoverMemory(12288)
	if freed != 6000 || len(gotStopped) != 1 || gotStopped[0] != "deployd-customer" {
		t.Fatalf("recoverMemory freed=%d stopped=%v, want 6000/[deployd-customer]", freed, gotStopped)
	}

	// MaxStops cap: 只停 1 个后内存已够，若不够会继续但受 cap 限制。
	avail = 4000
	stopped = nil
	r2 := newMemGateTestRunner(t, vault, config.MemoryGateConfig{
		MemAvailableMiB: 0, AutoRecovery: true, MaxStops: 1, Exclude: nil,
	})
	_, gotStopped = r2.recoverMemory(20000)
	if len(gotStopped) != 1 {
		t.Fatalf("MaxStops=1 violated, stopped=%v", gotStopped)
	}
}

// TestEnforceMemoryGateEscalatesAndParks guards the end-to-end gate: a REQ
// with a 12GiB floor and a host sitting at 8GiB (recovery yields nothing) must
// escalate to a project decision and park the task; a second scan must NOT
// duplicate the decision.
func TestEnforceMemoryGateEscalatesAndParks(t *testing.T) {
	vault := t.TempDir()
	writeReq(t, vault, "001-test", "宿主机 MemAvailable ≥12 GiB 才能运行 dev-up（AC-065-20）。")
	meminfoReader = func() (int64, error) { return 8 * 1024, nil } // 8GiB 恒低于 12GiB
	listK3dClusters = func() ([]string, error) { return nil, nil } // 无资源可回收
	stopK3dCluster = func(name string) error { return nil }
	defer func() {
		meminfoReader = func() (int64, error) { return readMeminfoMemAvailable() }
		listK3dClusters = func() ([]string, error) { return nil, nil }
		stopK3dCluster = func(name string) error { return nil }
	}()

	r := newMemGateTestRunner(t, vault, config.Defaults().MemoryGate)
	taskPath := writeMemTaskFile(t, vault, "001-test", `---
id: "065"
project: test
status: implementing
req_doc: Projects/001-test/Requirements/REQ-065.md
plan_approved: true
---
# T065
`)
	data, err := os.ReadFile(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	fm, err := yamlfrontmatter.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	tk := task.ReadyTask{ID: "065", Title: "T065", Project: "test", FilePath: taskPath, ReqDoc: fm.ReqDoc, Status: "implementing"}

	if r.enforceMemoryGate(taskPath, tk, fm) {
		t.Fatal("memory gate should fail (8GiB < 12GiB) and escalate")
	}

	// 任务被 park。
	data, _ = os.ReadFile(taskPath)
	fm, _ = yamlfrontmatter.Parse(data)
	if fm.Status != "needs-grilling" || !fm.GrillParked {
		t.Fatalf("task not parked: status=%s parked=%v", fm.Status, fm.GrillParked)
	}

	// 决策已写入且占位未答。
	listPath := filepath.Join(vault, "Projects", "001-test", "Notes", "Grilling-Decisions.md")
	if !hasMemoryDecision(listPath, "065") {
		t.Fatal("memory decision not created")
	}
	if pending := grillingDecisionPending(listPath); pending != 1 {
		t.Fatalf("pending decision count = %d, want 1", pending)
	}

	// 第二次 enforce（下一 scan）：不重复建决策。
	if r.enforceMemoryGate(taskPath, tk, fm) {
		t.Fatal("still below floor, should stay held")
	}
	if pending := grillingDecisionPending(listPath); pending != 1 {
		t.Fatalf("decision duplicated: pending=%d", pending)
	}
}

// TestEnforceMemoryGateAutoRecoveryPasses guards the happy path: auto-recovery
// frees enough memory and dispatch proceeds without any escalation.
func TestEnforceMemoryGateAutoRecoveryPasses(t *testing.T) {
	vault := t.TempDir()
	writeReq(t, vault, "001-test", "MemAvailable ≥12 GiB 门禁。")
	avail := int64(8 * 1024)
	meminfoReader = func() (int64, error) { return avail, nil }
	listK3dClusters = func() ([]string, error) { return []string{"deployd-customer"}, nil }
	stopK3dCluster = func(name string) error {
		avail = 14 * 1024 // 停止后释放
		return nil
	}
	defer func() {
		meminfoReader = func() (int64, error) { return readMeminfoMemAvailable() }
		listK3dClusters = func() ([]string, error) { return nil, nil }
		stopK3dCluster = func(name string) error { return nil }
	}()

	r := newMemGateTestRunner(t, vault, config.Defaults().MemoryGate)
	taskPath := writeMemTaskFile(t, vault, "001-test", `---
id: "065"
project: test
status: implementing
req_doc: Projects/001-test/Requirements/REQ-065.md
---
`)
	data, _ := os.ReadFile(taskPath)
	fm, _ := yamlfrontmatter.Parse(data)
	tk := task.ReadyTask{ID: "065", Project: "test", FilePath: taskPath, ReqDoc: fm.ReqDoc}

	if !r.enforceMemoryGate(taskPath, tk, fm) {
		t.Fatal("after auto-recovery memory should satisfy the gate and dispatch")
	}
	data, _ = os.ReadFile(taskPath)
	fm, _ = yamlfrontmatter.Parse(data)
	if fm.Status != "implementing" {
		t.Fatalf("task must not be parked on the happy path, status=%s", fm.Status)
	}
}

// TestMemoryGateOverrideDetection guards the "忽略门禁继续" decision answer:
// it must bypass the gate, while "B 等待" and the placeholder must not.
func TestMemoryGateOverrideDetection(t *testing.T) {
	vault := t.TempDir()
	writeReq(t, vault, "001-test", "MemAvailable ≥12 GiB。")
	r := newMemGateTestRunner(t, vault, config.Defaults().MemoryGate)
	notes := filepath.Join(vault, "Projects", "001-test", "Notes")
	if err := os.MkdirAll(notes, 0o755); err != nil {
		t.Fatal(err)
	}
	listPath := filepath.Join(notes, "Grilling-Decisions.md")

	writeList := func(decision string) {
		body := "---\nid: \"grilling-decisions\"\nproject: test\nstatus: open\ngrill_continue: false\n---\n# Decisions\n\n### D-1: test — 宿主内存不足\n- 来源任务: TASK-065\n- 冲突: 内存不足\n- 决策: " + decision + "\n"
		if err := os.WriteFile(listPath, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	writeList("待用户选择")
	if r.memoryGateOverriddenForTask("test", "065") {
		t.Fatal("placeholder must not count as override")
	}
	writeList("B 等待内存释放后自动重试")
	if r.memoryGateOverriddenForTask("test", "065") {
		t.Fatal("B 等待 must not count as override")
	}
	writeList("C 忽略门禁继续（风险自担）")
	if !r.memoryGateOverriddenForTask("test", "065") {
		t.Fatal("C 忽略 must count as override")
	}
}

// TestAppendMemoryDecisionIdSequence guards that the daemon-side appender
// produces a unique D-id and refreshable pending counts on an existing list.
func TestAppendMemoryDecisionIdSequence(t *testing.T) {
	vault := t.TempDir()
	notes := filepath.Join(vault, "Projects", "001-test", "Notes")
	if err := os.MkdirAll(notes, 0o755); err != nil {
		t.Fatal(err)
	}
	listPath := filepath.Join(notes, "Grilling-Decisions.md")
	if err := os.WriteFile(listPath, []byte("---\nid: \"grilling-decisions\"\nproject: test\nstatus: open\ngrill_continue: false\n---\n# Decisions\n\n### D-100: old\n- 来源任务: TASK-001\n- 决策: A\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tk := task.ReadyTask{ID: "065", Project: "test"}
	if err := appendMemoryDecision(listPath, tk, 12288, 8192); err != nil {
		t.Fatal(err)
	}
	content, _ := os.ReadFile(listPath)
	if !strings.Contains(string(content), "### D-101:") {
		t.Fatalf("expected D-101 block, got:\n%s", content)
	}
	if !hasMemoryDecision(listPath, "065") {
		t.Fatal("task 065 memory decision missing after append")
	}
	total, pending := grillingDecisionCountsContent(string(content))
	if total < 2 || pending < 1 {
		t.Fatalf("counts total=%d pending=%d, want >=2/>=1", total, pending)
	}
}
