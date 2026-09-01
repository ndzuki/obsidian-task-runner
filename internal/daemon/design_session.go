package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ndzuki/obsidian-task-runner/internal/designlib"
	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

// errDesignTargetUnwritable reports that the vault Design directory cannot be
// written by the daemon — a deterministic environment defect that no LLM
// session can fix and that blind retries against the same path cannot
// converge. runReplanGate maps it to ErrDesignTargetUnwritable so the task
// blocks with an actionable message instead of burning design sessions.
var errDesignTargetUnwritable = errors.New("design target unwritable")

// designProbeName is the write probe file created inside the design library
// root before a global design session is dispatched.
const designProbeName = ".otg-design-probe"

// checkDesignTargetWritable verifies the design library directory accepts
// writes before an expensive design session runs. The daemon runs on the
// host with vault write access while the session's own sandbox may not — but
// a directory the daemon cannot write can never receive the session's
// artifacts either, so probing here fails fast (seconds, not a 10-90 minute
// session that ends in an invalid library).
func checkDesignTargetWritable(root string) error {
	probe := filepath.Join(root, designProbeName)
	f, err := os.OpenFile(probe, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("%w: %s: %v", errDesignTargetUnwritable, root, err)
	}
	_ = f.Close()
	_ = os.Remove(probe)
	return nil
}

