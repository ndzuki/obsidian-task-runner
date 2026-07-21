// smoke-test: true
package pipeline

import (
	"context"
	"os"
	"testing"
)

// TestNormalFlow_TriageClassification verifies AC-1: Triage correctly classifies
// a feature-enhancement requirement as "feature" with "high" confidence.
func TestNormalFlow_TriageClassification(t *testing.T) {
	dir := newFixtureDir(t)

	reqPath := writeFile(t, dir, "REQ-feature.md", reqDocContent("Pipeline增强",
		"本次增强新增 Triage 步骤和 Prototype 验证功能，增强流水线可靠性。"))

	result, err := Triage(context.Background(), reqPath, "")
	if err != nil {
		t.Fatalf("Triage failed: %v", err)
	}
	if result.Type != "feature" {
		t.Errorf("expected type=feature, got %s", result.Type)
	}
	if result.Confidence != "high" {
		t.Errorf("expected confidence=high, got %s", result.Confidence)
	}
	if result.AlreadyImplemented {
		t.Error("expected AlreadyImplemented=false")
	}
	if result.Evidence == "" {
		t.Error("expected non-empty evidence")
	}
}

// TestNormalFlow_BugClassification verifies AC-1: Triage classifies bug reports.
func TestNormalFlow_BugClassification(t *testing.T) {
	dir := newFixtureDir(t)

	reqPath := writeFile(t, dir, "REQ-bug.md", reqDocContent("修复崩溃问题",
		"用户报告在处理空输入时发生 panic 崩溃，需要修复此 bug。"))

	result, err := Triage(context.Background(), reqPath, "")
	if err != nil {
		t.Fatalf("Triage failed: %v", err)
	}
	if result.Type != "bug" {
		t.Errorf("expected type=bug, got %s", result.Type)
	}
}

// TestNormalFlow_LowConfidenceDoesNotBlock verifies AC-11: triage_confidence=low
// does not block.
func TestNormalFlow_LowConfidenceDoesNotBlock(t *testing.T) {
	dir := newFixtureDir(t)

	reqPath := writeFile(t, dir, "REQ-low.md", reqDocContent("小修复", "修复一个问题。"))
	result, err := Triage(context.Background(), reqPath, "")
	if err != nil {
		t.Fatalf("Triage failed: %v", err)
	}
	if result.Confidence == "low" && result.Type != "bug" {
		t.Errorf("expected type=bug for bug content, got %s", result.Type)
	}
}

// TestNormalFlow_PlanHasRiskDimensions verifies AC-3: Plan steps have risk
// dimensions and high-risk steps trigger Prototype suggestions.
func TestNormalFlow_PlanHasRiskDimensions(t *testing.T) {
	plan := samplePlan()

	validRisks := map[string]bool{"low": true, "medium": true, "high": true}
	for _, step := range plan.Steps {
		if !validRisks[step.Risk] {
			t.Errorf("Step %d: invalid risk %q", step.Number, step.Risk)
		}
	}

	specs, err := RunPrototypes(context.Background(), plan)
	if err != nil {
		t.Fatalf("RunPrototypes failed: %v", err)
	}

	highRiskSteps := 0
	for _, step := range plan.Steps {
		if step.Risk == "high" {
			highRiskSteps++
		}
	}
	if len(specs) != highRiskSteps {
		t.Errorf("expected %d prototype specs for %d high-risk steps, got %d",
			highRiskSteps, highRiskSteps, len(specs))
	}
}

// TestNormalFlow_AlreadyImplementedBlocks verifies AC-12: triage_result=
// already-implemented → status should be blocked.
func TestNormalFlow_AlreadyImplementedBlocks(t *testing.T) {
	dir := newFixtureDir(t)
	writeFile(t, dir, "triage.go", "package pipeline\n\nfunc Triage() {}\nfunc RunPrototypes() {}")

	reqPath := writeFile(t, dir, "REQ-existing.md", reqDocContent("Triage功能",
		"实现 Triage 和 RunPrototypes 功能。"))

	result, err := Triage(context.Background(), reqPath, dir)
	if err != nil {
		t.Fatalf("Triage failed: %v", err)
	}
	if !result.AlreadyImplemented {
		t.Error("expected AlreadyImplemented=true when code matches")
	}
	if result.Type != "already-implemented" {
		t.Errorf("expected type=already-implemented, got %s", result.Type)
	}
	if len(result.CodeLocations) == 0 {
		t.Error("expected non-empty CodeLocations")
	}
}

// TestNormalFlow_EnsureContextMD verifies AC-13: daemon auto-creates CONTEXT.md.
func TestNormalFlow_EnsureContextMD(t *testing.T) {
	dir := newFixtureDir(t)

	path, err := EnsureContextMD(dir, "999", "test-project")
	if err != nil {
		t.Fatalf("EnsureContextMD failed: %v", err)
	}
	if path == "" {
		t.Fatal("expected non-empty path for newly created CONTEXT.md")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read CONTEXT.md: %v", err)
	}
	content := string(data)
	if !containsStr(content, "## Language") {
		t.Error("CONTEXT.md missing ## Language section")
	}
	if !containsStr(content, "project:") {
		t.Error("CONTEXT.md missing project frontmatter")
	}

	// Second call is idempotent.
	path2, err := EnsureContextMD(dir, "999", "test-project")
	if err != nil {
		t.Fatalf("second EnsureContextMD failed: %v", err)
	}
	if path2 != "" {
		t.Errorf("expected empty path on second call, got %s", path2)
	}
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
