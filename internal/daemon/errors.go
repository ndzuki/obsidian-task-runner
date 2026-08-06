package daemon

import "github.com/ndzuki/obsidian-task-runner/internal/task"

type ErrorCode string

const (
	ErrPriorityAssessmentFailed ErrorCode = "PRIORITY_ASSESSMENT_FAILED"
	ErrReqMissing               ErrorCode = "REQ_MISSING"
	ErrReqAmbiguous             ErrorCode = "REQ_AMBIGUOUS"
	ErrPathEscape               ErrorCode = "PATH_ESCAPE"
	ErrConfigInvalid            ErrorCode = "CONFIG_INVALID"
	ErrRemoteConfigIncomplete   ErrorCode = "REMOTE_CONFIG_INCOMPLETE"
	ErrRemotePartialCreate      ErrorCode = "REMOTE_PARTIAL_CREATE"
	ErrModelFailed              ErrorCode = "MODEL_FAILED"
	ErrAPIKeyUnavailable        ErrorCode = task.PhaseErrorCodeAPIKeyUnavailable
	ErrModelQuotaExhausted      ErrorCode = "MODEL_QUOTA_EXHAUSTED"
	ErrPhaseTimeout             ErrorCode = "PHASE_TIMEOUT"
	ErrPhaseInterrupted         ErrorCode = "PHASE_INTERRUPTED"
	ErrValidationFailed         ErrorCode = "VALIDATION_FAILED"
	ErrDocumentInvalid          ErrorCode = "DOCUMENT_INVALID"
	ErrTaskWriteConflict        ErrorCode = "TASK_WRITE_CONFLICT"
	ErrTaskFieldTampered        ErrorCode = "TASK_FIELD_TAMPERED"
	ErrTaskSchemaOutdated       ErrorCode = "TASK_SCHEMA_OUTDATED"
	ErrGitConflict              ErrorCode = "GIT_CONFLICT"
	ErrGitDirty                 ErrorCode = "GIT_DIRTY"
	ErrGitHubUnavailable        ErrorCode = "GITHUB_UNAVAILABLE"
	ErrRepoMismatch             ErrorCode = "REPO_MISMATCH"
	ErrBaseCommitMismatch       ErrorCode = "BASE_COMMIT_MISMATCH"
	ErrBranchOwnershipConflict  ErrorCode = "BRANCH_OWNERSHIP_CONFLICT"
	ErrDependencyCycle          ErrorCode = "DEPENDENCY_CYCLE"
	ErrPrerequisiteSmokeFailed  ErrorCode = "PREREQUISITE_SMOKE_FAILED"
	ErrInternal                 ErrorCode = "INTERNAL"
)

var stableErrorCodes = []ErrorCode{
	ErrPriorityAssessmentFailed,
	ErrReqMissing,
	ErrReqAmbiguous,
	ErrPathEscape,
	ErrConfigInvalid,
	ErrRemoteConfigIncomplete,
	ErrRemotePartialCreate,
	ErrModelFailed,
	ErrAPIKeyUnavailable,
	ErrModelQuotaExhausted,
	ErrPhaseTimeout,
	ErrPhaseInterrupted,
	ErrValidationFailed,
	ErrDocumentInvalid,
	ErrTaskWriteConflict,
	ErrTaskFieldTampered,
	ErrTaskSchemaOutdated,
	ErrGitConflict,
	ErrGitDirty,
	ErrGitHubUnavailable,
	ErrRepoMismatch,
	ErrBaseCommitMismatch,
	ErrBranchOwnershipConflict,
	ErrDependencyCycle,
	ErrPrerequisiteSmokeFailed,
	ErrInternal,
}

type recoveryPolicy string

const (
	recoveryPriorityFallback  recoveryPolicy = "priority-fallback"
	recoveryRetryThenBlock    recoveryPolicy = "retry-then-block"
	recoveryFallbackThenBlock recoveryPolicy = "fallback-then-block"
	recoveryConflict          recoveryPolicy = "conflict"
	recoveryReview            recoveryPolicy = "review"
	recoveryBlock             recoveryPolicy = "block"
)

func recoveryForPhase(phase string, code ErrorCode) recoveryPolicy {
	// API key unavailable is an external condition (e.g. KeePassXC locked):
	// block immediately without burning the retry budget, and let the daemon
	// auto-resume once the key becomes reachable again.
	if code == ErrAPIKeyUnavailable {
		return recoveryBlock
	}
	switch phase {
	case "priority":
		return recoveryPriorityFallback
	case "refining", "planning":
		return recoveryRetryThenBlock
	case "round2":
		return recoveryFallbackThenBlock
	case "merge":
		if code == ErrGitConflict {
			return recoveryConflict
		}
		return recoveryReview
	default:
		return recoveryBlock
	}
}
