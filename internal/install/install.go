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

	"github.com/ndzuki/obsidian-task-runner/internal/config"
	"github.com/ndzuki/obsidian-task-runner/internal/jsonorder"
	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
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

	// 1. Check dependencies（executor 默认 dsh，无其他执行器依赖）
	for _, bin := range []string{"git"} {
		if _, err := exec.LookPath(bin); err != nil {
			return fmt.Errorf("missing dependency: %s", bin)
		}
	}

	// 1.5 Build the otg binary itself (versioned via ldflags) so `otg
	// version` reports the release instead of dev/unknown. Daemon is already
	// stopped, so the target can be atomically replaced.
	if err := buildOTGBinary(opts); err != nil && !d {
		return err
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
		if err := os.MkdirAll(filepath.Join(opts.ObsidianVault, "Projects"), 0755); err != nil {
			return fmt.Errorf("create Projects dir: %w", err)
		}
		if err := os.MkdirAll(opts.NewProjectRoot, 0755); err != nil {
			return fmt.Errorf("create project root %s: %w", opts.NewProjectRoot, err)
		}
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
		if err := os.WriteFile(dst, content, 0644); err != nil {
			return fmt.Errorf("write dashboard to %s: %w", dst, err)
		}
	}

	// 8. Configure systemd
	if opts.SystemdEnabled {
		if err := ConfigureSystemd(opts); err != nil && !d {
			return fmt.Errorf("systemd: %w", err)
		}
	}

	if d {
		fmt.Println("[DRY RUN] Installation preview complete")
	}
	return nil
}

// buildOTGBinary compiles the otg binary with version ldflags into the
// daemon's expected path (~/.local/bin/otg, matching the systemd ExecStart).
// It runs from the source checkout (install is invoked there, consistent
// with installSkill's relative paths), so git describe/rev-parse provide the
// release identity. The build writes a temp file and renames over the target
// so a running binary can be replaced atomically. A failed build warns and
// keeps the previous binary — skill/systemd installation must not be blocked
// by an unavailable toolchain.
func buildOTGBinary(opts Options) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	dest := filepath.Join(home, ".local", "bin", "otg")
	if opts.DryRun {
		fmt.Printf("[DRY RUN] Would build otg to %s (version ldflags from git)\n", dest)
		return nil
	}
	if _, err := exec.LookPath("go"); err != nil {
		fmt.Fprintf(os.Stderr, "warning: go not found, keeping existing otg binary at %s\n", dest)
		return nil
	}
	version := "dev"
	commit := "unknown"
	if out, err := exec.Command("git", "describe", "--tags", "--always", "--dirty").Output(); err == nil {
		version = strings.TrimSpace(string(out))
	}
	if out, err := exec.Command("git", "rev-parse", "--short", "HEAD").Output(); err == nil {
		commit = strings.TrimSpace(string(out))
	}
	ldflags := fmt.Sprintf("-X main.Version=%s -X main.Commit=%s", version, commit)
	tmp := dest + ".tmp"
	// -tags sqlite_fts5 是强制项：知识库检索库依赖 SQLite FTS5（mattn/go-sqlite3
	// 的 opt-in 编译宏），漏掉 tag 的二进制能编译能跑但所有 kb 命令报
	// "no such module: fts5"。与 Makefile build 目标保持强制一致。
	cmd := exec.Command("go", "build", "-tags", "sqlite_fts5", "-ldflags", ldflags, "-o", tmp, "./cmd/otg/")
	output, buildErr := cmd.CombinedOutput()
	if buildErr != nil {
		_ = os.Remove(tmp)
		fmt.Fprintf(os.Stderr, "warning: otg build failed (%v), keeping existing binary: %s\n", buildErr, strings.TrimSpace(string(output)))
		return nil
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		fmt.Fprintf(os.Stderr, "warning: cannot replace otg binary: %v\n", err)
		return nil
	}
	fmt.Printf("otg binary installed: %s (%s)\n", dest, version)
	return nil
}

