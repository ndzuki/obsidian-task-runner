// smoke-test: true
package pipeline

import (
	"context"
	"testing"
)

// TestDiagnosingBugs_SixPhaseCycle verifies AC-7: DiagnosisReport captures all
// six phases of the debugging cycle.
func TestDiagnosingBugs_SixPhaseCycle(t *testing.T) {
	report := sixPhaseDiagnosisReport()

	// Phase must be one of the six defined stages.
	validPhases := map[string]bool{
		"feedback_loop": true,
		"reproduce":     true,
		"hypothesize":   true,
		"verify":        true,
		"fix":           true,
		"postmortem":    true,
	}
	if !validPhases[report.Phase] {
		t.Errorf("invalid phase %q — must be one of the six stages", report.Phase)
	}

	// All fields must be populated for a complete report.
	if report.ReproduceSteps == "" {
		t.Error("ReproduceSteps is empty")
	}
	if len(report.ExcludedHypotheses) == 0 {
		t.Error("ExcludedHypotheses is empty — must document ruled-out causes")
	}
	if report.ResidualSymptoms == "" {
		t.Error("ResidualSymptoms is empty")
	}

	// When phase is "fix", Resolution must be populated.
	if report.Phase == "fix" && report.Resolution == "" {
		t.Error("Resolution is empty despite Phase=fix")
	}
}

// TestDiagnosingBugs_StringOutput verifies AC-7: String() produces Markdown
// containing all key sections for interactive messages.
func TestDiagnosingBugs_StringOutput(t *testing.T) {
	report := sixPhaseDiagnosisReport()
	output := report.String()

	requiredSections := []string{
		"Diagnosis Report",
		"Reproduce Steps",
		"Excluded Hypotheses",
		"Residual Symptoms",
		"Resolution",
	}
	for _, section := range requiredSections {
		if !containsStr(output, section) {
			t.Errorf("String() output missing section %q", section)
		}
	}
}

// TestDiagnosingBugs_ExcludedHypothesesTracked verifies AC-7: excluded
// hypotheses prevent redundant investigation.
func TestDiagnosingBugs_ExcludedHypothesesTracked(t *testing.T) {
	report := sixPhaseDiagnosisReport()

	if len(report.ExcludedHypotheses) < 2 {
		t.Error("expected at least 2 excluded hypotheses")
	}

	// Each hypothesis should be substantive, not empty.
	for i, h := range report.ExcludedHypotheses {
		if len(h) < 10 {
			t.Errorf("hypothesis %d too short: %q", i, h)
		}
	}
}

// TestDiagnosingBugs_MockRunnerReturnsReport verifies the mock runner
// returns a complete DiagnosisReport for pipeline consumption.
func TestDiagnosingBugs_MockRunnerReturnsReport(t *testing.T) {
	runner := &MockImplementRunner{
		DiagnosisReport: sixPhaseDiagnosisReport(),
	}

	report, err := runner.RunDiagnosingBugs(context.Background(), "nil pointer at foo.go:42")
	if err != nil {
		t.Fatalf("RunDiagnosingBugs failed: %v", err)
	}
	if report == nil {
		t.Fatal("expected non-nil report")
	}
	if report.Phase != "fix" {
		t.Errorf("expected phase=fix, got %s", report.Phase)
	}
}
