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
	ID             string   `yaml:"id"`
	Title          string   `yaml:"title"`
	Project        string   `yaml:"project"`
	ProjectID      string   `yaml:"project_id"`
	Assignee       string   `yaml:"assignee"`
	ReqDoc         string   `yaml:"req_doc"`
	NewProject     bool     `yaml:"new_project"`
	BlockedBy      []string `yaml:"blocked_by"`
	AutoApprove    bool     `yaml:"auto_approve"`
	AutoMerge      bool     `yaml:"auto_merge"`
	OffPeakOnly    bool     `yaml:"off_peak_only"`
	DueDate        string   `yaml:"due_date"`
	PlanApproved   bool     `yaml:"plan_approved"`
	MergeApproved  bool     `yaml:"merge_approved"`
	ResumeApproved bool     `yaml:"resume_approved"`
	CloseApproved  bool     `yaml:"close_approved"`

	// System-owned lifecycle fields.
	Status              string `yaml:"status"`
	Maturity            string `yaml:"maturity"`
	RefineVersion       int    `yaml:"refine_version"`
	RefineReqHash       string `yaml:"refine_req_hash"`
	RefineRetryCount    int    `yaml:"refine_retry_count"`
	AutoResumeCount     int    `yaml:"auto_resume_count"`
	AutoResumePending   bool   `yaml:"auto_resume_pending"`
	RefineError         string `yaml:"refine_error"`
	PlanReqHash         string `yaml:"plan_req_hash"`
	PlanVersion         int    `yaml:"plan_version"`
	PlanningRetryCount  int    `yaml:"planning_retry_count"`
	PhaseError          string `yaml:"phase_error"`
	PhaseErrorCode      string `yaml:"phase_error_code"`
	PhaseLog            string `yaml:"phase_log"`
	BlockedPhase        string `yaml:"blocked_phase"`
	PendingReq          bool   `yaml:"pending_req"`
	CheckpointCommit    string `yaml:"checkpoint_commit"`
	TargetBranch        string `yaml:"target_branch"`
	PRURL               string `yaml:"pr_url"`
	Completed           string `yaml:"completed"`
	AdrApproved         bool   `yaml:"adr_approved"`
	AdrProposed         any    `yaml:"adr_proposed"`
	AdrWritten          any    `yaml:"adr_written"`
	KnowledgeExtracted  bool   `yaml:"knowledge_extracted"`
	KnowledgeRefs       []string `yaml:"knowledge_refs"`
	KnowledgeApplied    string   `yaml:"knowledge_applied"`
	GrillOwner          string `yaml:"grill_owner"`
	GrillStartedAt      string `yaml:"grill_started_at"`
	GrillHeartbeatAt    string `yaml:"grill_heartbeat_at"`
	GrillTimeoutMinutes int    `yaml:"grill_timeout_minutes"`
	GrillDone           bool   `yaml:"grill_done"`
	GrillResolution     string `yaml:"grill_resolution"`
	GrillContext        string   `yaml:"grill_context"`
	GrillContinue       bool   `yaml:"grill_continue"`
	GrillPrevStatus     string `yaml:"grill_prev_status"`
	GrillParked         bool   `yaml:"grill_parked"`
	GrillRepeat         int    `yaml:"grill_repeat"`
	AutoAccepted        string `yaml:"auto_accepted"`
	ReqRefineCount      int    `yaml:"req_refine_count"`
	TaskSchemaVersion   int    `yaml:"task_schema_version"`

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
	end := strings.Index(rest, "\n---")
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
	// auto_merge defaults to true: entering review auto-approves the merge
	// unless the task explicitly opts out (auto_merge: false in frontmatter).
	if !hasFrontmatterKey(&doc, "auto_merge") {
		fm.AutoMerge = true
	}
	applyCompatibilityDefaults(&fm)
	return &fm, nil
}

// hasFrontmatterKey reports whether the YAML mapping node contains the key.
func hasFrontmatterKey(doc *yaml.Node, key string) bool {
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return false
	}
	for i := 0; i+1 < len(doc.Content[0].Content); i += 2 {
		if doc.Content[0].Content[i].Value == key {
			return true
		}
	}
	return false
}

// FieldDefault pairs a canonical frontmatter key with the value to write
// when the key is absent.
type FieldDefault struct {
	Key   string
	Value interface{}
}