// installSkill copies the obsidian-task-runner skill tree into the install dir.
func installSkill(opts Options) error {
	dest := opts.SkillInstallDir
	src := opts.SrcDir
	if src == "" {
		src = "obsidian-task-runner"
	}
	if opts.DryRun {
		fmt.Printf("[DRY RUN] Would copy %s → %s\n", src, dest)
		return nil
	}

	// Protect user config: backup vault-map.json before any destructive operation.
	vaultMapPath := filepath.Join(dest, "config", "vault-map.json")
	var savedVaultMap []byte
	if data, err := os.ReadFile(vaultMapPath); err == nil {
		savedVaultMap = data
	}

	// Remove old installation if forced, then restore user config.
	if opts.Force {
		if err := os.RemoveAll(dest); err != nil {
			return fmt.Errorf("remove old installation %s: %w", dest, err)
		}
	}

	// Create parent dir
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return fmt.Errorf("create parent dir %s: %w", filepath.Dir(dest), err)
	}

	// Copy skill files (native Go copy for portability)
	if err := copyDir(src, dest); err != nil {
		return fmt.Errorf("copy skill: %w", err)
	}

	// Restore user's vault-map.json — never overwritten by install.
	if savedVaultMap != nil {
		if err := os.MkdirAll(filepath.Dir(vaultMapPath), 0755); err != nil {
			return fmt.Errorf("restore vault-map dir: %w", err)
		}
		if err := os.WriteFile(vaultMapPath, savedVaultMap, 0644); err != nil {
			return fmt.Errorf("restore vault-map: %w", err)
		}
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

	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		return fmt.Errorf("create agents dir %s: %w", agentsDir, err)
	}

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

	// Minimal fresh-install config: only what a new operator must decide.
	// Everything else (tuning, off-peak, KB backends, env cleanup, memory
	// gate) ships as code defaults and is visible via `otg config show
	// --effective` / docs/config-reference.md.
	models := map[string]string{}
	for k, v := range config.DefaultModels() {
		models[k] = v
	}
	cfg := map[string]interface{}{
		"obsidian_vault":   opts.ObsidianVault,
		"new_project_root": opts.NewProjectRoot,
		"projects":         []interface{}{},
		"models":           models,
		"notifications":    map[string]interface{}{"desktop": opts.NotifyEnabled},
		// 0 = no global cap; per-project capacity governs (default 2).
		"max_concurrent_tasks":             0,
		"max_concurrent_tasks_per_project": 2,
		"poll_interval_minutes":            opts.PollIntervalMin,
		"agent_server_managed":             true,
	}
	// Field order is deliberate: essentials first, "projects" pinned last —
	// appending a new project is the most frequent manual edit.
	orderedKeys := []string{
		"agent_server_managed", "max_concurrent_tasks",
		"max_concurrent_tasks_per_project", "models", "new_project_root",
		"notifications", "obsidian_vault", "poll_interval_minutes", "projects",
	}
	// Merge new defaults into existing config — never overwrite user values,
	// and never reshuffle the user's hand-curated field order.
	if existing, err := os.ReadFile(mapFile); err == nil {
		obj, err := jsonorder.Parse(existing)
		if err == nil {
			for _, k := range orderedKeys {
				if _, exists := obj.Get(k); !exists {
					obj.Set(k, cfg[k])
				}
			}
			// Back up raw bytes before writing to prevent data loss on crash.
			backup := existing
			newData, err := jsonorder.Marshal(obj)
			if err == nil {
				newData = append(newData, '\n')
				if err := yamlfrontmatter.AtomicWrite(mapFile, newData); err != nil {
					// Restore backup on failure
					if restoreErr := os.WriteFile(mapFile, backup, 0644); restoreErr != nil {
						return fmt.Errorf("atomic write vault-map: %w (restore backup: %v)", err, restoreErr)
					}
					return fmt.Errorf("atomic write vault-map: %w", err)
				}
				fmt.Println("vault-map.json updated with new defaults")
				return nil
			}
		}
		fmt.Println("vault-map.json exists but unparseable, skipping (never overwritten)")
		return nil
	}

	// No existing file — create fresh atomically with the ordered layout.
	obj := &jsonorder.OrderedJSON{}
	for _, k := range orderedKeys {
		obj.Set(k, cfg[k])
	}
	if opts.DryRun {
		data, _ := jsonorder.Marshal(obj)
		fmt.Printf("[DRY RUN] Would create vault-map.json:\n%s\n", string(data))
		return nil
	}
	data, _ := jsonorder.Marshal(obj)
	data = append(data, '\n')

	if err := os.MkdirAll(filepath.Dir(mapFile), 0755); err != nil {
		return fmt.Errorf("create vault-map dir: %w", err)
	}
	return os.WriteFile(mapFile, data, 0644)
}

