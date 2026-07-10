// Package install provides the skill installation logic.
package install

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Options holds installation configuration.
type Options struct {
	ObsidianVault      string
	NewProjectRoot     string
	SkillInstallDir    string
	NotifyEnabled      bool
	PollIntervalMin    int
	SystemdEnabled     bool
	Force              bool
	DryRun             bool
	SrcDir             string // source directory with skill files
}

// Run performs the installation.
func Run(opts Options) error {
	d := opts.DryRun

	// 1. Check dependencies
	for _, bin := range []string{"git", "omp"} {
		if _, err := exec.LookPath(bin); err != nil {
			return fmt.Errorf("missing dependency: %s", bin)
		}
	}

	// 2. Install skill files
	if err := installSkill(opts); err != nil && !d {
		return err
	}

	// 3. Install task-verifier
	if err := installTaskVerifier(opts); err != nil && !d {
		return err
	}

	// 4. Generate vault-map.json
	if err := generateVaultMap(opts); err != nil && !d {
		return fmt.Errorf("vault-map: %w", err)
	}

	// 5. Create OMP symlink
	if err := createOMPSymlink(opts); err != nil && !d {
		return fmt.Errorf("OMP symlink: %w", err)
	}

	// 6. Configure shell environment
	if err := configureShell(opts); err != nil && !d {
		return fmt.Errorf("shell config: %w", err)
	}

	// 7. Create required directories
	if !d {
		os.MkdirAll(filepath.Join(opts.ObsidianVault, "Projects"), 0755)
		os.MkdirAll(opts.NewProjectRoot, 0755)
	}

	// 8. Configure systemd
	if opts.SystemdEnabled {
		if err := configureSystemd(opts); err != nil && !d {
			return fmt.Errorf("systemd: %w", err)
		}
	}

	if d {
		fmt.Println("[DRY RUN] Installation preview complete")
	}
	return nil
}

func installSkill(opts Options) error {
	dest := opts.SkillInstallDir
	src := opts.SrcDir
	if src == "" {
		src = "obsidian-task-runner"
	}

	d := opts.DryRun
	if d {
		fmt.Printf("[DRY RUN] Would copy %s → %s\n", src, dest)
		return nil
	}

	// Remove old installation if forced
	if opts.Force {
		os.RemoveAll(dest)
	}

	// Create parent dir
	os.MkdirAll(filepath.Dir(dest), 0755)

	// Copy skill files (using cp -r for simplicity)
	cmd := exec.Command("cp", "-rT", src, dest)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("copy skill: %w\n%s", err, output)
	}

	fmt.Println("skill installed to", dest)
	return nil
}

func installTaskVerifier(opts Options) error {
	agentsDir := filepath.Join(opts.SkillInstallDir, "..", "..", "agent", "agents")
	if opts.DryRun {
		fmt.Printf("[DRY RUN] Would install task-verifier to %s\n", agentsDir)
		return nil
	}

	os.MkdirAll(agentsDir, 0755)

	src := filepath.Join("agents", "task-verifier.md")
	dest := filepath.Join(agentsDir, "task-verifier.md")

	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("read task-verifier: %w", err)
	}
	if err := os.WriteFile(dest, data, 0644); err != nil {
		return fmt.Errorf("write task-verifier: %w", err)
	}
	return nil
}

func generateVaultMap(opts Options) error {
	mapFile := filepath.Join(opts.SkillInstallDir, "config", "vault-map.json")
	if _, err := os.Stat(mapFile); err == nil && !opts.Force {
		fmt.Println("vault-map.json exists, skipping (use --force to overwrite)")
		return nil
	}

	config := map[string]interface{}{
		"obsidian_vault":        opts.ObsidianVault,
		"new_project_root":      opts.NewProjectRoot,
		"projects":              []interface{}{},
		"notifications":         map[string]interface{}{"desktop": opts.NotifyEnabled},
		"poll_interval_minutes": opts.PollIntervalMin,
	}

	if opts.DryRun {
		data, _ := json.MarshalIndent(config, "", "  ")
		fmt.Printf("[DRY RUN] Would write %s:\n%s\n", mapFile, string(data))
		return nil
	}

	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	os.MkdirAll(filepath.Dir(mapFile), 0755)
	return os.WriteFile(mapFile, data, 0644)
}

