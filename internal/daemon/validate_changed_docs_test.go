package daemon

import (
	"bytes"
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

func newValidateRunner(t *testing.T, buf *bytes.Buffer) *Runner {
	t.Helper()
	logger := log.New(buf, "", 0)
	if buf == nil {
		logger = log.New(io.Discard, "", 0)
	}
	return &Runner{
		cfg:    &config.Config{Notifications: config.NotifConfig{Desktop: false}},
		logger: logger,
	}
}

func writeTrackedFile(t *testing.T, repo, rel, content string) {
	t.Helper()
	abs := filepath.Join(repo, rel)
	// Stage a seed version so the file is tracked, then write the real
	// content as an unstaged working-tree modification — git diff
	// --name-only only reports the latter.
	seed := "---\nid: \"seed\"\n---\n# seed\n"
	if err := os.WriteFile(abs, []byte(seed), 0644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", repo, "add", rel).CombinedOutput(); err != nil {
		t.Fatalf("git add %s: %v: %s", rel, err, out)
	}
	if err := os.WriteFile(abs, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

// TestValidateChangedDocsSkipsNonGitRoot covers the vault-only demo project
// regression: repoDir is the vault project directory (not a git root), so
// git diff would resolve the enclosing vault repo and fabricate doubled
// paths. Validation must be skipped entirely, with no "damaged" output.
func TestValidateChangedDocsSkipsNonGitRoot(t *testing.T) {
	dir := t.TempDir()
	repo := createRepository(t, dir) // git root at <dir>/repo
	sub := filepath.Join(repo, "sub")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatal(err)
	}
	// A corrupt file inside the non-root dir must never be reported: the
	// whole subtree is not a standalone repo.
	writeTrackedFile(t, repo, "sub/corrupt.md", "no frontmatter at all\n")

	var buf bytes.Buffer
	runner := newValidateRunner(t, &buf)
	if err := runner.validateChangedDocs(sub, "001", "T1", "refining", "refining", ""); err != nil {
		t.Fatalf("non-git-root dir must skip validation, got error: %v", err)
	}

	logs := buf.String()
	if strings.Contains(logs, "damaged") {
		t.Fatalf("non-git-root dir must skip validation, got damage report: %s", logs)
	}
	if !strings.Contains(logs, "not a git root") {
		t.Fatalf("expected skip log line, got: %s", logs)
	}
}

// TestValidateChangedDocsAutoRepairs verifies salvageable corruption is
// fixed silently (no user notification) and the file passes validation
// afterwards.
func TestValidateChangedDocsAutoRepairs(t *testing.T) {
	dir := t.TempDir()
	repo := createRepository(t, dir)
	// Agent output leaked into the YAML block: the exact salvageable
	// failure mode Repair exists for. (The old ValidateDocument silently
	// passed this — fixed frontmatter-block parsing makes it fail, and
	// Repair drops the non-key line, leaving a complete valid TASK.)
	writeTrackedFile(t, repo, "TASK-001-demo.md", `---
id: "001"
title: "演示"
project: "demo"
project_id: "010"
status: refining
req_doc: Projects/010-demo/Requirements/REQ-001-demo.md
assignee: "default"
!!!garbage leaked by agent
---
# Task
`)

	var buf bytes.Buffer
	runner := newValidateRunner(t, &buf)
	if err := runner.validateChangedDocs(repo, "001", "T1", "refining", "refining", ""); err != nil {
		t.Fatalf("salvageable corruption must not block: %v", err)
	}

	logs := buf.String()
	if strings.Contains(logs, "damaged") {
		t.Fatalf("salvageable corruption must be auto-repaired, not reported: %s", logs)
	}
	if !strings.Contains(logs, "auto-repaired") {
		t.Fatalf("expected auto-repair log line, got: %s", logs)
	}
	if err := yamlfrontmatter.ValidateDocument(filepath.Join(repo, "TASK-001-demo.md")); err != nil {
		t.Fatalf("file not valid after auto-repair: %v", err)
	}
}

// TestValidateChangedDocsNotifiesOnlyUnrepairable verifies documents that
// cannot be salvaged (no frontmatter delimiters) still surface to the user.
func TestValidateChangedDocsNotifiesOnlyUnrepairable(t *testing.T) {
	dir := t.TempDir()
	repo := createRepository(t, dir)
	// Syntactically valid but incomplete TASK (missing project/req_doc):
	// Repair cannot backfill required fields, so re-validation still fails
	// and the user must be notified.
	writeTrackedFile(t, repo, "TASK-001-demo.md", `---
id: "001"
status: refining
---
# Task
`)

	var buf bytes.Buffer
	runner := newValidateRunner(t, &buf)
	err := runner.validateChangedDocs(repo, "001", "T1", "refining", "round2", "")

	if err == nil {
		t.Fatal("unrepairable document must return an error so the phase blocks (P1-3)")
	}
	if got := buf.String(); !strings.Contains(got, "damaged") {
		t.Fatalf("unrepairable document must be reported, got: %s", got)
	}
}

// TestValidateChangedDocsRepairFails covers the second unrepairable path:
// an unclosed frontmatter block fails validation AND Repair cannot salvage
// it (no closing delimiter to rebuild against) — the user must be notified.
func TestValidateChangedDocsRepairFails(t *testing.T) {
	dir := t.TempDir()
	repo := createRepository(t, dir)
	writeTrackedFile(t, repo, "TASK-002-broken.md", `---
id: "002"
status: refining
`)

	var buf bytes.Buffer
	runner := newValidateRunner(t, &buf)
	err := runner.validateChangedDocs(repo, "002", "T2", "refining", "refining", "")

	if err == nil {
		t.Fatal("unclosed frontmatter must return an error so the phase blocks (P1-3)")
	}
	if got := buf.String(); !strings.Contains(got, "damaged") {
		t.Fatalf("unclosed frontmatter must be reported, got: %s", got)
	}
}

// TestValidatePhaseDocumentsBlocksOnTaskCorruption is the P1-3 gate test: a
// corrupted task document after a phase must surface as an error through the
// combined gate (the caller escalates it to DOCUMENT_INVALID and stops
// advancing — previously this only logged and the phase marched on).
func TestValidatePhaseDocumentsBlocksOnTaskCorruption(t *testing.T) {
	dir := t.TempDir()
	repo := createRepository(t, dir)
	taskPath := filepath.Join(repo, "TASK-001-demo.md")
	if err := os.WriteFile(taskPath, []byte("---\nid: \"001\"\nstatus: refining\n"), 0644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	runner := newValidateRunner(t, &buf)
	err := runner.validatePhaseDocuments(taskPath, repo, "001", "T1", "implementing", "round2", "")
	if err == nil {
		t.Fatal("corrupt task document must block the phase gate")
	}
	if !strings.Contains(err.Error(), "frontmatter corrupt") && !strings.Contains(err.Error(), "damaged") {
		t.Fatalf("unexpected gate error: %v", err)
	}
}

// TestGitTopLevel verifies the root resolution used by the git-root guard.
func TestGitTopLevel(t *testing.T) {
	dir := t.TempDir()
	repo := createRepository(t, dir)
	sub := filepath.Join(repo, "sub")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatal(err)
	}
	top, err := gitTopLevel(sub)
	if err != nil {
		t.Fatalf("gitTopLevel: %v", err)
	}
	if filepath.Clean(top) != filepath.Clean(repo) {
		t.Fatalf("gitTopLevel(%s) = %q, want %q", sub, top, repo)
	}
	if _, err := gitTopLevel(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("gitTopLevel on missing dir must fail")
	}
}
