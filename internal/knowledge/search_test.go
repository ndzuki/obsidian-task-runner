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
