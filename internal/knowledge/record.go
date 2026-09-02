package knowledge

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

// AppendApplicationRecord appends one "应用验证通过" line to every knowledge
// document referenced by a delivered task (TASK knowledge_refs). A merged
// task is applied-and-verified by definition, so the daemon records it
// automatically — no agent session involved, closing the "used it but never
// recorded it" gap.
//
// Idempotent: a line for the same (date, project) is never duplicated, so
// repeat merges, re-runs, and legacy re-extraction are no-ops. Missing
// documents are skipped silently. Returns the number of documents updated.
//
// dbPath selects the retrieval store for the heat-bump mirror (daemon passes
// KBPath(vault, override); "" skips the mirror).
func AppendApplicationRecord(vaultDir, projectName, dbPath string, refPaths []string) (int, error) {
	if len(refPaths) == 0 {
		return 0, nil
	}
	refsDir := filepath.Join(vaultDir, "References")
	line := fmt.Sprintf("- %s %s 应用验证通过", time.Now().Format("2006-01-02"), projectName)
	added := 0
	for _, ref := range refPaths {
		path := filepath.Join(refsDir, filepath.FromSlash(ref))
		data, err := os.ReadFile(path)
		if err != nil {
			continue // missing doc: nothing to record
		}
		content := string(data)
		if strings.Contains(content, line) {
			continue // already recorded for this project/date
		}
		updated := appendRecordLine(content, line)
		if err := yamlfrontmatter.AtomicWrite(path, []byte(updated)); err != nil {
			return added, fmt.Errorf("record application on %s: %w", ref, err)
		}
		// A delivered task applied this document — bump its heat so reused
		// experience ranks higher in later retrieval.
		if _, herr := IncrementHits(vaultDir, dbPath, []string{ref}); herr != nil {
			return added, fmt.Errorf("bump heat on %s: %w", ref, herr)
		}
		added++
	}
	return added, nil
}

// appendRecordLine inserts the record line at the end of the "## 应用记录"
// section, creating the section at the end of the document when absent.
// Later appends stay grouped under the same section heading.
func appendRecordLine(content, line string) string {
	const header = "## 应用记录"
	if idx := strings.Index(content, header); idx >= 0 {
		secStart := idx + len(header)
		if secStart < len(content) && content[secStart] == '\n' {
			secStart++
		}
		// Section body runs until the next "## " heading or EOF.
		secEnd := len(content)
		if n := strings.Index(content[secStart:], "\n## "); n >= 0 {
			secEnd = secStart + n
		}
		insert := line + "\n"
		if secEnd > secStart && content[secEnd-1] != '\n' {
			insert = "\n" + insert
		}
		return content[:secEnd] + insert + content[secEnd:]
	}
	trimmed := strings.TrimRight(content, "\n")
	if trimmed == "" {
		return header + "\n" + line + "\n"
	}
	return trimmed + "\n\n" + header + "\n" + line + "\n"
}