// taskFieldOrder is the canonical frontmatter key order: user-facing fields
// first, daemon-maintained fields last. NormalizeTaskFrontmatter reorders
// documents to this sequence (unknown keys keep their relative order and are
// appended after all known keys), so frontmatter stays readable and stable
// as the schema evolves. Covers every known key so any existing field can be
// placed.
var taskFieldOrder = []string{
	// Identity and status (required, human-owned).
	"id", "title", "project", "project_id", "assignee", "req_doc", "status",
	// Priority assessment (human-readable output).
	"priority", "priority_assessment_status", "priority_assessment_attempts",
	"priority_assessment_started_at", "priority_assessed_at", "priority_assessed_value",
	"priority_impact", "priority_urgency", "priority_workaround",
	"priority_score", "priority_confidence", "priority_reason", "priority_recommendation",
	// Gate decisions (human-owned).
	"plan_approved", "auto_merge", "merge_approved", "adr_approved",
	"resume_approved", "close_approved", "pending_req",
	// Metadata (template 🟡/🟢 sections).
	"tags", "epic", "blocked_by", "blocks", "target_env", "new_project",
	"due_date", "estimated_hours", "actual_hours", "component", "parent",
	"reviewer", "author", "template", "off_peak_only", "auto_approve",
	// Timestamps.
	"created", "updated",
	// Lifecycle (daemon-maintained).
	"maturity", "refine_version", "refine_req_hash", "refine_retry_count",
	"refine_error", "plan_req_hash", "plan_version", "planning_retry_count",
	"checkpoint_commit", "target_branch", "pr_url", "completed",
	"merge_status", "approved_head", "task_schema_version", "req_refine_count",
	// Blocking and failure state (daemon-maintained, least user-facing).
	"blocked_phase", "phase_error", "phase_error_code", "phase_log",
	"auto_resume_pending", "auto_resume_count",
	// Grilling lease (daemon-maintained).
	"grill_owner", "grill_started_at", "grill_heartbeat_at",
	"grill_timeout_minutes", "grill_done", "grill_resolution",
	"grill_context", "grill_continue", "grill_prev_status",
	"grill_parked", "grill_repeat", "auto_accepted",
	// Review / rework / closure.
	"review_feedback", "rework_resolution",
	"closure_reason", "closure_note", "replacement_task",
	// Project scaffold and remote creation.
	"scaffold", "remote_create", "github_owner", "repository_name",
	"repository_visibility", "repository_description", "repository_url",
	// ADR bookkeeping.
	"adr_proposed", "adr_written", "knowledge_extracted", "knowledge_refs", "knowledge_applied",
	// Deprecated migration-only field.
	"switch_settings",
}

// taskFieldDefaults maps backfillable keys to their template default values.
// Required human fields (id/title/project/project_id/req_doc/assignee) are
// deliberately absent: a missing required field marks an unready task and
// must not be silently fabricated.
var taskFieldDefaults = map[string]interface{}{
	// Gate fields (template 🔵 section).
	"plan_approved":   false,
	"auto_merge":      true,
	"merge_approved":  false,
	"adr_approved":    false,
	"resume_approved": false,
	"close_approved":  false,
	"pending_req":     false,

	// System lifecycle fields (template ⚪ section).
	"status":               "blocked",
	"maturity":             "",
	"refine_version":       0,
	"refine_req_hash":      "",
	"refine_retry_count":   0,
	"refine_error":         "",
	"plan_req_hash":        "",
	"plan_version":         0,
	"planning_retry_count": 0,
	"blocked_phase":        "",
	"phase_error":          "",
	"phase_error_code":     "",
	"phase_log":            "",
	"checkpoint_commit":    "",
	"target_branch":        "",
	"pr_url":               "",
	"completed":            "",
	"task_schema_version":  1,
	"auto_resume_pending":  false,
	"auto_resume_count":    0,

	// Merge loop fields.
	"merge_status":  "",
	"approved_head": "",

	// Grilling lease fields.
	"grill_owner":           "",
	"grill_started_at":      "",
	"grill_heartbeat_at":    "",
	"grill_timeout_minutes": 30,
	"grill_done":            false,
	"grill_resolution":      "",
	"grill_context":         "",
	"grill_continue":        false,
	"grill_prev_status":     "",
	"grill_parked":          false,
	"grill_repeat":          0,
	"auto_accepted":         "",

	// Review / rework / closure fields.
	"review_feedback":   "",
	"rework_resolution": "",
	"closure_reason":    "",
	"closure_note":      "",
	"replacement_task":  "",

	// Template non-commented defaults.
	"tags":                           []interface{}{},
	"epic":                           "",
	"blocked_by":                     []interface{}{},
	"blocks":                         []interface{}{},
	"priority":                       "",
	"priority_assessment_started_at": "",
	"priority_assessed_at":           "",
	"priority_assessment_attempts":   0,
	"priority_assessed_value":        "",
	"priority_impact":                "",
	"priority_urgency":               "",
	"priority_workaround":            "",
	"priority_score":                 0,
	"priority_confidence":            0,
	"priority_reason":                "",
	"priority_recommendation":        "",
	"new_project":                    false,
	"target_env":                     "staging",
	"adr_proposed":                   []interface{}{},
	"adr_written":                    []interface{}{},
	"knowledge_extracted":            false,
	"knowledge_refs":                 []interface{}{},
	"knowledge_applied":              "",
	"switch_settings":                false,
}

