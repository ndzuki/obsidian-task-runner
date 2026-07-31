package knowledge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRebuildINDEX(t *testing.T) {
	tmp := t.TempDir()
	refs := filepath.Join(tmp, "References")

	// Create sample References/ structure
	createRefFile(t, refs, "core/go/connect-rpc.md", `---
topics: [go, golang, connect]
level: reference
updated: "2026-07-31"
source: "https://connectrpc.com/docs/go/"
verified: false
aliases: [Connect-Go手册]
---

# Connect-Go 完整手册

一些正文内容。
`)

	createRefFile(t, refs, "core/go/wire-di.md", `---
topics: [go, golang, wire]
level: reference
updated: "2026-07-28"
source: "local"
verified: true
aliases: [Wire依赖注入]
---

# Golang Wire 完全指南

正文。
`)

	createRefFile(t, refs, "extended/linux/ssh-guide.md", `---
topics: [linux, ssh]
level: beginner
updated: "2026-06-15"
source: "local"
verified: false
aliases: []
---

# SSH 使用指南

正文内容。
`)

	n, err := RebuildINDEX(tmp)
	if err != nil {
		t.Fatalf("RebuildINDEX: %v", err)
	}
	if n != 3 {
		t.Fatalf("expected 3 entries, got %d", n)
	}

	// Read the generated INDEX.md
	idxPath := filepath.Join(refs, "INDEX.md")
	data, err := os.ReadFile(idxPath)
	if err != nil {
		t.Fatalf("read INDEX.md: %v", err)
	}
	content := string(data)

	checks := []string{
		"# References INDEX",
		"core/go/connect-rpc.md",
		"core/go/wire-di.md",
		"extended/linux/ssh-guide.md",
		"Connect-Go 完整手册",
		"Golang Wire 完全指南",
		"SSH 使用指南",
		"go, golang, connect",
		"reference",
		"verified",
	}
	for _, c := range checks {
		if !strings.Contains(content, c) {
			t.Errorf("INDEX.md missing: %q", c)
		}
	}

	// Verify verified:true entry
	if !strings.Contains(content, "wire-di.md") || !strings.Contains(content, "true") {
		t.Error("INDEX.md should contain wire-di.md with verified:true")
	}
}

func TestRebuildINDEX_Empty(t *testing.T) {
	tmp := t.TempDir()
	refs := filepath.Join(tmp, "References")
	// Create empty directories
	for _, d := range []string{"core", "extended", "archived"} {
		os.MkdirAll(filepath.Join(refs, d), 0o755)
	}

	n, err := RebuildINDEX(tmp)
	if err != nil {
		t.Fatalf("RebuildINDEX empty: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected 0 entries, got %d", n)
	}

	idxPath := filepath.Join(refs, "INDEX.md")
	data, err := os.ReadFile(idxPath)
	if err != nil {
		t.Fatalf("read INDEX.md: %v", err)
	}
	if !strings.Contains(string(data), "总计 0 篇") {
		t.Error("INDEX.md should say 0 entries")
	}
}

func TestClassifyADR(t *testing.T) {
	tests := []struct {
		adr      adrInfo
		expected string
	}{
		{adrInfo{Decision: "所有正式业务接口以 protobuf 为唯一契约源，使用 Connect", Title: "Connect 单端口统一协议面"}, "core/go/connect-rpc.md"},
		{adrInfo{Decision: "中心以数据库 Command Outbox 作为待投递命令权威", Title: "持久 Command Outbox"}, "core/go/outbox-reliable-delivery.md"},
		{adrInfo{Decision: "使用离散主状态 blocked → ready → refining", Title: "状态机驱动 TASK 生命周期"}, "core/go/state-machine-pattern.md"},
		{adrInfo{Decision: "运行时业务代码只使用 Go SDK", Title: "Go SDK-only 集群执行"}, "core/kubernetes/operator-development-guide.md"},
		{adrInfo{Decision: "这是一条纯业务决策，不涉及通用技术模式", Title: "业务特定决策"}, ""},
	}
	for _, tt := range tests {
		got := classifyADR(tt.adr)
		if got != tt.expected {
			t.Errorf("classifyADR(%q) = %q, want %q", tt.adr.Title, got, tt.expected)
		}
	}
}

func TestParseFrontmatter(t *testing.T) {
	data := []byte(`---
topics: [go, golang]
level: advanced
updated: "2026-07-31"
source: "local"
verified: false
aliases: [test]
---

# Title

Body text.
`)
	fm, body, err := parseFrontmatter(data)
	if err != nil {
		t.Fatalf("parseFrontmatter: %v", err)
	}
	if fm["level"] != "advanced" {
		t.Errorf("level = %v", fm["level"])
	}
	topics, ok := fm["topics"].([]interface{})
	if !ok || len(topics) != 2 {
		t.Errorf("topics = %v", fm["topics"])
	}
	if !strings.Contains(body, "Body text") {
		t.Error("body missing content")
	}
}

func TestExtractH1(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"# Hello\n\nbody", "Hello"},
		{"## Sub\n# Main Title\nbody", "Main Title"},
		{"no heading here", ""},
		{"# 中文标题\nbody", "中文标题"},
	}
	for _, tt := range tests {
		got := extractH1([]byte(tt.input))
		if got != tt.expected {
			t.Errorf("extractH1(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func createRefFile(t *testing.T, refsDir, relPath, content string) {
	t.Helper()
	fullPath := filepath.Join(refsDir, relPath)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
