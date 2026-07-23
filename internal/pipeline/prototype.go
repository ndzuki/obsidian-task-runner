// smoke-test: true
package pipeline

import (
	"context"
	"fmt"
)

// RunPrototypes parses a Plan for high-risk steps and returns the corresponding
// PrototypeSpec entries. In the real daemon, this would execute OMP prototype
// agents for throwaway validation. The smoke test stub returns the parsed specs
// without executing external processes.
func RunPrototypes(ctx context.Context, plan *Plan) ([]PrototypeSpec, error) {
	if plan == nil {
		return nil, nil
	}
	var specs []PrototypeSpec
	for _, step := range plan.Steps {
		if step.Risk == "high" {
			spec := PrototypeSpec{
				StepNumber:  step.Number,
				Description: fmt.Sprintf("验证 Step %d (%s) 在边界条件下的行为", step.Number, step.Description),
				Commands: []string{
					fmt.Sprintf("go test ./internal/pipeline/ -run TestStep%d -count=1", step.Number),
				},
			}
			specs = append(specs, spec)
		}
	}
	return specs, nil
}

// RiskForStep determines the risk level for a plan step using the two-factor
// model: uncertainty × impact.
//
//	high   — high uncertainty (undocumented API, new framework, cross-system
//	         interaction) OR large impact (core data model change, breaking API)
//	medium — some uncertainty but locally verifiable, or medium impact
//	low    — high certainty (existing pattern, CRUD extension, docs), small impact
func RiskForStep(description string, files []string) string {
	// Simplified heuristic for smoke test.
	highRiskTerms := []string{"fsnotify", "进程管理", "mock fixture", "kill", "跨系统", "破坏性"}
	mediumRiskTerms := []string{"解析", "分类", "生成", "新建 skill", "Prototype"}

	for _, term := range highRiskTerms {
		if contains(description, term) {
			return "high"
		}
	}
	for _, f := range files {
		for _, term := range highRiskTerms {
			if contains(f, term) {
				return "high"
			}
		}
	}

	for _, term := range mediumRiskTerms {
		if contains(description, term) {
			return "medium"
		}
	}
	return "low"
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchSubstring(s, substr)
}

func searchSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