func createOMPSymlink(opts Options) error {
	linkPath := filepath.Join(os.Getenv("HOME"), ".omp", "agent", "skills", "obsidian-task-runner")

	if opts.DryRun {
		fmt.Printf("[DRY RUN] Would create symlink %s → %s\n", linkPath, opts.SkillInstallDir)
		return nil
	}

	os.MkdirAll(filepath.Dir(linkPath), 0755)
	os.Remove(linkPath) // remove old symlink/file
	return os.Symlink(opts.SkillInstallDir, linkPath)
}

func configureShell(opts Options) error {
	if opts.DryRun {
		fmt.Println("[DRY RUN] Would configure shell environment")
		return nil
	}

	home := os.Getenv("HOME")
	shell := filepath.Base(os.Getenv("SHELL"))
	if shell == "" {
		shell = "bash"
	}

	var rcFile string
	switch shell {
	case "zsh":
		rcFile = filepath.Join(home, ".zshrc")
	case "fish":
		rcFile = filepath.Join(home, ".config", "fish", "config.fish")
	default:
		rcFile = filepath.Join(home, ".bashrc")
	}

	os.MkdirAll(filepath.Dir(rcFile), 0755)

	// Check if already configured
	existing, _ := os.ReadFile(rcFile)
	if strings.Contains(string(existing), "OBSIDIAN_VAULT="+opts.ObsidianVault) {
		return nil // already configured
	}

	var exportLine string
	switch shell {
	case "fish":
		exportLine = fmt.Sprintf("set -Ux OBSIDIAN_VAULT %s\n", opts.ObsidianVault)
	default:
		exportLine = fmt.Sprintf("export OBSIDIAN_VAULT=%s\n", opts.ObsidianVault)
	}

	f, err := os.OpenFile(rcFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open %s: %w", rcFile, err)
	}
	defer f.Close()
	f.WriteString("\n# Obsidian Task Runner\n")
	f.WriteString(exportLine)
	return nil
}

func configureSystemd(opts Options) error {
	if opts.DryRun {
		fmt.Println("[DRY RUN] Would configure systemd units")
		return nil
	}

	home := os.Getenv("HOME")
	userDir := filepath.Join(home, ".config", "systemd", "user")
	os.MkdirAll(userDir, 0755)

	// Build PATH
	path := "/usr/local/bin:/usr/bin:/bin"
	if runtime.GOARCH == "amd64" {
		if d := filepath.Join(home, "go", "bin"); dirExists(d) {
			path = d + ":" + path
		}
		if d := filepath.Join(home, ".local", "bin"); dirExists(d) {
			path = d + ":" + path
		}
	}

	// Write service files
	services := map[string]string{
		"omp-task-runner.service": fmt.Sprintf(`[Unit]
Description=扫描 Obsidian Vault 并处理可执行的 OMP 任务(兜底轮询,由 timer 触发)

[Service]
Type=oneshot
Environment=OBSIDIAN_VAULT=%s
Environment=PATH=%s
ExecStart=%s/.local/bin/otg daemon --once
`, opts.ObsidianVault, path, home),
		"omp-task-runner.timer": fmt.Sprintf(`[Unit]
Description=Obsidian Task Runner 兜底轮询

[Timer]
OnBootSec=1min
OnUnitActiveSec=%dmin
RandomizedDelaySec=10

[Install]
WantedBy=timers.target
`, opts.PollIntervalMin),
		"omp-task-watcher.service": fmt.Sprintf(`[Unit]
Description=Obsidian Task Watcher — 监听 Projects/ 文件变化,触发 OMP 处理

[Service]
Type=simple
Environment=OBSIDIAN_VAULT=%s
Environment=PATH=%s
ExecStart=%s/.local/bin/otg daemon
Restart=on-failure
RestartSec=10

[Install]
WantedBy=default.target
`, opts.ObsidianVault, path, home),
	}

	for name, content := range services {
		if err := os.WriteFile(filepath.Join(userDir, name), []byte(content), 0644); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
	}

	// Enable and start
	if _, err := exec.LookPath("systemctl"); err == nil {
		exec.Command("systemctl", "--user", "daemon-reload").Run()
		exec.Command("systemctl", "--user", "enable", "--now", "omp-task-runner.timer").Run()
		if _, err := exec.LookPath("inotifywait"); err == nil {
			exec.Command("systemctl", "--user", "enable", "--now", "omp-task-watcher.service").Run()
		}
		fmt.Println("systemd units installed and enabled")
	}
	return nil
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
