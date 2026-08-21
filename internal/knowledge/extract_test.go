package knowledge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

func writeTaskFile(t *testing.T, vault, project, adrWritten string) string {
	t.Helper()
	tasksDir := filepath.Join(vault, "Projects", project, "Tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	taskPath := filepath.Join(tasksDir, "TASK-001-test.md")
	content := "---\nid: \"001\"\ntitle: Test task\nproject: " + project + "\nassignee: gpt\nstatus: done\nadr_written: [" + adrWritten + "]\n---\n"
	if err := os.WriteFile(taskPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return taskPath
}

// writeRefIndexFile seeds a References/ document so classifyADR (index-driven)
// can route the ADR under test.
func writeRefIndexFile(t *testing.T, vault, rel, topics string) {
	t.Helper()
	path := filepath.Join(vault, "References", rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\ntopics: [" + topics + "]\nlevel: reference\nupdated: \"2026-08-05\"\nsource: \"local\"\nverified: false\n---\n# Doc\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeProjectADRFile(t *testing.T, vault, project, name, title, decision string) {
	t.Helper()
	adrDir := filepath.Join(vault, "Projects", project, "Notes", "adr")
	if err := os.MkdirAll(adrDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nadr_id: \"012\"\ntitle: " + title + "\nstatus: accepted\n---\n\n## Decision\n\n" + decision + "\n"
	if err := os.WriteFile(filepath.Join(adrDir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestExtractTaskKnowledgeIdempotent(t *testing.T) {
	vault := t.TempDir()
	project := "bench-project"
	writeRefIndexFile(t, vault, "core/go/connect-rpc.md", "connect,grpc")
	writeProjectADRFile(t, vault, project, "ADR-012-connect.md", "Connect Protocol", "使用 Connect 统一协议。")

	taskPath := writeTaskFile(t, vault, project, "ADR-012-connect")

	result, err := ExtractTaskKnowledge(vault, project, taskPath)
	if err != nil {
		t.Fatalf("ExtractTaskKnowledge: %v", err)
	}
	if result.UpdatedRefs != 1 {
		t.Fatalf("want 1 updated ref, got new=%d updated=%d errors=%v", result.NewRefs, result.UpdatedRefs, result.Errors)
	}
	// The knowledge file exists and is classified under connect.
	if _, err := os.Stat(filepath.Join(vault, "References", "core", "go", "connect-rpc.md")); err != nil {
		t.Fatalf("extracted knowledge file missing: %v", err)
	}

	// Second run must be a no-op thanks to the knowledge_extracted marker.
	result2, err := ExtractTaskKnowledge(vault, project, taskPath)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if result2.NewRefs != 0 || result2.UpdatedRefs != 0 || len(result2.Touched) != 0 {
		t.Fatalf("second run must be a no-op, got new=%d updated=%d touched=%v", result2.NewRefs, result2.UpdatedRefs, result2.Touched)
	}
	data, err := os.ReadFile(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	fm, err := yamlfrontmatter.Parse(data)
	if err != nil || fm == nil {
		t.Fatalf("reparse task: %v", err)
	}
	if !fm.KnowledgeExtracted {
		t.Fatal("knowledge_extracted marker missing after extraction")
	}
}

// TestExtractTaskKnowledgePartialFailureKeepsMarker 钉住失败语义：任何提取
// 错误（此处：项目 ADR 目录缺失导致扫描出错）不得写入 knowledge_extracted
// marker——daemon 补救扫描下一轮重试，而不是把失败运行当作已完成。
func TestExtractTaskKnowledgePartialFailureKeepsMarker(t *testing.T) {
	vault := t.TempDir()
	project := "bench-project"
	// 任务声明了 ADR-012 但项目没有 Notes/adr 目录——scanADRs 失败，
	// 运行记录错误。
	taskPath := writeTaskFile(t, vault, project, `"ADR-012"`)

	result, err := ExtractTaskKnowledge(vault, project, taskPath)
	if err != nil {
		t.Fatalf("ExtractTaskKnowledge: %v", err)
	}
	if len(result.Errors) == 0 {
		t.Fatal("want extraction errors from missing ADR directory")
	}
	data, _ := os.ReadFile(taskPath)
	fm, _ := yamlfrontmatter.Parse(data)
	if fm.KnowledgeExtracted {
		t.Fatal("failed extraction must NOT write the knowledge_extracted marker")
	}
}

func TestExtractTaskKnowledgeShortADRRef(t *testing.T) {
	vault := t.TempDir()
	project := "bench-project"
	writeRefIndexFile(t, vault, "core/go/connect-rpc.md", "connect,grpc")
	writeProjectADRFile(t, vault, project, "ADR-012-connect.md", "Connect Protocol", "使用 Connect 统一协议。")

	// adr_written uses the short form "ADR-012"; the file id is ADR-012-connect.
	taskPath := writeTaskFile(t, vault, project, "ADR-012")

	result, err := ExtractTaskKnowledge(vault, project, taskPath)
	if err != nil {
		t.Fatalf("ExtractTaskKnowledge: %v", err)
	}
	if result.UpdatedRefs != 1 {
		t.Fatalf("short ADR ref must extract 1 updated ref, got %d", result.UpdatedRefs)
	}
}

func TestExtractTaskKnowledgeUnclassifiedAutoArchived(t *testing.T) {
	vault := t.TempDir()
	project := "bench-project"
	writeRefIndexFile(t, vault, "core/go/connect-rpc.md", "connect,grpc")
	// An ADR that exists but matches no knowledge topic.
	writeProjectADRFile(t, vault, project, "ADR-013-business.md", "Business Rule", "这是一条纯业务决策，不涉及通用技术模式。")

	taskPath := writeTaskFile(t, vault, project, "ADR-013-business")

	result, err := ExtractTaskKnowledge(vault, project, taskPath)
	if err != nil {
		t.Fatalf("ExtractTaskKnowledge: %v", err)
	}
	if result.NewRefs != 0 || result.UpdatedRefs != 0 {
		t.Fatalf("unmatched ADR must extract nothing, got new=%d updated=%d", result.NewRefs, result.UpdatedRefs)
	}
	// Unclassified knowledge is auto-archived, never dropped or deferred to
	// manual review.
	if len(result.Unclassified) != 1 || result.Unclassified[0] != "ADR-013-business" {
		t.Fatalf("want ADR-013-business unclassified, got %v", result.Unclassified)
	}
	stored := filepath.Join(vault, "References", "uncategorized", "ADR-013-business.md")
	if _, err := os.Stat(stored); err != nil {
		t.Fatalf("unclassified ADR must be archived under uncategorized/: %v", err)
	}
	// Still marked extracted so later merges do not rescan.
	data, _ := os.ReadFile(taskPath)
	fm, _ := yamlfrontmatter.Parse(data)
	if !fm.KnowledgeExtracted {
		t.Fatal("task without matching ADR must still be marked extracted")
	}
}

func TestReclassifyUncategorized(t *testing.T) {
	vault := t.TempDir()
	project := "bench-project"
	// Archived doc with no matching topic yet.
	writeRefIndexFile(t, vault, "core/go/connect-rpc.md", "connect,grpc")
	adrDir := filepath.Join(vault, "Projects", project, "Notes", "adr")
	if err := os.MkdirAll(adrDir, 0o755); err != nil {
		t.Fatal(err)
	}
	archived := filepath.Join(vault, "References", "uncategorized", "ADR-020-helm.md")
	if err := os.MkdirAll(filepath.Dir(archived), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\ntopics: [uncategorized, adr]\nlevel: intermediate\nupdated: \"2026-08-05\"\nsource: \"local\"\nverified: false\n---\n# Helm 部署决策\n\n> 来源：[ADR-020](Projects/" + project + "/Notes/adr/ADR-020.md)\n\n## 决策摘要\n\n部署统一通过 Helm Chart 模板化。\n"
	if err := os.WriteFile(archived, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	// Before the KB has a helm topic, nothing reclassifies.
	if moved, err := ReclassifyUncategorized(vault); err != nil || moved != 0 {
		t.Fatalf("want 0 moved before topic exists, got %d err=%v", moved, err)
	}
	// Add the matching topic, then the archived doc migrates into it.
	writeRefIndexFile(t, vault, "extended/helm/helm-patterns.md", "helm")
	InvalidateRefIndex(filepath.Join(vault, "References"))
	moved, err := ReclassifyUncategorized(vault)
	if err != nil {
		t.Fatalf("ReclassifyUncategorized: %v", err)
	}
	if moved != 1 {
		t.Fatalf("want 1 moved, got %d", moved)
	}
	if _, err := os.Stat(archived); !os.IsNotExist(err) {
		t.Fatal("archived doc must be removed after migration")
	}
	target := filepath.Join(vault, "References", "extended", "helm", "helm-patterns.md")
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("target doc missing: %v", err)
	}
	if !strings.Contains(string(data), "ADR-020") {
		t.Fatal("target doc must contain the migrated ADR provenance")
	}
}

func TestExtractTaskKnowledgePitfalls(t *testing.T) {
	vault := t.TempDir()
	project := "bench-project"
	refsDir := filepath.Join(vault, "References", "extended", "tools")
	if err := os.MkdirAll(refsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(refsDir, "kulala-http-client.md")
	seed := "---\ntopics: [kulala, http-client]\nlevel: reference\nupdated: \"2026-08-06\"\nsource: \"local\"\nverified: false\naliases: []\n---\n# Kulala\n\n> summary\n\n## 要点\n- a\n\n## 更新记录\n- 2026-08-06 init\n"
	if err := os.WriteFile(target, []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}

	taskDir := filepath.Join(vault, "Projects", project, "Tasks")
	if err := os.MkdirAll(taskDir, 0o755); err != nil {
		t.Fatal(err)
	}
	taskPath := filepath.Join(taskDir, "TASK-007-test.md")
	taskContent := `---
id: "007"
title: Test pitfall task
project: ` + project + `
assignee: gpt
status: done
adr_written: []
---

## 实现记录

- AC-1 done

## 踩坑记录

### 2026-08-07: Kulala pre-request 脚本不执行

- 现象: pre-request script（小于号 ./pre.js）引用后请求直接失败，无 Authorization 头
- 失败方案: 把密钥写进 http-client.env.json 公共文件
- 根因: 公共 env 文件会被提交，且脚本路径相对当前文件解析
- 成功方案: 改用 pre-request script + request-scoped variable
- 相关文档: extended/tools/kulala-http-client.md
`
	if err := os.WriteFile(taskPath, []byte(taskContent), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := ExtractTaskKnowledge(vault, project, taskPath)
	if err != nil {
		t.Fatalf("ExtractTaskKnowledge: %v", err)
	}
	if len(result.Touched) == 0 {
		t.Fatalf("pitfall must touch a knowledge doc, got %+v", result)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	for _, want := range []string{
		"踩坑实践（TASK-007）",
		"把密钥写进 http-client.env.json 公共文件",
		"改用 pre-request script + request-scoped variable",
		"2026-08-07",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("target doc missing %q:\n%s", want, content)
		}
	}

	// Second run must be a no-op (knowledge_extracted marker).
	before := content
	result2, err := ExtractTaskKnowledge(vault, project, taskPath)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if len(result2.Touched) != 0 {
		t.Fatalf("second run must not re-extract, got %+v", result2)
	}
	after, _ := os.ReadFile(target)
	if string(after) != before {
		t.Fatal("pitfall note duplicated on re-extraction")
	}
}

func TestParsePitfalls(t *testing.T) {
	body := `## 实现记录
- x

## 踩坑记录
<!-- 注释行不解析 -->

### 2026-08-07: 方案 A 失败

- 现象: 500
- 失败方案: A
- 根因: 缺参数
- 成功方案: B
- 相关文档: core/go/connect-rpc.md

### 无日期条目

- 现象: 超时
- 成功方案: 重试
`
	pits := parsePitfalls(body)
	if len(pits) != 2 {
		t.Fatalf("parsePitfalls = %d entries, want 2", len(pits))
	}
	if pits[0].Time != "2026-08-07" || pits[0].Failed != "A" || pits[0].Success != "B" || pits[0].Refs != "core/go/connect-rpc.md" {
		t.Fatalf("entry 0 = %+v", pits[0])
	}
	if pits[1].Time != "" || pits[1].Failed != "" {
		t.Fatalf("entry 1 = %+v", pits[1])
	}
	if got := parsePitfalls("## 实现记录\n- no pitfalls here\n"); got != nil {
		t.Fatalf("expected nil, got %+v", got)
	}
}

func TestTailLogTail(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "phase.log")
	if err := os.WriteFile(logPath, []byte("line1\nline2\nerror: connection reset\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := tailLogTail(logPath, 4096); got != "error: connection reset" {
		t.Fatalf("tail = %q, want last line", got)
	}
	if got := tailLogTail("", 4096); got != "" {
		t.Fatalf("empty path must return empty, got %q", got)
	}
	if got := tailLogTail(filepath.Join(dir, "missing.log"), 4096); got != "" {
		t.Fatalf("missing log must return empty, got %q", got)
	}
	// Long line is collapsed and capped.
	long := strings.Repeat("x", 500) + " tail-marker"
	if err := os.WriteFile(logPath, []byte(long), 0o644); err != nil {
		t.Fatal(err)
	}
	got := tailLogTail(logPath, 4096)
	if !strings.Contains(got, "tail-marker") || len(got) > 205 {
		t.Fatalf("capped tail = %q (len %d)", got, len(got))
	}
}

func TestAbsorbKnowledgePitfall(t *testing.T) {
	vault := t.TempDir()
	refsDir := filepath.Join(vault, "References", "extended", "tools")
	if err := os.MkdirAll(refsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(refsDir, "kulala-http-client.md")
	seed := "---\ntopics: [kulala, http-client]\nlevel: reference\nupdated: \"2026-08-07\"\nsource: \"local\"\nverified: false\naliases: []\n---\n# Kulala\n\n> summary\n\n## 更新记录\n- init\n"
	if err := os.WriteFile(target, []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}

	lesson := `### 2026-08-07: 密钥写进公共 env 文件

- 现象: Authorization 泄漏风险
- 失败方案: 把密钥写进 http-client.env.json 公共文件
- 根因: 公共文件会被提交
- 成功方案: 用 pre-request script 读取
- 相关文档: extended/tools/kulala-http-client.md
`
	res, err := AbsorbKnowledge(vault, "daily", lesson, false)
	if err != nil {
		t.Fatalf("AbsorbKnowledge: %v", err)
	}
	if res.Appended != 1 || res.Duplicates != 0 || len(res.Archived) != 0 {
		t.Fatalf("absorb = %+v, want 1 appended", res)
	}

	// Same lesson again → duplicate (note body unchanged), and the repeat
	// encounter bumps the document's heat (hits 0 → 1).
	res2, err := AbsorbKnowledge(vault, "daily", lesson, false)
	if err != nil {
		t.Fatalf("second absorb: %v", err)
	}
	if res2.Appended != 0 || res2.Duplicates != 1 {
		t.Fatalf("dedup absorb = %+v, want 1 duplicate", res2)
	}
	after, _ := os.ReadFile(target)
	if !strings.Contains(string(after), "hits: 1") {
		t.Fatal("duplicate absorb must bump heat (hits: 1)")
	}
	if strings.Count(string(after), "踩坑实践") != 1 {
		t.Fatal("duplicate absorb must not duplicate the note body")
	}
}

func TestAbsorbKnowledgeUnclassifiedArchived(t *testing.T) {
	vault := t.TempDir()
	refsDir := filepath.Join(vault, "References", "core")
	if err := os.MkdirAll(refsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	seed := "---\ntopics: [connect]\nlevel: reference\nupdated: \"2026-08-07\"\nsource: \"local\"\nverified: false\naliases: []\n---\n# Connect\n\n> summary\n"
	if err := os.WriteFile(filepath.Join(refsDir, "connect-rpc.md"), []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}
	lesson := `### 2026-08-07: 纯业务规则试错

- 现象: 规则计算错误
- 失败方案: 方案 A
- 根因: 业务语义未对齐
- 成功方案: 方案 B
`
	res, err := AbsorbKnowledge(vault, "daily", lesson, false)
	if err != nil {
		t.Fatalf("AbsorbKnowledge: %v", err)
	}
	if res.Appended != 0 || len(res.Archived) != 1 {
		t.Fatalf("absorb = %+v, want 1 archived", res)
	}
	entries, _ := filepath.Glob(filepath.Join(vault, "References", "uncategorized", "TASK-interactive-pitfall-*.md"))
	if len(entries) != 1 {
		t.Fatalf("archived files = %v, want 1", entries)
	}
}

func TestAbsorbKnowledgeSummary(t *testing.T) {
	vault := t.TempDir()
	refsDir := filepath.Join(vault, "References", "extended", "tools")
	if err := os.MkdirAll(refsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(refsDir, "kulala-http-client.md")
	seed := "---\ntopics: [kulala, http-client]\nlevel: reference\nupdated: \"2026-08-07\"\nsource: \"local\"\nverified: false\naliases: []\n---\n# Kulala\n\n> summary\n\n## 更新记录\n- init\n"
	if err := os.WriteFile(target, []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}

	summary := "Kulala 集合用 @no-log 防止 Authorization 头落盘"
	res, err := AbsorbKnowledge(vault, "daily", summary, true)
	if err != nil {
		t.Fatalf("AbsorbKnowledge summary: %v", err)
	}
	if res.Appended != 1 {
		t.Fatalf("summary absorb = %+v, want 1 appended", res)
	}
	data, _ := os.ReadFile(target)
	if !strings.Contains(string(data), "经验总结（interactive）") {
		t.Fatal("summary note missing from target doc")
	}

	// Same title again → duplicate.
	res2, err := AbsorbKnowledge(vault, "daily", summary, true)
	if err != nil {
		t.Fatalf("second summary: %v", err)
	}
	if res2.Duplicates != 1 {
		t.Fatalf("summary dedup = %+v, want 1 duplicate", res2)
	}
}

func TestAppendPitfallNoteDedup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "doc.md")
	seed := "---\ntopics: [x]\nlevel: reference\nupdated: \"2026-08-07\"\nsource: \"local\"\nverified: false\naliases: []\n---\n# Doc\n\n> s\n"
	if err := os.WriteFile(path, []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}
	p := taskPitfall{Time: "2026-08-07", Title: "方案 X 失败", Symptom: "500", Failed: "方案 X", RootCause: "缺参数", Success: "方案 Y"}
	ok, err := appendPitfallNote(path, p, "p", "1")
	if err != nil || !ok {
		t.Fatalf("first append ok=%v err=%v", ok, err)
	}
	// Same failed approach, different title → duplicate.
	dup := p
	dup.Title = "另一个标题"
	ok, err = appendPitfallNote(path, dup, "p", "2")
	if err != nil {
		t.Fatalf("dup append: %v", err)
	}
	if ok {
		t.Fatal("same failed approach must be deduplicated")
	}
	// Same title, different failed approach → duplicate too.
	dup2 := p
	dup2.Failed = "方案 Z"
	ok, err = appendPitfallNote(path, dup2, "p", "3")
	if err != nil {
		t.Fatalf("dup2 append: %v", err)
	}
	if ok {
		t.Fatal("same title must be deduplicated")
	}
}

func TestIncrementHitsAndRankBoost(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	refsDir := filepath.Join(vault, "References", "extended", "tools")
	if err := os.MkdirAll(refsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name string) {
		content := "---\ntopics: [probe, hot]\nlevel: reference\nupdated: \"2026-08-07\"\nsource: \"local\"\nverified: false\naliases: []\nhits: 0\n---\n# " + name + "\n\n> summary\n"
		if err := os.WriteFile(filepath.Join(refsDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("cold.md")
	write("hot.md")

	n, err := IncrementHits(vault, []string{"extended/tools/hot.md", "extended/tools/missing.md"})
	if err != nil {
		t.Fatalf("IncrementHits: %v", err)
	}
	if n != 1 {
		t.Fatalf("bumped = %d, want 1 (missing skipped)", n)
	}
	if _, err := IncrementHits(vault, []string{"extended/tools/hot.md"}); err != nil {
		t.Fatal(err)
	}
	// The bump must preserve the KB v2 frontmatter contract: updated stays
	// YYYY-MM-DD and the document still validates.
	hotPath := filepath.Join(refsDir, "hot.md")
	if err := ValidateRefFile(hotPath); err != nil {
		t.Fatalf("hits bump broke KB v2 schema: %v", err)
	}
	after, _ := os.ReadFile(hotPath)
	if !strings.Contains(string(after), "updated: \"2026-08-07\"") {
		t.Fatal("hits bump must not rewrite the updated field")
	}

	dbPath := filepath.Join(dir, "kb.sqlite")
	if _, err := SyncKnowledgeDB(vault, dbPath, nil); err != nil {
		t.Fatal(err)
	}
	hits, err := SearchKnowledgeDB(dbPath, "probe hot", 2, true, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("no hits")
	}
	// Both docs match the topics equally; the hot doc (hits=2) must rank first.
	if hits[0].Path != "extended/tools/hot.md" {
		t.Fatalf("heat boost failed, top = %+v", hits)
	}
}

func TestPromoteToCore(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	refsDir := filepath.Join(vault, "References", "extended", "tools")
	if err := os.MkdirAll(refsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\ntopics: [probe]\nlevel: reference\nupdated: \"2026-08-07\"\nsource: \"local\"\nverified: false\naliases: []\nhits: 4\n---\n# Hot Doc\n\n> summary\n"
	if err := os.WriteFile(filepath.Join(refsDir, "hot.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cold := strings.Replace(content, "hits: 4", "hits: 1", 1)
	if err := os.WriteFile(filepath.Join(refsDir, "cold.md"), []byte(cold), 0o644); err != nil {
		t.Fatal(err)
	}

	moved, err := PromoteToCore(vault, 3)
	if err != nil {
		t.Fatalf("PromoteToCore: %v", err)
	}
	if len(moved) != 1 {
		t.Fatalf("moved = %v, want 1", moved)
	}
	if _, err := os.Stat(filepath.Join(refsDir, "hot.md")); !os.IsNotExist(err) {
		t.Fatal("source must be removed after promotion")
	}
	if _, err := os.Stat(filepath.Join(vault, "References", "core", "tools", "hot.md")); err != nil {
		t.Fatalf("core target missing: %v", err)
	}
	movedData, _ := os.ReadFile(filepath.Join(vault, "References", "core", "tools", "hot.md"))
	if !strings.Contains(string(movedData), "自动升级至 core/") {
		t.Fatal("promotion must leave a migration trail in 更新记录")
	}
	// Cold doc stays in extended/.
	if _, err := os.Stat(filepath.Join(refsDir, "cold.md")); err != nil {
		t.Fatal("cold doc must stay in extended/")
	}
}

func TestExtractTaskKnowledgeDanglingADRRef(t *testing.T) {
	vault := t.TempDir()
	project := "bench-project"
	writeRefIndexFile(t, vault, "core/go/connect-rpc.md", "connect,grpc")
	writeProjectADRFile(t, vault, project, "ADR-012-connect.md", "Connect Protocol", "使用 Connect 统一协议。")

	// adr_written references an ADR that does not exist in the project: a
	// dangling reference, not unclassified knowledge — nothing is archived.
	taskPath := writeTaskFile(t, vault, project, "ADR-999-nonexistent")

	result, err := ExtractTaskKnowledge(vault, project, taskPath)
	if err != nil {
		t.Fatalf("ExtractTaskKnowledge: %v", err)
	}
	if result.NewRefs != 0 || result.UpdatedRefs != 0 || len(result.Unclassified) != 0 {
		t.Fatalf("dangling ref must be a no-op, got %+v", result)
	}
	if _, err := os.Stat(filepath.Join(vault, "References", "uncategorized")); err == nil {
		t.Fatal("dangling ref must not create uncategorized archive")
	}
}

// ── adr_written 真实形态（逗号串 + Notes/adr/ 前缀）───────────────
//
// Round 1 把 adr_written 写成单个逗号分隔字符串且每项带 Notes/adr/ 前缀：
// `Notes/adr/ADR-001-….md,Notes/adr/ADR-002-….md`。旧 collectADRIDs 把整串
// 当一个 id，matchesADRID 永不命中 → 扫描到 N 个 ADR 却提取 0 条
// （daemon 日志特征 `adrs=6 new=0 updated=0`）。这些测试钉住归一化语义。

func TestCollectADRIDsSplitsCSVAndStripsPaths(t *testing.T) {
	got := collectADRIDs("Notes/adr/ADR-001-a.md, Notes/adr/ADR-002-b.md")
	want := []string{"ADR-001-a", "ADR-002-b"}
	if len(got) != len(want) {
		t.Fatalf("collectADRIDs(csv) = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("collectADRIDs(csv) = %v, want %v", got, want)
		}
	}

	if got := collectADRIDs([]any{"Notes/adr/ADR-003-c.md", "ADR-004", "", "Notes/adr/ADR-005-d"}); len(got) != 3 {
		t.Fatalf("collectADRIDs(list) = %v, want 3 ids", got)
	}
	if got := collectADRIDs(nil); got != nil {
		t.Fatalf("collectADRIDs(nil) = %v, want nil", got)
	}
	if got := collectADRIDs(""); got != nil {
		t.Fatalf("collectADRIDs(\"\") = %v, want nil", got)
	}
}

func TestMatchesADRIDFullPathAndShortRefs(t *testing.T) {
	id := "ADR-001-go-containerregistry-operator"
	for _, ref := range []string{
		"Notes/adr/ADR-001-go-containerregistry-operator.md",
		"ADR-001-go-containerregistry-operator.md",
		"ADR-001",
		"ADR-001-go-containerregistry-operator",
	} {
		if !matchesADRID(id, []string{ref}) {
			t.Errorf("matchesADRID(%q, %q) = false, want true", id, ref)
		}
	}
	if matchesADRID(id, []string{"ADR-002"}) {
		t.Error("matchesADRID must reject a different ADR id")
	}
}

func TestExtractTaskKnowledgeFullPathADRRefs(t *testing.T) {
	vault := t.TempDir()
	project := "bench-project"
	writeRefIndexFile(t, vault, "core/go/connect-rpc.md", "connect,grpc")
	writeProjectADRFile(t, vault, project, "ADR-012-connect.md", "Connect Protocol", "使用 Connect 统一协议。")
	writeProjectADRFile(t, vault, project, "ADR-013-grpc.md", "gRPC Deprecation", "gRPC 服务统一迁移 Connect。")

	tasksDir := filepath.Join(vault, "Projects", project, "Tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// 真实形态：单个逗号串，带 Notes/adr/ 路径前缀与 .md 后缀。
	taskPath := filepath.Join(tasksDir, "TASK-001-test.md")
	content := "---\nid: \"001\"\ntitle: Test task\nproject: " + project + "\nassignee: gpt\nstatus: done\nadr_written: Notes/adr/ADR-012-connect.md,Notes/adr/ADR-013-grpc.md\nknowledge_extracted: false\n---\n"
	if err := os.WriteFile(taskPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := ExtractTaskKnowledge(vault, project, taskPath)
	if err != nil {
		t.Fatalf("ExtractTaskKnowledge: %v", err)
	}
	if result.ADRCount != 2 {
		t.Fatalf("want 2 scanned ADRs, got %d", result.ADRCount)
	}
	if result.UpdatedRefs != 2 {
		t.Fatalf("both referenced ADRs must extract into the KB doc, got new=%d updated=%d errors=%v", result.NewRefs, result.UpdatedRefs, result.Errors)
	}
	data, err := os.ReadFile(filepath.Join(vault, "References", "core", "go", "connect-rpc.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ADR-012-connect", "ADR-013-grpc", "使用 Connect 统一协议"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("KB doc missing %q:\n%s", want, data)
		}
	}
	taskData, _ := os.ReadFile(taskPath)
	fm, _ := yamlfrontmatter.Parse(taskData)
	if fm == nil || !fm.KnowledgeExtracted {
		t.Fatal("successful extraction must set knowledge_extracted")
	}
}

// TestExtractTaskKnowledgePracticeNoteIdempotent 钉住重试安全：补救扫描在
// store 同步失败时把 marker 重置为 false 并重跑整条管道，实践条目必须去重，
// 不能让同一条 ADR 决策在知识文档里重复追加。
func TestExtractTaskKnowledgePracticeNoteIdempotent(t *testing.T) {
	vault := t.TempDir()
	project := "bench-project"
	writeRefIndexFile(t, vault, "core/go/connect-rpc.md", "connect,grpc")
	writeProjectADRFile(t, vault, project, "ADR-012-connect.md", "Connect Protocol", "使用 Connect 统一协议。")

	taskPath := writeTaskFile(t, vault, project, "ADR-012-connect")

	if _, err := ExtractTaskKnowledge(vault, project, taskPath); err != nil {
		t.Fatalf("first run: %v", err)
	}
	// Simulate the sync-failure recovery path: reset the marker and re-run.
	if err := yamlfrontmatter.Update(taskPath, map[string]interface{}{"knowledge_extracted": false}); err != nil {
		t.Fatal(err)
	}
	result, err := ExtractTaskKnowledge(vault, project, taskPath)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if result.UpdatedRefs != 0 {
		t.Fatalf("second run must not re-append the practice note, got %+v", result)
	}
	data, _ := os.ReadFile(filepath.Join(vault, "References", "core", "go", "connect-rpc.md"))
	if n := strings.Count(string(data), "**来源**：[ADR-012-connect]"); n != 1 {
		t.Fatalf("practice note duplicated: %d occurrences, want 1:\n%s", n, data)
	}
}
