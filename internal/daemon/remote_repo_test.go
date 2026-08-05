package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStripProjectPrefix(t *testing.T) {
	cases := []struct{ in, want string }{
		{"001-release-manager", "release-manager"},
		{"release-manager", "release-manager"},
		{"003-obsidian-task-runner", "obsidian-task-runner"},
		{"123", "123"}, // all digits without dash
		{"", ""},
	}
	for _, c := range cases {
		if got := stripProjectPrefix(c.in); got != c.want {
			t.Errorf("stripProjectPrefix(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDistillRequirementDescription(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	reqPath := filepath.Join(vault, "Projects", "p", "Requirements", "REQ-001.md")
	if err := os.MkdirAll(filepath.Dir(reqPath), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nid: \"001\"\ntitle: 自动化流水线\n---\n\n# 标题\n\n## 需求摘要\n\n构建一套自动化任务流水线，支持多项目。\n\n## 验收标准\n"
	if err := os.WriteFile(reqPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	got := distillRequirementDescription(vault, "Projects/p/Requirements/REQ-001.md")
	if got == "" {
		t.Fatal("description must not be empty")
	}
	if got != "自动化流水线：构建一套自动化任务流水线，支持多项目。" {
		t.Fatalf("distilled = %q", got)
	}
	if len([]rune(got)) > 200 {
		t.Fatal("description must be truncated to 200 runes")
	}
}

func TestGithubOwnerFromVaultMap(t *testing.T) {
	dir := t.TempDir()
	mapFile := filepath.Join(dir, "vault-map.json")
	content := `{"projects": [{"name": "alpha", "git_remote": "github.com/ndzuki/alpha"}]}`
	if err := os.WriteFile(mapFile, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := githubOwnerFromVaultMap(mapFile); got != "ndzuki" {
		t.Fatalf("owner = %q, want ndzuki", got)
	}
	if got := githubOwnerFromVaultMap(filepath.Join(dir, "missing.json")); got != "" {
		t.Fatalf("missing file must yield empty owner, got %q", got)
	}
}
