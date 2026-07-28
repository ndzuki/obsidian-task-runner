// Package yamlfrontmatter provides parsing and atomic update for YAML frontmatter
// in Obsidian Markdown files.
//
// Parses the YAML block between `---` delimiters using gopkg.in/yaml.v3.
// The Update function writes back with tmp→fsync→rename for atomicity.
package yamlfrontmatter

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
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
	// Human-owned fields.
	ID              string   `yaml:"id"`
	Title           string   `yaml:"title"`
	Project         string   `yaml:"project"`
	ProjectID       string   `yaml:"project_id"`
	Assignee        string   `yaml:"assignee"`
	ReqDoc          string   `yaml:"req_doc"`
	NewProject      bool     `yaml:"new_project"`
	BlockedBy       []string `yaml:"blocked_by"`
	AutoApprove     bool     `yaml:"auto_approve"`
	OffPeakOnly     bool     `yaml:"off_peak_only"`
	DueDate         string   `yaml:"due_date"`
	PlanApproved    bool     `yaml:"plan_approved"`
	MergeApproved   bool     `yaml:"merge_approved"`
	ResumeApproved  bool     `yaml:"resume_approved"`
	CloseApproved   bool     `yaml:"close_approved"`

	// System-owned lifecycle fields.
	Status             string `yaml:"status"`
	Maturity           string `yaml:"maturity"`
	RefineVersion      int    `yaml:"refine_version"`
	RefineReqHash      string `yaml:"refine_req_hash"`
	RefineRetryCount   int    `yaml:"refine_retry_count"`
	RefineError        string `yaml:"refine_error"`
	PlanReqHash        string `yaml:"plan_req_hash"`
	PlanVersion        int    `yaml:"plan_version"`
	PlanningRetryCount int    `yaml:"planning_retry_count"`
	PhaseError         string `yaml:"phase_error"`
	PhaseErrorCode     string `yaml:"phase_error_code"`
	PhaseLog           string `yaml:"phase_log"`
	BlockedPhase       string `yaml:"blocked_phase"`
	PendingReq        bool   `yaml:"pending_req"`
	CheckpointCommit   string `yaml:"checkpoint_commit"`
	TargetBranch       string `yaml:"target_branch"`
	PRURL              string `yaml:"pr_url"`
	Completed          string `yaml:"completed"`
	AdrApproved        bool   `yaml:"adr_approved"`
	AdrProposed        any    `yaml:"adr_proposed"`
	AdrWritten         any    `yaml:"adr_written"`
	GrillOwner         string `yaml:"grill_owner"`
	GrillStartedAt     string `yaml:"grill_started_at"`
	GrillHeartbeatAt   string `yaml:"grill_heartbeat_at"`
	GrillTimeoutMinutes int   `yaml:"grill_timeout_minutes"`
	GrillDone          bool   `yaml:"grill_done"`
	GrillResolution    string `yaml:"grill_resolution"`
	GrillContext       string `yaml:"grill_context"`
	GrillPrevStatus    string `yaml:"grill_prev_status"`
	ReqRefineCount     int    `yaml:"req_refine_count"`
	TaskSchemaVersion  int    `yaml:"task_schema_version"`

	// Shared fields: daemon proposes, users may override.
	Priority                    string `yaml:"priority"`
	PriorityAssessmentStatus    string `yaml:"priority_assessment_status"`
	PriorityAssessmentAttempts  int    `yaml:"priority_assessment_attempts"`
	PriorityAssessmentStartedAt string `yaml:"priority_assessment_started_at"`
	PriorityAssessedAt          string `yaml:"priority_assessed_at"`
	PriorityAssessedValue       string `yaml:"priority_assessed_value"`
	PriorityImpact              string `yaml:"priority_impact"`
	PriorityUrgency             string `yaml:"priority_urgency"`
	PriorityWorkaround          string `yaml:"priority_workaround"`
	PriorityScore               int    `yaml:"priority_score"`
	PriorityConfidence          string `yaml:"priority_confidence"`
	PriorityReason              string `yaml:"priority_reason"`
	PriorityRecommendation      string `yaml:"priority_recommendation"`
	ReviewFeedback              string `yaml:"review_feedback"`
	ReworkResolution            string `yaml:"rework_resolution"`

	// Closed terminal state.
	ClosureReason   string `yaml:"closure_reason"`
	ClosureNote     string `yaml:"closure_note"`
	ReplacementTask string `yaml:"replacement_task"`

	// Declarative project scaffold intent.
	Scaffold ScaffoldIntent `yaml:"scaffold"`
	Template string         `yaml:"template"`

	// GitHub remote creation and merge authorization.
	RemoteCreate          bool   `yaml:"remote_create"`
	GitHubOwner           string `yaml:"github_owner"`
	RepositoryName        string `yaml:"repository_name"`
	RepositoryVisibility  string `yaml:"repository_visibility"`
	RepositoryDescription string `yaml:"repository_description"`
	RepositoryURL         string `yaml:"repository_url"`
	MergeStatus           string `yaml:"merge_status"`
	ApprovedHead          string `yaml:"approved_head"`

	// General task metadata retained by templates and dashboards.
	Created        string   `yaml:"created"`
	Updated        string   `yaml:"updated"`
	EstimatedHours float64  `yaml:"estimated_hours"`
	ActualHours    float64  `yaml:"actual_hours"`
	Reviewer       string   `yaml:"reviewer"`
	Author         string   `yaml:"author"`
	Component      string   `yaml:"component"`
	Tags           []string `yaml:"tags"`
	Epic           string   `yaml:"epic"`
	Parent         string   `yaml:"parent"`
	Blocks         []string `yaml:"blocks"`
	TargetEnv      string   `yaml:"target_env"`

	// Deprecated migration-only field. New code must use Assignee.
	SwitchSettings bool `yaml:"switch_settings"`

	// Extra holds YAML keys not explicitly mapped above.
	Extra map[string]any `yaml:",inline"`
}

