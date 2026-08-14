package daemon

import (
	"encoding/json"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndzuki/obsidian-task-runner/internal/config"
	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

// TestDetectStaleDoneReopens guards the stale-terminal reopen loop: a done
// task whose plan_version >= 2 carries a checkpoint_commit that is NOT an
// ancestor of origin/main is an undelivered increment frozen behind a fake
// terminal (TASK-018: an external frontmatter write restored the baseline
// done; downstream TASK-071 starved on the dependency gate). Such tasks
// reopen to refining with a full generation reset. Tasks whose checkpoint
// IS merged, plan < 2, no checkpoint, no merged PR, or an unresolvable
// project stay untouched — conservative.
func TestDetectStaleDoneReopens(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	tasksDir := filepath.Join(vault, "Projects", "001-test", "Tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	repoDir := filepath.Join(dir, "repo")
	base, checkpoint := initGitRepo(t, repoDir)
	// A second repo whose remote main ALREADY carries the checkpoint while
	// the local origin/main mirror is stale (pre-fetch state right after a
	// forge-side merge) — the regression shape of TASK-018 2026-08-14.
	mergedRepoDir := filepath.Join(dir, "merged-repo")
	_, mergedCheckpoint := initMergedRemoteRepo(t, mergedRepoDir)

	// vault-map registers the "test" project → repoDir, so the scan resolves it.
	skillDir := filepath.Join(dir, "skills")
	mapFile := filepath.Join(skillDir, "config", "vault-map.json")
	if err := os.MkdirAll(filepath.Dir(mapFile), 0o755); err != nil {
		t.Fatal(err)
	}
	vm := map[string]interface{}{
		"projects": []map[string]interface{}{
			{"name": "test", "path": repoDir, "git_remote": "github.com/x/test", "project_id": "001"},
			{"name": "merged", "path": mergedRepoDir, "git_remote": "github.com/x/merged", "project_id": "002"},
		},
	}
	data, _ := json.MarshalIndent(vm, "", "  ")
	if err := os.WriteFile(mapFile, data, 0o644); err != nil {
		t.Fatal(err)
	}

	writeTask := func(id, status, merge, plan, ckpt, project string) string {
		t.Helper()
		content := "---\nid: \"" + id + "\"\ntitle: T" + id + "\nproject: " + project + "\nassignee: default\nstatus: " + status + "\nmerge_status: " + merge + "\nplan_version: " + plan + "\ncheckpoint_commit: " + ckpt + "\nreopen_count: 0\npending_req: false\nknowledge_extracted: true\n---\n# T\n"
		path := filepath.Join(tasksDir, "TASK-"+id+"-t.md")
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	statusOf := func(path string) string {
		t.Helper()
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		fm, err := yamlfrontmatter.Parse(raw)
		if err != nil || fm == nil {
			t.Fatalf("parse: %v", err)
		}
		return fm.Status
	}
	fmOf := func(path string) *yamlfrontmatter.Frontmatter {
		t.Helper()
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		fm, err := yamlfrontmatter.Parse(raw)
		if err != nil || fm == nil {
			t.Fatalf("parse: %v", err)
		}
		return fm
	}

	stale := writeTask("001", "done", "merged", "6", checkpoint, "test")               // unmerged checkpoint: reopen
	delivered := writeTask("002", "done", "merged", "6", base, "test")                 // checkpoint in origin/main: keep
	legacy := writeTask("003", "done", "merged", "1", checkpoint, "test")              // single delivery: keep
	nockpt := writeTask("004", "done", "merged", "6", "", "test")                      // no checkpoint: keep
	unmerged := writeTask("005", "done", "", "6", checkpoint, "test")                  // done w/o merged PR: DoneReopensMerge owns it
	norepo := writeTask("006", "done", "merged", "6", checkpoint, "missing")           // unresolvable project: keep
	freshMerged := writeTask("007", "done", "merged", "6", mergedCheckpoint, "merged") // merged on remote, stale local mirror: keep

	runner := New(&config.Config{ObsidianVault: vault, SkillInstallDir: skillDir})
	runner.logger = log.New(io.Discard, "", 0)

	if n := runner.detectStaleDoneReopens(); n != 1 {
		t.Fatalf("reopened %d, want 1", n)
	}
	if got := statusOf(stale); got != "refining" {
		t.Fatalf("stale task = %q, want refining", got)
	}
	fm := fmOf(stale)
	if !fm.PendingReq || fm.ReopenCount != 1 || fm.MergeStatus != "" || fm.KnowledgeExtracted {
		t.Fatalf("stale reopen missing generation reset: pending_req=%v reopen_count=%d merge_status=%q knowledge_extracted=%v",
			fm.PendingReq, fm.ReopenCount, fm.MergeStatus, fm.KnowledgeExtracted)
	}
	for _, p := range []string{delivered, legacy, nockpt, unmerged, norepo, freshMerged} {
		if got := statusOf(p); got != "done" {
			t.Fatalf("task %s must stay done, got %q", filepath.Base(p), got)
		}
	}

	// Idempotent: second run reopens nothing.
	if n := runner.detectStaleDoneReopens(); n != 0 {
	}
}

// initGitRepo creates a local repo with a base commit on main, wired to a
// bare fake remote whose main carries only the base commit (the local
// origin/main mirror is synced to it), then adds a second (unpushed)
// checkpoint commit. Returns (base, checkpoint).
func initGitRepo(t *testing.T, repoDir string) (string, string) {
	t.Helper()
	remoteDir := filepath.Join(filepath.Dir(repoDir), filepath.Base(repoDir)+".git")
	if err := os.MkdirAll(remoteDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, remoteDir, "init", "-q", "--bare")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoDir, "init", "-q", "-b", "main")
	runGit(t, repoDir, "commit", "--allow-empty", "-q", "-m", "base")
	base := gitOut(t, repoDir, "rev-parse", "HEAD")
	runGit(t, repoDir, "remote", "add", "origin", remoteDir)
	runGit(t, repoDir, "push", "-q", "-u", "origin", "main")
	runGit(t, repoDir, "commit", "--allow-empty", "-q", "-m", "checkpoint-wip")
	checkpoint := gitOut(t, repoDir, "rev-parse", "HEAD")
	return base, checkpoint
}

// initMergedRemoteRepo builds the delivery-just-landed shape: the remote
// main carries the checkpoint (as after a forge-side merge), but the local
// origin/main mirror is rolled back to base — the pre-fetch state the stale
// done detector races right after gh pr merge (TASK-018 2026-08-14: the
// detector reopened a delivered task minutes after PR #76 merged). Returns
// (base, checkpoint).
func initMergedRemoteRepo(t *testing.T, repoDir string) (string, string) {
	t.Helper()
	base, checkpoint := initGitRepo(t, repoDir)
	runGit(t, repoDir, "push", "-q", "origin", "main")
	runGit(t, repoDir, "update-ref", "refs/remotes/origin/main", base)
	return base, checkpoint
}

func runGit(t *testing.T, repoDir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.email=t@t", "-c", "user.name=t", "-c", "commit.gpgsign=false", "-c", "gpg.format=", "-c", "user.signingkey="}, args...)...)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func gitOut(t *testing.T, repoDir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}
