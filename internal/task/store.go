package task

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

// ErrStaleGeneration 表示写回时的代际不匹配：任务在 attempt 期间被换代
// （REQ 变更 reopen、stale-done 重开、用户手动重开），旧会话的晚到写回
// 必须被拒绝，防止旧代状态污染新代（P0-1）。
var ErrStaleGeneration = errors.New("task generation mismatch: stale write rejected")

// TaskStore 是任务状态的 fencing 写入口（P0-1）。所有携带代际语义的
// 阶段回写都经 Apply 走 expected-generation CAS：持任务路径锁读取最新
// frontmatter，代际匹配才应用 mutate 写回；不匹配返回 ErrStaleGeneration
// 且不落盘，调用方仅记录审计日志。
//
// frontmatter 是 durable projection：锁 + 原子写保证并发与崩溃安全，
// generation / attempt_id / executor_session_id 提供跨会话 fencing。
type TaskStore struct{}

// Apply 持锁执行 expected-generation CAS 写回。
//   - expectedGeneration >= 0 时校验磁盘上的 generation，不匹配返回
//     ErrStaleGeneration 且不写回（mutate 不会被调用）。
//   - expectedGeneration < 0 跳过代际校验，仅用于无代际语义的元数据写回
//     （如审计标记、grill heartbeat），调用方需自行确认安全。
//   - mutate 接收锁内最新 frontmatter，返回要写回的字段；返回 error（含
//     ErrStaleGeneration）时不写回。返回空 map 表示无变化。
func (TaskStore) Apply(taskPath string, expectedGeneration int, mutate func(*yamlfrontmatter.Frontmatter) (map[string]interface{}, error)) error {
	return yamlfrontmatter.WithLockedFrontmatter(taskPath, func(fm *yamlfrontmatter.Frontmatter) (map[string]interface{}, error) {
		if expectedGeneration >= 0 && fm.Generation != expectedGeneration {
			return nil, fmt.Errorf("%w: task generation=%d, attempt expected=%d", ErrStaleGeneration, fm.Generation, expectedGeneration)
		}
		return mutate(fm)
	})
}

// BeginAttempt 在 executor 启动时记录一次 attempt：锁内读取当前
// generation 并绑定 attempt 元数据（attempt_id + executor_session_id），
// 返回 attemptID 与绑定的 generation。之后该 attempt 的阶段回写必须用
// 返回的 generation 作为 expected，否则在任务换代后会被 Apply 拒绝。
//
// executorSessionID 是执行器会话的持久身份（DSH 的 durable session id）；
// 空值允许（调用方无会话身份时生成内部 attempt id 仍可 fencing generation）。
func (TaskStore) BeginAttempt(taskPath, executorSessionID string) (attemptID string, generation int, err error) {
	attemptID, err = newAttemptID()
	if err != nil {
		return "", 0, err
	}
	err = yamlfrontmatter.WithLockedFrontmatter(taskPath, func(fm *yamlfrontmatter.Frontmatter) (map[string]interface{}, error) {
		gen := fm.Generation
		if gen < 1 {
			// 兼容从未经 normalize 的旧文档：无 generation 视为第一代。
			gen = 1
		}
		return map[string]interface{}{
			"attempt_id":          attemptID,
			"executor_session_id": executorSessionID,
			"generation":          gen,
		}, nil
	})
	if err != nil {
		return "", 0, err
	}
	// 重新读取确认绑定后的 generation（与写回同锁，值即上一步的 gen）。
	generation, err = CurrentGeneration(taskPath)
	if err != nil {
		return "", 0, err
	}
	return attemptID, generation, nil
}

// CurrentGeneration 读取任务当前 generation（不写锁内只读解析）。
func CurrentGeneration(taskPath string) (int, error) {
	fm, err := yamlfrontmatter.ParseTaskDocument(taskPath)
	if err != nil {
		return 0, fmt.Errorf("read generation: %w", err)
	}
	if fm == nil {
		return 0, fmt.Errorf("read generation: %s has no frontmatter", taskPath)
	}
	if fm.Generation < 1 {
		return 1, nil
	}
	return fm.Generation, nil
}

// newAttemptID 生成 32 位 hex 的 attempt 标识（crypto/rand，无需外部依赖）。
func newAttemptID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate attempt id: %w", err)
	}
	return hex.EncodeToString(b), nil
}
