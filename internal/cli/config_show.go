package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ndzuki/obsidian-task-runner/internal/config"
	"github.com/spf13/cobra"
)

var (
	configShowEffective bool
	configShowRedact    bool
	configShowJSON      bool
)

// configCmd is the parent for config subcommands.
var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Manage and inspect configuration",
	Long:  `Subcommands for viewing and modifying the task-runner configuration.`,
}

var configShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Show effective configuration",
	Long: `Displays the current configuration with source annotations.

With --effective (default), shows the resolved values after merging
defaults, vault-map.json, and environment variables.

With --redact, replaces home-directory prefixes with ~ to mask
user-identifying paths.`,
	RunE: runConfigShow,
}

type configEntry struct {
	Key    string `json:"key"`
	Value  string `json:"value"`
	Source string `json:"source"` // default | file | env
}

func runConfigShow(cmd *cobra.Command, args []string) error {
	defaults := config.Defaults()
	cfg, err := config.Load(daemonMapFile)
	if err != nil {
		return err
	}

	home, _ := os.UserHomeDir()
	redact := func(s string) string {
		if configShowRedact && home != "" {
			return strings.Replace(s, home, "~", 1)
		}
		return s
	}

	// Determine the effective map-file path for source annotations.
	effectiveMapFile := daemonMapFile
	if effectiveMapFile == "" {
		effectiveMapFile = filepath.Join(home, ".omp", "skills", "obsidian-task-runner", "config", "vault-map.json")
	}
	fileExists := false
	if _, err := os.Stat(effectiveMapFile); err == nil {
		fileExists = true
	}

	entries := []configEntry{
		mkEntry("obsidian_vault", cfg.ObsidianVault, defaults.ObsidianVault, "OBSIDIAN_VAULT", fileExists),
		mkEntry("new_project_root", cfg.NewProjectRoot, defaults.NewProjectRoot, "", fileExists),
		mkEntry("poll_interval_minutes", fmt.Sprintf("%d", cfg.PollIntervalMin), fmt.Sprintf("%d", defaults.PollIntervalMin), "", fileExists),
		mkEntry("max_concurrent_tasks", fmt.Sprintf("%d", cfg.MaxConcurrentTasks), fmt.Sprintf("%d", defaults.MaxConcurrentTasks), "", fileExists),
		mkEntry("omp_cmd", cfg.OMPCmd, defaults.OMPCmd, "OMP_CMD", fileExists),
		mkEntry("log_dir", cfg.LogDir, defaults.LogDir, "", fileExists),
		mkEntry("skill_install_dir", cfg.SkillInstallDir, defaults.SkillInstallDir, "", fileExists),
		mkEntry("notifications.desktop", fmt.Sprintf("%v", cfg.Notifications.Desktop), fmt.Sprintf("%v", defaults.Notifications.Desktop), "", fileExists),
	}

	// Add project entries.
	for i, p := range cfg.Projects {
		key := fmt.Sprintf("projects[%d].name", i)
		entries = append(entries, configEntry{Key: key, Value: p.Name, Source: "file"})
		key = fmt.Sprintf("projects[%d].path", i)
		entries = append(entries, configEntry{Key: key, Value: redact(p.Path), Source: "file"})
		if p.GitRemote != "" {
			key = fmt.Sprintf("projects[%d].git_remote", i)
			entries = append(entries, configEntry{Key: key, Value: p.GitRemote, Source: "file"})
		}
		if p.ProjectID != "" {
			key = fmt.Sprintf("projects[%d].project_id", i)
			entries = append(entries, configEntry{Key: key, Value: p.ProjectID, Source: "file"})
		}
	}

	// Add model entries (sorted for deterministic output).
	if len(cfg.Models) > 0 {
		modelKeys := make([]string, 0, len(cfg.Models))
		for k := range cfg.Models {
			modelKeys = append(modelKeys, k)
		}
		sort.Strings(modelKeys)
		for _, k := range modelKeys {
			entries = append(entries, configEntry{
				Key:    "models." + k,
				Value:  cfg.Models[k],
				Source: "file",
			})
		}
	}

	if configShowRedact {
		for i := range entries {
			entries[i].Value = redact(entries[i].Value)
		}
	}
	if configShowJSON {
		data, _ := json.MarshalIndent(entries, "", "  ")
		fmt.Fprintln(cmd.OutOrStdout(), string(data))
		return nil
	}

	// Plain-text output: align with the widest key.
	maxKey := 0
	for _, e := range entries {
		if len(e.Key) > maxKey {
			maxKey = len(e.Key)
		}
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%-*s  %-8s  VALUE\n", maxKey, "KEY", "SOURCE")
	fmt.Fprintf(cmd.OutOrStdout(), "%s  %s  %s\n", strings.Repeat("-", maxKey), "--------", "-----")
	for _, e := range entries {
		fmt.Fprintf(cmd.OutOrStdout(), "%-*s  %-8s  %s\n", maxKey, e.Key, e.Source, redact(e.Value))
	}
	return nil
}

// mkEntry builds a configEntry with source detection.
func mkEntry(key, cfgVal, defaultVal, envKey string, fileExists bool) configEntry {
	present := "default"
	if os.Getenv(envKey) != "" {
		present = "env"
	} else if fileExists && cfgVal != defaultVal {
		present = "file"
	} else if fileExists {
		present = "file"
	}
	return configEntry{Key: key, Value: cfgVal, Source: present}
}

func init() {
	configShowCmd.Flags().BoolVar(&configShowEffective, "effective", true, "Show effective (resolved) values")
	configShowCmd.Flags().BoolVar(&configShowRedact, "redact", false, "Mask home-directory prefixes")
	configShowCmd.Flags().BoolVar(&configShowJSON, "json", false, "Output in JSON format")
	configCmd.Flags().StringVar(&daemonMapFile, "map-file", "", "Path to vault-map.json")
	configCmd.AddCommand(configShowCmd)
	rootCmd.AddCommand(configCmd)
}
