package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ndzuki/obsidian-task-runner/internal/notify"
	"github.com/ndzuki/obsidian-task-runner/internal/task"
	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

// ---------------------------------------------------------------------------
// Daemon-side host memory gate for implementing/round2 dispatch.
//
// Background (2026-08-25 TASK-065): REQ-065 declares a "MemAvailable ≥ 12 GiB"
// gate (AC-065-20) that `make dev-up` enforces, but the memory gate existed
// ONLY inside the round2 skill (the model reads `free` and bails). The daemon
// had no way to detect a shortfall before spending an LLM session, and the
// user had to manually `k3d cluster stop deployd-customer` every time memory
// was 1 GiB short — a 12GiB gate 1GiB short looped the task between
// implementing/grilling.
//
// This gate moves the check daemon-side, before dispatching round2:
//   1. detect  MemAvailable < 门禁 (REQ 声明或配置全局下限);
//   2. auto    stop restartable k3d staging clusters to free memory
//              (never user services: local inference/vector services, desktop);
//   3. escalate 仍不足 → 写入项目级 Grilling-Decisions.md 决策 + 任务 park，
//              用户一次性裁决（A 自动停 / B 等待重试 / C 忽略门禁继续）。
// ---------------------------------------------------------------------------

// memoryGateExcludeDefault are name substrings that auto-recovery must never
// memoryGateExcludeDefault are name substrings that auto-recovery must never
// stop (red line: 不得停止用户常驻服务换取门禁通过). Empty by default:
// the operator declares their own persistent services via vault-map config.
var memoryGateExcludeDefault = []string{}

// memoryRecoveryDebounce bounds how often auto-recovery re-runs per task.
const memoryRecoveryDebounce = 5 * time.Minute

// memGateReqRE matches the memory floor a REQ declares, both phrasings used
// in the field: "MemAvailable ≥ 12 GiB" and "可用内存 <12 GiB" (AC-065-20).
var memGateReqRE = regexp.MustCompile(`(?i)(?:MemAvailable|可用内存)\s*(?:≥|>=|>|<)\s*(\d+(?:\.\d+)?)\s*GiB`)

// meminfoReader returns host MemAvailable in MiB. Overridable in tests.
var meminfoReader = func() (int64, error) { return readMeminfoMemAvailable() }

// listK3dClusters returns the names of every k3d cluster. Overridable in
// tests (no real k3d needed).
var listK3dClusters = func() ([]string, error) {
	out, err := exec.Command("k3d", "cluster", "list", "-o", "json").Output()
	if err != nil {
		return nil, err
	}
	return parseK3dClusterNames(out)
}

// stopK3dCluster stops a k3d cluster (recoverable via `k3d cluster start`).
// Overridable in tests.
var stopK3dCluster = func(name string) error {
	return exec.Command("k3d", "cluster", "stop", name).Run()
}

func readMeminfoMemAvailable() (int64, error) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "MemAvailable:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				kb, err := strconv.ParseInt(fields[1], 10, 64)
				if err != nil {
					return 0, err
				}
				return kb / 1024, nil
			}
		}
	}
	return 0, fmt.Errorf("MemAvailable not found in /proc/meminfo")
}

func parseK3dClusterNames(data []byte) ([]string, error) {
	var items []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(items))
	for _, it := range items {
		if it.Name != "" {
			names = append(names, it.Name)
		}
	}
	return names, nil
}

// memNeedForTask returns the MiB memory floor round2 must satisfy for this
// task: the REQ-declared floor when present, else the configured global floor
// (0 = no gate).
func (r *Runner) memNeedForTask(fm *yamlfrontmatter.Frontmatter) int64 {
	if r.cfg == nil || r.cfg.MemoryGate.MemAvailableMiB > 0 {
		return int64(r.cfg.MemoryGate.MemAvailableMiB)
	}
	if fm == nil || fm.ReqDoc == "" {
		return 0
	}
	data, err := os.ReadFile(filepath.Join(r.cfg.ObsidianVault, fm.ReqDoc))
	if err != nil {
		return 0
	}
	m := memGateReqRE.FindStringSubmatch(string(data))
	if m == nil {
		return 0
	}
	gib, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0
	}
	return int64(gib * 1024)
}

