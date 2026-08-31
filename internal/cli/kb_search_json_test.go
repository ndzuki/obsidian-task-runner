//go:build sqlite_fts5

package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndzuki/obsidian-task-runner/internal/knowledge"
)

// TestKBSearchJSONOutput guards the `otg kb search --json` contract that the
// interactive KB-first precompute (agent-server) depends on: machine-readable
// JSON array of {path,title,summary,...}, with warnings on stderr only.
func TestKBSearchJSONOutput(t *testing.T) {
	vault := t.TempDir()
	refs := filepath.Join(vault, "References", "core", "go")
	if err := os.MkdirAll(refs, 0o755); err != nil {
		t.Fatal(err)
	}
	doc := `---
topics: [go, rpc]
aliases: [连接复用]
verified: true
---
# Go Connect RPC

> 本地优先、连接复用最佳实践。

## 约束
- 复用长连接
## 踩坑实践
- 不要每请求新建连接
`
	if err := os.WriteFile(filepath.Join(refs, "connect-rpc.md"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(vault, "kb.sqlite")
	if _, err := knowledge.RebuildKnowledgeDB(vault, dbPath, nil); err != nil {
		t.Fatalf("RebuildKnowledgeDB: %v", err)
	}

	// Isolated map file: the CLI loads vault-map.json for optional embedding
	// config; pointing at an empty file keeps the test hermetic (must not
	// read the user's real vault-map.json under the test user's HOME).
	mapFile := filepath.Join(vault, "vault-map.json")
	if err := os.WriteFile(mapFile, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}

	root := newRootCommand("test")
	root.AddCommand(newKnowledgeCommand())
	out := new(bytes.Buffer)
	errOut := new(bytes.Buffer)
	root.SetOut(out)
	root.SetErr(errOut)
	root.SetArgs([]string{"kb", "search", "--json", "--limit", "3", "--vault", vault, "--db", dbPath, "--map-file", mapFile, "go rpc"})
	if err := root.Execute(); err != nil {
		t.Fatalf("kb search --json: %v", err)
	}

	var hits []struct {
		Path    string  `json:"path"`
		Title   string  `json:"title"`
		Summary string  `json:"summary"`
		Score   float64 `json:"score"`
	}
	if err := json.Unmarshal(out.Bytes(), &hits); err != nil {
		t.Fatalf("output is not valid JSON: %v: %s", err, out.String())
	}
	if len(hits) == 0 {
		t.Fatalf("expected at least one hit, got empty: %s", out.String())
	}
	found := false
	for _, h := range hits {
		if strings.HasSuffix(h.Path, "connect-rpc.md") {
			found = true
			if h.Title == "" {
				t.Fatalf("hit missing title: %+v", h)
			}
		}
	}
	if !found {
		t.Fatalf("expected connect-rpc.md hit, got: %s", out.String())
	}
	if errOut.Len() > 0 {
		t.Fatalf("warnings must stay on stderr, stdout is JSON only; stderr=%q", errOut.String())
	}
}

// TestKBSearchJSONEmptyDB guards the degenerate case: no indexed docs → `[]`
// (not an error), so the interactive precompute degrades to index-digest.
func TestKBSearchJSONEmptyDB(t *testing.T) {
	vault := t.TempDir()
	dbPath := filepath.Join(vault, "empty.sqlite")
	mapFile := filepath.Join(vault, "vault-map.json")
	if err := os.WriteFile(mapFile, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}

	root := newRootCommand("test")
	root.AddCommand(newKnowledgeCommand())
	out := new(bytes.Buffer)
	errOut := new(bytes.Buffer)
	root.SetOut(out)
	root.SetErr(errOut)
	root.SetArgs([]string{"kb", "search", "--json", "--vault", vault, "--db", dbPath, "--map-file", mapFile, "anything"})
	if err := root.Execute(); err != nil {
		t.Fatalf("kb search --json (empty db): %v", err)
	}
	if got := strings.TrimSpace(out.String()); got != "[]" {
		t.Fatalf("empty db JSON = %q, want []", got)
	}
}
