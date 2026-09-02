package knowledge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAppendRecordLineCreatesSection(t *testing.T) {
	content := "---\ntopics: [go]\n---\n# Title\n\n> summary\n\n## 要点\n- a\n"
	got := appendRecordLine(content, "- 2026-08-06 demo 应用验证通过")
	if !strings.Contains(got, "## 应用记录\n- 2026-08-06 demo 应用验证通过") {
		t.Fatalf("section not created at end:\n%s", got)
	}
	if !strings.HasPrefix(got, content) {
		t.Fatalf("original content modified:\n%s", got)
	}
}

func TestAppendRecordLineInsertsIntoExistingSection(t *testing.T) {
	content := "# Title\n\n## 应用记录\n- 2026-08-01 old 应用验证通过\n\n## 更新记录\n- x\n"
	got := appendRecordLine(content, "- 2026-08-06 demo 应用验证通过")
	if !strings.Contains(got, "## 应用记录\n- 2026-08-01 old 应用验证通过\n- 2026-08-06 demo 应用验证通过") {
		t.Fatalf("new record not inserted after existing ones:\n%s", got)
	}
	if !strings.Contains(got, "## 更新记录") {
		t.Fatalf("later section lost:\n%s", got)
	}
}

func TestAppendApplicationRecord(t *testing.T) {
	dir := t.TempDir()
	refsDir := filepath.Join(dir, "References", "core", "go")
	if err := os.MkdirAll(refsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	doc := filepath.Join(refsDir, "connect-rpc.md")
	content := "---\ntopics: [go, connect]\n---\n# Connect\n\n> summary\n"
	if err := os.WriteFile(doc, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "References", "core", "nope.md")

	added, err := AppendApplicationRecord(dir, "demo", "", []string{"core/go/connect-rpc.md", "core/nope.md"})
	if err != nil {
		t.Fatalf("AppendApplicationRecord: %v", err)
	}
	if added != 1 {
		t.Fatalf("added = %d, want 1 (missing doc skipped)", added)
	}
	data, _ := os.ReadFile(doc)
	if !strings.Contains(string(data), "demo 应用验证通过") {
		t.Fatalf("record not written:\n%s", data)
	}

	// Idempotent: same project/date again → no duplicate, no error.
	added, err = AppendApplicationRecord(dir, "demo", "", []string{"core/go/connect-rpc.md"})
	if err != nil || added != 0 {
		t.Fatalf("second call: added=%d err=%v, want 0,nil", added, err)
	}
	data, _ = os.ReadFile(doc)
	if strings.Count(string(data), "demo 应用验证通过") != 1 {
		t.Fatalf("duplicate record written:\n%s", data)
	}
	_ = missing
}

// TestAppendApplicationRecordMirrorsConfiguredStore: merge 路径的应用记录
// heat bump 必须镜像进配置库，且不复活 XDG 默认库。
func TestAppendApplicationRecordMirrorsConfiguredStore(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	vault := filepath.Join(dir, "vault")
	refsDir := filepath.Join(vault, "References", "core", "go")
	if err := os.MkdirAll(refsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	doc := filepath.Join(refsDir, "connect-rpc.md")
	content := "---\ntopics: [go, connect]\nlevel: reference\nupdated: \"2026-08-07\"\nsource: \"local\"\nverified: false\naliases: []\nhits: 0\n---\n# Connect\n\n> summary\n"
	if err := os.WriteFile(doc, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(dir, "kb.sqlite")
	if _, err := SyncKnowledgeDB(vault, dbPath, nil); err != nil {
		t.Fatal(err)
	}

	added, err := AppendApplicationRecord(vault, "demo", dbPath, []string{"core/go/connect-rpc.md"})
	if err != nil {
		t.Fatalf("AppendApplicationRecord: %v", err)
	}
	if added != 1 {
		t.Fatalf("added = %d, want 1", added)
	}

	db, err := openKB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var hits int
	if err := db.QueryRow(`SELECT hits FROM kb_docs WHERE path=?`, "core/go/connect-rpc.md").Scan(&hits); err != nil {
		t.Fatal(err)
	}
	if hits != 1 {
		t.Fatalf("configured store hits = %d, want 1", hits)
	}
	if _, err := os.Stat(KBPath(vault, "")); err == nil {
		t.Fatalf("default store must not be created by a configured-store application record")
	}
}
