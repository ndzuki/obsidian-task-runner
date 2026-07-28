package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
	"github.com/spf13/cobra"
)

// CurrentTaskSchemaVersion is the latest task_schema_version the migrator targets.
const CurrentTaskSchemaVersion = 1

var (
	migrateTasksDryRun bool
	migrateTasksWrite  bool
)

// migrationDefaults are the default values for fields introduced in
// task_schema_version=1 that may be missing from older TASK documents.
var migrationDefaults = map[string]interface{}{
	"task_schema_version":             CurrentTaskSchemaVersion,
	"phase_error_code":                "",
	"priority_assessment_status":      "",
	"priority_assessment_attempts":    0,
	"priority_assessment_started_at":  "",
	"priority_assessed_at":            "",
	"priority_assessed_value":         "",
	"priority_impact":                 "",
	"priority_urgency":                "",
	"priority_workaround":             "",
	"priority_score":                  0,
	"priority_confidence":             "",
	"priority_reason":                 "",
	"priority_recommendation":         "",
	"close_approved":                  false,
	"closure_reason":                  "",
	"closure_note":                    "",
	"replacement_task":                "",
	"review_feedback":                 "",
	"rework_resolution":               "",
}

var migrateTasksCmd = &cobra.Command{
	Use:   "migrate-tasks <vault_path>",
	Short: "Add missing frontmatter fields to all TASK documents",
	Long: `Scans the vault's Projects/*/Tasks/*.md tree and for each TASK document
with task_schema_version < current, adds missing frontmatter fields
with their default values.

Use --dry-run to list tasks that would be migrated.
Use --write to atomically update each TASK file.`,
	Args: cobra.ExactArgs(1),
	RunE: runMigrateTasks,
}

func runMigrateTasks(cmd *cobra.Command, args []string) error {
	vaultPath := args[0]
	projectsDir := filepath.Join(vaultPath, "Projects")
	projEntries, err := os.ReadDir(projectsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("Projects directory not found: %s", projectsDir)
		}
		return fmt.Errorf("read Projects dir: %w", err)
	}

	migrated := 0
	skipped := 0

	for _, proj := range projEntries {
		if !proj.IsDir() {
			continue
		}
		tasksDir := filepath.Join(projectsDir, proj.Name(), "Tasks")
		entries, err := os.ReadDir(tasksDir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" {
				continue
			}
			filePath := filepath.Join(tasksDir, entry.Name())

			data, err := os.ReadFile(filePath)
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "  %s: read error: %v\n", entry.Name(), err)
				continue
			}

			fm, err := yamlfrontmatter.Parse(data)
			if err != nil || fm == nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "  %s: parse error: %v\n", entry.Name(), err)
				continue
			}

			if fm.TaskSchemaVersion >= CurrentTaskSchemaVersion {
				skipped++
				continue
			}

			// Determine which fields are truly missing.
			updates := buildMigrationUpdates(data, fm)

			if len(updates) == 0 {
				skipped++
				continue
			}

			migrated++
			fmt.Fprintf(cmd.OutOrStdout(), "  %s (schema %d → %d): %d fields\n",
				filePath, fm.TaskSchemaVersion, CurrentTaskSchemaVersion, len(updates))

			if migrateTasksWrite {
				if err := yamlfrontmatter.Update(filePath, updates); err != nil {
					return fmt.Errorf("update %s: %w", filePath, err)
				}
			}
		}
	}

	fmt.Fprintf(cmd.OutOrStdout(), "\nScanned: %d migrated, %d skipped\n", migrated, skipped)
	return nil
}

// buildMigrationUpdates returns only the updates for fields that are actually
// missing from the task's frontmatter, keeping the schema version bump.
func buildMigrationUpdates(data []byte, fm *yamlfrontmatter.Frontmatter) map[string]interface{} {
	updates := make(map[string]interface{})

	for key, val := range migrationDefaults {
		if fieldIsEmpty(fm, key) {
			updates[key] = val
		}
	}

	return updates
}

// fieldIsEmpty checks whether a known frontmatter field has its zero value.
func fieldIsEmpty(fm *yamlfrontmatter.Frontmatter, key string) bool {
	switch key {
	case "task_schema_version":
		return fm.TaskSchemaVersion < CurrentTaskSchemaVersion
	case "phase_error_code":
		return fm.PhaseErrorCode == ""
	case "priority_assessment_status":
		return fm.PriorityAssessmentStatus == ""
	case "priority_assessment_attempts":
		return fm.PriorityAssessmentAttempts == 0
	case "priority_assessment_started_at":
		return fm.PriorityAssessmentStartedAt == ""
	case "priority_assessed_at":
		return fm.PriorityAssessedAt == ""
	case "priority_assessed_value":
		return fm.PriorityAssessedValue == ""
	case "priority_impact":
		return fm.PriorityImpact == ""
	case "priority_urgency":
		return fm.PriorityUrgency == ""
	case "priority_workaround":
		return fm.PriorityWorkaround == ""
	case "priority_score":
		return fm.PriorityScore == 0
	case "priority_confidence":
		return fm.PriorityConfidence == ""
	case "priority_reason":
		return fm.PriorityReason == ""
	case "priority_recommendation":
		return fm.PriorityRecommendation == ""
	case "close_approved":
		return !fm.CloseApproved
	case "closure_reason":
		return fm.ClosureReason == ""
	case "closure_note":
		return fm.ClosureNote == ""
	case "replacement_task":
		return fm.ReplacementTask == ""
	case "review_feedback":
		return fm.ReviewFeedback == ""
	case "rework_resolution":
		return fm.ReworkResolution == ""
	}
	return true
}

func init() {
	migrateTasksCmd.Flags().BoolVar(&migrateTasksDryRun, "dry-run", false, "List tasks that would be migrated without writing")
	migrateTasksCmd.Flags().BoolVar(&migrateTasksWrite, "write", false, "Atomically update each TASK file")
	rootCmd.AddCommand(migrateTasksCmd)
}
