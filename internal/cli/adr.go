package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
	"github.com/spf13/cobra"
)

// ── write-adr ─────────────────────────────────────────────────────────────────

var writeAdrCmd = &cobra.Command{
	Use:   "write-adr <project_dir> <adr_id> <title> <content>",
	Short: "Write an ADR file with atomic write and validation",
	Long: `Creates Notes/adr/ADR-<id>-<slug>.md in the project directory using
atomic write (tmp → fsync → rename). The content is written as the Markdown
body, preceded by a validated YAML frontmatter with adr_id, title, status,
and created fields.`,
	Args: cobra.ExactArgs(4),
	RunE: runWriteAdr,
}

func runWriteAdr(cmd *cobra.Command, args []string) error {
	projectDir := args[0]
	adrID := args[1]
	title := args[2]
	bodyContent := args[3]

	if adrID == "" || title == "" {
		return fmt.Errorf("adr_id and title are required")
	}

	// Derive slug from title: lowercase, replace spaces/underscores with hyphens.
	slug := strings.ToLower(title)
	slug = strings.ReplaceAll(slug, " ", "-")
	slug = strings.ReplaceAll(slug, "_", "-")

	adrDir := filepath.Join(projectDir, "Notes", "adr")
	if err := os.MkdirAll(adrDir, 0755); err != nil {
		return fmt.Errorf("create adr directory: %w", err)
	}

	filename := fmt.Sprintf("ADR-%s-%s.md", adrID, slug)
	filePath := filepath.Join(adrDir, filename)

	// Build the ADR markdown content.
	content := fmt.Sprintf(`---
adr_id: "%s"
title: "%s"
project: ""
status: accepted
task_ref: ""
created: ""
---

# ADR-%s: %s

%s
`, adrID, title, adrID, title, bodyContent)

	if err := yamlfrontmatter.WriteADR(filePath, content); err != nil {
		return fmt.Errorf("write ADR %s: %w", filename, err)
	}

	// Validate after write.
	if err := yamlfrontmatter.ValidateADR(filePath); err != nil {
		return fmt.Errorf("validate ADR %s: %w", filename, err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "%s: written\n", filename)
	return nil
}

// ── validate-adr ──────────────────────────────────────────────────────────────

var validateAdrCmd = &cobra.Command{
	Use:   "validate-adr <adr_path>",
	Short: "Validate an ADR document's frontmatter structure",
	Long:  `Parses the YAML frontmatter and checks for required ADR fields.`,
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := yamlfrontmatter.ValidateADR(args[0]); err != nil {
			return fmt.Errorf("%s: %w", args[0], err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s: ADR frontmatter OK\n", args[0])
		return nil
	},
}
