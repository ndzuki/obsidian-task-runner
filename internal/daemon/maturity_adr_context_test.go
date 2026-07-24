package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndzuki/obsidian-task-runner/internal/task"
	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

func TestMaturityGateADRContextFlow(t *testing.T) {
	vault := t.TempDir()

	projDir := filepath.Join(vault, "Projects", "001-demo")
	reqDir := filepath.Join(projDir, "Requirements")
	tasksDir := filepath.Join(projDir, "Tasks")
	adrDir := filepath.Join(projDir, "Notes", "adr")
	for _, d := range []string{reqDir, tasksDir, adrDir} {
		os.MkdirAll(d, 0755)
	}

	// ADR-001: PostgreSQL is the sole business database
	os.WriteFile(filepath.Join(adrDir, "ADR-001-postgresql-sole-db.md"), []byte(`---
adr_id: "001"
title: "Use PostgreSQL as the sole business database"
status: accepted
---
# ADR-001: PostgreSQL as sole business database
## Decision
PostgreSQL is the only business database. No other storage engine.
`), 0644)

	// CONTEXT.md
	os.MkdirAll(filepath.Join(projDir, "Notes"), 0755)
	os.WriteFile(filepath.Join(projDir, "Notes", "CONTEXT.md"), []byte(`---
project: "001-demo"
---
## Language
**Orchestrator**: 调度引擎，管理 ReleaseBundle 生命周期。
**CleanupService**: GC 服务，使用 PostgreSQL advisory lock。
`), 0644)

	// REQ that introduces Redis → conflicts with ADR-001
	os.WriteFile(filepath.Join(reqDir, "REQ-100-redis-cache.md"), []byte(`---
id: "100"
title: Add Redis Cache
---
# Add Redis Cache
## 目标
引入 Redis 作为 Orchestrator 查询缓存。
`), 0644)

	// TASK with maturity gate grill_context
	taskContent := `---
id: "100"
title: Add Redis Cache Layer
project: "001-demo"
project_id: "001"
assignee: gpt
req_doc: Projects/001-demo/Requirements/REQ-100-redis-cache.md
status: needs-grilling
plan_approved: false
grill_done: false
grill_resolution: ""
grill_context: "maturity=mostly_mature
Failed checks:
- ADR consistency: REQ adds Redis but ADR-001 states PostgreSQL is sole DB
CONTEXT.md terminology:
- Orchestrator: 调度引擎 (relevant: REQ targets orchestrator path)
- CleanupService: GC 服务，PostgreSQL advisory lock (relevant: cache during GC)
ADR context:
- ADR-001 (accepted): Use PostgreSQL as sole business database
Follow-up dimensions:
- Is Redis business data or transient cache?
- Should ADR-001 be amended?"
grill_prev_status: ""
priority: P2
---
# TASK-100
`
	taskPath := filepath.Join(tasksDir, "TASK-100-redis-cache.md")
	os.WriteFile(taskPath, []byte(taskContent), 0644)

	// Test 1: FindReadyTasks picks it up
	ready, err := task.FindReadyTasks(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(ready) != 1 {
		t.Fatalf("want 1, got %d", len(ready))
	}
	if ready[0].Status != "needs-grilling" {
		t.Errorf("status = %q, want needs-grilling", ready[0].Status)
	}

	// Test 2: grill_context preserved through FindReadyTasks
	gc := ready[0].GrillContext
	for _, want := range []string{
		"ADR consistency", "ADR-001", "sole business database",
		"Orchestrator", "CleanupService", "Follow-up dimensions",
	} {
		if !strings.Contains(gc, want) {
			t.Errorf("GrillContext missing %q", want)
		}
	}
	t.Logf("grill_context: %d bytes with ADR+CONTEXT sections", len(gc))

	// Test 3: prepareBatch handles needs-grilling inline
	skillDir := filepath.Join(vault, "skills")
	os.MkdirAll(skillDir, 0755)
	runner := newTestRunner(skillDir, "/bin/true", filepath.Join(vault, "logs"), 1)
	pending := runner.prepareBatch(ready)
	if len(pending) != 0 {
		t.Fatalf("prepareBatch returned %d, want 0 (grilling stays inline)", len(pending))
	}

	// Test 4: ADR file is physically present and parseable
	adrPath := filepath.Join(adrDir, "ADR-001-postgresql-sole-db.md")
	data, err := os.ReadFile(adrPath)
	if err != nil {
		t.Fatal(err)
	}
	fm, err := yamlfrontmatter.Parse(data)
	if err != nil || fm == nil {
		t.Fatal("ADR parse failed")
	}
	if fm.Status != "accepted" {
		t.Errorf("ADR status = %q, want accepted", fm.Status)
	}

	// Test 5: grill_context survives round-trip (not cleared by daemon)
	taskData, err := os.ReadFile(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	taskFM, err := yamlfrontmatter.Parse(taskData)
	if err != nil || taskFM == nil {
		t.Fatal("task parse failed")
	}
	if taskFM.GrillContext == "" {
		t.Fatal("GrillContext was cleared — user would get empty grilling tab!")
	}
	t.Logf("grill_context survived (%d bytes)", len(taskFM.GrillContext))
}
