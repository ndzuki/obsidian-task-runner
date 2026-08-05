package task

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
