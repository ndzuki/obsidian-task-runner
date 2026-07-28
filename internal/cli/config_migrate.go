package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"

	"github.com/ndzuki/obsidian-task-runner/internal/config"
	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
	"github.com/spf13/cobra"
)

// CurrentConfigVersion is the latest config_version the migrator targets.
const CurrentConfigVersion = 1

var (
	configMigrateDryRun bool
	configMigrateWrite  bool
)

var configMigrateCmd = &cobra.Command{
	Use:   "migrate",
	Short: "Upgrade vault-map.json to current config_version",
	Long: `Loads the existing vault-map.json, compares its config_version against
the current schema version, and adds any missing fields from defaults.

Use --dry-run to preview changes without writing.
Use --write to atomically update the file.`,
	RunE: runConfigMigrate,
}

func runConfigMigrate(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load(daemonMapFile)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	if cfg.ConfigVersion >= CurrentConfigVersion {
		fmt.Fprintln(cmd.OutOrStdout(), "config_version is already current — nothing to migrate")
		return nil
	}

	defaults := config.Defaults()
	oldJSON, _ := json.MarshalIndent(cfg, "", "  ")

	mergeConfigDefaults(cfg, defaults)
	cfg.ConfigVersion = CurrentConfigVersion

	newJSON, _ := json.MarshalIndent(cfg, "", "  ")

	if configMigrateDryRun || !configMigrateWrite {
		fmt.Fprintf(cmd.OutOrStdout(), "--- old (config_version=%d)\n", CurrentConfigVersion-1)
		fmt.Fprintf(cmd.OutOrStdout(), "+++ new (config_version=%d)\n", CurrentConfigVersion)
		fmt.Fprint(cmd.OutOrStdout(), diffJSON(oldJSON, newJSON))
	}

	if configMigrateWrite {
		mapPath := daemonMapFile
		if mapPath == "" {
			home, _ := os.UserHomeDir()
			mapPath = filepath.Join(home, ".omp", "skills", "obsidian-task-runner", "config", "vault-map.json")
		}
		dir := filepath.Dir(mapPath)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("create config dir: %w", err)
		}
		if err := yamlfrontmatter.AtomicWrite(mapPath, append(newJSON, '\n')); err != nil {
			return fmt.Errorf("write config: %w", err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Wrote %s (config_version=%d)\n", mapPath, CurrentConfigVersion)
	}

	return nil
}

// mergeConfigDefaults fills zero/nil fields in dst from src.
func mergeConfigDefaults(dst, src *config.Config) {
	dv := reflect.ValueOf(dst).Elem()
	sv := reflect.ValueOf(src).Elem()
	t := dv.Type()

	for i := range t.NumField() {
		df := dv.Field(i)
		sf := sv.Field(i)
		if !df.CanSet() {
			continue
		}
		switch df.Kind() {
		case reflect.String:
			if df.String() == "" && sf.String() != "" {
				df.SetString(sf.String())
			}
		case reflect.Int, reflect.Int64:
			if df.Int() == 0 && sf.Int() != 0 {
				df.SetInt(sf.Int())
			}
		case reflect.Slice:
			if df.IsNil() && !sf.IsNil() {
				df.Set(reflect.AppendSlice(reflect.MakeSlice(df.Type(), 0, sf.Len()), sf))
			}
		case reflect.Map:
			if df.IsNil() {
				if !sf.IsNil() {
					df.Set(reflect.MakeMap(df.Type()))
					for _, k := range sf.MapKeys() {
						df.SetMapIndex(k, sf.MapIndex(k))
					}
				}
			} else {
				for _, k := range sf.MapKeys() {
					if df.MapIndex(k).IsValid() {
						continue
					}
					df.SetMapIndex(k, sf.MapIndex(k))
				}
			}
		}
	}
}

// diffJSON returns a simple line-by-line diff of two JSON byte slices.
func diffJSON(old, new []byte) string {
	oldLines := splitLines(string(old))
	newLines := splitLines(string(new))

	var out string
	for _, l := range oldLines {
		if !containsLine(newLines, l) {
			out += fmt.Sprintf("- %s\n", l)
		}
	}
	for _, l := range newLines {
		if !containsLine(oldLines, l) {
			out += fmt.Sprintf("+ %s\n", l)
		}
	}
	if out == "" {
		out = "  (no changes)\n"
	}
	return out
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := range len(s) {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

func containsLine(lines []string, s string) bool {
	for _, l := range lines {
		if l == s {
			return true
		}
	}
	return false
}

func init() {
	configMigrateCmd.Flags().BoolVar(&configMigrateDryRun, "dry-run", false, "Print diff without writing")
	configMigrateCmd.Flags().BoolVar(&configMigrateWrite, "write", false, "Atomically write migrated config")
	configMigrateCmd.Flags().StringVar(&daemonMapFile, "map-file", "", "Path to vault-map.json")
	configCmd.AddCommand(configMigrateCmd)
}
