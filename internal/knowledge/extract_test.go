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
