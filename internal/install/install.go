// Package install provides the skill installation logic.
package install

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Options holds installation configuration.
type Options struct {
	ObsidianVault   string
	NewProjectRoot  string
	SkillInstallDir string
	NotifyEnabled   bool
	PollIntervalMin int
	SystemdEnabled  bool
	Force           bool
	DryRun          bool
	SrcDir          string // source directory with skill files
	RestartSystemd  bool   // stop daemon before install, restart after
}

// Run performs the installation.
func Run(opts Options) error {
	d := opts.DryRun
	// 0. Stop running daemon so binary/skills can be safely overwritten
	if opts.RestartSystemd && !d {
		stopDaemon()
	}

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

	// 5b. Install skill-doctor (dependency diagnostic tool)
	if err := installSkillDoctor(opts); err != nil && !d {
		return fmt.Errorf("skill-doctor: %w", err)
	}

	// 5c. Install skill registry
	if err := installRegistry(opts); err != nil && !d {
		return fmt.Errorf("skill registry: %w", err)
	}
	// 5d. Install phase sub-skills as top-level skills
	if err := installPhaseSkills(opts); err != nil && !d {
		return fmt.Errorf("phase skills: %w", err)
	}
	// 5e. Validate required external skills
	if !d {
		if missing, err := validateRequiredSkills(); err != nil {
			return fmt.Errorf("cannot check required skills: %w", err)
		} else if len(missing) > 0 {
			return fmt.Errorf("missing required external skills: %s\n\nInstall them with:\n  skill-doctor install %s",
				strings.Join(missing, ", "), strings.Join(missing, " "))
		}
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

	// 7b. Deploy dashboard template to vault (Dataview-powered)
	if !d && opts.ObsidianVault != "" {
		dst := filepath.Join(opts.ObsidianVault, "Tasks-Dashboard.md")
		srcFile := filepath.Join(opts.SrcDir, "Tasks-Dashboard.md")
		content, err := os.ReadFile(srcFile)
		if err != nil {
			content = []byte(`# 任务总览

> 从文件路径提取项目名，按 ` + "`project_id`" + ` 聚合。Dataview 插件自动刷新。

## 按项目汇总

` + "```dataview" + `
TABLE
  length(rows) AS "任务数",
  length(filter(rows, (r) => r.status = "ready")) AS "就绪",
  length(filter(rows, (r) => r.status = "implementing")) AS "实现中",
  length(filter(rows, (r) => r.status = "plan-review")) AS "待审阅",
  length(filter(rows, (r) => r.status = "review")) AS "待合并",
  length(filter(rows, (r) => r.status = "done")) AS "已完成",
  length(filter(rows, (r) => r.status = "blocked")) AS "阻塞"
FROM "Projects"
FLATTEN regexreplace(file.folder, "^Projects/(\d+)-.*$", "$1") AS project_id
FLATTEN regexreplace(file.folder, "^Projects/[^/]+/([^/]+)/.*$", "$1") AS category
WHERE project_id AND category = "Tasks"
GROUP BY project_id
SORT project_id ASC
` + "```")
		}
		os.WriteFile(dst, content, 0644)
		fmt.Println("dashboard deployed to vault")
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

	// Copy skill files (native Go copy for portability)
	if err := copyDir(src, dest); err != nil {
		return fmt.Errorf("copy skill: %w", err)
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
	if _, err := os.Stat(mapFile); err == nil {
		fmt.Println("vault-map.json exists, skipping (never overwritten — contains user config)")
		return nil
	}

	config := map[string]interface{}{
		"obsidian_vault":        opts.ObsidianVault,
		"new_project_root":      opts.NewProjectRoot,
		"projects":              []interface{}{},
		"models":                map[string]string{"default": "deepseek/deepseek-v4-flash"},
		"notifications":         map[string]interface{}{"desktop": opts.NotifyEnabled},
		"poll_interval_minutes": opts.PollIntervalMin,
		"max_concurrent_tasks":  2,
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
// installPhaseSkills copies the four phase sub-skills (refining, round1, round2, merge)
func installPhaseSkills(opts Options) error {
	home, _ := os.UserHomeDir()
	skillRoot := filepath.Join(home, ".omp", "skills")
	srcBase := opts.SrcDir
	if srcBase == "" {
		srcBase = "obsidian-task-runner"
	}
	phases := []struct{ name, srcRel string }{
		{"obsidian-task-runner-refining", "skills/refining/SKILL.md"},
		{"obsidian-task-runner-round1", "skills/round1/SKILL.md"},
		{"obsidian-task-runner-round2", "skills/round2/SKILL.md"},
		{"obsidian-task-runner-merge", "skills/merge/SKILL.md"},
	}
	if opts.DryRun {
		for _, p := range phases {
			fmt.Printf("[DRY RUN] Would install %s\n", p.name)
		}
		return nil
	}
	for _, p := range phases {
		src := filepath.Join(srcBase, p.srcRel)
		destDir := filepath.Join(skillRoot, p.name)
		dest := filepath.Join(destDir, "SKILL.md")
		os.MkdirAll(destDir, 0755)
		data, err := os.ReadFile(src)
		if err != nil {
			return fmt.Errorf("read %s: %w", src, err)
		}
		if err := os.WriteFile(dest, data, 0644); err != nil {
			return fmt.Errorf("write %s: %w", dest, err)
		}
		// Agent skill symlink
		agentDir := filepath.Join(home, ".omp", "agent", "skills")
		os.MkdirAll(agentDir, 0755)
		link := filepath.Join(agentDir, p.name)
		os.Remove(link)
		if err := os.Symlink(destDir, link); err != nil {
			return fmt.Errorf("symlink %s → %s: %w", link, destDir, err)
		}
		fmt.Printf("phase skill installed: %s\n", p.name)
	}
	return nil
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
	for _, line := range strings.Split(string(existing), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "export OBSIDIAN_VAULT="+opts.ObsidianVault {
			return nil // already configured
		}
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
	if d := filepath.Join(home, "go", "bin"); dirExists(d) {
		path = d + ":" + path
	}
	if d := filepath.Join(home, ".local", "bin"); dirExists(d) {
		path = d + ":" + path
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
		if out, err := exec.Command("systemctl", "--user", "daemon-reload").CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: systemctl daemon-reload failed: %v\n%s\n", err, out)
		}
		if out, err := exec.Command("systemctl", "--user", "enable", "--now", "omp-task-runner.timer").CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: systemctl enable timer failed: %v\n%s\n", err, out)
		}
		if _, err := exec.LookPath("inotifywait"); err == nil {
			if out, err := exec.Command("systemctl", "--user", "enable", "--now", "omp-task-watcher.service").CombinedOutput(); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: systemctl enable watcher failed: %v\n%s\n", err, out)
			}
		}
		fmt.Println("systemd units installed and enabled")
	}
	return nil
}

// installSkillDoctor copies the skill-doctor script to ~/.omp/bin/.
func installSkillDoctor(opts Options) error {
	home, _ := os.UserHomeDir()
	ompRoot := filepath.Join(home, ".omp")
	destDir := filepath.Join(ompRoot, "bin")
	dest := filepath.Join(destDir, "skill-doctor")
	src := filepath.Join("scripts", "skill-doctor")

	if opts.DryRun {
		fmt.Printf("[DRY RUN] Would copy %s → %s\n", src, dest)
		return nil
	}

	os.MkdirAll(destDir, 0755)
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("read skill-doctor: %w", err)
	}
	if err := os.WriteFile(dest, data, 0644); err != nil {
		return fmt.Errorf("write skill-doctor: %w", err)
	}
	os.Chmod(dest, 0755)
	fmt.Println("skill-doctor installed to", dest)
	return nil
}

// installRegistry copies the skill registry to ~/.omp/config/.
func installRegistry(opts Options) error {
	home, _ := os.UserHomeDir()
	ompRoot := filepath.Join(home, ".omp")
	destDir := filepath.Join(ompRoot, "config")
	dest := filepath.Join(destDir, "skill-registry.json")
	src := filepath.Join("config", "skill-registry.json")

	if opts.DryRun {
		fmt.Printf("[DRY RUN] Would copy %s → %s\n", src, dest)
		return nil
	}

	os.MkdirAll(destDir, 0755)
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("read skill registry: %w", err)
	}
	if err := os.WriteFile(dest, data, 0644); err != nil {
		return fmt.Errorf("write skill registry: %w", err)
	}
	fmt.Println("skill registry installed to", dest)
	return nil
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// copyDir recursively copies a directory tree from src to dst using native Go I/O.
func copyDir(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return fmt.Errorf("read dir %s: %w", src, err)
	}
	if err := os.MkdirAll(dst, 0755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dst, err)
	}
	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())
		if entry.IsDir() {
			if err := copyDir(srcPath, dstPath); err != nil {
				return err
			}
		} else {
			data, err := os.ReadFile(srcPath)
			if err != nil {
				return fmt.Errorf("read %s: %w", srcPath, err)
			}
			if err := os.WriteFile(dstPath, data, 0644); err != nil {
				return fmt.Errorf("write %s: %w", dstPath, err)
			}
		}
	}
	return nil
}


