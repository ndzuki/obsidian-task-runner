package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ndzuki/obsidian-task-runner/internal/designlib"
)

// runGlobalDesignSession executes the one-shot project architecture session
// through DSH, then treats the validated Design library as the durable result.
// A process success without valid four-class design artifacts is a failure: no
// revision is bumped and later task sessions must not consume a partial library.
func (r *Runner) runGlobalDesignSession(ctx context.Context, project, taskID, taskPath, reqDoc, repoDir string) error {
	projectDir := resolveVaultProjectDir(r.cfg.ObsidianVault, project)
	if projectDir == "" {
		return fmt.Errorf("design session %s: project %q not found in vault", taskID, project)
	}
	layout, err := designlib.Ensure(projectDir)
	if err != nil {
		return err
	}

	workingDir := projectDir
	if repoDir != "" {
		if info, statErr := os.Stat(repoDir); statErr == nil && info.IsDir() {
			workingDir = repoDir
		}
	}
	if r.designExecutor == nil {
		r.designExecutor = newDSHExecutorWithProfile(r.cfg.DSHCmd, r.cfg.DSHProfile, "")
	}
	spec := PhaseSpec{
		Phase:           "design",
		Model:           "deepseek_magic/deepseek-v4-pro",
		ReasoningEffort: "max",
		SkillPrompt: fmt.Sprintf(
			"/obsidian-task-runner-design project_dir=%s project=%s task_id=%s task_path=%s req_doc=%s design_dir=%s",
			projectDir, project, taskID, taskPath, filepath.Join(r.cfg.ObsidianVault, reqDoc), layout.Root,
		),
		Timeout:    r.cfg.PhaseTimeout("design"),
		WorkingDir: workingDir,
	}
	if spec.Timeout <= 0 {
		spec.Timeout = 90 * time.Minute
	}
	handle, err := r.designExecutor.Start(ctx, spec, TaskSnapshot{
		TaskID:   taskID,
		TaskPath: taskPath,
		Project:  project,
		RepoDir:  repoDir,
	})
	if err != nil {
		return fmt.Errorf("design session %s start: %w", taskID, err)
	}
	result, err := handle.Wait()
	if err != nil {
		return fmt.Errorf("design session %s wait: %w", taskID, err)
	}
	if result == nil {
		return fmt.Errorf("design session %s returned no result", taskID)
	}
	if result.Code != OutcomeSuccess {
		reason := strings.TrimSpace(result.Error)
		if reason == "" {
			reason = string(result.Code)
		}
		return fmt.Errorf("design session %s failed: %s", taskID, reason)
	}
	if err := layout.Validate(); err != nil {
		return fmt.Errorf("design session %s produced invalid library: %w", taskID, err)
	}
	sessionID := result.ResumeToken
	if sessionID == "" {
		sessionID = fmt.Sprintf("design-%s-%d", taskID, time.Now().UnixNano())
	}
	if _, err := layout.BumpRevision(sessionID); err != nil {
		return fmt.Errorf("design session %s record revision: %w", taskID, err)
	}
	return nil
}
