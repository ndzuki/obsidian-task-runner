package task

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

func TestCompactPlanHistory(t *testing.T) {
	dir := t.TempDir()
	taskPath := filepath.Join(dir, "TASK-001.md")
	content := `---
id: "001"
status: plan-review
---

## 需求摘要

test

## 实现计划

### v1 · 2026-07-01
#### Step 1: old
| 维度 | 内容 |
|------|------|
| 目标 | x |

### v2 · 2026-07-02
#### Step 1: mid
| 维度 | 内容 |
|------|------|
| 目标 | y |

### v3 · 2026-07-03
#### Step 1: new
| 维度 | 内容 |
|------|------|
| 目标 | z |

## 验收记录

ok
`
	if err := os.WriteFile(taskPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	compacted, err := CompactPlanHistory(taskPath, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !compacted {
		t.Fatal("expected compaction")
	}
	data, _ := os.ReadFile(taskPath)
	s := string(data)
	if !strings.Contains(s, "折叠：v1–v1") {
		t.Fatalf("missing folded marker: %s", s)
	}
	if strings.Contains(s, "### v1 · 2026-07-01") {
		t.Fatal("v1 block must be folded away")
	}
	if !strings.Contains(s, "### v2 · 2026-07-02") || !strings.Contains(s, "### v3 · 2026-07-03") {
		t.Fatal("kept versions must survive verbatim")
	}
	if !strings.Contains(s, "## 验收记录") {
		t.Fatal("section after 实现计划 must be preserved")
	}
	// Frontmatter intact.
	if !strings.Contains(s, `id: "001"`) {
		t.Fatal("frontmatter must be preserved")
	}
}

// TestCompactPrototypeHistory guards the prototype-section folding: Round 1
// appends a full write-up per replan, and gated tasks (AC-066-17 style)
// accumulate 8+ copies. Old copies fold to a marker; the newest survives
// verbatim.
func TestCompactPrototypeHistory(t *testing.T) {
	dir := t.TempDir()
	taskPath := filepath.Join(dir, "TASK-003.md")
	content := `---
id: "003"
---

## 需求摘要

test

## Prototype 建议（v1，旧）

旧原型内容 A

## Prototype 建议（v2，旧）

旧原型内容 B

## Prototype 建议（v3，最新）

新原型内容 C

## 实现计划

### v1 · 2026-07-01
plan
`
	if err := os.WriteFile(taskPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	compacted, err := CompactPrototypeHistory(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	if !compacted {
		t.Fatal("expected prototype compaction")
	}
	data, _ := os.ReadFile(taskPath)
	s := string(data)
	if strings.Contains(s, "旧原型内容 A") || strings.Contains(s, "旧原型内容 B") {
		t.Fatal("old prototype blocks must be folded away")
	}
	if !strings.Contains(s, "新原型内容 C") {
		t.Fatal("newest prototype block must survive verbatim")
	}
	if !strings.Contains(s, "折叠：旧 Prototype 建议") {
		t.Fatalf("missing folded marker: %s", s)
	}
	if !strings.Contains(s, "## 实现计划") {
		t.Fatal("section after prototypes must be preserved")
	}
	if !strings.Contains(s, `id: "003"`) {
		t.Fatal("frontmatter must be preserved")
	}
}

func TestCompactPrototypeHistorySingle(t *testing.T) {
	dir := t.TempDir()
	taskPath := filepath.Join(dir, "TASK-004.md")
	content := "---\nid: \"004\"\n---\n\n## Prototype 建议\n\n唯一原型\n"
	if err := os.WriteFile(taskPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	compacted, err := CompactPrototypeHistory(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	if compacted {
		t.Fatal("no compaction expected with a single prototype section")
	}
}

func TestCompactPlanHistoryBelowThreshold(t *testing.T) {
	dir := t.TempDir()
	taskPath := filepath.Join(dir, "TASK-002.md")
	content := "---\nid: \"002\"\n---\n\n## 实现计划\n\n### v1 · 2026-07-01\nx\n"
	if err := os.WriteFile(taskPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	compacted, err := CompactPlanHistory(taskPath, 3)
	if err != nil {
		t.Fatal(err)
	}
	if compacted {
		t.Fatal("no compaction expected below threshold")
	}
}

// TestCompactPlanContentPure guards the extracted pure transform: folding
// logic is now unit-testable without file I/O and must preserve the exact
// same output as before the AtomicReadModifyWrite refactor.
func TestCompactPlanContentPure(t *testing.T) {
	content := `---
id: "001"
status: plan-review
---

## 实现计划

### v1 · 2026-07-01
#### Step 1: old

### v2 · 2026-07-02
#### Step 1: mid

### v3 · 2026-07-03
#### Step 1: new

## 验收记录

ok
`
	next, did, err := compactPlanContent(content, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !did {
		t.Fatal("expected compaction")
	}
	if !strings.Contains(next, "折叠：v1–v1") {
		t.Fatalf("missing folded marker in: %s", next)
	}
	if !strings.Contains(next, "### v3") {
		t.Fatalf("newest plan version must be kept verbatim: %s", next)
	}
	if strings.Contains(next, "### v1 · 2026-07-01") {
		t.Fatalf("old plan version must be folded away: %s", next)
	}
	// 验收记录 must be preserved.
	if !strings.Contains(next, "## 验收记录\n\nok") {
		t.Fatalf("section after plan lost: %s", next)
	}
}

// TestCompactPrototypeContentPure guards the prototype folding pure transform.
func TestCompactPrototypeContentPure(t *testing.T) {
	content := `---
id: "001"
status: implementing
---

## 实现计划

### v1
plan

## Prototype 建议

old prototype

## Prototype 建议（v2，…）

new prototype

## 验收记录

ok
`
	next, did, err := compactPrototypeContent(content)
	if err != nil {
		t.Fatal(err)
	}
	if !did {
		t.Fatal("expected prototype compaction")
	}
	if !strings.Contains(next, "折叠：旧 Prototype 建议") {
		t.Fatalf("missing prototype folded marker: %s", next)
	}
	if !strings.Contains(next, "new prototype") {
		t.Fatalf("newest prototype must be kept: %s", next)
	}
	if strings.Contains(next, "old prototype") {
		t.Fatalf("old prototype must be folded: %s", next)
	}
	if !strings.Contains(next, "## 验收记录\n\nok") {
		t.Fatalf("section after prototype lost: %s", next)
	}
}

// TestCompactDoesNotLoseConcurrentUpdate is the P0-3 regression guard: a
// frontmatter state write-back (Update) racing a history compaction must not
// be lost. Both share the task-path flock, so whichever runs second observes
// the first's result. This test hammers both concurrently and asserts the
// final file contains both the compaction marker AND the concurrent update.
func TestCompactDoesNotLoseConcurrentUpdate(t *testing.T) {
	dir := t.TempDir()
	taskPath := filepath.Join(dir, "TASK-001.md")
	content := `---
id: "001"
status: planning
---

## 实现计划

### v1 · 2026-07-01
old

### v2 · 2026-07-02
mid

### v3 · 2026-07-03
new

## 验收记录

ok
`
	if err := os.WriteFile(taskPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	const workers = 8
	var wg sync.WaitGroup
	errs := make(chan error, workers*2)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := CompactPlanHistory(taskPath, 2); err != nil {
				errs <- fmt.Errorf("compact: %w", err)
			}
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			// A concurrent daemon-style frontmatter state write.
			if err := yamlfrontmatter.Update(taskPath, map[string]interface{}{"status": "implementing", "plan_approved": true}); err != nil {
				errs <- fmt.Errorf("update: %w", err)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}

	data, err := os.ReadFile(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if !strings.Contains(s, "折叠：v1–v1") {
		t.Fatalf("compaction marker missing after concurrent writes:\n%s", s)
	}
	if !strings.Contains(s, "status: implementing") || !strings.Contains(s, "plan_approved: true") {
		t.Fatalf("concurrent frontmatter update lost:\n%s", s)
	}
}
