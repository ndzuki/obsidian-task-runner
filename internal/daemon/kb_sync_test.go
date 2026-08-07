package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ndzuki/obsidian-task-runner/internal/knowledge"
)

// writeKBRef writes a valid KB v2 reference document under the vault.
func writeKBRef(t *testing.T, vault, rel, body string) string {
	t.Helper()
	path := filepath.Join(vault, "References", rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\ntopics: [probe]\nlevel: beginner\nupdated: \"2026-08-07\"\nsource: \"local\"\nverified: false\naliases: []\n---\n\n# Probe\n\n" + body + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// waitSyncDone blocks until the debounced store-sync goroutine (if any) has
// finished. kbSyncRunning is set synchronously before the goroutine starts and
// cleared in its defer after the SQLite commit, so observing false after a
// trigger means the sync either never started or fully committed.
func waitSyncDone(t *testing.T, runner *Runner, what string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for runner.kbSyncRunning.Load() {
		if time.Now().After(deadline) {
			t.Fatalf("store sync %s did not finish in time", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestMaybeSyncKnowledgeDBIndexesNewRef guards the watcher-driven retrieval
// store sync: after a References/ write, the debounced sync must index the new
// document so kb search finds it without any manual otg kb command.
func TestMaybeSyncKnowledgeDBIndexesNewRef(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	writeKBRef(t, vault, "core/go/probe-ref.md", "probe marker zzq9 unique term")

	runner := newTestRunner(t.TempDir(), "omp", filepath.Join(dir, "logs"), 1)
	runner.cfg.ObsidianVault = vault
	runner.cfg.KBDb = filepath.Join(dir, "kb.sqlite")

	runner.maybeSyncKnowledgeDB()
	waitSyncDone(t, runner, "initial sync")

	dbPath := knowledge.KBPath(vault, runner.cfg.KBDb)
	hits, err := knowledge.SearchKnowledgeDB(dbPath, "zzq9", 2, true, nil, 0)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("new References/ doc not indexed after maybeSyncKnowledgeDB")
	}
}

// TestMaybeSyncKnowledgeDBDebounces: a second trigger inside the 10s window
// must not start another sync goroutine — write bursts (agent intake batches)
// coalesce into one store sync.
func TestMaybeSyncKnowledgeDBDebounces(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	runner := newTestRunner(t.TempDir(), "omp", filepath.Join(dir, "logs"), 1)
	runner.cfg.ObsidianVault = vault
	runner.cfg.KBDb = filepath.Join(dir, "kb.sqlite")

	runner.maybeSyncKnowledgeDB()
	waitSyncDone(t, runner, "first sync")

	// Second trigger within the debounce window: dropped, no goroutine.
	runner.maybeSyncKnowledgeDB()
	if runner.kbSyncRunning.Load() {
		t.Fatal("debounce window violated: second trigger started a sync")
	}
	// The debounce timestamp must also gate a later trigger inside 10s.
	time.Sleep(50 * time.Millisecond)
	runner.maybeSyncKnowledgeDB()
	if runner.kbSyncRunning.Load() {
		t.Fatal("second trigger inside window started a sync")
	}
}
