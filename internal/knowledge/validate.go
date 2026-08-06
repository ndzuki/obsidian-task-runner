package knowledge

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// validLevels is the KB v2 level enum.
var validLevels = map[string]bool{
	"beginner": true, "intermediate": true, "advanced": true, "reference": true,
}

var updatedDateRE = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// ValidateRefFile enforces the KB v2 six-field frontmatter contract on a
// References/ document: topics non-empty, level enum, updated ISO date,
// source non-empty, verified present, aliases present (may be empty).
// The daemon runs this on References writes so interactive/agent intake that
// skips the skill's five checks is caught and surfaced instead of silently
// polluting the index.
func ValidateRefFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read: %w", err)
	}
	fm, _, err := parseFrontmatter(data)
	if err != nil {
		return fmt.Errorf("frontmatter: %w", err)
	}
	if fm == nil {
		return fmt.Errorf("frontmatter: missing")
	}
	topics, _ := fm["topics"].([]any)
	if len(topics) == 0 {
		return fmt.Errorf("topics: must contain at least one keyword")
	}
	if lvl, _ := fm["level"].(string); !validLevels[lvl] {
		return fmt.Errorf("level: %q not in {beginner, intermediate, advanced, reference}", lvl)
	}
	if updated, _ := fm["updated"].(string); !updatedDateRE.MatchString(updated) {
		return fmt.Errorf("updated: %q not YYYY-MM-DD", updated)
	}
	if source, _ := fm["source"].(string); strings.TrimSpace(source) == "" {
		return fmt.Errorf("source: must be a URL or \"local\"")
	}
	if _, ok := fm["verified"]; !ok {
		return fmt.Errorf("verified: missing")
	}
	if _, ok := fm["aliases"]; !ok {
		return fmt.Errorf("aliases: missing")
	}
	return nil
}
