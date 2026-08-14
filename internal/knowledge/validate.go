package knowledge

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
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

// NormalizeRefFile self-heals the common KB v2 frontmatter violations from
// agent/interactive intake that skips the skill's checks: RFC3339 (or other
// parseable) timestamps in updated/created — the schema pins them to
// YYYY-MM-DD — and an empty source. Rewrites only the offending lines,
// preserving every other field, their order, and quoting style (same
// field-preserving pattern as bumpHitsField). Returns whether the document
// was rewritten; already-valid or unfixable documents are left untouched
// (unfixable ones keep failing ValidateRefFile so the caller's alert path
// still fires).
func NormalizeRefFile(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("read: %w", err)
	}
	content := string(data)
	if !strings.HasPrefix(content, "---\n") {
		return false, nil
	}
	rest := content[4:]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return false, nil
	}
	fmText := rest[:end]
	body := rest[end+4:]
	lines := strings.Split(fmText, "\n")
	changed := false
	for i, line := range lines {
		key, val, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.Trim(strings.TrimSpace(val), `"'`)
		switch key {
		case "updated", "created":
			if updatedDateRE.MatchString(val) {
				continue
			}
			if t, terr := time.Parse(time.RFC3339, val); terr == nil {
				lines[i] = key + ": \"" + t.Format("2006-01-02") + "\""
				changed = true
			}
		case "source":
			if val == "" {
				lines[i] = "source: \"local\""
				changed = true
			}
		}
	}
	if !changed {
		return false, nil
	}
	updated := "---\n" + strings.Join(lines, "\n") + "\n---" + body
	if err := yamlfrontmatter.AtomicWrite(path, []byte(updated)); err != nil {
		return false, fmt.Errorf("write: %w", err)
	}
	return true, nil
}
