package task

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// EnsureContextTerm appends a domain term to the project's CONTEXT.md
// ## Language section. If the term already exists (matched by **Term**:),
// it is a no-op. The function only appends; it never overwrites existing
// entries.
func EnsureContextTerm(projectDir, term, definition string) error {
	contextPath := filepath.Join(projectDir, "Notes", "CONTEXT.md")
	data, err := os.ReadFile(contextPath)
	if err != nil {
		return fmt.Errorf("read CONTEXT.md: %w", err)
	}

	content := string(data)

	// Check if term already exists in the Language section
	if strings.Contains(content, "**"+term+"**:") {
		return nil // already present
	}

	// Locate the ## Language section
	langIdx := strings.Index(content, "## Language")
	if langIdx == -1 {
		return fmt.Errorf("CONTEXT.md missing ## Language section")
	}

	// Find the next ## heading after ## Language — this is where we insert.
	// Searches for "\n## " starting after the "## Language" line.
	afterLang := content[langIdx+len("## Language"):]
	nextHeading := strings.Index(afterLang, "\n## ")

	entry := "\n**" + term + "**: " + definition

	var newContent string
	if nextHeading == -1 {
		// No subsequent section; append at end of file.
		newContent = strings.TrimRight(content, "\n") + entry + "\n"
	} else {
		insertPos := langIdx + len("## Language") + nextHeading
		// Insert before the next heading, preserving the blank-line separator.
		newContent = content[:insertPos] + entry + "\n" + content[insertPos:]
	}

	if err := os.WriteFile(contextPath, []byte(newContent), 0644); err != nil {
		return fmt.Errorf("write CONTEXT.md: %w", err)
	}

	return nil
}
