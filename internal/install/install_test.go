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
		Models         map[string]string `json:"models"`
		FallbackModels map[string]string `json:"fallback_models"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatalf("parse vault map: %v", err)
	}
	if got := config.Models["default"]; got != "gateway/gpt-5.4-mini" {
		t.Fatalf("default model = %q, want %q", got, "gateway/gpt-5.4-mini")
	}
	if _, ok := config.Models["flash"]; ok {
		t.Fatal("legacy flash model must not be generated")
	}
	for _, assignee := range []string{"gpt", "default", "deepseek"} {
		if got := config.FallbackModels[assignee]; got != "deepseek/deepseek-v4-flash" {
			t.Fatalf("fallback model for %s = %q, want %q", assignee, got, "deepseek/deepseek-v4-flash")
		}
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
	for _, name := range []string{"otg-task-runner.service", "otg-task-watcher.service"} {
		data, err := os.ReadFile(filepath.Join(home, ".config", "systemd", "user", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if !strings.Contains(string(data), "Environment=PATH="+shims+":") {
			t.Fatalf("%s PATH missing mise shims:\n%s", name, data)
		}
	}
}

// TestLinkTopLevelSkills guards the agent-discovery registration: every
// top-level skill under ~/.omp/skills/ gets a symlink in
// ~/.omp/agent/skills/ (dependency skills like knowledge-base were never
// registered by installPhaseSkills, so skill:// resolution failed in agent
// sessions while skill-doctor still found them). Idempotent: existing
// entries are preserved and a second run links nothing; DryRun writes
// nothing.
func TestLinkTopLevelSkills(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	skillRoot := filepath.Join(home, ".omp", "skills")
	agentDir := filepath.Join(home, ".omp", "agent", "skills")
	for _, name := range []string{"knowledge-base", "grilling", "wayfinder"} {
		if err := os.MkdirAll(filepath.Join(skillRoot, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Pre-existing discovery entry must be preserved untouched.
	if err := os.MkdirAll(filepath.Join(agentDir, "keep-me"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := linkTopLevelSkills(Options{}); err != nil {
		t.Fatalf("linkTopLevelSkills: %v", err)
	}
	for _, name := range []string{"knowledge-base", "grilling", "wayfinder"} {
		if _, err := os.Lstat(filepath.Join(agentDir, name)); err != nil {
			t.Fatalf("missing discovery link %s: %v", name, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(agentDir, "keep-me")); err != nil {
		t.Fatalf("pre-existing entry removed: %v", err)
	}

	// Idempotent: second run must not replace or duplicate anything.
	before, err := os.ReadDir(agentDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := linkTopLevelSkills(Options{}); err != nil {
		t.Fatalf("second linkTopLevelSkills: %v", err)
	}
	after, err := os.ReadDir(agentDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != len(after) {
		t.Fatalf("discovery entries changed on re-run: %d → %d", len(before), len(after))
	}

	// DryRun must not create links or directories.
	fresh := t.TempDir()
	t.Setenv("HOME", fresh)
	if err := os.MkdirAll(filepath.Join(fresh, ".omp", "skills", "probe"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := linkTopLevelSkills(Options{DryRun: true}); err != nil {
		t.Fatalf("dry-run linkTopLevelSkills: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(fresh, ".omp", "agent", "skills", "probe")); !os.IsNotExist(err) {
		t.Fatalf("dry-run created a link (err=%v)", err)
	}
	if _, err := os.Lstat(filepath.Join(fresh, ".omp", "agent", "skills")); !os.IsNotExist(err) {
		t.Fatalf("dry-run created the agent skill dir (err=%v)", err)
	}
}