type ScaffoldIntent struct {
	Kind         string         `yaml:"kind"`
	Capabilities []string       `yaml:"capabilities"`
	Preferences  map[string]any `yaml:"preferences"`
	Notes        string         `yaml:"notes"`
}

func applyCompatibilityDefaults(fm *Frontmatter) {
	if fm.TaskSchemaVersion != 0 || fm.PriorityAssessmentStatus != "" {
		return
	}
	if fm.Priority == "" {
		fm.PriorityAssessmentStatus = "pending"
		return
	}
	fm.PriorityAssessmentStatus = "completed"
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

	// Empty frontmatter returns a zero-value legacy-compatible struct.
	if fmBlock == "" {
		fm := &Frontmatter{}
		applyCompatibilityDefaults(fm)
		return fm, nil
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
	applyCompatibilityDefaults(&fm)
	return &fm, nil
}

// Update atomically updates frontmatter fields in a task markdown file.
// Fields are updated via yaml.Node to preserve order and handle block scalars.
// Validation runs BEFORE writing — a corrupt result is never persisted.
func Update(path string, updates map[string]interface{}) error {
	return WithLockedFrontmatter(path, func(_ *Frontmatter) (map[string]interface{}, error) {
		return updates, nil
	})
}

// WithLockedFrontmatter serializes all TASK writes for a canonical path.
// The callback observes the latest frontmatter while the task-path flock is held.
func WithLockedFrontmatter(path string, mutate func(*Frontmatter) (map[string]interface{}, error)) error {
	cleanPath, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return fmt.Errorf("resolve task path: %w", err)
	}
	unlock, err := acquireTaskLock(cleanPath)
	if err != nil {
		return err
	}
	defer unlock()

	data, err := os.ReadFile(cleanPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", cleanPath, err)
	}
	fm, err := Parse(data)
	if err != nil {
		return err
	}
	if fm == nil {
		return fmt.Errorf("%s has no frontmatter", cleanPath)
	}
	updates, err := mutate(fm)
	if err != nil {
		return err
	}
	if len(updates) == 0 {
		return nil
	}
	return updateUnlocked(cleanPath, data, updates)
}

func updateUnlocked(path string, data []byte, updates map[string]interface{}) error {
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

	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(fmText), &doc); err != nil {
		return fmt.Errorf("parse frontmatter: %w", err)
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("frontmatter is not a mapping")
	}
	mapping := doc.Content[0]

	now := time.Now().Format("2006-01-02T15:04:05-07:00")
	updates["updated"] = now
	if _, ok := updates["created"]; !ok {
		if created := extractFieldRaw(fmText, "created"); created == "" || created == `""` {
			updates["created"] = now
		}
	}
	for key, value := range updates {
		setMappingValue(mapping, key, value)
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return fmt.Errorf("encode frontmatter: %w", err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("close frontmatter encoder: %w", err)
	}
	newContent := "---\n" + strings.TrimSuffix(buf.String(), "\n") + "\n---" + body
	if _, err := Parse([]byte(newContent)); err != nil {
		return fmt.Errorf("update would produce invalid frontmatter: %w", err)
	}
	return atomicWrite(path, []byte(newContent))
}

func acquireTaskLock(path string) (func(), error) {
	sum := sha256.Sum256([]byte(path))
	lockPath := filepath.Join(os.TempDir(), fmt.Sprintf("otg-task-%x.lock", sum[:]))
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open task lock: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("lock task: %w", err)
	}
	return func() {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
	}, nil
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
	defer func() {
		_ = os.Remove(tmpPath)
	}()

	if _, err := tmp.Write(data); err != nil {
		closeErr := tmp.Close()
		if closeErr != nil {
			return fmt.Errorf("write temp: %w (close: %v)", err, closeErr)
		}
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		closeErr := tmp.Close()
		if closeErr != nil {
			return fmt.Errorf("fsync temp: %w (close: %v)", err, closeErr)
		}
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
	if fm, err := Parse(data); err == nil {
		if fm == nil {
			return fmt.Errorf("no frontmatter to repair")
		}
		return nil
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
	newContent := "---\n" + newFM + "\n---" + body

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
