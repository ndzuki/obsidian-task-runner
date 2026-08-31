package daemon

import (
	"testing"

	"github.com/ndzuki/obsidian-task-runner/internal/task"
)

// TestCanAutoApproveMerge guards the auto_merge re-authorization gate
// (TASK-051/059 lesson): a merge-failure fallback that is REQ-stable with
// repair budget left re-authorizes automatically — including GITHUB_UNAVAILABLE
// (transient keyring/network failures recover; the merge gate re-checks gh
// auth and revokes again when the CLI is genuinely absent) — while the
// permanent repo-mismatch defect, REQ drift, exhausted budget, and manual
// tasks stay human-gated. A fresh review (no phase error) always qualifies.
func TestCanAutoApproveMerge(t *testing.T) {
	const reqHash = "sha256:abc"
	tests := []struct {
		name string
		task task.ReadyTask
		hash string
		max  int
		want bool
	}{
		{
			name: "fresh review auto-approves",
			task: task.ReadyTask{Status: "review", AutoMerge: true},
			want: true,
		},
		{
			name: "review with merge error and budget re-authorizes",
			task: task.ReadyTask{Status: "conflict", AutoMerge: true,
				PhaseErrorCode: "GIT_CONFLICT", PlanReqHash: reqHash, MergeRetryCount: 2},
			hash: reqHash,
			max:  3,
			want: true,
		},
		{
			name: "review with base-commit-mismatch error re-authorizes",
			task: task.ReadyTask{Status: "conflict", AutoMerge: true,
				PhaseErrorCode: "BASE_COMMIT_MISMATCH", PlanReqHash: reqHash, MergeRetryCount: 0},
			hash: reqHash,
			max:  3,
			want: true,
		},
		{
			name: "already approved stays approved",
			task: task.ReadyTask{Status: "conflict", AutoMerge: true, MergeApproved: true,
				PhaseErrorCode: "GIT_CONFLICT", PlanReqHash: reqHash, MergeRetryCount: 0},
			hash: reqHash,
			max:  3,
			want: false,
		},
		{
			name: "manual task never auto-approves",
			task: task.ReadyTask{Status: "review", AutoMerge: false},
			want: false,
		},
		{
			name: "pending_req revokes auto-approval",
			task: task.ReadyTask{Status: "review", AutoMerge: true, PendingReq: true},
			want: false,
		},
		{
			name: "gh unavailable without req hash stays manual",
			task: task.ReadyTask{Status: "review", AutoMerge: true, PhaseErrorCode: "GITHUB_UNAVAILABLE"},
			want: false,
		},
		{
			name: "gh unavailable with stable req re-authorizes (transient keyring/network)",
			task: task.ReadyTask{Status: "review", AutoMerge: true,
				PhaseErrorCode: "GITHUB_UNAVAILABLE", PlanReqHash: reqHash, MergeRetryCount: 0},
			hash: reqHash,
			max:  3,
			want: true,
		},
		{
			name: "repo mismatch is permanent",
			task: task.ReadyTask{Status: "conflict", AutoMerge: true, PhaseErrorCode: "REPO_MISMATCH"},
			want: false,
		},
		{
			name: "req drift routes to refining instead",
			task: task.ReadyTask{Status: "conflict", AutoMerge: true,
				PhaseErrorCode: "BASE_COMMIT_MISMATCH", PlanReqHash: "sha256:old", MergeRetryCount: 0},
			hash: reqHash,
			max:  3,
			want: false,
		},
		{
			name: "exhausted budget stays manual",
			task: task.ReadyTask{Status: "conflict", AutoMerge: true,
				PhaseErrorCode: "GIT_CONFLICT", PlanReqHash: reqHash, MergeRetryCount: 3},
			hash: reqHash,
			max:  3,
			want: false,
		},
		{
			name: "other status never auto-approves",
			task: task.ReadyTask{Status: "implementing", AutoMerge: true},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := canAutoApproveMerge(tt.task, tt.hash, tt.max); got != tt.want {
				t.Fatalf("canAutoApproveMerge(%+v) = %v, want %v", tt.task, got, tt.want)
			}
		})
	}
}
