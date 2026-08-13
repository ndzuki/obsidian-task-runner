package daemon

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"

	"github.com/ndzuki/obsidian-task-runner/internal/config"
	"github.com/ndzuki/obsidian-task-runner/internal/task"
	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

// TestReqNormalizeRefreshesTaskHashes guards the hash-sync contract: when
// syncReqSchemaDefaults backfills REQ frontmatter, linked tasks whose stored
// refine/plan hashes match the pre-write REQ must follow the new bytes —
// otherwise OnReqChanged treats the daemon's own normalization as a
// requirement change and batch-reopens every linked task (2026-08-12: 19
// tasks flipped to refining by a single schema backfill).
func TestReqNormalizeRefreshesTaskHashes(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	reqRel := filepath.Join("Projects", "001-demo", "Requirements", "REQ-001-legacy.md")
	reqPath := filepath.Join(vault, reqRel)
	taskPath := filepath.Join(vault, "Projects", "001-demo", "Tasks", "TASK-001-legacy.md")
	for _, p := range []string{filepath.Dir(reqPath), filepath.Dir(taskPath)} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Legacy REQ missing created/updated/tags — the backfill target.
	legacy := "---\nid: \"001\"\ntitle: Legacy\n---\n# Body\n"
	if err := os.WriteFile(reqPath, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}

	runner := New(&config.Config{ObsidianVault: vault})
	runner.logger = log.New(io.Discard, "", 0)
	oldHash := runner.reqHash(reqRel)

	taskContent := fmt.Sprintf("---\nid: \"001\"\ntitle: T\ntask_schema_version: 1\nstatus: review\nreq_doc: %s\nrefine_req_hash: %s\nplan_req_hash: %s\n---\n# T\n", reqRel, oldHash, oldHash)
	if err := os.WriteFile(taskPath, []byte(taskContent), 0o644); err != nil {
		t.Fatal(err)
	}

	runner.syncReqSchemaDefaults()

	raw, err := os.ReadFile(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	fm, err := yamlfrontmatter.Parse(raw)
	if err != nil || fm == nil {
		t.Fatalf("parse task: %v", err)
	}
	newHash := runner.reqHash(reqRel)
	if newHash == oldHash {
		t.Fatal("normalize did not change REQ bytes")
	}
	if fm.RefineReqHash != newHash || fm.PlanReqHash != newHash {
		t.Fatalf("task hashes not refreshed: refine=%q plan=%q, want %q", fm.RefineReqHash, fm.PlanReqHash, newHash)
	}

	// OnReqChanged must now treat the rewrite as absorbed: no task reopens.
	results := task.OnReqChanged(vault, reqRel, "")
	if len(results) != 0 {
		t.Fatalf("OnReqChanged re-opened absorbed normalize: %+v", results)
	}
}

// TestReqNormalizeKeepsStaleTaskHashes guards the other side of the sync
// contract: a stored hash older than the pre-write REQ is a real unabsorbed
// change and must survive normalization untouched.
func TestReqNormalizeKeepsStaleTaskHashes(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	reqRel := filepath.Join("Projects", "001-demo", "Requirements", "REQ-001-legacy.md")
	reqPath := filepath.Join(vault, reqRel)
	taskPath := filepath.Join(vault, "Projects", "001-demo", "Tasks", "TASK-001-legacy.md")
	for _, p := range []string{filepath.Dir(reqPath), filepath.Dir(taskPath)} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	legacy := "---\nid: \"001\"\ntitle: Legacy\n---\n# Body\n"
	if err := os.WriteFile(reqPath, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}

	runner := New(&config.Config{ObsidianVault: vault})
	runner.logger = log.New(io.Discard, "", 0)
	stale := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	taskContent := fmt.Sprintf("---\nid: \"001\"\ntitle: T\ntask_schema_version: 1\nstatus: review\nreq_doc: %s\nrefine_req_hash: %s\nplan_req_hash: %s\n---\n# T\n", reqRel, stale, stale)
	if err := os.WriteFile(taskPath, []byte(taskContent), 0o644); err != nil {
		t.Fatal(err)
	}

	runner.syncReqSchemaDefaults()

	raw, err := os.ReadFile(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	fm, err := yamlfrontmatter.Parse(raw)
	if err != nil || fm == nil {
		t.Fatalf("parse task: %v", err)
	}
	if fm.RefineReqHash != stale || fm.PlanReqHash != stale {
		t.Fatalf("stale hashes must survive: refine=%q plan=%q", fm.RefineReqHash, fm.PlanReqHash)
	}

	// The real pending change still surfaces through OnReqChanged.
	results := task.OnReqChanged(vault, reqRel, "")
	if len(results) == 0 {
		t.Fatal("OnReqChanged missed the genuine unabsorbed change")
	}
}
