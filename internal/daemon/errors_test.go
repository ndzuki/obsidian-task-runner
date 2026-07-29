package daemon

import "testing"

func TestRecoveryForPhase(t *testing.T) {
	tests := []struct {
		phase string
		code  ErrorCode
		want  recoveryPolicy
	}{
		{phase: "priority", code: ErrPriorityAssessmentFailed, want: recoveryPriorityFallback},
		{phase: "refining", code: ErrModelFailed, want: recoveryRetryThenBlock},
		{phase: "planning", code: ErrPhaseTimeout, want: recoveryRetryThenBlock},
		{phase: "round2", code: ErrModelFailed, want: recoveryFallbackThenBlock},
		{phase: "merge", code: ErrGitConflict, want: recoveryConflict},
		{phase: "merge", code: ErrGitHubUnavailable, want: recoveryReview},
	}
	for _, tt := range tests {
		t.Run(tt.phase+string(tt.code), func(t *testing.T) {
			if got := recoveryForPhase(tt.phase, tt.code); got != tt.want {
				t.Fatalf("recoveryForPhase(%q, %q) = %q, want %q", tt.phase, tt.code, got, tt.want)
			}
		})
	}
}

func TestStableErrorCodesAreUnique(t *testing.T) {
	seen := map[ErrorCode]bool{}
	for _, code := range stableErrorCodes {
		if code == "" || seen[code] {
			t.Fatalf("invalid or duplicate error code %q", code)
		}
		seen[code] = true
	}
	if len(seen) != 23 {
		t.Fatalf("stable error code count = %d, want 23", len(seen))
	}
}