// installPhaseSkills copies bundled phase and sidecar skills as top-level
// skills into the DSH skill directory (~/.dsh/skills). DSH discovers skills by
// scanning that directory directly — no agent symlink layer is needed.
func installPhaseSkills(opts Options) error {
	home, _ := os.UserHomeDir()
	skillRoot := filepath.Join(home, ".dsh", "skills")
	srcBase := opts.SrcDir
	if srcBase == "" {
		srcBase = "obsidian-task-runner"
	}
	// Phase skill list comes from skills/manifest — the single source shared
	// with Makefile sync-docs; add a new phase skill there only.
	manifest, err := os.ReadFile(filepath.Join(srcBase, "skills", "manifest"))
	if err != nil {
		return fmt.Errorf("read phase skill manifest: %w", err)
	}
	var phases []struct{ name, srcRel string }
	for _, line := range strings.Split(string(manifest), "\n") {
		name := strings.TrimSpace(line)
		if name == "" || strings.HasPrefix(name, "#") {
			continue
		}
		phases = append(phases, struct{ name, srcRel string }{
			name:   "obsidian-task-runner-" + name,
			srcRel: filepath.Join("skills", name, "SKILL.md"),
		})
	}
	if opts.DryRun {
		for _, phase := range phases {
			fmt.Printf("[DRY RUN] Would install %s\n", phase.name)
		}
		return nil
	}
	for _, phase := range phases {
		src := filepath.Join(srcBase, phase.srcRel)
		destDir := filepath.Join(skillRoot, phase.name)
		dest := filepath.Join(destDir, "SKILL.md")
		if err := os.MkdirAll(destDir, 0o755); err != nil {
			return fmt.Errorf("create phase skill dir %s: %w", destDir, err)
		}
		data, err := os.ReadFile(src)
		if err != nil {
			return fmt.Errorf("read %s: %w", src, err)
		}
		if err := os.WriteFile(dest, data, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", dest, err)
		}
		fmt.Printf("phase skill installed: %s\n", phase.name)
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

	if err := os.MkdirAll(filepath.Dir(rcFile), 0755); err != nil {
		return fmt.Errorf("create shell config dir: %w", err)
	}

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
	if _, err := f.WriteString("\n# Obsidian Task Runner\n"); err != nil {
		closeErr := f.Close()
		if closeErr != nil {
			return fmt.Errorf("write header to %s: %w (close: %v)", rcFile, err, closeErr)
		}
		return fmt.Errorf("write header to %s: %w", rcFile, err)
	}
	if _, err := f.WriteString(exportLine); err != nil {
		closeErr := f.Close()
		if closeErr != nil {
			return fmt.Errorf("write export line to %s: %w (close: %v)", rcFile, err, closeErr)
		}
		return fmt.Errorf("write export line to %s: %w", rcFile, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", rcFile, err)
	}
	return nil
}

// ConfigureSystemd 将用户 systemd 单元写入 ~/.config/systemd/user 并启用，
// 供 otg install 与 otg install-systemd 使用。
func ConfigureSystemd(opts Options) error {
	if opts.DryRun {
		fmt.Println("[DRY RUN] Would configure systemd units")
		return nil
	}

	home := os.Getenv("HOME")
	userDir := filepath.Join(home, ".config", "systemd", "user")
	if err := os.MkdirAll(userDir, 0755); err != nil {
		return fmt.Errorf("create systemd user dir: %w", err)
	}

	// Build PATH for the units. Conventional user dirs first, then mise
	// shims: dsh is installed under ~/.local/share/mise/installs/... and
	// exposed via the shims dir, so without it the daemon cannot exec dsh
	// (observed: "exec: dsh: executable file not found in $PATH" every scan,
	// starving every implementing task behind the failed slots).
	path := buildSystemdPath(home)

	// dsh is executed by absolute path, not via PATH lookup: systemd fails
	// to resolve bare `dsh` through the mise shims dir (a symlink-only
	// directory) with "Unable to locate executable 'dsh': No such file or
	// directory" (status=203/EXEC, observed systemd 261). The bin.js shebang
	// (`#!/usr/bin/env node`) still resolves node via PATH, so the node
	// install bin dir is prepended in buildSystemdPath.
	dshExec := "dsh"
	if abs := filepath.Join(home, ".local", "share", "mise", "installs", "node", "latest", "bin", "dsh"); fileExists(abs) {
		dshExec = abs
	}

	// agent_server_managed 决定 agent-server 所有权，systemd 单元必须跟随，
	// 否则形成死锁（2026-08-31 事故）：
	//   - managed=true（daemon 自管，默认）：watcher 不得 Requires dsh-agent-server，
	//     也不启用该 service——否则每次 watcher 启动，systemd 强制拉起
	//     dsh-agent-server（Requires + Restart=always）抢占 8799，daemon 自管
	//     agent-server 永远 bind 失败、健康检查误连外部实例、任务审计冻结。
	//   - managed=false（systemd 外部管理）：watcher Requires dsh-agent-server，
	//     等它 healthy 后 watcher 再启动（现状）。
	agentServerManaged := true
	if mapFile := filepath.Join(home, ".dsh", "skills", "obsidian-task-runner", "config", "vault-map.json"); fileExists(mapFile) {
		if data, err := os.ReadFile(mapFile); err == nil {
			var m map[string]any
			if json.Unmarshal(data, &m) == nil {
				if v, ok := m["agent_server_managed"].(bool); ok {
					agentServerManaged = v
				}
			}
		}
	}

	// Write service files. dsh-web is an independent user service.
	// dsh-agent-server + otg-task-watcher 的关系由 agent_server_managed 决定
	//（见上）：managed=false 时 watcher 依赖外部 systemd 的 agent-server。
	watcherRequires := ""
	enableUnits := []string{"dsh-web.service", "otg-task-watcher.service"}
	if !agentServerManaged {
		watcherRequires = "After=dsh-agent-server.service\nRequires=dsh-agent-server.service\n"
		enableUnits = []string{"dsh-agent-server.service", "dsh-web.service", "otg-task-watcher.service"}
	}
	services := map[string]string{
		"dsh-agent-server.service": fmt.Sprintf(`[Unit]
Description=DSH Agent Server (headless-agent-server for obsidian-task-runner)
After=network.target

[Service]
Type=simple
Environment=PATH=%s
Environment=XDG_RUNTIME_DIR=/run/user/%%U
Environment=DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/%%U/bus
ExecStart=%s --profile headless-agent-server
Restart=always
RestartSec=3
TimeoutStartSec=30

[Install]
WantedBy=default.target
`, path, dshExec),
		"dsh-web.service": fmt.Sprintf(`[Unit]
Description=DSH Web UI
After=network.target

[Service]
Type=simple
Environment=PATH=%s
Environment=XDG_RUNTIME_DIR=/run/user/%%U
Environment=DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/%%U/bus
ExecStart=%s --profile web
Restart=always
RestartSec=3
TimeoutStartSec=30

[Install]
WantedBy=default.target
`, path, dshExec),
		"otg-task-watcher.service": fmt.Sprintf(`[Unit]
Description=Obsidian Task Watcher — 监听 Projects/ 文件变化,触发任务处理
%s[Service]
Type=simple
Environment=OBSIDIAN_VAULT=%s
Environment=PATH=%s
Environment=XDG_RUNTIME_DIR=/run/user/%%U
Environment=DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/%%U/bus
ExecStart=%s/.local/bin/otg daemon
Restart=on-failure
RestartSec=10

[Install]
WantedBy=default.target
`, watcherRequires, opts.ObsidianVault, path, home),
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
		for _, unit := range enableUnits {
			if out, err := exec.Command("systemctl", "--user", "enable", "--now", unit).CombinedOutput(); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: systemctl enable %s failed: %v\n%s\n", unit, err, out)
			}
		}
		// managed=true：确保 dsh-agent-server 不常驻（避免抢占 8799）。
		if agentServerManaged {
			if out, err := exec.Command("systemctl", "--user", "disable", "--now", "dsh-agent-server.service").CombinedOutput(); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: systemctl disable dsh-agent-server failed: %v\n%s\n", err, out)
			}
		}
		fmt.Println("systemd units installed and enabled")
	}
	return nil
}

