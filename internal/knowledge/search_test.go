package knowledge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSearchDoc(t *testing.T, refsDir, name, topics, body string) {
	t.Helper()
	content := "---\ntopics: [" + topics + "]\nlevel: reference\nupdated: \"2026-08-03\"\nsource: \"local\"\nverified: false\naliases: []\n---\n# " + strings.Title(name) + "\n\n> summary " + topics + "\n\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(refsDir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestTokenize(t *testing.T) {
	cases := map[string][]string{
		"connect rpc":        {"connect", "rpc"},
		"kubernetes 集群":      {"kubernetes", "集群"},
		"k8s CRD controller": {"k8s", "crd", "controller"},
	}
	for input, want := range cases {
		got := tokenize(input)
		if len(got) != len(want) {
			t.Fatalf("tokenize(%q) = %v, want %v", input, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("tokenize(%q) = %v, want %v", input, got, want)
			}
		}
	}
}

func TestChunkDocument(t *testing.T) {
	data := []byte("---\ntopics: [go, connect]\n---\n# Connect\n\n> summary\n\n前言内容。\n\n## 要点\n- a\n- b\n\n## 更新记录\n- 2026-08-01 x\n")
	chunks := chunkDocument(data)
	if len(chunks) != 3 {
		t.Fatalf("chunks = %d, want 3 (preamble + 2 sections)", len(chunks))
	}
	if chunks[0].heading != "" || !strings.Contains(chunks[0].text, "前言内容") {
		t.Fatalf("chunk 0 = %+v, want preamble with body text", chunks[0])
	}
	if chunks[1].heading != "## 要点" || !strings.Contains(chunks[1].text, "- a") {
		t.Fatalf("chunk 1 = %+v, want 要点 section", chunks[1])
	}
	if chunks[2].heading != "## 更新记录" {
		t.Fatalf("chunk 2 heading = %q, want ## 更新记录", chunks[2].heading)
	}
	// Every chunk carries the document prefix (topics + title + summary).
	for _, c := range chunks {
		if !strings.Contains(c.text, "connect") || !strings.Contains(c.text, "Connect") {
			t.Fatalf("chunk %q missing document context prefix", c.heading)
		}
	}
	// Empty body → no chunks (preamble flush skips empty content).
	if got := chunkDocument([]byte("---\ntopics: [go]\n---\n")); len(got) != 0 {
		t.Fatalf("empty body chunks = %d, want 0", len(got))
	}
}

func TestChunkDocumentSplitsLongSections(t *testing.T) {
	// A very long section is truncated to its head (CPU-inference budget):
	// the chunk stays one per section but is capped at 300 body chars.
	long := strings.Repeat("段落内容示例。", 200) // 1400 chars
	body := "---\ntopics: [go]\n---\n# T\n\n> s\n\n## 长节\n" + long + "\n\n## 尾节\n- x\n"
	chunks := chunkDocument([]byte(body))
	if len(chunks) != 3 {
		t.Fatalf("chunks = %d, want 3 (preamble + 2 sections)", len(chunks))
	}
	for _, c := range chunks {
		if c.heading == "## 长节" && len(c.text) > 300+len("## 长节\n")+128 {
			t.Fatalf("chunk %q not truncated to head: %d chars", c.heading, len(c.text))
		}
		if !strings.Contains(c.text, "段落内容示例") {
			t.Fatalf("chunk %q missing body content", c.heading)
		}
	}
}

func TestRankDeduplicatesTopics(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	refsDir := filepath.Join(vault, "References", "core")
	if err := os.MkdirAll(refsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Three docker docs sharing topics, one unique doc.
	writeSearchDoc(t, refsDir, "docker-a.md", "docker, container, cli", "docker 命令 A")
	writeSearchDoc(t, refsDir, "docker-b.md", "docker, container, compose", "docker 命令 B")
	writeSearchDoc(t, refsDir, "k8s.md", "kubernetes, k8s, kind", "kind 集群")
	idx, err := BuildSearchIndex(vault)
	if err != nil {
		t.Fatal(err)
	}
	// All docs score via shared "docker"/"container"/"k8s" terms.
	hits := idx.Search("docker container", 5)
	// Dedup keeps at most one docker-family doc + possibly k8s (no 2-topic
	// overlap with docker cluster? k8s shares none of docker,container) →
	// expect ≤2 results.
	if len(hits) > 2 {
		got := make([]string, 0, len(hits))
		for _, h := range hits {
			got = append(got, h.Path)
		}
		t.Fatalf("dedup expected ≤2, got %v", got)
	}
}

func TestSearchRanksByRelevance(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	refsDir := filepath.Join(vault, "References", "core")
	if err := os.MkdirAll(refsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSearchDoc(t, refsDir, "connect-rpc.md", "connect, rpc, grpc, protobuf",
		"Connect 是轻量 RPC 框架，一套 handler 同时支持 Connect/gRPC/gRPC-Web 三种协议。")
	writeSearchDoc(t, refsDir, "helm-chart.md", "helm, chart, kubernetes",
		"Helm 是 Kubernetes 包管理工具，Chart 结构与模板语法。")
	writeSearchDoc(t, refsDir, "journalctl.md", "journalctl, systemd, linux",
		"journalctl 查询 systemd 日志：按服务/时间/优先级过滤。")

	idx, err := BuildSearchIndex(vault)
	if err != nil {
		t.Fatalf("BuildSearchIndex: %v", err)
	}

	hits := idx.Search("connect grpc 协议", 3)
	if len(hits) == 0 || hits[0].Path != "core/connect-rpc.md" {
		t.Fatalf("top hit = %+v, want core/connect-rpc.md", hits)
	}
	// Chinese query via bigrams.
	hits = idx.Search("日志 查询", 3)
	if len(hits) == 0 || hits[0].Path != "core/journalctl.md" {
		t.Fatalf("chinese top hit = %+v, want core/journalctl.md", hits)
	}
	// No match → empty.
	if hits := idx.Search("zircon mesh", 3); len(hits) != 0 {
		t.Fatalf("unexpected hits for zircon: %+v", hits)
	}
}
