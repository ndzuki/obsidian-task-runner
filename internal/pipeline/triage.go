// smoke-test: true
// Package pipeline implements smoke-test stubs for the enhanced task runner pipeline.
// All code in this package is throwaway — it verifies the pipeline enhancements
// defined in the obsidian-task-runner SKILL.md and will NOT be merged to main.
package pipeline

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// ── Core Types ──────────────────────────────────────────────────────────────

// TriageResult captures the outcome of the Triage classification step.
type TriageResult struct {
	Type               string   // "feature" | "bug" | "already-implemented"
	Confidence         string   // "high" | "medium" | "low"
	Evidence           string   // classification rationale (matched keywords, code search hits)
	AlreadyImplemented bool     // true only when type="already-implemented"
	CodeLocations      []string // code locations when already-implemented, nil otherwise
}

// PlanStep is a single step in the implementation plan.
type PlanStep struct {
	Number      int
	Description string
	Files       []string
	Risk        string // "low" | "medium" | "high"
	DependsOn   int    // preceding step number, 0 if none
}

// PrototypeSpec describes a throwaway prototype validation for a high-risk step.
type PrototypeSpec struct {
	StepNumber  int      // associated Plan Step number
	Description string   // validation goal (from ## Prototype 建议 entries)
	Commands    []string // suggested throwaway validation commands
}

// Plan holds the full implementation plan with optional prototype suggestions.
type Plan struct {
	Steps       []PlanStep
	Prototypes  []PrototypeSpec // high-risk step prototype entries
	RiskSummary string          // optional overall risk summary
}

// ACResult records the outcome of a single acceptance criterion.
type ACResult struct {
	ACID     string // "AC-1", "AC-2", ...
	Status   string // "PASS" | "FAIL"
	Evidence string // pass/fail evidence
	ErrorLog string // test output snippet on failure
	Refined  bool   // whether this AC triggered a requirement gap diversion
}

// DiagnosisReport captures the six-phase debugging cycle output.
type DiagnosisReport struct {
	ReproduceSteps     string   // steps to reproduce the failure
	ExcludedHypotheses []string // hypotheses that were ruled out
	ResidualSymptoms   string   // symptoms still unexplained
	PendingQuestions   []string // questions awaiting user confirmation
	Phase              string   // "feedback_loop" | "reproduce" | "hypothesize" | "verify" | "fix" | "postmortem"
	Resolution         string   // fix applied (populated after Phase="fix")
}

// String renders the DiagnosisReport as Markdown for AC-7 interactive messages.
func (r *DiagnosisReport) String() string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("## Diagnosis Report — Phase: %s\n\n", r.Phase))
	b.WriteString(fmt.Sprintf("**Reproduce Steps**: %s\n\n", r.ReproduceSteps))
	if len(r.ExcludedHypotheses) > 0 {
		b.WriteString("**Excluded Hypotheses**:\n")
		for _, h := range r.ExcludedHypotheses {
			b.WriteString(fmt.Sprintf("- %s\n", h))
		}
		b.WriteString("\n")
	}
	b.WriteString(fmt.Sprintf("**Residual Symptoms**: %s\n\n", r.ResidualSymptoms))
	if len(r.PendingQuestions) > 0 {
		b.WriteString("**Pending Questions**:\n")
		for _, q := range r.PendingQuestions {
			b.WriteString(fmt.Sprintf("- %s\n", q))
		}
		b.WriteString("\n")
	}
	if r.Resolution != "" {
		b.WriteString(fmt.Sprintf("**Resolution**: %s\n", r.Resolution))
	}
	return b.String()
}

// ── Mock Runners ────────────────────────────────────────────────────────────

// MockPlanRunner mocks the Round 1 plan phase for smoke test scenarios.
// Fields are assigned directly — nil pointer panics are intentional failure signals.
type MockPlanRunner struct {
	TriageResult *TriageResult
	Plan         *Plan
	TriageErr    error
	PlanErr      error
}

func (m *MockPlanRunner) RunTriage(ctx context.Context, reqPath string) (*TriageResult, error) {
	return m.TriageResult, m.TriageErr
}

