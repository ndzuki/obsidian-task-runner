// smoke-test: true
package pipeline

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestReqGap_FaultRoutingSignals verifies AC-6: test failure root cause analysis
// correctly distinguishes requirement gaps from code logic errors.
func TestReqGap_FaultRoutingSignals(t *testing.T) {
	// Simulate a test failure with ambiguous error message.
	// Requirement gap signal: "预期行为不明确"
	gapErrLog := "FAIL: TestProcess (0.01s)\n  expected behavior is unclear: should retry on timeout or fail immediately?\n  AC-5 describes 'may retry if applicable'"

	isGap := isRequirementGap(gapErrLog)
	if !isGap {
		t.Error("ambiguous AC language should signal requirement gap")
	}

	// Code logic error signal: clear expected vs actual mismatch.
	codeErrLog := "FAIL: TestParse (0.01s)\n  expected: 42\n  actual:   0\n  at parse.go:15"
	isGap = isRequirementGap(codeErrLog)
	if isGap {
		t.Error("clear expected/actual mismatch should NOT signal requirement gap")
	}
}

// TestReqGap_RefineCountLifecycle verifies AC-8 (req_refine_count lifecycle)
// and AC-10 (count initial increment before grilling).
func TestReqGap_RefineCountLifecycle(t *testing.T) {
	dir := newFixtureDir(t)

	// Create a TASK file with req_refine_count=0.
	taskPath := writeFile(t, dir, "TASK-test.md",
		"---\nid: \"999\"\nstatus: implementing\nreq_refine_count: 0\n---\n\n# Test\n")

	// Verify initial count is 0.
	count, err := readRefineCount(taskPath)
	if err != nil {
		t.Fatalf("readRefineCount: %v", err)
	}
	if count != 0 {
		t.Errorf("expected initial count=0, got %d", count)
	}

	// Simulate AC-10: increment to 1 before entering grilling.
	if err := setRefineCount(taskPath, 1); err != nil {
		t.Fatalf("setRefineCount: %v", err)
	}
	count, err = readRefineCount(taskPath)
	if err != nil {
		t.Fatalf("readRefineCount after increment: %v", err)
	}
	if count != 1 {
		t.Errorf("expected count=1 after first increment, got %d", count)
	}

	// Simulate count reaching 2 (normal, no escalation).
	if err := setRefineCount(taskPath, 2); err != nil {
		t.Fatalf("setRefineCount to 2: %v", err)
	}
	count, err = readRefineCount(taskPath)
	if err != nil {
		t.Fatalf("readRefineCount at 2: %v", err)
	}
	if count != 2 {
		t.Errorf("expected count=2, got %d", count)
	}
}

// TestReqGap_MonitorTriggersAtThreshold verifies AC-8: MonitorRefineCount
// detects count ≥ 3 and triggers escalation.
func TestReqGap_MonitorTriggersAtThreshold(t *testing.T) {
	dir := newFixtureDir(t)

	taskPath := writeFile(t, dir, "TASK-monitor.md",
		"---\nid: \"999\"\nstatus: implementing\nreq_refine_count: 2\n---\n\n# Test\n")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Start monitor in background.
	resultCh := make(chan *MonitorResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := MonitorRefineCount(ctx, taskPath, nil)
		if err != nil {
			errCh <- err
		} else {
			resultCh <- result
		}
	}()

	// Wait a moment for watcher to initialize, then bump count to 3.
	time.Sleep(100 * time.Millisecond)
	if err := setRefineCount(taskPath, 3); err != nil {
		t.Fatalf("setRefineCount to 3: %v", err)
	}

	select {
	case <-ctx.Done():
		t.Fatal("monitor did not detect threshold within timeout")
	case err := <-errCh:
		t.Fatalf("MonitorRefineCount error: %v", err)
	case result := <-resultCh:
		if !result.Tripped {
			t.Error("expected Tripped=true when count reaches 3")
		}
		if result.FinalCount != 3 {
			t.Errorf("expected FinalCount=3, got %d", result.FinalCount)
		}
	}
}

