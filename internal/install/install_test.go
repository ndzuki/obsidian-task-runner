package install

import (
	"encoding/json"
	"os"
	"path/filepath"
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
	if got := config.Models["default"]; got != "deepseek/deepseek-v4-flash" {
		t.Fatalf("default model = %q, want %q", got, "deepseek/deepseek-v4-flash")
	}
	if _, ok := config.Models["flash"]; ok {
		t.Fatal("legacy flash model must not be generated")
	}
}
