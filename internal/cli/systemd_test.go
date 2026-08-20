package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
)

// writeVaultMap 在 skillDir/config 下写入 vault-map.json。
func writeVaultMap(t *testing.T, skillDir, vault string, pollMin int) {
	t.Helper()
	configDir := filepath.Join(skillDir, "config")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir config: %v", err)
	}
	content := fmt.Sprintf(`{"obsidian_vault":%q,"poll_interval_minutes":%d}`, vault, pollMin)
	if err := os.WriteFile(filepath.Join(configDir, "vault-map.json"), []byte(content), 0o644); err != nil {
		t.Fatalf("write vault-map: %v", err)
	}
}

func TestReadVaultMap(t *testing.T) {
	dir := t.TempDir()
	if vault, poll := readVaultMap(dir); vault != "" || poll != 0 {
		t.Fatalf("missing file: got vault=%q poll=%d", vault, poll)
	}

	writeVaultMap(t, dir, "/vault/main", 45)
	vault, poll := readVaultMap(dir)
	if vault != "/vault/main" || poll != 45 {
		t.Fatalf("got vault=%q poll=%d, want /vault/main 45", vault, poll)
	}

	if err := os.WriteFile(filepath.Join(dir, "config", "vault-map.json"), []byte("{broken"), 0o644); err != nil {
		t.Fatalf("write broken config: %v", err)
	}
	if vault, poll := readVaultMap(dir); vault != "" || poll != 0 {
		t.Fatalf("broken json: got vault=%q poll=%d", vault, poll)
	}
}

// TestResolveInstallVault 验证 vault 解析优先级：flag > OBSIDIAN_VAULT env >
// vault-map.json > 默认路径。
func TestResolveInstallVault(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OBSIDIAN_VAULT", "")
	writeVaultMap(t, dir, "/vault/from-map", 30)

	if got := resolveInstallVault(dir, ""); got != "/vault/from-map" {
		t.Fatalf("map fallback: got %q", got)
	}

	t.Setenv("OBSIDIAN_VAULT", "/vault/from-env")
	if got := resolveInstallVault(dir, ""); got != "/vault/from-env" {
		t.Fatalf("env: got %q", got)
	}
	if got := resolveInstallVault(dir, "/vault/from-flag"); got != "/vault/from-flag" {
		t.Fatalf("flag: got %q", got)
	}

	t.Setenv("OBSIDIAN_VAULT", "")
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, "Documents", "Obsidian", "MainVault")
	if got := resolveInstallVault(t.TempDir(), ""); got != want {
		t.Fatalf("default: got %q want %q", got, want)
	}
}

// TestResolveInstallPoll 验证轮询间隔优先级：flag > POLL_INTERVAL_MINUTES env
// > vault-map.json > 30。先清空环境变量，避免外部导出干扰 map fallback 断言。
func TestResolveInstallPoll(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("POLL_INTERVAL_MINUTES", "")
	writeVaultMap(t, dir, "/vault/x", 45)

	if got := resolveInstallPoll(0, false, dir); got != 45 {
		t.Fatalf("map fallback: got %d", got)
	}

	t.Setenv("POLL_INTERVAL_MINUTES", "42")
	if got := resolveInstallPoll(0, false, dir); got != 42 {
		t.Fatalf("env: got %d", got)
	}
	if got := resolveInstallPoll(15, true, dir); got != 15 {
		t.Fatalf("flag: got %d", got)
	}

	t.Setenv("POLL_INTERVAL_MINUTES", "")
	if got := resolveInstallPoll(0, false, t.TempDir()); got != 30 {
		t.Fatalf("default: got %d", got)
	}
}

// TestInstallSystemdDryRun 验证 dry-run 模式不产生副作用：不写 systemd 单元、
// 不调 systemctl，仅返回成功。
func TestInstallSystemdDryRun(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	skillDir := filepath.Join(homeDir, ".dsh", "skills", "obsidian-task-runner")
	writeVaultMap(t, skillDir, "/vault/cli", 33)
	t.Setenv("SKILL_INSTALL_DIR", skillDir)
	t.Setenv("OBSIDIAN_VAULT", "")

	// 克隆包级命令，避免触碰全局 rootCmd。
	root := &cobra.Command{Use: "otg", SilenceUsage: true, SilenceErrors: true}
	clone := *installSystemdCmd
	root.AddCommand(&clone)
	root.SetArgs([]string{"install-systemd", "--dry-run"})
	if err := root.Execute(); err != nil {
		t.Fatalf("install-systemd --dry-run: %v", err)
	}

	// dry-run 不得创建 ~/.config/systemd/user 目录（单元写入的落点）。
	userDir := filepath.Join(homeDir, ".config", "systemd", "user")
	if _, err := os.Stat(userDir); !os.IsNotExist(err) {
		t.Fatalf("dry-run 不应创建 %s", userDir)
	}
}