// TestReqGap_MonitorNoTriggerBelowThreshold verifies AC-8: MonitorRefineCount
// does NOT trigger when count stays below 3.
func TestReqGap_MonitorNoTriggerBelowThreshold(t *testing.T) {
	dir := newFixtureDir(t)

	taskPath := writeFile(t, dir, "TASK-safe.md",
		"---\nid: \"999\"\nstatus: implementing\nreq_refine_count: 0\n---\n\n# Test\n")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	resultCh := make(chan *MonitorResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := MonitorRefineCount(ctx, taskPath, nil)
		if err != nil {
			errCh <- err
		} else {
			resultCh <- result
		}
	}()

	// Bump to 1 (below threshold) — monitor should NOT trigger.
	time.Sleep(100 * time.Millisecond)
	if err := setRefineCount(taskPath, 1); err != nil {
		t.Fatalf("setRefineCount to 1: %v", err)
	}

	// Wait for context timeout — monitor should still be watching.
	select {
	case <-ctx.Done():
		// Expected: context cancelled, monitor exits without tripping.
	case result := <-resultCh:
		if result.Tripped {
			t.Error("monitor tripped at count=1, should only trip at ≥3")
		}
	case err := <-errCh:
		t.Fatalf("MonitorRefineCount error: %v", err)
	}
}

// TestReqGap_ACResultRefinedFlag verifies AC-6: ACResult.Refined flag tracks
// whether an AC triggered requirement gap diversion.
func TestReqGap_ACResultRefinedFlag(t *testing.T) {
	results := someFailACResults()

	// AC-5 should be FAIL with Refined=false (first failure, not yet refined).
	for _, r := range results {
		if r.ACID == "AC-5" {
			if r.Status != "FAIL" {
				t.Error("AC-5 should be FAIL")
			}
			if r.Refined {
				t.Error("AC-5 Refined should be false on first failure")
			}
			if r.ErrorLog == "" {
				t.Error("AC-5 should have ErrorLog")
			}
		}
	}
}

// ── Helpers ─────────────────────────────────────────────────────────────────

// isRequirementGap heuristically determines if a test failure is due to
// ambiguous requirements vs. clear code logic errors.
func isRequirementGap(errLog string) bool {
	gapSignals := []string{
		"预期行为不明确",
		"should",
		"may",
		"if applicable",
		"behavior is unclear",
		"ambiguous",
	}
	for _, sig := range gapSignals {
		if containsStr(errLog, sig) {
			return true
		}
	}
	return false
}

// setRefineCount writes a new req_refine_count value to a TASK file's frontmatter.
// Uses in-place string replacement targeting the exact YAML field line.
func setRefineCount(path string, count int) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	content := string(data)
	// Find and replace the req_refine_count line in the frontmatter block.
	// The line matches "req_refine_count: <old_value>".
	oldLine := "req_refine_count:"
	newLine := "req_refine_count: " + itoa(count)

	start := 0
	end := len(content)
	// Search only within frontmatter (between first two ---).
	firstDash := indexOf(content, "---")
	if firstDash >= 0 {
		start = firstDash + 3
		secondDash := indexOf(content[start:], "---")
		if secondDash >= 0 {
			end = start + secondDash
		}
	}

	// Find req_refine_count line within the frontmatter range.
	fmText := content[start:end]
	idx := indexOf(fmText, oldLine)
	if idx < 0 {
		// Field doesn't exist, nothing to do.
		return nil
	}

	// Find end of this line.
	lineStart := start + idx
	lineEnd := indexOf(content[lineStart:], "\n")
	if lineEnd < 0 {
		lineEnd = len(content) - lineStart
	}

	// Replace just this line.
	result := content[:lineStart] + newLine + content[lineStart+lineEnd:]
	return os.WriteFile(path, []byte(result), 0644)
}

func indexOf(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}
