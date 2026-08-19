package daemon

import "testing"

func TestParseDshErrorLine(t *testing.T) {
	tests := []struct {
		name        string
		stderr      string
		wantCode    string
		wantMessage string
	}{
		{name: "code and message", stderr: "dsh: QUOTA: quota exceeded\n", wantCode: "QUOTA", wantMessage: "quota exceeded"},
		{name: "invalid credential", stderr: "noise\ndsh: INVALID_CREDENTIAL: the API key is blank\n", wantCode: "INVALID_CREDENTIAL", wantMessage: "the API key is blank"},
		{name: "plain no code", stderr: "dsh: some direct failure\n", wantCode: "", wantMessage: "some direct failure"},
		{name: "no dsh line", stderr: "unrelated output\n", wantCode: "", wantMessage: ""},
		{name: "empty", stderr: "", wantCode: "", wantMessage: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, msg := parseDshErrorLine(tt.stderr)
			if code != tt.wantCode || msg != tt.wantMessage {
				t.Fatalf("parseDshErrorLine() = (%q, %q), want (%q, %q)", code, msg, tt.wantCode, tt.wantMessage)
			}
		})
	}
}

func TestDshFailureResultMapping(t *testing.T) {
	tests := []struct {
		name     string
		stderr   string
		wantCode ExecOutcome
	}{
		{name: "quota", stderr: "dsh: QUOTA: quota exceeded", wantCode: OutcomeQuotaExhausted},
		{name: "empty response", stderr: "dsh: EMPTY_RESPONSE: empty", wantCode: OutcomeEmptyResponse},
		{name: "invalid credential", stderr: "dsh: INVALID_CREDENTIAL: blank key", wantCode: OutcomeKeyUnavailable},
		{name: "unknown code", stderr: "dsh: SERVER: internal", wantCode: OutcomeFailed},
		{name: "no stderr", stderr: "", wantCode: OutcomeFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := dshFailureResult("round2", tt.stderr, errStub("exit 1"))
			if res.Code != tt.wantCode {
				t.Fatalf("Code=%q, want %q", res.Code, tt.wantCode)
			}
			if res.Error == "" {
				t.Fatal("failure result must carry a reason")
			}
		})
	}
}

// errStub is a minimal error for the reason fallback.
type errStub string

func (e errStub) Error() string { return string(e) }
