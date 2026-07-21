// smoke-test: true
package pipeline

import (
	"os"
	"path/filepath"
	"testing"
)

// newFixtureDir creates a temporary directory for test fixtures and registers
// cleanup. Returns the directory path.
func newFixtureDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	return dir
}

// writeFile creates a file with content in dir and returns the full path.
func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("writeFile %s: %v", name, err)
	}
	return path
}

// reqDocContent returns a minimal requirement document in markdown for testing.
func reqDocContent(title, body string) string {
	return "---\nid: \"999\"\ntitle: \"" + title + "\"\n---\n\n# " + title + "\n\n" + body + "\n"
}

// samplePlan returns a Plan with risk-annotated steps for test scenarios.
func samplePlan() *Plan {
	return &Plan{
		Steps: []PlanStep{
			{Number: 1, Description: "创建核心类型", Files: []string{"types.go"}, Risk: "low", DependsOn: 0},
			{Number: 2, Description: "实现 fsnotify 文件监听", Files: []string{"monitor.go"}, Risk: "high", DependsOn: 1},
			{Number: 3, Description: "mock fixture 集成", Files: []string{"fixtures_test.go"}, Risk: "high", DependsOn: 2},
			{Number: 4, Description: "编写测试场景", Files: []string{"scenario_test.go"}, Risk: "medium", DependsOn: 3},
		},
		RiskSummary: "2 个高风险步骤，涉及 fsnotify 和进程管理",
	}
}

// allPassACResults returns all AC-1 through AC-9 as PASS.
func allPassACResults() []ACResult {
	return []ACResult{
		{ACID: "AC-1", Status: "PASS", Evidence: "triage_result: feature, triage_confidence: high"},
		{ACID: "AC-2", Status: "PASS", Evidence: "术语 'grill_context' 即时写入 CONTEXT.md"},
		{ACID: "AC-3", Status: "PASS", Evidence: "Step 3 标记 high → Prototype 建议已生成"},
		{ACID: "AC-4", Status: "PASS", Evidence: "每 AC 有独立 Red→Green→Refactor 提交"},
		{ACID: "AC-5", Status: "PASS", Evidence: "捕获 1 个 🟡 tautological，已修复"},
		{ACID: "AC-6", Status: "PASS", Evidence: "nil pointer → diagnosing-bugs；需求歧义 → requirement-elaborator"},
		{ACID: "AC-7", Status: "PASS", Evidence: "反馈→复现→假设→验证→修复→事后分析 完整"},
		{ACID: "AC-8", Status: "PASS", Evidence: "count=2 时正常循环，未触发升级"},
		{ACID: "AC-9", Status: "PASS", Evidence: "status: review, req_refine_count: 0"},
	}
}

// someFailACResults returns mixed PASS/FAIL for testing fault routing.
func someFailACResults() []ACResult {
	return []ACResult{
		{ACID: "AC-1", Status: "PASS", Evidence: "triage_result: feature"},
		{ACID: "AC-2", Status: "PASS", Evidence: "CONTEXT.md 已更新"},
		{ACID: "AC-3", Status: "PASS", Evidence: "Prototype 建议已生成"},
		{ACID: "AC-4", Status: "PASS", Evidence: "Tracer Bullet 正常"},
		{ACID: "AC-5", Status: "FAIL", Evidence: "tautological 测试未修复", ErrorLog: "PASS: TestFoo (0.00s)\n  expected: mock.X\n  actual:   mock.X", Refined: false},
	}
}

// tautologicalQualityReport returns a test-quality report with tautological findings.
func tautologicalQualityReport() *TestQualityReport {
	return &TestQualityReport{
		Issues: []TestQualityIssue{
			{
				File:        "foo_test.go:15",
				Level:       "🟡 important",
				Description: "tautological assertion: assert.Equal(t, mock.X, mock.X)",
				Suggestion:  "用独立期望值替换",
			},
			{
				File:        "foo_test.go:22",
				Level:       "🟡 important",
				Description: "测试调用未导出函数 helper()",
				Suggestion:  "改为通过公共接口间接验证",
			},
		},
		Summary: "🔴 0 / 🟡 2 / 🟢 4",
	}
}

// sixPhaseDiagnosisReport returns a complete six-phase DiagnosisReport.
func sixPhaseDiagnosisReport() *DiagnosisReport {
	return &DiagnosisReport{
		ReproduceSteps:     "1. 运行 go test -run TestFoo\n2. 观察 nil pointer panic at foo.go:42",
		ExcludedHypotheses: []string{"环境配置错误（CI 与本地行为一致）", "并发竞态（单 goroutine 复现）", "输入数据异常（固定 fixture 输入）"},
		ResidualSymptoms:   "nil pointer 在 bar.Load() 返回 nil 但调用方未检查",
		PendingQuestions:   []string{},
		Phase:              "fix",
		Resolution:         "在 bar.Load() 调用后添加 nil check，返回明确错误",
	}
}
