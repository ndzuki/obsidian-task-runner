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
activity: high
aliases: [Connect-Go手册]
---

# Connect-Go 完整手册

> Connect 官方 Go 文档全量手册，RPC 框架速查。

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

	// Verify the KB v2 summary column picks up the abstract after h1.
	if !strings.Contains(content, "Connect 官方 Go 文档全量手册") {
		t.Error("INDEX.md should contain the extracted summary for connect-rpc.md")
	}

	// Verify activity metadata column and the new domain-semantics layer label.
	if !strings.Contains(content, "activity") {
		t.Error("INDEX.md should have an activity column")
	}
	if !strings.Contains(content, "平台与架构技术") {
		t.Error("INDEX.md core layer should be labeled 平台与架构技术")
	}
	// Sort: verified=true (wire-di) ranks before activity=high (connect-rpc).
	wi := strings.Index(content, "wire-di.md")
	ci := strings.Index(content, "connect-rpc.md")
	if wi < 0 || ci < 0 || wi > ci {
		t.Errorf("sort order wrong: wire-di(%d) should appear before connect-rpc(%d)", wi, ci)
	}
}

func TestRebuildINDEXNoiseMarking(t *testing.T) {
	tmp := t.TempDir()
	refs := filepath.Join(tmp, "References")
	createRefFile(t, refs, "core/networking/openresty.md", `---
topics: [networking, openresty]
level: intermediate
updated: "2026-08-01"
source: "https://claude.ai/chat/abc123"
verified: false
aliases: []
---

# OpenResty 安全

> 安全防护配置模式。

## 项目结构
- openresty/
  - lua/config.lua
  - scripts/deploy.sh
`)

	if _, err := RebuildINDEX(tmp); err != nil {
		t.Fatalf("RebuildINDEX: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(refs, "INDEX.md"))
	content := string(data)
	if !strings.Contains(content, "含噪音待清理") {
		t.Error("INDEX.md should mark noisy file as 含噪音待清理")
	}
	if !strings.Contains(content, "openresty.md") {
		t.Error("INDEX.md should list the noisy file")
	}
}

func TestRebuildINDEX_Empty(t *testing.T) {
	tmp := t.TempDir()
	refs := filepath.Join(tmp, "References")
	// Create empty directories
	for _, d := range []string{"core", "extended", "archived"} {
		if err := os.MkdirAll(filepath.Join(refs, d), 0o755); err != nil {
			t.Fatal(err)
		}
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

func TestMarkVerified(t *testing.T) {
	dir := t.TempDir()
	ref := filepath.Join(dir, "core", "go")
	if err := os.MkdirAll(ref, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(ref, "test-pattern.md")
	if err := os.WriteFile(path, []byte(`---
topics: [go, pattern]
level: intermediate
updated: "2026-08-01"
source: local
verified: false
aliases: []
---

# Test Pattern

> 摘要
`), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := MarkVerified([]string{path}); err != nil {
		t.Fatalf("MarkVerified: %v", err)
	}
	data, _ := os.ReadFile(path)
	fm, _, err := parseFrontmatter(data)
	if err != nil {
		t.Fatal(err)
	}
	if fm["verified"] != true {
		t.Fatalf("verified = %v, want true", fm["verified"])
	}
	if fm["topics"] == nil {
		t.Fatal("topics lost after MarkVerified")
	}
}

func TestHasNoise(t *testing.T) {
	cases := []struct {
		content string
		want    bool
	}{
		{"# A\n\n> summary\n## 目录\n- x", false},
		{"# A\n\nhttps://claude.ai/chat/abc123\n", true},
		{"# A\n\n## 项目结构\n```\ntree\n```\n", true},
		{"# A\n\n## 文件清单和说明\n- a\n", true},
	}
	for _, c := range cases {
		if got := hasNoise([]byte(c.content)); got != c.want {
			t.Errorf("hasNoise(%q) = %v, want %v", c.content[:20], got, c.want)
		}
	}
}

func TestAppendFailurePattern(t *testing.T) {
	tmp := t.TempDir()
	refs := filepath.Join(tmp, "References", "core")
	if err := os.MkdirAll(refs, 0o755); err != nil {
		t.Fatal(err)
	}
	kb := filepath.Join(refs, "daemon-stuck-task-patterns.md")
	seed := `---
topics: [daemon]
level: reference
updated: "2026-08-01"
source: local
verified: true
aliases: []
---

# Daemon 卡死模式

## 模式 1：已有模式

**现象**：旧内容

## 检查清单

- [ ] 旧条目

## 更新记录

- 2026-08-01 — 初始
`
	if err := os.WriteFile(kb, []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}

	// First occurrence: appended as 模式 2, before 检查清单.
	if err := AppendFailurePattern(tmp, "API_KEY_UNAVAILABLE", "round2", "123", "/tmp/x.log"); err != nil {
		t.Fatalf("AppendFailurePattern: %v", err)
	}
	data, _ := os.ReadFile(kb)
	content := string(data)
	if !strings.Contains(content, "## 模式 2：API_KEY_UNAVAILABLE") {
		t.Fatal("pattern 2 not appended")
	}
	if !strings.Contains(content, "KeePassXC 未解锁") {
		t.Fatal("root cause map missing for API_KEY_UNAVAILABLE")
	}
	// 检查清单 must stay after the pattern block.
	ci := strings.Index(content, "## 检查清单")
	pat := strings.Index(content, "## 模式 2")
	if ci < pat {
		t.Fatal("检查清单 should remain after the new pattern")
	}

	// Second occurrence with same code+phase: deduped, file unchanged.
	before := string(data)
	if err := AppendFailurePattern(tmp, "API_KEY_UNAVAILABLE", "round2", "456", "/tmp/y.log"); err != nil {
		t.Fatalf("dedup call: %v", err)
	}
	after, _ := os.ReadFile(kb)
	if string(after) != before {
		t.Fatal("dedup failed: file changed on repeated code+phase")
	}

	// Missing knowledge base: silent no-op.
	if err := AppendFailurePattern(filepath.Join(tmp, "nope"), "MODEL_FAILED", "planning", "1", ""); err != nil {
		t.Fatalf("missing KB should be nil, got %v", err)
	}
}
