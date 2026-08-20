package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ndzuki/obsidian-task-runner/internal/config"
	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
	"github.com/spf13/cobra"
)

func newRootCommand(_ string) *cobra.Command {
	root := &cobra.Command{
		Use:           "otg",
		Short:         "Obsidian Task Runner — Go implementation",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newConfigCommand(), newStatusCommand(), newReviewCommand(), newMigrateTasksCommand())
	return root
}

func newConfigCommand() *cobra.Command {
	command := &cobra.Command{Use: "config", Short: "Inspect and migrate vault-map configuration"}
	command.AddCommand(newConfigShowCommand(), newConfigMigrateCommand())
	return command
}

func newConfigShowCommand() *cobra.Command {
	var mapFile string
	var effective bool
	var redact bool
	command := &cobra.Command{
		Use:   "show",
		Short: "Show configuration",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(mapFile)
			if err != nil {
				return err
			}
			payload, err := configPayload(cfg, redact, effective)
			if err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), payload)
		},
	}
	command.Flags().StringVar(&mapFile, "map-file", "", "path to vault-map.json")
	command.Flags().BoolVar(&effective, "effective", false, "show effective values after defaults and environment overrides")
	command.Flags().BoolVar(&redact, "redact", false, "redact model and credential-like values")
	return command
}

func configPayload(cfg *config.Config, redact, effective bool) (map[string]interface{}, error) {
	data, err := json.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("marshal configuration: %w", err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("decode configuration payload: %w", err)
	}
	if redact {
		if models, ok := payload["models"].(map[string]interface{}); ok {
			for key := range models {
				models[key] = "<redacted>"
			}
		}
		for key := range payload {
			lower := strings.ToLower(key)
			if strings.Contains(lower, "token") || strings.Contains(lower, "credential") || strings.Contains(lower, "authorization") {
				payload[key] = "<redacted>"
			}
		}
	}
	if effective {
		payload["_sources"] = map[string]string{
			"precedence": "CLI > OTG_* env > vault-map.json > defaults",
			"config":     cfg.ConfigPath,
		}
	}
	return payload, nil
}

func newConfigMigrateCommand() *cobra.Command {
	var mapFile string
	var write bool
	command := &cobra.Command{
		Use:   "migrate",
		Short: "Preview or write the current configuration schema",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(mapFile)
			if err != nil {
				return err
			}
			path := cfg.ConfigPath
			if path == "" {
				path = mapFile
			}
			payload, err := mergeRawConfig(path, cfg)
			if err != nil {
				return err
			}
			data, err := json.MarshalIndent(payload, "", "  ")
			if err != nil {
				return fmt.Errorf("marshal migrated configuration: %w", err)
			}
			data = append(data, '\n')
			if !write {
				_, err = cmd.OutOrStdout().Write(data)
				return err
			}
			if path == "" {
				return fmt.Errorf("configuration path is required for --write")
			}
			return os.WriteFile(path, data, 0o600)
		},
	}
	command.Flags().StringVar(&mapFile, "map-file", "", "path to vault-map.json")
	command.Flags().BoolVar(&write, "write", false, "write the migrated configuration")
	return command
}

func mergeRawConfig(path string, cfg *config.Config) (map[string]interface{}, error) {
	raw := map[string]interface{}{}
	if path != "" {
		if data, err := os.ReadFile(path); err == nil {
			if err := json.Unmarshal(data, &raw); err != nil {
				return nil, fmt.Errorf("parse raw configuration: %w", err)
			}
		} else if !os.IsNotExist(err) {
			return nil, err
		}
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	var effective map[string]interface{}
	if err := json.Unmarshal(data, &effective); err != nil {
		return nil, err
	}
	for key, value := range effective {
		raw[key] = value
	}
	return raw, nil
}

func newStatusCommand() *cobra.Command {
	var jsonOutput bool
	command := &cobra.Command{
		Use:   "status",
		Short: "Show daemon and queue status",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			payload := map[string]interface{}{
				"daemon_lock":   "unknown",
				"recent_scan":   "unknown",
				"running_tasks": []interface{}{},
				"queue_length":  0,
				"blocked":       []interface{}{},
			}
			if jsonOutput {
				return writeJSON(cmd.OutOrStdout(), payload)
			}
			_, err := fmt.Fprintln(cmd.OutOrStdout(), "daemon lock: unknown\nqueue: 0")
			return err
		},
	}
	command.Flags().BoolVar(&jsonOutput, "json", false, "emit JSON")
	return command
}