// runGlobalDesignSession executes the one-shot project architecture session
// through DSH, then treats the validated Design library as the durable
// result. A process success without valid four-class design artifacts in the
// REAL vault Design directory is a failure: no revision is bumped and later
// task sessions must not consume a partial library.
//
// Contract (TASK-065 lesson, 2026-08-24): the session's working directory is
// the vault Design directory itself, so the workspace-write sandbox scope
// covers exactly the artifact tree. Sessions from before this contract staged
// artifacts under <repo>/.design-stage/ when the vault was outside their
// write scope; the daemon validates and imports such staging as a fallback,
// because the daemon itself can write the vault.
func (r *Runner) runGlobalDesignSession(ctx context.Context, project, taskID, taskPath, reqDoc, repoDir string) error {
	projectDir := resolveVaultProjectDir(r.cfg.ObsidianVault, project)
	if projectDir == "" {
		return fmt.Errorf("design session %s: project %q not found in vault", taskID, project)
	}
	layout, err := designlib.Ensure(projectDir)
	if err != nil {
		return err
	}
	if err := checkDesignTargetWritable(layout.Root); err != nil {
		return fmt.Errorf("design session %s: %w", taskID, err)
	}
	if r.designExecutor == nil {
		r.designExecutor = newDSHExecutorWithProfile(r.cfg.DSHCmd, r.cfg.DSHProfile, "")
	}
	// WorkingDir = the Design directory: the session's workspace-write scope
	// is exactly the artifact tree (glossary + contracts/decisions/waves).
	// The repo is passed as a read-only input path for code evidence.
	reqPath := filepath.Join(r.cfg.ObsidianVault, reqDoc)
	spec := PhaseSpec{
		Phase:           "design",
		Model:           r.cfg.Model("deepseek"),
		ReasoningEffort: "max",
		SkillPrompt: fmt.Sprintf(
			"/obsidian-task-runner-design project_dir=%s project=%s task_id=%s task_path=%s req_doc=%s design_dir=%s repo_dir=%s",
			projectDir, project, taskID, taskPath, reqPath, layout.Root, repoDir,
		),
		Timeout:    r.cfg.PhaseTimeout("design"),
		WorkingDir: layout.Root,
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
		// Legacy fallback: sessions dispatched before the Design-dir working
		// contract could not write the vault and staged artifacts under
		// <repo>/.design-stage/. Validate that staging and import it — the
		// daemon has vault write access even when the session did not.
		imported, importErr := importStagedDesignLibrary(projectDir, repoDir)
		if importErr != nil {
			return fmt.Errorf("design session %s produced invalid library: %w (staged import failed: %v; session tail: %s)", taskID, err, importErr, sessionTail(result.Stdout))
		}
		if !imported {
			return fmt.Errorf("design session %s produced invalid library: %w (no valid staging under %s; session tail: %s)", taskID, err, stagingRoot(repoDir), sessionTail(result.Stdout))
		}
		if err := layout.Validate(); err != nil {
			return fmt.Errorf("design session %s: imported staging still invalid: %w", taskID, err)
		}
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

// stagingRoot returns the legacy staging directory path inside a repo (used
// in error messages only; never created here).
func stagingRoot(repoDir string) string {
	if repoDir == "" {
		return "<no repo>"
	}
	return filepath.Join(repoDir, ".design-stage")
}

// sessionTail returns the final portion of a design session's stdout so an
// invalid-library error carries the session's own conclusion (e.g. "vault is
// read-only, run: cp -r .design-stage/...") instead of only the validator
// message.
func sessionTail(stdout string) string {
	const max = 800
	s := strings.TrimSpace(stdout)
	if s == "" {
		return "<no session output>"
	}
	if len(s) <= max {
		return s
	}
	return "…" + s[len(s)-max:]
}

// findStagedDesignRoot locates a legacy staged design library under
// <repoDir>/.design-stage/Projects/: first the exact project directory name,
// then (fallback) any subdirectory whose Design library validates. Returns ""
// when nothing usable exists.
func findStagedDesignRoot(repoDir, projectDir string) string {
	if repoDir == "" {
		return ""
	}
	projectsRoot := filepath.Join(repoDir, ".design-stage", "Projects")
	exact := filepath.Join(projectsRoot, filepath.Base(projectDir))
	if info, err := os.Stat(exact); err == nil && info.IsDir() {
		if designlib.ForProject(exact).Validate() == nil {
			return exact
		}
	}
	entries, err := os.ReadDir(projectsRoot)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		candidate := filepath.Join(projectsRoot, e.Name())
		if designlib.ForProject(candidate).Validate() == nil {
			return candidate
		}
	}
	return ""
}

// importStagedDesignLibrary copies a validated staged library from the repo's
// .design-stage into the real vault Design directory. It is the legacy
// fallback for design sessions that ran before the Design-dir working
// contract and could not write the vault themselves. Returns true when a
// staged library existed and was imported; an invalid staging is reported as
// (false, nil) so the caller keeps the authoritative session error.
func importStagedDesignLibrary(projectDir, repoDir string) (bool, error) {
	staged := findStagedDesignRoot(repoDir, projectDir)
	if staged == "" {
		return false, nil
	}
	src := designlib.ForProject(staged)
	if err := src.Validate(); err != nil {
		return false, nil
	}
	dst := designlib.ForProject(projectDir)

	type pair struct{ src, dst string }
	files := []pair{{filepath.Join(src.Root, designlib.GlossaryFile), dst.GlossaryPath()}}
	for _, dir := range []string{designlib.ContractsDir, designlib.DecisionsDir, designlib.WavesDir} {
		entries, err := os.ReadDir(filepath.Join(src.Root, dir))
		if err != nil {
			return false, fmt.Errorf("read staged %s: %w", dir, err)
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			files = append(files, pair{
				src: filepath.Join(src.Root, dir, e.Name()),
				dst: filepath.Join(dst.Root, dir, e.Name()),
			})
		}
	}
	for _, f := range files {
		data, err := os.ReadFile(f.src)
		if err != nil {
			return false, fmt.Errorf("read staged artifact %s: %w", f.src, err)
		}
		if err := os.MkdirAll(filepath.Dir(f.dst), 0o755); err != nil {
			return false, fmt.Errorf("mkdir %s: %w", filepath.Dir(f.dst), err)
		}
		if err := yamlfrontmatter.AtomicWrite(f.dst, data); err != nil {
			return false, fmt.Errorf("import %s: %w", f.dst, err)
		}
	}
	if err := dst.Validate(); err != nil {
		return false, fmt.Errorf("imported library invalid: %w", err)
	}
	return true, nil
}
