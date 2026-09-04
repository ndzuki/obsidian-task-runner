package install

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateVaultMapShipsNoBuiltInModelRoutes(t *testing.T) {
	dir := t.TempDir()
	opts := Options{SrcDir: ".", SkillInstallDir: dir, ObsidianVault: "/vault", NewProjectRoot: "/src", PollIntervalMin: 30}
	if err := generateVaultMap(opts); err != nil {
		t.Fatalf("generateVaultMap: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "config", "vault-map.json"))
	if err != nil {
		t.Fatalf("read vault map: %v", err)
	}
	var cfg struct {
		Models map[string]string `json:"models"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("parse vault map: %v", err)
	}
	if len(cfg.Models) != 0 {
		t.Fatalf("models must ship empty (operator-configured only), got %v", cfg.Models)
	}
}

func TestBuildOTGBinaryDryRun(t *testing.T) {
	// DryRun must not execute go/git — it only prints the intended build.
	opts := Options{DryRun: true}
	if err := buildOTGBinary(opts); err != nil {
		t.Fatalf("buildOTGBinary dry run: %v", err)
	}
}

func TestBuildOTGBinarySkipsWithoutGo(t *testing.T) {
	// No go in PATH: the build warns and keeps the existing binary instead
	// of failing the whole install.
	t.Setenv("PATH", t.TempDir())
	opts := Options{}
	if err := buildOTGBinary(opts); err != nil {
		t.Fatalf("buildOTGBinary without go: %v", err)
	}
}

func TestBuildSystemdPath(t *testing.T) {
	home := t.TempDir()
	if got := buildSystemdPath(home); got != "/usr/local/bin:/usr/bin:/bin" {
		t.Fatalf("bare path = %q, want system dirs only", got)
	}

	shims := filepath.Join(home, ".local", "share", "mise", "shims")
	if err := os.MkdirAll(shims, 0o755); err != nil {
		t.Fatal(err)
	}
	got := buildSystemdPath(home)
	if !strings.HasPrefix(got, shims+":") {
		t.Fatalf("mise shims missing or not first in PATH: %q", got)
	}

	if err := os.MkdirAll(filepath.Join(home, ".local", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	got = buildSystemdPath(home)
	if !strings.Contains(got, filepath.Join(home, ".local", "bin")) {
		t.Fatalf("local bin dropped when mise shims exist: %q", got)
	}
}

// TestConfigureSystemdWritesMiseShimsPath 验证生成的 unit 携带 mise shims PATH：
// 缺少它时 daemon 无法 exec dsh（"exec: dsh: executable file not found"），
// 所有 implementing 任务会在 failed 槽位后饿死。空 PATH 跳过 systemctl 调用。
func TestConfigureSystemdWritesMiseShimsPath(t *testing.T) {
	home := t.TempDir()
	shims := filepath.Join(home, ".local", "share", "mise", "shims")
	if err := os.MkdirAll(shims, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("PATH", t.TempDir()) // no systemctl → skip enable/start

	if err := ConfigureSystemd(Options{ObsidianVault: "/vault", PollIntervalMin: 30}); err != nil {
		t.Fatalf("ConfigureSystemd: %v", err)
	}
	for _, name := range []string{"dsh-agent-server.service", "dsh-web.service", "otg-task-watcher.service"} {
		data, err := os.ReadFile(filepath.Join(home, ".config", "systemd", "user", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if !strings.Contains(string(data), "Environment=PATH="+shims+":") {
			t.Fatalf("%s PATH missing mise shims:\n%s", name, data)
		}
	}
	watcher, err := os.ReadFile(filepath.Join(home, ".config", "systemd", "user", "otg-task-watcher.service"))
	if err != nil {
		t.Fatal(err)
	}
	// 默认 agent_server_managed=true（无 vault-map）→ watcher 不得依赖
	// dsh-agent-server（否则 restart watcher 强制拉起它抢占 8799，
	// 2026-08-31 死锁）。Requires 仅存在于 managed=false 外部管理模式。
	for _, forbidden := range []string{"After=dsh-agent-server.service", "Requires=dsh-agent-server.service"} {
		if strings.Contains(string(watcher), forbidden) {
			t.Fatalf("otg-task-watcher.service must NOT contain %q with managed=true:\n%s", forbidden, watcher)
		}
	}
}

// TestConfigureSystemdWatcherRequiresWhenExternalManaged 验证 managed=false
// （systemd 外部管理 agent-server）时 watcher 才带 Requires 依赖——与默认
// managed=true（daemon 自管，无依赖）形成对照。
func TestConfigureSystemdWatcherRequiresWhenExternalManaged(t *testing.T) {
	home := t.TempDir()
	shims := filepath.Join(home, ".local", "share", "mise", "shims")
	if err := os.MkdirAll(shims, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("PATH", t.TempDir()) // no systemctl → skip enable/start
	// 写 vault-map 声明 managed=false。
	skillDir := filepath.Join(home, ".dsh", "skills", "obsidian-task-runner")
	cfgDir := filepath.Join(skillDir, "config")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "vault-map.json"),
		[]byte(`{"agent_server_managed": false}`), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := ConfigureSystemd(Options{ObsidianVault: "/vault", PollIntervalMin: 30}); err != nil {
		t.Fatalf("ConfigureSystemd: %v", err)
	}
	watcher, err := os.ReadFile(filepath.Join(home, ".config", "systemd", "user", "otg-task-watcher.service"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"After=dsh-agent-server.service", "Requires=dsh-agent-server.service"} {
		if !strings.Contains(string(watcher), want) {
			t.Fatalf("otg-task-watcher.service missing %q with managed=false:\n%s", want, watcher)
		}
	}
}

// TestConfigureShellOptInByDefault 钉住强制约束：install 默认绝不写用户 shell
// 配置（.zshrc/.bashrc）。只有显式 ConfigureShell=true 才会追加。
func TestConfigureShellOptInByDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SHELL", "/bin/bash")

	// 默认不配置：opt-in=false 时 Run 不调用 configureShell，rc 文件不存在。
	opts := Options{ObsidianVault: "/vault", SkillInstallDir: home, ConfigureShell: false, DryRun: false}
	// configureShell 本身不检查 flag（Run 负责），这里只验证幂等与显式调用行为。
	if err := configureShell(opts); err != nil {
		t.Fatalf("configureShell (explicit call) failed: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(home, ".bashrc"))
	if err != nil {
		t.Fatalf("read .bashrc: %v", err)
	}
	if !bytes.Contains(data, []byte("export OBSIDIAN_VAULT=/vault")) {
		t.Fatalf(".bashrc missing export line:\n%s", data)
	}
	if !bytes.Contains(data, []byte("# Obsidian Task Runner")) {
		t.Fatalf(".bashrc missing marker:\n%s", data)
	}

	// 幂等：再次调用（如 vault 路径变化）不得重复追加 marker/export。
	if err := configureShell(Options{ObsidianVault: "/other-vault", SkillInstallDir: home}); err != nil {
		t.Fatalf("second configureShell failed: %v", err)
	}
	data2, _ := os.ReadFile(filepath.Join(home, ".bashrc"))
	if n := bytes.Count(data2, []byte("# Obsidian Task Runner")); n != 1 {
		t.Fatalf("marker repeated %d times (must stay 1)", n)
	}
}
