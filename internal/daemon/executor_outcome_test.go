package daemon

import "testing"

func TestMapExecOutcome(t *testing.T) {
	tests := []struct {
		name     string
		result   *ExecutionResult
		wantOut  ExecOutcome
		wantCode ErrorCode
		wantErr  string
	}{
		{name: "success", result: &ExecutionResult{Code: OutcomeSuccess}, wantOut: OutcomeSuccess, wantCode: "", wantErr: ""},
		{name: "timeout", result: &ExecutionResult{Code: OutcomeTimedOut}, wantOut: OutcomeTimedOut, wantCode: ErrPhaseTimeout, wantErr: "phase timed out"},
		{name: "timeout active", result: &ExecutionResult{Code: OutcomeTimedOutActive}, wantOut: OutcomeTimedOutActive, wantCode: ErrPhaseInterrupted, wantErr: "phase session still active after timeout window (next scan resumes)"},
		{name: "interrupted", result: &ExecutionResult{Code: OutcomeInterrupted}, wantOut: OutcomeInterrupted, wantCode: ErrPhaseInterrupted, wantErr: "interrupted by daemon shutdown"},
		{name: "quota", result: &ExecutionResult{Code: OutcomeQuotaExhausted}, wantOut: OutcomeQuotaExhausted, wantCode: ErrModelQuotaExhausted, wantErr: "model quota exhausted"},
		{name: "key unavailable", result: &ExecutionResult{Code: OutcomeKeyUnavailable}, wantOut: OutcomeKeyUnavailable, wantCode: ErrAPIKeyUnavailable, wantErr: "api key unavailable"},
		{name: "empty response", result: &ExecutionResult{Code: OutcomeEmptyResponse}, wantOut: OutcomeEmptyResponse, wantCode: ErrModelFailed, wantErr: "empty model response"},
		{name: "failed with error", result: &ExecutionResult{Code: OutcomeFailed, Error: "provider down"}, wantOut: OutcomeFailed, wantCode: ErrModelFailed, wantErr: "provider down"},
		{name: "failed no error", result: &ExecutionResult{Code: OutcomeFailed}, wantOut: OutcomeFailed, wantCode: ErrModelFailed, wantErr: "phase failed"},
		{name: "nil result", result: nil, wantOut: OutcomeFailed, wantCode: ErrModelFailed, wantErr: "executor returned no result"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, code, reason := mapExecOutcome(tt.result)
			if out != tt.wantOut || code != tt.wantCode || reason != tt.wantErr {
				t.Fatalf("mapExecOutcome() = (%q, %q, %q), want (%q, %q, %q)", out, code, reason, tt.wantOut, tt.wantCode, tt.wantErr)
			}
		})
	}
}