// fieldOrderIndex maps canonical key → position in taskFieldOrder.
var fieldOrderIndex = func() map[string]int {
	index := make(map[string]int, len(taskFieldOrder))
	for i, key := range taskFieldOrder {
		index[key] = i
	}
	return index
}()

// missingDefaults computes the ordered list of absent keys with their
// backfill values. priority_assessment_status follows Parse's compatibility
// semantics: it derives from whether priority was set. Returns nil when
// nothing is missing.
func missingDefaults(doc *yaml.Node, fm *Frontmatter) []FieldDefault {
	var missing []FieldDefault
	for _, key := range taskFieldOrder {
		if def, ok := taskFieldDefaults[key]; ok && !hasFrontmatterKey(doc, key) {
			missing = append(missing, FieldDefault{Key: key, Value: def})
		}
	}
	if !hasFrontmatterKey(doc, "priority_assessment_status") {
		value := "pending"
		if fm.Priority != "" {
			value = "completed"
		}
		missing = append(missing, FieldDefault{Key: "priority_assessment_status", Value: value})
	}
	if len(missing) == 0 {
		return nil
	}
	return missing
}

// MissingDefaults returns the ordered list of frontmatter keys absent from
// the document with their backfill values. Keys that are present — even with
// empty values — are never touched. Returns nil when nothing is missing or
// the document has no frontmatter.
func MissingDefaults(data []byte) ([]FieldDefault, error) {
	content := string(data)
	if !strings.HasPrefix(content, "---") {
		return nil, nil // no frontmatter: leave the document alone
	}
	rest := content[3:]
	end := strings.Index(rest, "\n---")
	if end == -1 {
		return nil, fmt.Errorf("frontmatter not closed")
	}
	fmBlock := strings.TrimSpace(rest[:end])

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
	return missingDefaults(&doc, &fm), nil
}

// buildCanonicalMapping returns the frontmatter mapping content reordered to
// taskFieldOrder: existing key nodes are reused verbatim (comments and styles
// preserved), absent backfillable keys are inserted at their canonical
// position, and unknown keys keep their relative order appended at the end.
// Returns the new content and whether its key sequence differs from the
// input.
func buildCanonicalMapping(mapping *yaml.Node, missing []FieldDefault) ([]*yaml.Node, bool) {
	existing := make(map[string][2]*yaml.Node, len(mapping.Content)/2)
	var unknown []*yaml.Node
	for i := 0; i < len(mapping.Content); i += 2 {
		key := mapping.Content[i].Value
		if _, known := fieldOrderIndex[key]; known {
			existing[key] = [2]*yaml.Node{mapping.Content[i], mapping.Content[i+1]}
		} else {
			unknown = append(unknown, mapping.Content[i], mapping.Content[i+1])
		}
	}
	missingValue := make(map[string]interface{}, len(missing))
	for _, m := range missing {
		missingValue[m.Key] = m.Value
	}

	newContent := make([]*yaml.Node, 0, len(mapping.Content)+2*len(missing))
	for _, key := range taskFieldOrder {
		if pair, ok := existing[key]; ok {
			newContent = append(newContent, pair[0], pair[1])
			continue
		}
		val, ok := missingValue[key]
		if !ok {
			continue // absent and not backfillable
		}
		keyNode := &yaml.Node{Kind: yaml.ScalarNode, Value: key, Tag: "!!str"}
		valNode := &yaml.Node{}
		if err := valNode.Encode(val); err != nil {
			valNode.SetString(fmt.Sprint(val))
		}
		newContent = append(newContent, keyNode, valNode)
	}
	newContent = append(newContent, unknown...)

	changed := len(newContent) != len(mapping.Content)
	if !changed {
		for i := 0; i < len(newContent); i += 2 {
			if newContent[i].Value != mapping.Content[i].Value {
				changed = true
				break
			}
		}
	}
	return newContent, changed
}