type reviewPayload struct {
	TaskID       string `json:"task_id"`
	TargetBranch string `json:"target_branch"`
	Worktree     string `json:"worktree"`
	BaseCommit   string `json:"base_commit"`
	HeadCommit   string `json:"head_commit"`
	Diffstat     string `json:"diffstat"`
	ACEvidence   string `json:"ac_evidence"`
}

func newReviewCommand() *cobra.Command {
	var jsonOutput bool
	var mapFile string
	command := &cobra.Command{
		Use:   "review <task_path>",
		Short: "Show Round 2 review evidence",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			payload, err := buildReviewPayload(args[0], mapFile)
			if err != nil {
				return err
			}
			if jsonOutput {
				return writeJSON(cmd.OutOrStdout(), payload)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "TASK-%s branch=%s\n%s\n", payload.TaskID, payload.TargetBranch, payload.Diffstat)
			return err
		},
	}
	command.Flags().BoolVar(&jsonOutput, "json", false, "emit JSON")
	command.Flags().StringVar(&mapFile, "map-file", "", "path to vault-map.json")
	return command
}

func buildReviewPayload(taskPath, mapFile string) (reviewPayload, error) {
	data, err := os.ReadFile(taskPath)
	if err != nil {
		return reviewPayload{}, fmt.Errorf("read task: %w", err)
	}
	fm, err := yamlfrontmatter.Parse(data)
	if err != nil || fm == nil {
		return reviewPayload{}, fmt.Errorf("parse task: %w", err)
	}
	payload := reviewPayload{TaskID: fm.ID, TargetBranch: fm.TargetBranch}
	cfg, loadErr := config.Load(mapFile)
	if loadErr == nil && fm.Project != "" {
		if repo, resolveErr := cfg.ResolveProject(fm.Project); resolveErr == nil {
			payload.Worktree = repo
			payload.HeadCommit = gitOutput(repo, "rev-parse", fm.TargetBranch)
			payload.BaseCommit = gitOutput(repo, "merge-base", "HEAD", fm.TargetBranch)
			if payload.BaseCommit != "" && payload.HeadCommit != "" {
				payload.Diffstat = gitOutput(repo, "diff", "--stat", payload.BaseCommit+"..."+payload.HeadCommit)
			}
		}
	}
	payload.ACEvidence = extractMarkdownSection(string(data), "验收记录")
	return payload, nil
}

func gitOutput(repo string, args ...string) string {
	output, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

func extractMarkdownSection(content, title string) string {
	marker := "## " + title
	start := strings.Index(content, marker)
	if start < 0 {
		return ""
	}
	rest := content[start+len(marker):]
	if end := strings.Index(rest, "\n## "); end >= 0 {
		rest = rest[:end]
	}
	return strings.TrimSpace(rest)
}

func newMigrateTasksCommand() *cobra.Command {
	var write bool
	command := &cobra.Command{
		Use:   "migrate-tasks <path>",
		Short: "Preview or write TASK schema upgrades (backfill + canonical field order)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			paths, err := taskMarkdownPaths(args[0])
			if err != nil {
				return err
			}
			for _, path := range paths {
				if !write {
					data, err := os.ReadFile(path)
					if err != nil {
						return err
					}
					missing, err := yamlfrontmatter.MissingDefaults(data)
					if err != nil {
						return err
					}
					if len(missing) == 0 {
						continue
					}
					keys := make([]string, 0, len(missing))
					for _, m := range missing {
						keys = append(keys, m.Key)
					}
					if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s: missing %s\n", path, strings.Join(keys, ", ")); err != nil {
						return err
					}
					continue
				}
				updated, err := yamlfrontmatter.NormalizeTaskFrontmatter(path)
				if err != nil {
					return err
				}
				if updated {
					if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s: normalized\n", path); err != nil {
						return err
					}
				}
			}
			return nil
		},
	}
	command.Flags().BoolVar(&write, "write", false, "write schema upgrades")
	return command
}

func taskMarkdownPaths(path string) ([]string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return []string{path}, nil
	}
	var paths []string
	err = filepath.WalkDir(path, func(candidate string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), "TASK-") && filepath.Ext(entry.Name()) == ".md" {
			paths = append(paths, candidate)
		}
		return nil
	})
	return paths, err
}

func writeJSON(writer io.Writer, value interface{}) error {
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}

var (
	configCmd       = newConfigCommand()
	statusCmd       = newStatusCommand()
	reviewCmd       = newReviewCommand()
	migrateTasksCmd = newMigrateTasksCommand()
	stagePlanCmd    = newStagePlanCommand()
)

func init() {
	rootCmd.AddCommand(configCmd, statusCmd, reviewCmd, migrateTasksCmd, stagePlanCmd)
}
