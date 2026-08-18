package cli

import (
	"bytes"
	"github.com/spf13/cobra"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// unregisterRoot 返回挂载了 unregister-project 命令的测试 root。
// newRootCommand 只挂 contract.go 里的命令，unregisterProjectCmd 注册在包级
// rootCmd 上，测试需显式挂载。
func unregisterRoot() *cobra.Command {
	root := &cobra.Command{Use: "otg", SilenceUsage: true, SilenceErrors: true}
	root.AddCommand(unregisterProjectCmd)
	return root
}

func TestUnregisterProjectCommandNotFound(t *testing.T) {
	dir := t.TempDir()
	mapFile := filepath.Join(dir, "vault-map.json")
	if err := os.WriteFile(mapFile, []byte(`{"projects":[]}`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	root := unregisterRoot()
	out := new(bytes.Buffer)
	root.SetOut(out)
	root.SetErr(out)
	root.SetArgs([]string{"unregister-project", "missing", "--map-file", mapFile})
	err := root.Execute()
	if err == nil {
		t.Fatal("unregistering a missing project must return a non-zero error")
	}
	if !strings.Contains(err.Error(), "missing") || !strings.Contains(err.Error(), mapFile) {
		t.Fatalf("error must name the project and map file, got: %v", err)
	}
}

func TestUnregisterProjectCommandRemovesEntry(t *testing.T) {
	dir := t.TempDir()
	mapFile := filepath.Join(dir, "vault-map.json")
	checkout := filepath.Join(dir, "checkout")
	if err := os.MkdirAll(checkout, 0o755); err != nil {
		t.Fatal(err)
	}
	content := `{"projects":[{"name":"demo","path":"` + checkout + `","git_remote":"github.com/x/demo"}]}`
	if err := os.WriteFile(mapFile, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	root := unregisterRoot()
	out := new(bytes.Buffer)
	root.SetOut(out)
	root.SetErr(out)
	root.SetArgs([]string{"unregister-project", "demo", "--map-file", mapFile})
	if err := root.Execute(); err != nil {
		t.Fatalf("unregister registered project: %v", err)
	}
	raw, _ := os.ReadFile(mapFile)
	if bytes.Contains(raw, []byte(`"demo"`)) {
		t.Fatalf("project entry must be removed from vault-map: %s", string(raw))
	}
}
