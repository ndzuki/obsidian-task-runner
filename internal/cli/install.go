package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"

	"github.com/ndzuki/obsidian-task-runner/internal/install"
	"github.com/spf13/cobra"
)

var (
	installForce          bool
	installDryRun         bool
	installVault          string
	installNewRoot        string
	installNotif          bool
	installPoll           int
	installSystemd        bool
	installConfigureShell bool
)

var installCmd = &cobra.Command{
	Use:   "install",
	Short: "Install skill to ~/.dsh/skills/ and configure systemd",
	Long: `Installs the obsidian-task-runner skill to ~/.dsh/skills/,
generates vault-map.json, optionally configures the shell environment
(--configure-shell, opt-in), and
optionally registers the DSH systemd units (dsh-agent-server,
dsh-web, otg-task-watcher).

Environment variables can also be used to configure installation:
  OBSIDIAN_VAULT, NEW_PROJECT_ROOT, NOTIFY_ENABLED,
  POLL_INTERVAL_MINUTES, SYSTEMD_ENABLED, SKILL_INSTALL_DIR`,
	RunE: runInstall,
}

func runInstall(cmd *cobra.Command, args []string) error {
	home, _ := os.UserHomeDir()

	skillDir := os.Getenv("SKILL_INSTALL_DIR")
	if skillDir == "" {
		skillDir = filepath.Join(home, ".dsh", "skills", "obsidian-task-runner")
	}

	vault := resolveInstallVault(skillDir, installVault)

	newRoot := installNewRoot
	if v := os.Getenv("NEW_PROJECT_ROOT"); v != "" && newRoot == "" {
		newRoot = v
	}
	if newRoot == "" {
		newRoot = filepath.Join(home, "src")
	}

	if v := os.Getenv("NOTIFY_ENABLED"); v != "" {
		installNotif, _ = strconv.ParseBool(v)
	}
	if v := os.Getenv("POLL_INTERVAL_MINUTES"); v != "" {
		installPoll, _ = strconv.Atoi(v)
	}
	if v := os.Getenv("SYSTEMD_ENABLED"); v != "" {
		installSystemd, _ = strconv.ParseBool(v)
	}

	opts := install.Options{
		ObsidianVault:   vault,
		NewProjectRoot:  newRoot,
		SkillInstallDir: skillDir,
		NotifyEnabled:   installNotif,
		PollIntervalMin: installPoll,
		SystemdEnabled:  installSystemd,
		ConfigureShell:  installConfigureShell,
		Force:           installForce,
		DryRun:          installDryRun,
		RestartSystemd:  installSystemd,
	}

	return install.Run(opts)
}

// readVaultMap 从已有 vault-map.json 提取用户的 vault 路径与轮询间隔。
// 读取失败时忽略错误——安装流程不能因配置问题失败。
func readVaultMap(skillDir string) (vault string, pollMin int) {
	data, err := os.ReadFile(filepath.Join(skillDir, "config", "vault-map.json"))
	if err != nil {
		return "", 0
	}
	var existing struct {
		ObsidianVault   string `json:"obsidian_vault"`
		PollIntervalMin int    `json:"poll_interval_minutes"`
	}
	if json.Unmarshal(data, &existing) != nil {
		return "", 0
	}
	return existing.ObsidianVault, existing.PollIntervalMin
}

// resolveInstallVault 确定 vault 路径，优先级：
// flag > OBSIDIAN_VAULT 环境变量 > 已有 vault-map.json > 通用默认路径。
// 优先采用已有配置而非默认值：重装绝不能悄悄把 daemon 指向不同的 vault
// （回归教训：otg install 曾把 systemd 单元的 OBSIDIAN_VAULT 改写为
// ~/Documents/Obsidian/MainVault，而真实 vault 配置在别处）。
func resolveInstallVault(skillDir, vaultFlag string) string {
	vault := vaultFlag
	if v := os.Getenv("OBSIDIAN_VAULT"); v != "" && vault == "" {
		vault = v
	}
	if vault == "" {
		vault, _ = readVaultMap(skillDir)
	}
	if vault == "" {
		home, _ := os.UserHomeDir()
		vault = filepath.Join(home, "Documents", "Obsidian", "MainVault")
	}
	return vault
}

func init() {
	installCmd.Flags().BoolVarP(&installDryRun, "dry-run", "n", false, "Preview changes without applying")
	installCmd.Flags().BoolVar(&installForce, "force", false, "Force overwrite of all files")
	installCmd.Flags().StringVar(&installVault, "vault", "", "Obsidian vault path")
	installCmd.Flags().StringVar(&installNewRoot, "new-project-root", "", "New project root directory")
	installCmd.Flags().BoolVar(&installNotif, "notifications", true, "Enable desktop notifications")
	installCmd.Flags().IntVar(&installPoll, "poll-interval", 30, "Polling interval in minutes")
	installCmd.Flags().BoolVar(&installSystemd, "systemd", true, "Register systemd units")
	installCmd.Flags().BoolVar(&installConfigureShell, "configure-shell", false, "Opt-in: append OBSIDIAN_VAULT to shell rc (default never touches shell config)")
	rootCmd.AddCommand(installCmd)
}
