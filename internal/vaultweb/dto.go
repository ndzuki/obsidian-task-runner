package vaultweb

// ProjectDTO is a project's navigation summary.
type ProjectDTO struct {
	ID        string         `json:"id"`       // numeric prefix, e.g. "001"
	Name      string         `json:"name"`     // bare name, e.g. "obsidian-task-runner"
	DirName   string         `json:"dir_name"` // full dir, e.g. "001-obsidian-task-runner"
	TaskCount int            `json:"task_count"`
	ByStatus  map[string]int `json:"by_status"`
}

// TaskDTO is the whitelisted task summary. It carries only fields safe to
// render — never the full markdown body.
type TaskDTO struct {
	ID             string   `json:"id"`
	Title          string   `json:"title"`
	Project        string   `json:"project"`
	Status         string   `json:"status"`
	Priority       string   `json:"priority"`
	Assignee       string   `json:"assignee"`
	PlanVersion    int      `json:"plan_version"`
	Generation     int      `json:"generation"`
	Stage          string   `json:"stage,omitempty"`
	BlockedBy      []string `json:"blocked_by,omitempty"`
	PhaseErrorCode string   `json:"phase_error_code,omitempty"`
	Updated        string   `json:"updated,omitempty"`
	ReqDoc         string   `json:"req_doc,omitempty"`
}

// Column is a view column definition.
type Column struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

// ViewDTO is a whitelisted projection of vault data. Rows are generic
// key-value maps so each view declares its own schema.
type ViewDTO struct {
	ViewID  string           `json:"view_id"`
	Project string           `json:"project"`
	Columns []Column         `json:"columns"`
	Rows    []map[string]any `json:"rows"`
}

// DesignSummaryDTO mirrors the design library inventory plus its validation
// verdict.
type DesignSummaryDTO struct {
	Revision    int      `json:"revision"`
	Valid       bool     `json:"valid"`
	Contracts   []string `json:"contracts"`
	Decisions   []string `json:"decisions"`
	Waves       []string `json:"waves"`
	HasGlossary bool     `json:"has_glossary"`
}

// TaskUpdateRequest is a whitelisted, generation-fenced write. Updates may
// only contain writableFields keys; System-owned fields are rejected by the
// service layer.
type TaskUpdateRequest struct {
	ExpectedGeneration int            `json:"expected_generation"`
	Updates            map[string]any `json:"updates"`
}