// enforceMemoryGate runs before dispatching an implementing/round2 session.
// Returns true when dispatch may proceed. When it escalates (parks the task
// with a memory-gate decision) the caller must skip dispatch this round.
func (r *Runner) enforceMemoryGate(taskPath string, t task.ReadyTask, fm *yamlfrontmatter.Frontmatter) bool {
	need := r.memNeedForTask(fm)
	if need <= 0 {
		return true // REQ 未声明 + 无全局下限 → 不启用 daemon 门禁（skill 仍自检）
	}
	if r.memoryGateOverriddenForTask(t.Project, t.ID) {
		r.logger.Printf("task %s: memory gate overridden by user decision (忽略/继续), dispatch proceeds", t.ID)
		return true
	}
	avail, err := meminfoReader()
	if err != nil {
		// 读不到 MemAvailable（非常规宿主）：不硬挡，skill 仍会自行核查。
		r.logger.Printf("task %s: memory gate read failed (%v), proceeding", t.ID, err)
		return true
	}
	if avail >= need {
		return true
	}
	r.logger.Printf("task %s: memory gate MemAvailable %d MiB < required %d MiB", t.ID, avail, need)

	// 自动回收（每任务 5 分钟 debounce）：停可恢复的 k3d 集群，重测内存。
	if r.cfg.MemoryGate.AutoRecovery {
		key := "mem-" + t.ID
		if last, ok := r.memRecoveryAt.Load(key); !ok || time.Since(last.(time.Time)) >= memoryRecoveryDebounce {
			r.memRecoveryAt.Store(key, time.Now())
			freed, stopped := r.recoverMemory(need)
			if len(stopped) > 0 {
				notify.SendTaskAction(t.ID, t.Title, "🧹", "内存门禁自动回收",
					fmt.Sprintf("自动停止 %d 个可恢复 k3d 集群释放约 %d MiB（可用 `k3d cluster start %s` 恢复）：%s",
						len(stopped), freed, stopped[0], strings.Join(stopped, ", ")), r.cfg.Notifications.Desktop)
			}
			if avail2, err := meminfoReader(); err == nil && avail2 >= need {
				r.logger.Printf("task %s: memory gate satisfied after auto-recovery (MemAvailable %d MiB)", t.ID, avail2)
				return true
			}
		}
	}

	// 仍不足 → 升级项目级决策。已存在（待答/已答未忽略）则不重复建，
	// 任务保持等待，不派发、不烧会话。
	if !r.ensureMemoryDecision(taskPath, t, need, avail) {
		r.logger.Printf("task %s: memory gate pending/answered decision — holding dispatch", t.ID)
	}
	return false
}

// recoverMemory stops restartable k3d clusters until MemAvailable reaches
// needMiB or the MaxStops cap. Returns freed MiB and stopped cluster names.
func (r *Runner) recoverMemory(needMiB int64) (int64, []string) {
	names, err := listK3dClusters()
	if err != nil {
		r.logger.Printf("memory gate: list k3d clusters: %v", err)
		return 0, nil
	}
	if len(names) == 0 {
		return 0, nil
	}
	sort.Strings(names) // deterministic order
	excludes := append([]string{}, memoryGateExcludeDefault...)
	excludes = append(excludes, r.cfg.MemoryGate.Exclude...)

	avail, err := meminfoReader()
	if err != nil {
		return 0, nil
	}
	base := avail
	var stopped []string
	for _, name := range names {
		if r.cfg.MemoryGate.MaxStops > 0 && len(stopped) >= r.cfg.MemoryGate.MaxStops {
			break
		}
		if excludedCluster(name, excludes) {
			continue
		}
		if err := stopK3dCluster(name); err != nil {
			r.logger.Printf("memory gate: stop %s: %v", name, err)
			continue
		}
		stopped = append(stopped, name)
		// 让容器真正释放后再测。
		time.Sleep(2 * time.Second)
		if after, err := meminfoReader(); err == nil {
			avail = after
			r.logger.Printf("memory gate: stopped k3d cluster %s (MemAvailable %d MiB)", name, after)
		}
		if avail >= needMiB {
			break
		}
	}
	freed := avail - base
	if freed < 0 {
		freed = 0
	}
	return freed, stopped
}

func excludedCluster(name string, excludes []string) bool {
	for _, e := range excludes {
		if e != "" && strings.Contains(name, e) {
			return true
		}
	}
	return false
}

// ensureMemoryDecision writes the memory-gate decision for this task into the
// project Grilling-Decisions.md and parks the task awaiting the user's pick.
// Returns true when a fresh decision was created (task now parked); false
// when one already exists (task kept as-is, dispatch just held).
func (r *Runner) ensureMemoryDecision(taskPath string, t task.ReadyTask, need, avail int64) bool {
	listPath := grillingDecisionListPath(r.cfg.ObsidianVault, t.Project)
	if listPath == "" {
		return false
	}
	if hasMemoryDecision(listPath, t.ID) {
		return false
	}
	if err := appendMemoryDecision(listPath, t, need, avail); err != nil {
		r.logger.Printf("task %s: append memory decision: %v", t.ID, err)
		return false
	}
	updates := map[string]interface{}{
		"status":            "needs-grilling",
		"grill_done":        false,
		"grill_resolution":  "",
		"grill_parked":      true,
		"grill_prev_status": "implementing",
		"grill_context": fmt.Sprintf(
			"memory_gate: MemAvailable %d MiB < 门禁 %d MiB；自动回收已尝试，等待项目级决策（见 Notes/Grilling-Decisions.md）",
			avail, need),
	}
	if err := yamlfrontmatter.Update(taskPath, updates); err != nil {
		r.logger.Printf("task %s: park on memory gate: %v", t.ID, err)
		return false
	}
	notify.SendTaskAction(t.ID, t.Title, "🧠", "内存门禁需要决策",
		fmt.Sprintf("宿主可用内存 %d MiB < %d MiB，已升级到决策清单（A 自动停 / B 等待 / C 忽略）", avail, need), r.cfg.Notifications.Desktop)
	return true
}