// NormalizeTaskFrontmatter backfills missing schema fields and reorders the
// frontmatter to the canonical taskFieldOrder (user-facing fields first,
// daemon-maintained fields last). Unknown keys keep their relative order and
// move to the end. Returns true when the document was rewritten, false when
// it was already canonical (or had no frontmatter). Existing values are never
// overwritten; created is backfilled once when absent and updated refreshes,
// matching Update's timestamp semantics.
func NormalizeTaskFrontmatter(path string) (bool, error) {
	cleanPath, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return false, fmt.Errorf("resolve task path: %w", err)
	}
	unlock, err := acquireTaskLock(cleanPath)
	if err != nil {
		return false, err
	}
	defer unlock()

	data, err := os.ReadFile(cleanPath)
	if err != nil {
		return false, fmt.Errorf("read %s: %w", cleanPath, err)
	}
	content := string(data)
	if !strings.HasPrefix(content, "---") {
		return false, nil // no frontmatter: leave the document alone
	}
	rest := content[3:]
	end := strings.Index(rest, "\n---")
	if end == -1 {
		return false, fmt.Errorf("frontmatter not closed")
	}
	fmText := rest[:end]
	body := rest[end+4:]
	if body == "" {
		body = "\n"
	}
	// Empty frontmatter (---\n---) is left alone, matching Parse's
	// legacy-compatible empty-block handling: nothing to backfill or reorder.
	if strings.TrimSpace(fmText) == "" {
		return false, nil
	}

	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(fmText), &doc); err != nil {
		return false, fmt.Errorf("parse frontmatter: %w", err)
	}
	// Same numeric normalization as Parse: frontmatter editors may serialize
	// estimated_hours/actual_hours as quoted strings ("42"), which would
	// otherwise fail the Decode below and block normalization entirely.
	if err := normalizeNumericStrings(&doc); err != nil {
		return false, fmt.Errorf("parse frontmatter: %w", err)
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return false, fmt.Errorf("frontmatter is not a mapping")
	}
	mapping := doc.Content[0]

	var fm Frontmatter
	if err := doc.Decode(&fm); err != nil {
		return false, fmt.Errorf("parse frontmatter: %w", err)
	}
	now := time.Now().Format("2006-01-02T15:04:05-07:00")
	missing := missingDefaults(&doc, &fm)
	if created := extractFieldRaw(fmText, "created"); created == "" || created == `""` {
		missing = append(missing, FieldDefault{Key: "created", Value: now})
	}
	// updated refreshes on every rewrite (matching updateUnlocked); including
	// it in missing places it at its canonical slot when absent instead of
	// appending it at the tail, so a single normalization pass converges.
	missing = append(missing, FieldDefault{Key: "updated", Value: now})

	newContent, changed := buildCanonicalMapping(mapping, missing)
	if !changed {
		return false, nil
	}
	mapping.Content = newContent
	// Refresh the timestamp value in place (position already canonical).
	setMappingValue(mapping, "updated", now)

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return false, fmt.Errorf("encode frontmatter: %w", err)
	}
	if err := enc.Close(); err != nil {
		return false, fmt.Errorf("close frontmatter encoder: %w", err)
	}
	newFM := strings.TrimSuffix(buf.String(), "\n")

	repairedBody := escapeBodyTags(body)
	newDoc := "---\n" + newFM + "\n---" + repairedBody
	if _, err := Parse([]byte(newDoc)); err != nil {
		return false, fmt.Errorf("normalize would produce invalid frontmatter: %w", err)
	}
	if err := atomicWrite(cleanPath, []byte(newDoc)); err != nil {
		return false, err
	}
	// Post-write validation: confirm the persisted document still parses, so
	// a normalization defect can never leave a task file the daemon cannot
	// read (the rewrite is atomic, but a logical corruption must surface).
	if data, err := os.ReadFile(cleanPath); err != nil {
		return true, fmt.Errorf("read back after normalize: %w", err)
	} else if _, err := Parse(data); err != nil {
		return true, fmt.Errorf("post-write validation failed: %w", err)
	}
	return true, nil
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

// AtomicWrite writes data to a temporary file, fsyncs, and renames atomically.
// The destination is never left in a partial state — either the old content
// survives or the new content is fully written.
func AtomicWrite(path string, data []byte) error {
	return atomicWrite(path, data)
}

