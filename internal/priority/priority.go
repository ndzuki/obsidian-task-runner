// Package priority validates and normalizes model-produced priority assessments.
package priority

import (
	"encoding/json"
	"fmt"
	"strings"
)

type Result struct {
	Priority       string `json:"priority"`
	Impact         string `json:"impact"`
	Urgency        string `json:"urgency"`
	Workaround     string `json:"workaround"`
	Score          int    `json:"score"`
	Confidence     string `json:"confidence"`
	Reason         string `json:"reason"`
	Recommendation string `json:"recommendation"`
}

var impactScores = map[string]int{
	"critical": 4,
	"high":     3,
	"medium":   2,
	"low":      1,
}

var urgencyScores = map[string]int{
	"immediate": 3,
	"near_term": 2,
	"normal":    1,
	"deferred":  0,
}

var workaroundScores = map[string]int{
	"none":      2,
	"partial":   1,
	"effective": 0,
}

func Decode(data []byte) (Result, error) {
	var result Result
	decoder := json.NewDecoder(strings.NewReader(strings.TrimSpace(string(data))))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return Result{}, fmt.Errorf("decode priority result: %w", err)
	}
	return ValidateAndNormalize(result)
}

func ValidateAndNormalize(result Result) (Result, error) {
	impact, ok := impactScores[result.Impact]
	if !ok {
		return Result{}, fmt.Errorf("invalid impact %q", result.Impact)
	}
	urgency, ok := urgencyScores[result.Urgency]
	if !ok {
		return Result{}, fmt.Errorf("invalid urgency %q", result.Urgency)
	}
	workaround, ok := workaroundScores[result.Workaround]
	if !ok {
		return Result{}, fmt.Errorf("invalid workaround %q", result.Workaround)
	}
	switch result.Confidence {
	case "high", "medium", "low":
	default:
		return Result{}, fmt.Errorf("invalid confidence %q", result.Confidence)
	}
	if strings.TrimSpace(result.Reason) == "" {
		return Result{}, fmt.Errorf("reason is required")
	}

	result.Score = impact + urgency + workaround
	result.Priority = priorityForScore(result.Score)
	result.Recommendation = ""
	if result.Impact == "critical" && result.Urgency == "immediate" && result.Workaround == "none" && result.Confidence == "high" {
		result.Recommendation = "P0"
	}
	return result, nil
}

func Fallback(reason string) Result {
	return Result{
		Priority:   "P2",
		Impact:     "medium",
		Urgency:    "normal",
		Workaround: "partial",
		Score:      4,
		Confidence: "low",
		Reason:     reason,
	}
}

func priorityForScore(score int) string {
	switch {
	case score >= 7:
		return "P1"
	case score >= 4:
		return "P2"
	case score >= 2:
		return "P3"
	default:
		return "P4"
	}
}