func (m *MockPlanRunner) GeneratePlan(ctx context.Context, reqPath string) (*Plan, error) {
	return m.Plan, m.PlanErr
}

// TestQualityReport captures the output of a test-quality review.
type TestQualityReport struct {
	Issues  []TestQualityIssue
	Summary string // e.g. "🔴 0 / 🟡 2 / 🟢 4"
}

// TestQualityIssue is a single finding from test-quality review.
type TestQualityIssue struct {
	File        string // file:line
	Level       string // "🔴 critical" | "🟡 important" | "🟢 info"
	Description string
	Suggestion  string
}

// extractCodeTerms naively extracts CamelCase and snake_case identifiers from text.
func extractCodeTerms(content string) []string {
	re := regexp.MustCompile(`\b([A-Z][a-zA-Z]+|[a-z]+(?:_[a-z]+)+)\b`)
	matches := re.FindAllString(content, -1)
	seen := make(map[string]bool)
	var result []string
	for _, m := range matches {
		if !seen[m] {
			seen[m] = true
			result = append(result, m)
		}
	}
	return result
}

type MockImplementRunner struct {
	ACResults         []ACResult
	TestQualityReport *TestQualityReport
	DiagnosisReport   *DiagnosisReport
	TracerBulletErr   error
	TestQualityErr    error
	DiagnosingBugsErr error
}

func (m *MockImplementRunner) RunTracerBullet(ctx context.Context, plan *Plan) ([]ACResult, error) {
	return m.ACResults, m.TracerBulletErr
}

func (m *MockImplementRunner) RunTestQuality(ctx context.Context, acResults []ACResult) (*TestQualityReport, error) {
	return m.TestQualityReport, m.TestQualityErr
}

func (m *MockImplementRunner) RunDiagnosingBugs(ctx context.Context, errLog string) (*DiagnosisReport, error) {
	return m.DiagnosisReport, m.DiagnosingBugsErr
}

// ── Triage ──────────────────────────────────────────────────────────────────

// bugKeywords matches bug report terminology for Triage classification.
var bugKeywords = regexp.MustCompile(`(?i)\b(bug|修复|异常|报错|崩溃|panic|nil\s*pointer)\b`)

// contextBugPattern matches phrases where bug keywords appear as context, not the primary topic.
var contextBugPattern = regexp.MustCompile(`(?i)(修复了.*(?:bug|问题).*后|在.*(?:bug|问题).*基础上)`)

// Triage classifies a requirement document into feature / bug / already-implemented.
// It reads the requirement file at reqPath and applies the decision tree:
//  1. already-implemented (highest priority) — code location search
//  2. bug — keyword matching against bugKeywords
//  3. feature — default fallback
//
// The cfg parameter provides project context for code searches.
func Triage(ctx context.Context, reqPath string, projectDir string) (*TriageResult, error) {
	data, err := os.ReadFile(reqPath)
	if err != nil {
		return nil, fmt.Errorf("triage: read req doc: %w", err)
	}
	content := string(data)

	// Priority 1: check if already implemented by searching for matching code.
	result := checkAlreadyImplemented(ctx, content, projectDir)
	if result != nil {
		return result, nil
	}

	// Priority 2: bug detection via keyword matching.
	if isBugReport(content) {
		return &TriageResult{
			Type:       "bug",
			Confidence: bugConfidence(content),
			Evidence:   fmt.Sprintf("关键词匹配: %s", extractMatchedKeywords(content)),
		}, nil
	}

	// Priority 3: default to feature enhancement.
	return &TriageResult{
		Type:       "feature",
		Confidence: "high",
		Evidence:   "默认分类: 未匹配 bug 关键词，未检测到已实现代码",
	}, nil
}

