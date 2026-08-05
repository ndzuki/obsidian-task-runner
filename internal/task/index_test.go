package task

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeIdxTask writes a minimal ready task document; extra is appended verbatim
// into the frontmatter (callers supply status/blocked_by overrides). The id is
// derived from the file name (TASK-001-a.md → "001").
func writeIdxTask(t *testing.T, dir, name, extra string) string {
	t.Helper()
	path := filepath.Join(dir, "Projects", "001-test", "Tasks", name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	id := strings.TrimPrefix(name, "TASK-")
	if i := strings.Index(id, "-"); i > 0 {
		id = id[:i]
	}
	content := "---\nid: \"" + id + "\"\ntitle: Test\nproject: 001-test\nproject_id: \"001\"\nassignee: gpt\n" + extra + "---\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func readyIDs(ready []ReadyTask) []string {
	ids := make([]string, 0, len(ready))
	for _, rt := range ready {
		ids = append(ids, rt.ID)
	}
	return ids
}

func TestIndexScanCachesAndRefreshes(t *testing.T) {
	dir := t.TempDir()
	idx := NewIndex()
	writeIdxTask(t, dir, "TASK-001-a.md", "status: ready\n")
	writeIdxTask(t, dir, "TASK-002-b.md", "status: ready\n")

	ready, err := idx.Scan(dir)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(ready) != 2 {
		t.Fatalf("want 2 ready, got %d", len(ready))
	}

	// Blocking one task must be visible on the next scan (mtime changed).
	// A blocked task without blocked_phase is auto-unblockable; use a real
	// phase failure to gate it.
	writeIdxTask(t, dir, "TASK-001-a.md", "status: blocked\nblocked_phase: round2\nphase_error_code: MODEL_FAILED\n")
	ready, err = idx.Scan(dir)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(ready) != 1 || ready[0].ID != "002" {
		t.Fatalf("want only TASK-002 after block, got %v", readyIDs(ready))
	}

	// Unchanged scan returns the same result from cache.
	ready, err = idx.Scan(dir)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(ready) != 1 || ready[0].ID != "002" {
		t.Fatalf("cache scan mismatch, got %v", readyIDs(ready))
	}
}

func TestIndexInvalidateForcesReread(t *testing.T) {
	dir := t.TempDir()
	idx := NewIndex()
	p := writeIdxTask(t, dir, "TASK-001-a.md", "status: ready\n")

	if ready, _ := idx.Scan(dir); len(ready) != 1 {
		t.Fatalf("want 1 ready, got %d", len(ready))
	}
	// Invalidate must drop the entry even when the file is unchanged.
	idx.Invalidate(p)
	if ready, _ := idx.Scan(dir); len(ready) != 1 {
		t.Fatalf("want 1 ready after invalidate, got %d", len(ready))
	}

	// Removed files must drop from results.
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	idx.Invalidate(p)
	if ready, _ := idx.Scan(dir); len(ready) != 0 {
		t.Fatalf("want 0 ready after remove, got %v", readyIDs(ready))
	}
}

func TestIndexDependencyUsesCache(t *testing.T) {
	dir := t.TempDir()
	idx := NewIndex()
	// A is phase-failure blocked (no blocked_phase) and depends on B; it is
	// auto-unblockable only while B is done.
	writeIdxTask(t, dir, "TASK-010-b.md", "status: done\n")
	writeIdxTask(t, dir, "TASK-001-a.md", "status: blocked\nblocked_by:\n  - TASK-010\n")

	ready, err := idx.Scan(dir)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(ready) != 1 || ready[0].ID != "001" {
		t.Fatalf("want TASK-001 auto-unblocked by done dependency, got %v", readyIDs(ready))
	}

	// Upstream flips away from done: the dependency check must see the new
	// state through the index cache.
	writeIdxTask(t, dir, "TASK-010-b.md", "status: ready\n")
	idx.Invalidate(filepath.Join(dir, "Projects", "001-test", "Tasks", "TASK-010-b.md"))
	ready, err = idx.Scan(dir)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	for _, rt := range ready {
		if rt.ID == "001" {
			t.Fatal("TASK-001 must stay gated while upstream is not done")
		}
	}
}

func TestSchedulingPhasesGatedOnDependencies(t *testing.T) {
	dir := t.TempDir()
	idx := NewIndex()
	writeIdxTask(t, dir, "TASK-010-b.md", "status: done\n")
	pa := writeIdxTask(t, dir, "TASK-001-a.md", "status: ready\nblocked_by:\n  - TASK-010\n")

	// Upstream done → ready task is schedulable (the done upstream itself is
	// not ready — done is terminal unless pending_req).
	ready, err := idx.Scan(dir)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(ready) != 1 || ready[0].ID != "001" {
		t.Fatalf("want TASK-001 ready (upstream done), got %v", readyIDs(ready))
	}

	// Upstream flips away from done → ready task must NOT be dispatched into
	// refining/planning (regression: TASK-066 ran 15 no-op replans while its
	// upstreams were unmerged).
	upstream := filepath.Join(dir, "Projects", "001-test", "Tasks", "TASK-010-b.md")
	writeIdxTask(t, dir, "TASK-010-b.md", "status: implementing\n")
	idx.Invalidate(upstream)
	ready, err = idx.Scan(dir)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	for _, rt := range ready {
		if rt.ID == "001" {
			t.Fatal("ready task must stay gated while upstream is not done")
		}
	}
	// The gated task is recorded so the daemon can explain the holdback.
	if len(idx.GatedPaths) != 1 || idx.GatedPaths[0] != pa {
		t.Fatalf("GatedPaths = %v, want [%s]", idx.GatedPaths, pa)
	}

	// needs-grilling stays schedulable while upstream is pending (grilling
	// clarifies requirements and does not touch code).
	writeIdxTask(t, dir, "TASK-001-a.md", "status: needs-grilling\nblocked_by:\n  - TASK-010\n")
	idx.Invalidate(pa)
	ready, err = idx.Scan(dir)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	found := false
	for _, rt := range ready {
		if rt.ID == "001" {
			found = true
		}
	}
	if !found {
		t.Fatal("needs-grilling must stay schedulable while upstream pending")
	}
}

func TestIndexScanReadsHeadOnlyWhenFrontmatterClosesEarly(t *testing.T) {
	dir := t.TempDir()
	idx := NewIndex()
	body := strings.Repeat("body line\n", 3000) // ~27 KB of body
	p := filepath.Join(dir, "Projects", "001-test", "Tasks", "TASK-001-a.md")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	data := []byte("---\nid: \"001\"\ntitle: Test\nproject: 001-test\nassignee: gpt\nstatus: ready\n---\n" + body)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}

	ready, err := idx.Scan(dir)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(ready) != 1 {
		t.Fatalf("want 1 ready from head-read file, got %d", len(ready))
	}
}