// atomicWrite is the unexported implementation shared within the package.
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
	//
	// Lines that don't match YAML patterns are collected separately. If they look
	// like markdown body (headings, blockquotes, tables), they are prepended to the
	// body — this recovers content lost when the closing "---" delimiter is missing
	// and a horizontal rule in the body is mistaken for the delimiter.
	lines := strings.Split(fmText, "\n")
	clean := make([]string, 0, len(lines))
	var discarded []string
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
			discarded = append(discarded, line)
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
		} else {
			discarded = append(discarded, line)
		}
	}
	newFM := strings.Join(clean, "\n")
	for strings.HasSuffix(newFM, "\n") {
		newFM = newFM[:len(newFM)-1]
	}

	// If discarded lines look like markdown body, prepend them to the body.
	// Body content starts with markdown structural elements: headings, blockquotes,
	// or table rows.  Orphaned free-text (no structural markers) is intentionally
	// dropped — it's likely OMP output that leaked into the frontmatter block.
	repairedBody := escapeBodyTags(body)
	if len(discarded) > 0 && looksLikeMarkdownBody(discarded) {
		// Trim leading blank lines from discarded block.
		start := 0
		for start < len(discarded) && strings.TrimSpace(discarded[start]) == "" {
			start++
		}
		recovered := "\n" + strings.Join(discarded[start:], "\n")
		repairedBody = recovered + repairedBody
	}

	newContent := "---\n" + newFM + "\n---" + repairedBody

	// Validate the repaired content before writing.
	if _, err := Parse([]byte(newContent)); err != nil {
		return fmt.Errorf("repair produced invalid frontmatter: %w", err)
	}
	return atomicWrite(path, []byte(newContent))
}

// markdownBodyStartRE matches lines that signal the start of markdown body content.
// Headings, blockquotes, and table rows are structural markers; free-form text
// without these markers is treated as orphaned YAML pollution.
var markdownBodyStartRE = regexp.MustCompile(`^(#{1,6}\s|\>\s|\|)`)

// looksLikeMarkdownBody returns true if the discarded lines appear to be markdown
// body content rather than orphaned text that leaked into the frontmatter block.
func looksLikeMarkdownBody(lines []string) bool {
	for _, line := range lines {
		if markdownBodyStartRE.MatchString(strings.TrimSpace(line)) {
			return true
		}
	}
	return false
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
	if fm.BlockedPhase != "" && fm.BlockedPhase != "refining" && fm.BlockedPhase != "planning" && fm.BlockedPhase != "implementing" {
		return nil, fmt.Errorf("invalid blocked_phase: %q", fm.BlockedPhase)
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

// ValidateADR checks whether an ADR file has valid frontmatter with required fields.
// Uses raw YAML parsing to avoid field name conflicts with the task Frontmatter struct.
func ValidateADR(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	// Parse raw YAML to check ADR-specific fields without task field conflicts.
	var raw map[string]interface{}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("parse frontmatter: %w", err)
	}
	if v, ok := raw["adr_id"]; !ok || v == nil || v == "" {
		return fmt.Errorf("missing required ADR field: adr_id")
	}
	if v, ok := raw["title"]; !ok || v == nil || v == "" {
		return fmt.Errorf("missing required ADR field: title")
	}
	if v, ok := raw["status"]; !ok || v == nil || v == "" {
		return fmt.Errorf("missing required ADR field: status")
	}
	return nil
}

// ValidateDocument auto-detects the document type and applies appropriate validation.
// Detects TASK, ADR, and REQ documents; falls back to syntax-only check for unknown types.
// All document types also get Markdown body tag scanning.
func ValidateDocument(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	// Parse raw YAML for type detection.
	var raw map[string]interface{}
	_ = yaml.Unmarshal(data, &raw)

	switch {
	case raw["adr_id"] != nil && raw["adr_id"] != "":
		// ADR document
		if v, ok := raw["adr_id"]; !ok || v == nil || v == "" {
			return fmt.Errorf("ADR missing required field: adr_id")
		}
		if v, ok := raw["title"]; !ok || v == nil || v == "" {
			return fmt.Errorf("ADR missing required field: title")
		}
		if v, ok := raw["status"]; !ok || v == nil || v == "" {
			return fmt.Errorf("ADR missing required field: status")
		}

	case raw["id"] != nil && raw["id"] != "" && raw["project_id"] != nil:
		// REQ document
		if v, ok := raw["id"]; !ok || v == nil || v == "" {
			return fmt.Errorf("REQ missing required field: id")
		}
		if v, ok := raw["title"]; !ok || v == nil || v == "" {
			return fmt.Errorf("REQ missing required field: title")
		}

	case raw["id"] != nil && raw["id"] != "" && raw["status"] != nil:
		// TASK document — full task validation.
		return ValidateTaskDocument(path)

	default:
		// Unknown document type — syntax + body tag only.
	}

	// Body tag scan for all document types.
	body := extractBody(data)
	if err := validateMarkdownBody(body); err != nil {
		return err
	}
	return nil
}

// WriteADR atomically writes an ADR markdown file with validation.
func WriteADR(path string, content string) error {
	return atomicWrite(path, []byte(content))
}