// checkAlreadyImplemented searches the project directory for code matching
// core function names mentioned in the requirement. Returns nil if no match.
func checkAlreadyImplemented(ctx context.Context, content, projectDir string) *TriageResult {
	if projectDir == "" {
		return nil
	}
	// Extract candidate function/type names from the requirement (simplified heuristic).
	candidates := extractCodeTerms(content)
	if len(candidates) == 0 {
		return nil
	}

	matched := 0
	var locations []string
	select {
	case <-ctx.Done():
		return nil
	default:
	}
	for _, term := range candidates {
		if loc := searchCodebase(ctx, projectDir, term); loc != "" {
			matched++
			locations = append(locations, loc)
		}
	}

	hitRate := float64(matched) / float64(len(candidates))
	if hitRate > 0.8 && matched >= 2 {
		confidence := "high"
		if hitRate < 0.9 {
			confidence = "medium"
		}
		return &TriageResult{
			Type:               "already-implemented",
			Confidence:         confidence,
			Evidence:           fmt.Sprintf("代码搜索命中率: %.0f%% (%d/%d)", hitRate*100, matched, len(candidates)),
			AlreadyImplemented: true,
			CodeLocations:      locations,
		}
	}
	return nil
}

// searchCodebase greps for a term in .go files under projectDir at any depth.
func searchCodebase(ctx context.Context, projectDir, term string) string {
	var found string
	filepath.WalkDir(projectDir, func(path string, d fs.DirEntry, err error) error {
		select {
		case <-ctx.Done():
			return filepath.SkipAll
		default:
		}
		if err != nil {
			return nil // skip unreadable entries
		}
		if d.IsDir() && strings.HasPrefix(d.Name(), ".") {
			return filepath.SkipDir // skip hidden dirs
		}
		if !d.IsDir() && strings.HasSuffix(d.Name(), ".go") {
			data, err := os.ReadFile(path)
			if err == nil && strings.Contains(string(data), term) {
				found = path
				return filepath.SkipAll // found it, stop walking
			}
		}
		return nil
	})
	return found
}

// isBugReport checks if the content is primarily a bug report (not contextual mentions).
func isBugReport(content string) bool {
	if contextBugPattern.MatchString(content) {
		return false
	}
	return bugKeywords.MatchString(content)
}

// bugConfidence estimates classification confidence based on keyword density.
func bugConfidence(content string) string {
	matches := bugKeywords.FindAllString(content, -1)
	switch {
	case len(matches) >= 3:
		return "high"
	case len(matches) >= 2:
		return "medium"
	default:
		return "low"
	}
}

// extractMatchedKeywords returns the matched bug keywords for evidence.
func extractMatchedKeywords(content string) string {
	matches := bugKeywords.FindAllString(content, -1)
	return strings.Join(matches, ", ")
}

// ── CONTEXT.md Template ─────────────────────────────────────────────────────

// contextTemplate is the auto-generated CONTEXT.md content when a project lacks one.
const contextTemplate = `---
project: "%s"
project_id: "%s"
type: context
status: active
created: "%s"
updated: "%s"
---

# %s — 共享语言

> 最后更新: %s
> 本文件由以下阶段自动维护：
> - Round 1：计划中引入新领域术语时追加
> - Round 2 + ADR：ADR 引入新架构概念时追加
> 不要手动编辑，agent 会按需追加新条目。

## Language

<!-- 领域词汇表。每个条目格式：**Term**: 一句话定义。_Avoid_: 废弃旧称。 -->`

// EnsureContextMD creates a CONTEXT.md template if one does not exist in the
// project's Notes directory. Returns the path if created, empty string if it
// already exists.
func EnsureContextMD(projectDir, projectID, projectSlug string) (string, error) {
	notesDir := filepath.Join(projectDir, "Notes")
	contextPath := filepath.Join(notesDir, "CONTEXT.md")
	if _, err := os.Stat(contextPath); err == nil {
		return "", nil // already exists
	}
	if err := os.MkdirAll(notesDir, 0755); err != nil {
		return "", fmt.Errorf("ensure context: mkdir Notes: %w", err)
	}
	now := time.Now().Format(time.RFC3339)
	content := fmt.Sprintf(contextTemplate, projectSlug, projectID, now, now, projectSlug, now)
	if err := os.WriteFile(contextPath, []byte(content), 0644); err != nil {
		return "", fmt.Errorf("ensure context: write: %w", err)
	}
	return contextPath, nil
}
