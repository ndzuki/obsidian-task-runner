package install

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateVaultMapUsesDefaultModelKey(t *testing.T) {
	skillDir := filepath.Join(t.TempDir(), "skill")
	opts := Options{
		ObsidianVault:   "/vault",
		NewProjectRoot:  "/src",
		SkillInstallDir: skillDir,
		NotifyEnabled:   true,
		PollIntervalMin: 30,
	}
	if err := generateVaultMap(opts); err != nil {
		t.Fatalf("generateVaultMap: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(skillDir, "config", "vault-map.json"))
	if err != nil {
		t.Fatalf("read vault map: %v", err)
	}
	var config struct {
		Models map[string]string `json:"models"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatalf("parse vault map: %v", err)
	}
	if got := config.Models["default"]; got != "deepseek_magic/gpt-5.4-mini" {
		t.Fatalf("default model = %q, want %q", got, "deepseek_magic/gpt-5.4-mini")
	}
	if got := config.Models["deepseek"]; got != "deepseek_magic/deepseek-v4-pro" {
		t.Fatalf("deepseek model = %q, want %q", got, "deepseek_magic/deepseek-v4-pro")
	}
	if got := config.Models["ds-official"]; got != "ds-official/deepseek-v4-pro" {
		t.Fatalf("ds-official model = %q, want %q", got, "ds-official/deepseek-v4-pro")
	}
	if got := config.Models["gpt"]; got != "openai/gpt-5.6-sol" {
		t.Fatalf("gpt model = %q, want %q", got, "openai/gpt-5.6-sol")
	}
	if _, ok := config.Models["flash"]; ok {
		t.Fatal("legacy flash model must not be generated")
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
// 缺少它时 daemon 无法 exec omp（"exec: omp: executable file not found"），
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
	for _, want := range []string{"After=dsh-agent-server.service", "Requires=dsh-agent-server.service"} {
		if !strings.Contains(string(watcher), want) {
			t.Fatalf("otg-task-watcher.service missing %q:\n%s", want, watcher)
		}
	}
}