// installSkillDoctor copies the skill-doctor script to ~/.dsh/bin/.
func installSkillDoctor(opts Options) error {
	home, _ := os.UserHomeDir()
	ompRoot := filepath.Join(home, ".dsh")
	destDir := filepath.Join(ompRoot, "bin")
	dest := filepath.Join(destDir, "skill-doctor")
	src := filepath.Join("scripts", "skill-doctor")

	if opts.DryRun {
		fmt.Printf("[DRY RUN] Would copy %s → %s\n", src, dest)
		return nil
	}

	if err := os.MkdirAll(destDir, 0755); err != nil {
		return fmt.Errorf("create skill-doctor dir: %w", err)
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("read skill-doctor: %w", err)
	}
	if err := os.WriteFile(dest, data, 0644); err != nil {
		return fmt.Errorf("write skill-doctor: %w", err)
	}
	if err := os.Chmod(dest, 0755); err != nil {
		return fmt.Errorf("chmod skill-doctor: %w", err)
	}
	fmt.Println("skill-doctor installed to", dest)
	return nil
}

// installRegistry copies the skill registry to ~/.dsh/config/.
func installRegistry(opts Options) error {
	home, _ := os.UserHomeDir()
	ompRoot := filepath.Join(home, ".dsh")
	destDir := filepath.Join(ompRoot, "config")
	dest := filepath.Join(destDir, "skill-registry.json")
	src := filepath.Join("config", "skill-registry.json")

	if opts.DryRun {
		fmt.Printf("[DRY RUN] Would copy %s → %s\n", src, dest)
		return nil
	}

	if err := os.MkdirAll(destDir, 0755); err != nil {
		return fmt.Errorf("create skill registry dir: %w", err)
	}
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

// buildSystemdPath assembles the PATH for systemd units: the mise node
// install bin dir (real `node` binary for the bin.js shebang), user bin dirs
// (go/bin, .local/bin) and mise shims take precedence over system dirs.
// Only existing directories are included, mirroring an interactive shell.
func buildSystemdPath(home string) string {
	path := "/usr/local/bin:/usr/bin:/bin"
	for _, d := range []string{
		filepath.Join(home, ".local", "share", "mise", "installs", "node", "latest", "bin"),
		filepath.Join(home, "go", "bin"),
		filepath.Join(home, ".local", "bin"),
		filepath.Join(home, ".local", "share", "mise", "shims"),
	} {
		if dirExists(d) {
			path = d + ":" + path
		}
	}
	return path
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
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
		"knowledge-base",
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve home: %w", err)
	}
	searchDirs := []string{
		filepath.Join(home, ".dsh", "skills"),
		filepath.Join(home, ".dsh", "skills"),
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
	// DSH era has no polling timer unit: the daemon is a watcher service only.
	// Wait for the watcher to finish its graceful shutdown (daemon SIGTERM →
	// session save → exit). Blocking here also serializes with the later
	// enable --now, so the new instance never races the old one.
	runBestEffort("stop task watcher", "systemctl", "--user", "stop", "otg-task-watcher.service")

	// Residual daemons not managed by systemd: terminate, then force kill.
	// (With the systemd stop above completed, these are normally no-ops.)
	runBestEffort("terminate daemon", "pkill", "-TERM", "-U", fmt.Sprintf("%d", os.Getuid()), "-f", "otg daemon")

	// Give processes time to exit, then force kill.
	time.Sleep(2 * time.Second)
	runBestEffort("kill daemon", "pkill", "-9", "-U", fmt.Sprintf("%d", os.Getuid()), "-f", "otg daemon")
	time.Sleep(1 * time.Second)

	// Clean up stale lock files (daemon acquires vault-hash locks).
	entries, err := os.ReadDir(os.TempDir())
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: read temp dir for stale locks: %v\n", err)
		return
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "otg-daemon-") && strings.HasSuffix(e.Name(), ".lock") {
			if err := os.Remove(filepath.Join(os.TempDir(), e.Name())); err != nil && !os.IsNotExist(err) {
				fmt.Fprintf(os.Stderr, "warning: remove stale daemon lock %s: %v\n", e.Name(), err)
			}
		}
	}
}

func runBestEffort(action, name string, args ...string) {
	if err := exec.Command(name, args...).Run(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: %s: %v\n", action, err)
	}
}
