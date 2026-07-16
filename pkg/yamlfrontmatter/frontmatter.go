// Package yamlfrontmatter provides parsing and atomic update for YAML frontmatter
// in Obsidian Markdown files.
//
// Parses the YAML block between `---` delimiters using gopkg.in/yaml.v3.
// The Update function writes back with tmp→fsync→rename for atomicity.
package yamlfrontmatter

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Frontmatter maps all known fields in a task document frontmatter.
// Unknown fields are preserved in Extra.
type Frontmatter struct {
	ID             string   `yaml:"id"`
	Title          string   `yaml:"title"`
	Project        string   `yaml:"project"`
	ProjectID      string   `yaml:"project_id"`
	NewProject     bool     `yaml:"new_project"`
	Template       string   `yaml:"template"`
	Status         string   `yaml:"status"`
	PlanApproved   bool     `yaml:"plan_approved"`
	MergeApproved  bool     `yaml:"merge_approved"`
	PendingReq     bool     `yaml:"pending_req"`
	OffPeakOnly    bool     `yaml:"off_peak_only"`
	PlanVersion    int      `yaml:"plan_version"`
	Created        string   `yaml:"created"`
	Updated        string   `yaml:"updated"`
	Completed      string   `yaml:"completed"`
	Priority       string   `yaml:"priority"`
	DueDate        string   `yaml:"due_date"`
	EstimatedHours float64  `yaml:"estimated_hours"`
	ActualHours    float64  `yaml:"actual_hours"`
	Assignee       string   `yaml:"assignee"`
	Reviewer       string   `yaml:"reviewer"`
	Author         string   `yaml:"author"`
	ReqDoc         string   `yaml:"req_doc"`
	Component      string   `yaml:"component"`
	Tags           []string `yaml:"tags"`
	Epic           string   `yaml:"epic"`
	Parent         string   `yaml:"parent"`
	Blocks         []string `yaml:"blocks"`
	BlockedBy      []string `yaml:"blocked_by"`
	TargetBranch   string   `yaml:"target_branch"`
	TargetEnv      string   `yaml:"target_env"`
	PRURL          string   `yaml:"pr_url"`
	SwitchSettings bool     `yaml:"switch_settings"`
	AutoApprove    bool     `yaml:"auto_approve"`

	// Extra holds any YAML keys not explicitly mapped above.
	Extra map[string]any `yaml:",inline"`
}

// normalizeNumericStrings converts quoted numeric values for known numeric
// fields to YAML numeric scalars before strict decoding. Obsidian and other
// frontmatter editors may serialize a number such as 42 as "42".
func normalizeNumericStrings(doc *yaml.Node) error {
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil
	}

	for i := 0; i+1 < len(doc.Content[0].Content); i += 2 {
		key := doc.Content[0].Content[i]
		value := doc.Content[0].Content[i+1]
		if (key.Value != "estimated_hours" && key.Value != "actual_hours") || value.Kind != yaml.ScalarNode || value.Tag != "!!str" {
			continue
		}
		if _, err := strconv.ParseFloat(value.Value, 64); err != nil {
			return fmt.Errorf("%s must be a number: %w", key.Value, err)
		}
		value.Tag = "!!float"
	}

	return nil
}

// Parse extracts YAML frontmatter from a markdown document.
// Returns nil, nil if the document has no frontmatter.
func Parse(data []byte) (*Frontmatter, error) {
	content := string(data)
	if !strings.HasPrefix(content, "---") {
		return nil, nil
	}
	rest := content[3:]
	end := strings.Index(rest, "---")
	if end == -1 {
		return nil, fmt.Errorf("frontmatter not closed")
	}
	fmBlock := strings.TrimSpace(rest[:end])

	// Empty frontmatter → return zero-value struct
	if fmBlock == "" {
		return &Frontmatter{}, nil
	}

	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(fmBlock), &doc); err != nil {
		return nil, fmt.Errorf("parse frontmatter: %w", err)
	}
	if err := normalizeNumericStrings(&doc); err != nil {
		return nil, fmt.Errorf("parse frontmatter: %w", err)
	}

	var fm Frontmatter
	if err := doc.Decode(&fm); err != nil {
		return nil, fmt.Errorf("parse frontmatter: %w", err)
	}
	return &fm, nil
}

