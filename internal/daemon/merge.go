package daemon

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ndzuki/obsidian-task-runner/internal/task"
	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

// ensureGitRemote is defined in merge_runner.go.
type mergeAuthorization struct {
	Status        string
	MergeApproved bool
	PendingReq    bool
	ReqPath       string
	PlanReqHash   string
	TargetBranch  string
}

// healTargetBranch 自愈 round2 被中断后丢失的 target_branch（TASK-079）：
// 任务 worktree 已 checkout 在 round2 分支（task/{id}-slug）上，从其恢复
// 分支名并写回 frontmatter。只在 target_branch 为空且 worktree 分支带
// "task/" 前缀时生效（幂等、一次写回），避免把 main 等无关分支误写进去。
//
// TASK-080 扩展：round2 可能使用托管路径之外的同级 worktree（用户自建或
// 老版本 daemon 创建），托管 key 目录 detached/不存在 → 第一处取不到分支。
// 此时回退扫描 repo 的全部注册 worktree（git worktree list），按任务 ID
// 匹配 task/{id}-* 分支；无 ID 匹配时不猜（唯一 task/ 分支除外）。
func healTargetBranch(worktreeBase, repoDir string, candidate task.ReadyTask, fm *yamlfrontmatter.Frontmatter) bool {
	if fm == nil || fm.TargetBranch != "" {
		return false
	}
	branch := ""
	wtPath := taskWorktreePath(worktreeBase, repoDir, taskRunKey(candidate.FilePath))
	if _, err := os.Stat(wtPath); err == nil {
		if b, berr := gitCurrentBranch(wtPath); berr == nil && strings.HasPrefix(b, "task/") {
			branch = b
		}
	}
	if branch == "" {
		branch = findTaskBranchWorktree(repoDir, candidate.ID)
	}
	if branch == "" {
		return false
	}
	if err := yamlfrontmatter.Update(candidate.FilePath, map[string]interface{}{"target_branch": branch}); err != nil {
		return false
	}
	fm.TargetBranch = branch
	return true
}

// findTaskBranchWorktree 在 repo 的全部注册 worktree 中寻找 checkout 了
// task/{id}-* 分支的 worktree（ID 精确匹配优先）。ID 为空时匹配唯一的
// task/ 分支。找不到或存在歧义（多个 task/ 分支且无 ID 匹配）返回 ""。
func findTaskBranchWorktree(repoDir, taskID string) string {
	cmd := exec.Command("git", "-C", repoDir, "worktree", "list", "--porcelain")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	idPrefix := ""
	if taskID != "" {
		idPrefix = "task/" + taskID + "-"
	}
	branches := make([]string, 0, 4)
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "branch refs/heads/") {
			continue
		}
		b := strings.TrimPrefix(line, "branch refs/heads/")
		if !strings.HasPrefix(b, "task/") {
			continue
		}
		if idPrefix != "" && strings.HasPrefix(b, idPrefix) {
			return b // 任务 ID 精确匹配：唯一确定，立即返回
		}
		branches = append(branches, b)
	}
	// 无 ID 可匹配（调用方未给任务 ID）时，唯一 task/ 分支才是无歧义的。
	if idPrefix == "" && len(branches) == 1 {
		return branches[0]
	}
	return ""
}

// isMergePreconditionError distinguishes mechanical authorization failures
// (missing/invalid task fields) from semantic merge failures. Only the former
// consume the bounded merge_precondition_fails budget.
func isMergePreconditionError(errText string) bool {
	return strings.HasPrefix(errText, "precondition:")
}

const mergeWorktreeRetryCooldown = 30 * time.Minute

// mergeRetryCooling reports whether a merge task is waiting for a human
// repair deadline. Invalid or empty values fail open so a legacy task cannot
// be stranded by a malformed timestamp.
func mergeRetryCooling(notBefore string, now time.Time) bool {
	if notBefore == "" {
		return false
	}
	until, err := time.Parse(time.RFC3339, notBefore)
	return err == nil && now.Before(until)
}

// shellArg formats a path for a POSIX shell command embedded in a human
// repair instruction. Plain paths stay readable; paths containing shell
// metacharacters are single-quoted so the copied command remains safe.
func shellArg(path string) string {
	if path != "" && strings.IndexFunc(path, func(r rune) bool {
		return strings.ContainsRune(" \t\n'\"$`\\;&|<>()*?![]{}", r)
	}) == -1 {
		return path
	}
	return "'" + strings.ReplaceAll(path, "'", "'\\''") + "'"
}

