package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ndzuki/obsidian-task-runner/internal/config"
	"github.com/ndzuki/obsidian-task-runner/internal/stageplan"
	"github.com/spf13/cobra"
)

var (
	stagePlanForce     bool
	stagePlanDryRun    bool
	stagePlanMaxPhases int
	stagePlanMinPer    int
	stagePlanMapFile   string
)

var stagePlanInitCmd = &cobra.Command{
	Use:   "init <project>",
	Short: "Deterministically derive delivery phases from task dependency topology",
	Long: `Derives delivery phases for a project's in-flight (not done/closed) tasks
from blocked_by topology — no LLM round-trip. Writes Notes/Stage-Plan.md
(creating it, or appending new phases for tasks that still have no stage)
and backfills the frontmatter stage field on every affected task.

Already-staged tasks are never touched; their ids still satisfy
dependencies when layering, so incremental runs converge.`,
	Args: cobra.ExactArgs(1),
	RunE: runStagePlanInit,
}

func newStagePlanCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "stage-plan",
		Short: "阶段化交付计划管理",
	}
	stagePlanInitCmd.Flags().BoolVar(&stagePlanForce, "force", false, "rebuild the whole stage plan: re-derive every in-flight task and REWRITE Stage-Plan.md from scratch (history phase blocks are replaced, not appended)")
	stagePlanInitCmd.Flags().BoolVar(&stagePlanDryRun, "dry-run", false, "print the derived phases without writing anything")
	stagePlanInitCmd.Flags().IntVar(&stagePlanMaxPhases, "max-phases", 4, "phase count ceiling")
	stagePlanInitCmd.Flags().IntVar(&stagePlanMinPer, "min-per-phase", 3, "tasks per phase floor before merging layers")
	stagePlanInitCmd.Flags().StringVar(&stagePlanMapFile, "map-file", "", "path to vault-map.json")
	command.AddCommand(stagePlanInitCmd)
	return command
}

func runStagePlanInit(cmd *cobra.Command, args []string) error {
	project := args[0]
	cfg, err := config.Load(stagePlanMapFile)
	if err != nil {
		return err
	}
	// The vault project dir holds Tasks/ and Notes/. ResolveProject returns
	// the git repo path, which is NOT where task documents live — use the
	// vault-map Path field (authoritative) when it points into the vault,
	// with a directory-name fallback.
	projDir := resolveProjectVaultDir(cfg, project)
	if projDir == "" {
		return fmt.Errorf("project %q: no matching directory under %s/Projects (check vault-map.json projects[].path or the Projects/ directory name)", project, cfg.ObsidianVault)
	}
	tasksDir := filepath.Join(projDir, "Tasks")
	notesDir := filepath.Join(projDir, "Notes")
	if _, err := os.Stat(tasksDir); err != nil {
		return fmt.Errorf("project %q: no Tasks/ at %s — the resolved path must point into the vault (Projects/<dir>), not the git repo; vault-map projects[].path = %q", project, tasksDir, projectPathField(cfg, project))
	}

	res, err := stageplan.Apply(tasksDir, notesDir, project,
		stageplan.Options{MinTasksPerPhase: stagePlanMinPer, MaxPhases: stagePlanMaxPhases},
		stageplan.ApplyOptions{Force: stagePlanForce, DryRun: stagePlanDryRun})
	if err != nil {
		return err
	}
	if len(res.Phases) == 0 {
		fmt.Printf("project %s: all in-flight tasks already staged\n", project)
		return nil
	}
	fmt.Printf("project %s: derived %d phase(s)\n", project, len(res.Phases))
	for _, p := range res.Phases {
		fmt.Printf("  Phase %d (%s): %s\n", p.Number, p.Name, strings.Join(p.Tasks, ", "))
	}
	if stagePlanDryRun {
		return nil
	}
	fmt.Printf("staged %d task(s)%s\n", res.Staged, map[bool]string{true: " (Stage-Plan.md created)", false: ""}[res.Created])
	return nil
}

// resolveProjectVaultDir resolves the vault-side project directory for a
// project name. The vault-map Path field is used only when it actually
// points inside the vault (some projects store the git repo path there,
// e.g. release-manager); otherwise fall back to a directory-name match
// ("001-release-manager" for "release-manager").
func resolveProjectVaultDir(cfg *config.Config, project string) string {
	vaultRoot := filepath.Clean(cfg.ObsidianVault)
	for _, p := range cfg.Projects {
		if p.Name != project {
			continue
		}
		dir := p.Path
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(vaultRoot, dir)
		}
		if strings.HasPrefix(filepath.Clean(dir), vaultRoot+string(filepath.Separator)) {
			return dir
		}
	}
	return findVaultProjectDir(vaultRoot, project)
}

// projectPathField returns the raw vault-map projects[].path for a project
// (used in error messages).
func projectPathField(cfg *config.Config, project string) string {
	for _, p := range cfg.Projects {
		if p.Name == project {
			return p.Path
		}
	}
	return ""
}

// findVaultProjectDir resolves the vault-side project directory for a
// project name: matches "001-release-manager" for "release-manager" (and
// exact names). Mirrors the daemon's project-dir lookup semantics.
func findVaultProjectDir(vaultPath, project string) string {
	projectsDir := filepath.Join(vaultPath, "Projects")
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if name == project || strings.HasSuffix(name, "-"+project) {
			return filepath.Join(projectsDir, name)
		}
	}
	return ""
}
