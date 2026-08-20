package priority

import "testing"

func TestValidateAndNormalize(t *testing.T) {
	tests := []struct {
		name          string
		input         Result
		wantScore     int
		wantPriority  string
		wantRecommend string
		wantErr       bool
	}{
		{
			name:      "high near term partial maps to P2",
			input:     Result{Impact: "high", Urgency: "near_term", Workaround: "partial", Score: 6, Priority: "P1", Confidence: "high", Reason: "core path"},
			wantScore: 6, wantPriority: "P2",
		},
		{
			name:      "critical immediate none recommends P0",
			input:     Result{Impact: "critical", Urgency: "immediate", Workaround: "none", Score: 9, Priority: "P0", Confidence: "high", Reason: "production outage"},
			wantScore: 9, wantPriority: "P1", wantRecommend: "P0",
		},
		{
			name:    "invalid dimension rejected",
			input:   Result{Impact: "catastrophic", Urgency: "normal", Workaround: "partial", Confidence: "low", Reason: "invalid"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ValidateAndNormalize(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if got.Score != tt.wantScore || got.Priority != tt.wantPriority || got.Recommendation != tt.wantRecommend {
				t.Fatalf("normalized = %+v, want score=%d priority=%s recommendation=%s", got, tt.wantScore, tt.wantPriority, tt.wantRecommend)
			}
		})
	}
}

func TestFallback(t *testing.T) {
	got := Fallback("model output invalid")
	if got.Priority != "P2" || got.Confidence != "low" || got.Score != 4 {
		t.Fatalf("Fallback() = %+v", got)
	}
}