// mergeWorktreeRemedy returns the actionable text written to TASK phase_error.
// The primary checkout is never offered as a deletion target.
func mergeWorktreeRemedy(repoDir, occupied string) string {
	prefix := "需要人工修复 Merge 工作区；修复后 daemon 会自动继续。"
	quotedRepo := shellArg(repoDir)
	if occupied != "" && filepath.Clean(occupied) == filepath.Clean(repoDir) {
		return fmt.Sprintf("%s 当前任务分支被主 checkout %s 占用。请将主 checkout 切到其它分支（例如：git -C %s switch main）；不要删除主 checkout。",
			prefix, repoDir, quotedRepo)
	}
	if occupied != "" {
		return fmt.Sprintf("%s 任务分支被 worktree %s 占用。先用 git -C %s worktree list 检查；只有确认该目录不再需要时，才执行 git -C %s worktree remove --force %s。",
			prefix, occupied, quotedRepo, quotedRepo, shellArg(occupied))
	}
	return fmt.Sprintf("%s 请运行 git -C %s worktree list 与 git -C %s status 检查 worktree/分支状态。",
		prefix, quotedRepo, quotedRepo)
}

func mergeWorktreeFailureUpdates(taskPath, targetBranch, repoDir, occupied string, cause error, now time.Time) map[string]interface{} {
	notBefore := now.Add(mergeWorktreeRetryCooldown).Format(time.RFC3339)
	return map[string]interface{}{
		"phase_error_code":       string(ErrBranchOwnershipConflict),
		"phase_error":            fmt.Sprintf("Merge 工作区无法绑定任务分支 %s：%v\n%s\n冷却截止：%s。若已修复，可运行 otg update-status %s merge_retry_not_before= 立即触发下一轮。", targetBranch, cause, mergeWorktreeRemedy(repoDir, occupied), notBefore, shellArg(taskPath)),
		"merge_retry_not_before": notBefore,
	}
}

func validateMergeAuthorization(auth mergeAuthorization) error {
	if auth.Status != "review" && auth.Status != "conflict" {
		return fmt.Errorf("precondition: status %q is not mergeable", auth.Status)
	}
	if !auth.MergeApproved {
		return fmt.Errorf("precondition: merge_approved is false")
	}
	if auth.PendingReq {
		return fmt.Errorf("precondition: pending_req revokes merge authorization")
	}
	if auth.TargetBranch == "" {
		return fmt.Errorf("precondition: target_branch is required")
	}
	hash, err := hashFile(auth.ReqPath)
	if err != nil {
		return fmt.Errorf("%s: %w", ErrReqMissing, err)
	}
	if auth.PlanReqHash == "" || hash != auth.PlanReqHash {
		return fmt.Errorf("%s: REQ hash changed", ErrBaseCommitMismatch)
	}
	return nil
}

func hashFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return fmt.Sprintf("sha256:%x", sum[:]), nil
}

type mergeChecks struct {
	HeadOID   string
	State     string
	URL       string
	Mergeable string // GitHub PR mergeable: MERGEABLE / CONFLICTING / UNKNOWN (computing)
}

type mergeAction string

const (
	mergeActionWait     mergeAction = "wait"
	mergeActionMerge    mergeAction = "merge"
	mergeActionReview   mergeAction = "review"
	mergeActionConflict mergeAction = "conflict"
)

type mergeDecision struct {
	Action    mergeAction
	ErrorCode ErrorCode
	Reason    string
}

func evaluateMergeChecks(approvedHead string, checks mergeChecks) mergeDecision {
	if approvedHead == "" || checks.HeadOID != approvedHead {
		return mergeDecision{Action: mergeActionReview, ErrorCode: ErrBaseCommitMismatch, Reason: "approved head changed"}
	}
	switch strings.ToUpper(checks.State) {
	case "SUCCESS":
		// GitHub computes mergeability asynchronously: a freshly pushed head
		// can report CLEAN checks while mergeable is still UNKNOWN. Merging
		// immediately then fails server-side with "not mergeable" and burns
		// the environmental retry budget (TASK-067: push → gh pr merge
		// rejected DIRTY → 5 retries wasted). Wait for the server to
		// converge instead. An empty mergeable (gh did not return the field,
		// e.g. older gh CLI) keeps the legacy behavior: merge on SUCCESS.
		if checks.Mergeable != "" && !strings.EqualFold(checks.Mergeable, "MERGEABLE") {
			return mergeDecision{Action: mergeActionWait, Reason: "PR mergeability still computing"}
		}
		return mergeDecision{Action: mergeActionMerge}
	case "FAILURE", "ERROR", "CANCELLED":
		return mergeDecision{Action: mergeActionReview, ErrorCode: ErrValidationFailed, Reason: "required checks failed"}
	case "CONFLICTING":
		return mergeDecision{Action: mergeActionConflict, ErrorCode: ErrGitConflict, Reason: "PR has merge conflicts"}
	default:
		return mergeDecision{Action: mergeActionWait}
	}
}