// Update atomically updates frontmatter fields in a task markdown file.
func Update(path string, updates map[string]interface{}) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	content := string(data)

	// Locate frontmatter boundaries
	rest := content[3:] // after opening "---"
	end := strings.Index(rest, "\n---")
	if end == -1 {
		return fmt.Errorf("%s frontmatter not closed", path)
	}
	fmText := rest[:end] // frontmatter body
	body := rest[end+4:] // skip "\n---\n"
	if body == "" {
		body = "\n"
	}

	// Timestamps
	now := time.Now().Format("\"2006-01-02T15:04:05-07:00\"")
	updates["updated"] = now
	if _, ok := updates["created"]; !ok {
		if created := extractFieldRaw(fmText, "created"); created == "" || created == `""` {
			updates["created"] = now
		}
	}

	// Line-by-line update
	lines := strings.Split(fmText, "\n")
	done := make(map[string]bool)
	for i, line := range lines {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		for k, v := range updates {
			if !done[k] && matchesKey(t, k) {
				lines[i] = formatField(k, v)
				done[k] = true
				break
			}
		}
	}
	// Append new keys
	for k, v := range updates {
		if !done[k] {
			lines = append(lines, formatField(k, v))
		}
	}

	newFM := strings.Join(lines, "\n")
	// Remove trailing blank line
	for strings.HasSuffix(newFM, "\n") {
		newFM = newFM[:len(newFM)-1]
	}
	newContent := "---\n" + newFM + "\n---" + body
	return atomicWrite(path, []byte(newContent))
}

// atomicWrite writes data to a temporary file, fsyncs, and renames.
func atomicWrite(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".otg-")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("fsync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename %s → %s: %w", tmpPath, path, err)
	}
	return nil
}

// extractFieldRaw extracts a field value from raw frontmatter text.
func extractFieldRaw(fmText string, key string) string {
	for _, line := range strings.Split(fmText, "\n") {
		trimmed := strings.TrimSpace(line)
		if matchesKey(trimmed, key) {
			parts := strings.SplitN(trimmed, ":", 2)
			if len(parts) == 2 {
				return strings.TrimSpace(parts[1])
			}
		}
	}
	return ""
}

// matchesKey checks if a frontmatter line starts with the given key.
func matchesKey(line string, key string) bool {
	if !strings.HasPrefix(line, key) {
		return false
	}
	// Ensure what follows is ": " or ":"
	rest := line[len(key):]
	if len(rest) == 0 || rest[0] != ':' {
		return false
	}
	return true
}

// formatField formats a frontmatter key=value line. Handles types.
func formatField(key string, val interface{}) string {
	switch v := val.(type) {
	case string:
		if v == "" {
			return key + `: ""`
		}
		// If it's already YAML-formatted (e.g. timestamps), don't re-quote
		if strings.HasPrefix(v, `"`) && strings.HasSuffix(v, `"`) {
			return key + ": " + v
		}
		// Simple strings: quote unless they look like YAML values
		if isSimpleValue(v) {
			return key + ": " + v
		}
		return key + `: "` + v + `"`
	case bool:
		if v {
			return key + ": true"
		}
		return key + ": false"
	case int:
		return key + ": " + fmt.Sprint(v)
	case float64:
		if v == float64(int(v)) {
			return key + ": " + fmt.Sprint(int(v))
		}
		return key + ": " + fmt.Sprint(v)
	default:
		return key + `: "` + fmt.Sprint(v) + `"`
	}
}

// isSimpleValue returns true if a value doesn't need quoting in YAML.
func isSimpleValue(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		switch c {
		case ':', '#', '{', '}', '[', ']', ',', '&', '*', '?', '|',
			'<', '>', '=', '!', '%', '@', '`', '\'', '"':
			return false
		}
	}
	return true
}