// validateRequiredSkills checks that the five mandatory external skills exist
// on disk. Returns the list of missing skill names.
func validateRequiredSkills() ([]string, error) {
	required := []string{
		"requirement-elaborator",
		"grilling",
		"domain-modeling",
		"diagnosing-bugs",
		"test-quality",
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve home: %w", err)
	}
	searchDirs := []string{
		filepath.Join(home, ".omp", "skills"),
		filepath.Join(home, ".omp", "agent", "skills"),
		filepath.Join(home, ".agents", "skills"),
	}
	var missing []string
	for _, name := range required {
		found := false
		for _, dir := range searchDirs {
			if _, err := os.Stat(filepath.Join(dir, name, "SKILL.md")); err == nil {
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, name)
		}
	}
	return missing, nil
}

// stopDaemon gracefully stops any running otg daemon processes.
func stopDaemon() {
	// Stop systemd units
	exec.Command("systemctl", "--user", "stop", "--no-block", "omp-task-runner.timer").Run()
	exec.Command("systemctl", "--user", "stop", "--no-block", "omp-task-watcher.service").Run()

	// SIGTERM for graceful shutdown
	exec.Command("pkill", "-TERM", "-U", fmt.Sprintf("%d", os.Getuid()), "-f", "otg daemon").Run()

	// Give processes time to exit, then force kill
	time.Sleep(2 * time.Second)
	exec.Command("pkill", "-9", "-U", fmt.Sprintf("%d", os.Getuid()), "-f", "otg daemon").Run()
	time.Sleep(1 * time.Second)

	// Clean up stale lock files (daemon acquires vault-hash locks)
	entries, _ := os.ReadDir(os.TempDir())
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "otg-daemon-") && strings.HasSuffix(e.Name(), ".lock") {
			os.Remove(filepath.Join(os.TempDir(), e.Name()))
		}
	}
}
