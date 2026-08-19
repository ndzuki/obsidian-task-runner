package task

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
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
//
// The write path uses yamlfrontmatter.AtomicReadModifyWrite, which holds the
// same task-path flock as Update — so compaction cannot lose a concurrent
// phase state write-back (P0-3).
func CompactPlanHistory(taskPath string, keep int) (bool, error) {
	if keep < 1 {
		keep = 1
	}
	var changed bool
	err := yamlfrontmatter.AtomicReadModifyWrite(taskPath, func(data []byte) ([]byte, error) {
		content := string(data)
		next, did, err := compactPlanContent(content, keep)
		if err != nil {
			return nil, err
		}
		if !did {
			return nil, nil
		}
		changed = true
		return []byte(next), nil
	})
	if err != nil {
		return changed, err
	}
	return changed, nil
}

// compactPlanContent is the pure content transform behind CompactPlanHistory.
// It is separate so the folding logic is unit-testable without file I/O.
func compactPlanContent(content string, keep int) (string, bool, error) {
	const header = "## 实现计划"
	start := strings.Index(content, header)
	if start < 0 {
		return content, false, nil
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
		return content, false, nil
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
		return content, false, nil
	}
	return newContent, true, nil
}

// prevVersion returns "N-1" for "N" (used for the folded range label).
func prevVersion(v string) string {
	var n int
	if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n > 1 {
		return fmt.Sprintf("%d", n-1)
	}
	return v
}

// prototypeSectionRE matches a "## Prototype 建议" heading, including the
// version-suffixed variants Round 1 writes ("## Prototype 建议（v11，…）").
var prototypeSectionRE = regexp.MustCompile(`(?m)^## Prototype 建议.*$`)

// CompactPrototypeHistory collapses old "## Prototype 建议" sections,
// keeping only the newest one verbatim. Round 1 appends a full prototype
// write-up per replan round; gated tasks (AC-066-17 style) can accumulate
// 8+ copies across replans, each tens of KB of repeated evidence. Older
// copies become a single folded marker — the newest copy keeps the full
// detail, and git history stays the audit trail.
//
// Write path shares the task-path flock via AtomicReadModifyWrite (P0-3).
func CompactPrototypeHistory(taskPath string) (bool, error) {
	var changed bool
	err := yamlfrontmatter.AtomicReadModifyWrite(taskPath, func(data []byte) ([]byte, error) {
		content := string(data)
		next, did, err := compactPrototypeContent(content)
		if err != nil {
			return nil, err
		}
		if !did {
			return nil, nil
		}
		changed = true
		return []byte(next), nil
	})
	if err != nil {
		return changed, err
	}
	return changed, nil
}

// compactPrototypeContent is the pure content transform behind
// CompactPrototypeHistory, unit-testable without file I/O.
func compactPrototypeContent(content string) (string, bool, error) {
	// Find section starts; a section runs to the next top-level heading.
	type section struct {
		start int // index of the "## Prototype" heading line
		end   int // index just past the last line of the section
	}
	var sections []section
	locs := prototypeSectionRE.FindAllStringIndex(content, -1)
	for i, loc := range locs {
		end := len(content)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		} else if next := strings.Index(content[loc[1]:], "\n## "); next >= 0 {
			end = loc[1] + next
		}
		// Last section may extend to EOF.
		sections = append(sections, section{start: loc[0], end: end})
	}
	if len(sections) <= 1 {
		return content, false, nil
	}

	// Keep the newest section verbatim; fold everything before it.
	kept := sections[len(sections)-1]
	folded := content[:sections[0].start] +
		"> 折叠：旧 Prototype 建议（v1–v" + fmt.Sprint(len(sections)-1) + " 个历史版本）见 Git 历史与「## 变更记录」。（daemon 自动压缩，降低会话 token 消耗）\n\n"
	folded += content[kept.start:]

	if folded == content {
		return content, false, nil
	}
	return folded, true, nil
}

// CompactTaskHistory runs the full set of history-folding passes on a task
// document (plan versions + prototype sections). It is safe to call on any
// task at any time — the scan-level oversize guard uses it so bloated docs
// converge even when a manual Round 1 bypassed the planning-completion
// compact path (TASK-066: 415KB from 17 replan copies).
func CompactTaskHistory(taskPath string) (bool, error) {
	changed := false
	for _, fold := range []func(string) (bool, error){
		func(p string) (bool, error) { return CompactPlanHistory(p, 3) },
		CompactPrototypeHistory,
	} {
		did, err := fold(taskPath)
		if err != nil {
			return changed, err
		}
		changed = changed || did
	}
	return changed, nil
}
