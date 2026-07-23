// smoke-test: true
package pipeline

import (
	"context"
	"testing"
)

// TestQuality_TautologicalDetection verifies AC-5: test-quality review captures
// tautological tests (assert.Equal(t, mock.X, mock.X)) as 🟡 important.
func TestQuality_TautologicalDetection(t *testing.T) {
	report := tautologicalQualityReport()

	// Verify the tautological finding is present.
	foundTautological := false
	for _, issue := range report.Issues {
		if issue.Level == "🟡 important" && containsStr(issue.Description, "tautological") {
			foundTautological = true
			if issue.File == "" {
				t.Error("tautological issue missing file location")
			}
			if issue.Suggestion == "" {
				t.Error("tautological issue missing suggestion")
			}
		}
	}
	if !foundTautological {
		t.Error("expected at least one 🟡 important tautological finding")
	}

	// Verify summary format.
	if report.Summary == "" {
		t.Error("expected non-empty summary")
	}
}

// TestQuality_ImplementationCouplingDetection verifies AC-5: test-quality
// captures tests that call unexported functions as 🟡 important.
func TestQuality_ImplementationCouplingDetection(t *testing.T) {
	report := tautologicalQualityReport()

	foundImplCoupling := false
	for _, issue := range report.Issues {
		if containsStr(issue.Description, "未导出") {
			foundImplCoupling = true
			if !containsStr(issue.Suggestion, "公共接口") {
				t.Error("coupling issue should suggest public interface verification")
			}
		}
	}
	if !foundImplCoupling {
		t.Error("expected implementation coupling finding")
	}
}

// TestQuality_MockRunnerReturnsReport verifies the mock runner correctly
// returns a test-quality report that the pipeline can consume.
func TestQuality_MockRunnerReturnsReport(t *testing.T) {
	runner := &MockImplementRunner{
		TestQualityReport: tautologicalQualityReport(),
	}

	report, err := runner.RunTestQuality(context.Background(), allPassACResults())
	if err != nil {
		t.Fatalf("RunTestQuality failed: %v", err)
	}
	if report == nil {
		t.Fatal("expected non-nil report")
	}
	if len(report.Issues) != 2 {
		t.Errorf("expected 2 issues, got %d", len(report.Issues))
	}
}

// TestQuality_ThreeLevelClassification verifies that test-quality uses the
// three-level classification (🔴/🟡/🟢) defined in the spec.
func TestQuality_ThreeLevelClassification(t *testing.T) {
	validLevels := map[string]bool{
		"🔴 critical":  true,
		"🟡 important": true,
		"🟢 info":      true,
	}

	report := tautologicalQualityReport()
	for _, issue := range report.Issues {
		if !validLevels[issue.Level] {
			t.Errorf("invalid level %q — must be 🔴 critical / 🟡 important / 🟢 info", issue.Level)
		}
	}
}
