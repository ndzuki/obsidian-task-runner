package task

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// planVersionRE matches a plan version block heading, e.g. "### v12 · 2026-07-31".
var planVersionRE = regexp.MustCompile(`(?m)^### v(\d+)`)

// CompactPlanHistory collapses old versions of the "## 实现计划" section,
// keeping only the newest `keep` versions verbatim; older versions become a
// single folded marker line. History stays auditable (execution summary,
// 变更记录, and git history keep the full trail) without bloating the
// document that every refining/planning session reads into context — TASK
// docs can reach 30-40KB mostly from historical plan versions.
// Returns true when the document was rewritten.
func CompactPlanHistory(taskPath string, keep int) (bool, error) {
	if keep < 1 {
		keep = 1
	}
	data, err := os.ReadFile(taskPath)
	if err != nil {
		return false, err
	}
	content := string(data)

	const header = "## 实现计划"
	start := strings.Index(content, header)
	if start < 0 {
		return false, nil
	}
	bodyStart := start + len(header)
	// Section ends at the next top-level "## " heading (or end of file).
	end := strings.Index(content[bodyStart:], "\n## ")
	section := content[start:]
	if end >= 0 {
		section = content[start : bodyStart+end]
	}

	matches := planVersionRE.FindAllStringSubmatchIndex(section, -1)
	if len(matches) <= keep {
		return false, nil
	}

	// matches[i] = [fullStart fullEnd verStart verEnd]; block i spans
	// matches[i][0] .. matches[i+1][0] (or section end).
	firstKept := len(matches) - keep
	firstKeptVersion := section[matches[firstKept][2]:matches[firstKept][3]]

	folded := section[:matches[0][0]] +
		fmt.Sprintf("\n> 折叠：v1–v%s 的完整历史见 Git 历史与「## 变更记录」。（%s）\n\n",
			prevVersion(firstKeptVersion), "daemon 自动压缩，降低会话 token 消耗")
	folded += section[matches[firstKept][0]:]

	newContent := content[:start] + folded
	if end >= 0 {
		newContent += content[bodyStart+end:]
	}
	if newContent == content {
		return false, nil
	}

	tmp, err := os.CreateTemp(filepath.Dir(taskPath), ".otg-compact-")
	if err != nil {
		return false, err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := tmp.WriteString(newContent); err != nil {
		_ = tmp.Close()
		return false, err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return false, err
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}
	if err := os.Rename(tmpPath, taskPath); err != nil {
		return false, err
	}
	return true, nil
}

// prevVersion returns "N-1" for "N" (used for the folded range label).
func prevVersion(v string) string {
	var n int
	if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n > 1 {
		return fmt.Sprintf("%d", n-1)
	}
	return v
}
