// Package yamlfrontmatter provides parsing and atomic update for YAML frontmatter
// in Obsidian Markdown files.
//
// Parses the YAML block between `---` delimiters using gopkg.in/yaml.v3.
// The Update function writes back with tmp→fsync→rename for atomicity.
package yamlfrontmatter

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Frontmatter maps all known fields in a task document frontmatter.
// Unknown fields are preserved in Extra.

// keyLineRE matches a valid YAML key: value line (with optional value).
var keyLineRE = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*\s*:\s*(\S.*)?$`)

// listItemRE matches a YAML list item line (indented dash).
var listItemRE = regexp.MustCompile(`^\s+-\s+\S`)

type Frontmatter struct {
	ID              string   `yaml:"id"`
	Title           string   `yaml:"title"`
	Project         string   `yaml:"project"`
	ProjectID       string   `yaml:"project_id"`
	NewProject      bool     `yaml:"new_project"`
	Template        string   `yaml:"template"`
	Status          string   `yaml:"status"` // blocked, ready, refining, needs-grilling, planning, plan-review, implementing, review, conflict, done
	PlanApproved    bool     `yaml:"plan_approved"`
	MergeApproved   bool     `yaml:"merge_approved"`
	AdrApproved     bool     `yaml:"adr_approved"`
	AdrProposed     any      `yaml:"adr_proposed"`
	AdrWritten      any      `yaml:"adr_written"`
	GrillContext    string   `yaml:"grill_context"`
	GrillPrevStatus string   `yaml:"grill_prev_status"`
	GrillDone       bool     `yaml:"grill_done"`
	PendingReq      bool     `yaml:"pending_req"`
	OffPeakOnly     bool     `yaml:"off_peak_only"`
	PlanVersion     int      `yaml:"plan_version"`
	Created         string   `yaml:"created"`
	Updated         string   `yaml:"updated"`
	Completed       string   `yaml:"completed"`
	Priority        string   `yaml:"priority"`
	DueDate         string   `yaml:"due_date"`
	EstimatedHours  float64  `yaml:"estimated_hours"`
	ActualHours     float64  `yaml:"actual_hours"`
	Assignee        string   `yaml:"assignee"`
	Reviewer        string   `yaml:"reviewer"`
	Author          string   `yaml:"author"`
	ReqDoc          string   `yaml:"req_doc"`
	Component       string   `yaml:"component"`
	Tags            []string `yaml:"tags"`
	Epic            string   `yaml:"epic"`
	Parent          string   `yaml:"parent"`
	Blocks          []string `yaml:"blocks"`
	BlockedBy       []string `yaml:"blocked_by"`
	TargetBranch    string   `yaml:"target_branch"`
	TargetEnv       string   `yaml:"target_env"`
	PRURL           string   `yaml:"pr_url"`
	SwitchSettings  bool     `yaml:"switch_settings"`
	AutoApprove     bool     `yaml:"auto_approve"`

	// ── Maturity gate ──
	Maturity         string `yaml:"maturity"` // fully_mature | mostly_mature | immature
	RefineVersion    int    `yaml:"refine_version"`
	RefineReqHash    string `yaml:"refine_req_hash"` // SHA-256 of full REQ bytes
	RefineRetryCount int    `yaml:"refine_retry_count"`
	RefineError      string `yaml:"refine_error"`

	// ── Planning ──
	PlanReqHash        string `yaml:"plan_req_hash"` // REQ hash at planning start
	PlanningRetryCount int    `yaml:"planning_retry_count"`

	// ── Phase recovery ──
	PhaseError     string `yaml:"phase_error"`
	PhaseLog       string `yaml:"phase_log"`
	BlockedPhase   string `yaml:"blocked_phase"` // refining | planning
	ResumeApproved bool   `yaml:"resume_approved"`

	// ── Grilling ownership ──
	GrillOwner          string `yaml:"grill_owner"`
	GrillStartedAt      string `yaml:"grill_started_at"`
	GrillTimeoutMinutes int    `yaml:"grill_timeout_minutes"`
	GrillResolution     string `yaml:"grill_resolution"` // resume | replan | ""

	// ── Checkpoint / refine ──
	CheckpointCommit string `yaml:"checkpoint_commit"`
	ReqRefineCount   int    `yaml:"req_refine_count"`

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
// Fields are updated via yaml.Node to preserve order and handle block scalars.
// Validation runs BEFORE writing — a corrupt result is never persisted.
func Update(path string, updates map[string]interface{}) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	content := string(data)
	if !strings.HasPrefix(content, "---") {
		return fmt.Errorf("%s has no frontmatter", path)
	}
	rest := content[3:]
	end := strings.Index(rest, "\n---")
	if end == -1 {
		return fmt.Errorf("%s frontmatter not closed", path)
	}
	fmText := rest[:end]
	body := rest[end+4:]
	if body == "" {
		body = "\n"
	}

	// Parse existing frontmatter as a YAML mapping node to preserve field order.
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(fmText), &doc); err != nil {
		return fmt.Errorf("parse frontmatter: %w", err)
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("frontmatter is not a mapping")
	}
	mapping := doc.Content[0]

	// Timestamps
	now := time.Now().Format("2006-01-02T15:04:05-07:00")
	updates["updated"] = now
	if _, ok := updates["created"]; !ok {
		if created := extractFieldRaw(fmText, "created"); created == "" || created == `""` {
			updates["created"] = now
		}
	}

	// Apply updates to the mapping node — existing keys are replaced in place,
	// new keys are appended at the end.  This preserves the original field order.
	for k, v := range updates {
		setMappingValue(mapping, k, v)
	}

	// Serialize back to YAML.
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return fmt.Errorf("encode frontmatter: %w", err)
	}
	enc.Close()
	newFM := strings.TrimSuffix(buf.String(), "\n")

	repairedBody := escapeBodyTags(body)
	newContent := "---\n" + newFM + "\n---" + repairedBody

	// Validate the generated content BEFORE writing — a corrupt frontmatter
	// (e.g. invalid multi-line edit) must be surfaced, not silently persisted.
	if _, err := Parse([]byte(newContent)); err != nil {
		return fmt.Errorf("update would produce invalid frontmatter: %w", err)
	}

	return atomicWrite(path, []byte(newContent))
}

// setMappingValue replaces or appends a key-value pair in a YAML mapping node.
func setMappingValue(mapping *yaml.Node, key string, val interface{}) {
	// Search for an existing key.
	for i := 0; i < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			newVal := &yaml.Node{}
			if err := newVal.Encode(val); err != nil {
				newVal.SetString(fmt.Sprint(val))
			}
			mapping.Content[i+1] = newVal
			return
		}
	}
	// Not found — append.
	keyNode := &yaml.Node{Kind: yaml.ScalarNode, Value: key, Tag: "!!str"}
	valNode := &yaml.Node{}
	if err := valNode.Encode(val); err != nil {
		valNode.SetString(fmt.Sprint(val))
	}
	mapping.Content = append(mapping.Content, keyNode, valNode)
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

// Validate checks whether a file's frontmatter is parseable.
// Returns nil if valid, or an error describing the parse failure.
// A file without frontmatter is considered invalid.
func Validate(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	fm, err := Parse(data)
	if err != nil {
		return err
	}
	if fm == nil {
		return fmt.Errorf("no frontmatter")
	}
	return nil
}

// Repair attempts to fix a corrupted frontmatter by extracting valid
// key: value lines and discarding any text that does not belong to a
// known YAML key. Returns nil if the file is already valid or after
// a successful repair. Returns an error if the file cannot be salvaged
// (e.g. no frontmatter delimiters).
func Repair(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	// If already valid, nothing to do. Parse returns nil, nil for files
	// without frontmatter — we cannot repair those.
	// If already valid, apply body tag escaping if needed, then return.
	if fm, err := Parse(data); err == nil {
		if fm == nil {
			return fmt.Errorf("no frontmatter to repair")
		}
		body := extractBody(data)
		repairedBody := escapeBodyTags(body)
		if repairedBody == body {
			return nil
		}
		// Rebuild with escaped body.
		newContent := string(data[:len(data)-len(body)]) + repairedBody
		if _, err := Parse([]byte(newContent)); err != nil {
			return fmt.Errorf("repair produced invalid frontmatter: %w", err)
		}
		return atomicWrite(path, []byte(newContent))
	}

	content := string(data)
	if !strings.HasPrefix(content, "---") {
		return fmt.Errorf("no frontmatter to repair")
	}
	rest := content[3:]
	end := strings.Index(rest, "\n---")
	if end == -1 {
		// try space after ---
		end = strings.Index(rest, "---")
		if end == -1 {
			return fmt.Errorf("frontmatter not closed; cannot repair")
		}
	}
	fmText := rest[:end]
	var body string
	if rest[end:] == "---" {
		body = "\n"
	} else {
		body = rest[end+4:] // skip "\n---\n"
		if body == "" {
			body = "\n"
		}
	}
	// Rebuild frontmatter: keep valid key:value pairs and list items.
	// Track block-scalar state so continuation lines (indented text after "|" or ">")
	// are preserved instead of being discarded as orphaned text.
	lines := strings.Split(fmText, "\n")
	clean := make([]string, 0, len(lines))
	inBlock := false
	for _, line := range lines {
		t := strings.TrimSpace(line)

		if inBlock {
			hasLeadingWS := len(line) > 0 && (line[0] == ' ' || line[0] == '\t')
			if t == "" || hasLeadingWS {
				clean = append(clean, line)
				continue
			}
			inBlock = false
		}

		if t == "" || strings.HasPrefix(t, "#") || t == "---" {
			continue
		}

		isBlockHeader := false
		if keyLineRE.MatchString(t) {
			parts := strings.SplitN(t, ":", 2)
			if len(parts) == 2 {
				vp := strings.TrimSpace(parts[1])
				if strings.HasPrefix(vp, "|") || strings.HasPrefix(vp, ">") {
					isBlockHeader = true
				}
			}
		}
		if isBlockHeader {
			clean = append(clean, line)
			inBlock = true
			continue
		}

		if keyLineRE.MatchString(t) || listItemRE.MatchString(line) {
			clean = append(clean, line)
		}
	}
	newFM := strings.Join(clean, "\n")
	for strings.HasSuffix(newFM, "\n") {
		newFM = newFM[:len(newFM)-1]
	}
	repairedBody := escapeBodyTags(body)
	newContent := "---\n" + newFM + "\n---" + repairedBody

	// Validate the repaired content before writing.
	if _, err := Parse([]byte(newContent)); err != nil {
		return fmt.Errorf("repair produced invalid frontmatter: %w", err)
	}
	return atomicWrite(path, []byte(newContent))
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

// matchesKey checks if a frontmatter line starts with the given key followed by ":".
func matchesKey(line string, key string) bool {
	return strings.HasPrefix(line, key+":")
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
		// Multi-line strings: use YAML literal block scalar
		if strings.Contains(v, "\n") {
			return key + ": |\n" + indentLines(v)
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
	case []string:
		if len(v) == 0 {
			return key + ": []"
		}
		lines := make([]string, len(v))
		for i, item := range v {
			lines[i] = "  - " + item
		}
		return key + ":\n" + strings.Join(lines, "\n")
	case []interface{}:
		if len(v) == 0 {
			return key + ": []"
		}
		lines := make([]string, len(v))
		for i, item := range v {
			lines[i] = "  - " + fmt.Sprint(item)
		}
		return key + ":\n" + strings.Join(lines, "\n")
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

// indentLines prefixes each line with two spaces for YAML literal block scalar.
func indentLines(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = "  " + line
	}
	return strings.Join(lines, "\n")
}

// ParseTaskDocument reads and validates a complete task document.
func ParseTaskDocument(path string) (*Frontmatter, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	fm, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("parse frontmatter: %w", err)
	}
	if fm == nil {
		return nil, fmt.Errorf("no frontmatter")
	}
	if fm.ID == "" {
		return nil, fmt.Errorf("missing required field: id")
	}
	if fm.Status == "" {
		return nil, fmt.Errorf("missing required field: status")
	}
	if fm.Project == "" {
		return nil, fmt.Errorf("missing required field: project")
	}
	if fm.ReqDoc == "" {
		return nil, fmt.Errorf("missing required field: req_doc")
	}
	// Extract body for markdown validation.
	body := extractBody(data)
	if err := validateMarkdownBody(body); err != nil {
		return nil, err
	}
	return fm, nil
}

// extractBody returns the markdown body portion after the closing frontmatter delimiter.
func extractBody(data []byte) string {
	content := string(data)
	if !strings.HasPrefix(content, "---") {
		return ""
	}
	rest := content[3:]
	idx := strings.Index(rest, "\n---")
	if idx == -1 {
		return ""
	}
	bodyStart := idx + 5 // skip "\n---\n"
	if bodyStart >= len(rest) {
		return ""
	}
	return rest[bodyStart:]
}

// ValidateTaskDocument checks parseability AND that required task fields are present.
func ValidateTaskDocument(path string) error {
	_, err := ParseTaskDocument(path)
	return err
}

// unescapedTagRE matches an angle-bracket HTML tag that is NOT backslash-escaped.
var unescapedTagRE = regexp.MustCompile(`(^|[^\\])<[a-zA-Z][a-zA-Z0-9-]*>`)

// validateMarkdownBody checks the markdown body for known rendering pitfalls.
func validateMarkdownBody(body string) error {
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		if m := unescapedTagRE.FindStringIndex(line); m != nil {
			return fmt.Errorf("body line %d: unescaped HTML tag %q — use \\<...\\> to render as literal text", i+1, line[m[0]:m[1]])
		}
	}
	return nil
}

// escapeBodyTags escapes unescaped angle-bracket HTML-like tags in Markdown body
// text so that Obsidian renders them as literal text instead of treating them
// as HTML elements.
func escapeBodyTags(body string) string {
	return unescapedTagRE.ReplaceAllStringFunc(body, func(match string) string {
		lead := match[:1]
		tag := match[1:]
		return lead + "\\<" + tag[1:len(tag)-1] + "\\>"
	})
}
