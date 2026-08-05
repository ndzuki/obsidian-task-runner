package task

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// benchVault creates n non-ready task documents in a temp vault.
func benchVault(b *testing.B, n int) string {
	b.Helper()
	dir := b.TempDir()
	tasksDir := filepath.Join(dir, "Projects", "001-bench", "Tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		b.Fatal(err)
	}
	for i := range n {
		id := fmt.Sprintf("%03d", i)
		content := fmt.Sprintf("---\nid: \"%s\"\ntitle: Bench %d\nproject: 001-bench\nproject_id: \"001\"\nassignee: gpt\nstatus: blocked\nblocked_phase: round2\nphase_error_code: MODEL_FAILED\nreq_doc: Projects/001-bench/Requirements/REQ-001.md\n---\nbody line %d\n", id, i, i)
		if err := os.WriteFile(filepath.Join(tasksDir, "TASK-"+id+"-bench.md"), []byte(content), 0o644); err != nil {
			b.Fatal(err)
		}
	}
	return dir
}

// BenchmarkIndexScanCold: fresh cache — every file is read+parsed.
func BenchmarkIndexScanCold(b *testing.B) {
	dir := benchVault(b, 1000)
	b.ResetTimer()
	for b.Loop() {
		idx := NewIndex()
		if _, err := idx.Scan(dir); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkIndexScanWarm: nothing changed since the last scan — stat only.
func BenchmarkIndexScanWarm(b *testing.B) {
	dir := benchVault(b, 1000)
	idx := NewIndex()
	if _, err := idx.Scan(dir); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for b.Loop() {
		if _, err := idx.Scan(dir); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkIndexScanOneChanged: a single write-back invalidated one entry —
// one re-read plus N stat checks (the steady-state headless-task pattern).
func BenchmarkIndexScanOneChanged(b *testing.B) {
	dir := benchVault(b, 1000)
	idx := NewIndex()
	if _, err := idx.Scan(dir); err != nil {
		b.Fatal(err)
	}
	changed := filepath.Join(dir, "Projects", "001-bench", "Tasks", "TASK-000-bench.md")
	b.ResetTimer()
	for b.Loop() {
		idx.Invalidate(changed)
		if _, err := idx.Scan(dir); err != nil {
			b.Fatal(err)
		}
	}
}