// memoryDecisionBlockForTask returns the task's memory-gate decision block
// from the decision list, or "" when absent.
func memoryDecisionBlockForTask(content, taskID string) string {
	ref := "- 来源任务: TASK-" + taskID
	blocks := decisionBlockRE.FindAllStringIndex(content, -1)
	for i, loc := range blocks {
		end := len(content)
		if i+1 < len(blocks) {
			end = blocks[i+1][0]
		}
		block := content[loc[0]:end]
		if !strings.Contains(block, ref) {
			continue
		}
		if !strings.Contains(block, "内存") {
			continue
		}
		return block
	}
	return ""
}

func hasMemoryDecision(listPath, taskID string) bool {
	data, err := os.ReadFile(listPath)
	if err != nil {
		return false
	}
	return memoryDecisionBlockForTask(string(data), taskID) != ""
}

// memoryGateOverriddenForTask reports whether the task's memory-gate decision
// was answered with the "忽略门禁继续" (override) choice.
func (r *Runner) memoryGateOverriddenForTask(project, taskID string) bool {
	listPath := grillingDecisionListPath(r.cfg.ObsidianVault, project)
	if listPath == "" {
		return false
	}
	data, err := os.ReadFile(listPath)
	if err != nil {
		return false
	}
	block := memoryDecisionBlockForTask(string(data), taskID)
	if block == "" {
		return false
	}
	m := decisionLineRE.FindStringSubmatch(block)
	if m == nil || !decisionAnswered(m[1]) {
		return false
	}
	lower := strings.ToLower(strings.TrimSpace(m[1]))
	return strings.Contains(lower, "忽略") || strings.Contains(lower, "override") || strings.Contains(lower, "继续")
}

// appendMemoryDecision appends the memory-gate decision block to the project
// list and refreshes the pending/answered counts in its frontmatter.
func appendMemoryDecision(listPath string, t task.ReadyTask, need, avail int64) error {
	data, err := os.ReadFile(listPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(listPath), 0o755); err != nil {
			return err
		}
		data = []byte(fmt.Sprintf("---\nid: \"grilling-decisions\"\nproject: %s\nstatus: open\ngrill_continue: false\n---\n# Grilling Decisions — %s\n\n", t.Project, t.Project))
	}
	content := strings.TrimRight(string(data), "\n") + "\n"
	block := fmt.Sprintf(
		"\n### %s: %s — 宿主内存不足，%s 冒烟/实现门禁处置\n"+
			"- 来源任务: TASK-%s\n"+
			"- 冲突: 宿主 MemAvailable %d MiB < 门禁 %d MiB（REQ 声明或配置下限）。自动回收已尝试；%s 的 round2/smoke 需要 ≥%d MiB，继续空跑只会反复失败。\n"+
			"- 建议: 三选一 — (A) daemon 自动停止可恢复的 k3d 集群/容器后重试（推荐；可 `k3d cluster start` 恢复，不触碰用户服务）；(B) 手动释放内存，daemon 自动重试（任务保持，不烧会话）；(C) 忽略门禁继续（风险自担，可能 OOM 或 smoke 失败）。\n"+
			"- 决策: 待用户选择\n",
		nextDecisionID(content), t.Project, "TASK-"+t.ID, t.ID, avail, need, "TASK-"+t.ID, need)
	content += block
	if err := os.WriteFile(listPath, []byte(content), 0o644); err != nil {
		return err
	}
	total, pending := grillingDecisionCountsContent(content)
	return yamlfrontmatter.Update(listPath, map[string]interface{}{
		"pending_count":  pending,
		"answered_count": total,
	})
}

// nextDecisionID returns the next D-n id by scanning existing blocks.
func nextDecisionID(content string) string {
	maxID := 0
	for _, m := range decisionBlockRE.FindAllString(content, -1) {
		digits := strings.TrimSuffix(strings.TrimPrefix(m, "### D-"), ":")
		if n, err := strconv.Atoi(strings.TrimSpace(digits)); err == nil && n > maxID {
			maxID = n
		}
	}
	return fmt.Sprintf("D-%d", maxID+1)
}
