package daemon

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/ndzuki/obsidian-task-runner/internal/designlib"
)

// designSliceMaxBytes caps the design-library slice injected into a task
// prompt. It mirrors context.go's fixed context budget: large enough to carry
// the relevant contracts/decisions/waves/glossary, small enough that a
// planning/round2 session reads its slice rather than the whole library
// (Phase 3d).
const designSliceMaxBytes = 32 << 10

// reqSummaryMaxBytes bounds how much of the REQ body feeds the design-library
// keyword matcher. Large requirement documents would otherwise inflate the
// lowercased matching copy for no extra slice-relevance gain.
const reqSummaryMaxBytes = 16 << 10

// designSliceForTask returns the design-library slice relevant to a task, or ""
// when the project has no populated design library. Relevance comes from the
// task id (frontmatter `related`) plus REQ keywords (filename token overlap).
func (r *Runner) designSliceForTask(project, taskID, reqDoc string) string {
	projDir := resolveVaultProjectDir(r.cfg.ObsidianVault, project)
	if projDir == "" {
		return ""
	}
	layout := designlib.ForProject(projDir)

	var reqSummary string
	if reqDoc != "" {
		if data, err := os.ReadFile(filepath.Join(r.cfg.ObsidianVault, reqDoc)); err == nil {
			reqSummary = string(data)
			if len(reqSummary) > reqSummaryMaxBytes {
				reqSummary = reqSummary[:reqSummaryMaxBytes]
			}
		}
	}

	slice, err := layout.SliceForTask(taskID, reqSummary, designSliceMaxBytes)
	if err != nil || slice == "" {
		return ""
	}
	return slice
}

// injectDesignLibrarySlice wraps a non-empty design slice for prompt injection.
// Kept as a pure function so tests assert the wrapper shape without disk IO.
func injectDesignLibrarySlice(skillPrompt, slice string) string {
	return fmt.Sprintf("%s\n\n<design_library>\n## 设计库切片（daemon 自动注入；任务只读相关切片，不重复推导全局架构）\n\n%s\n</design_library>", skillPrompt, slice)
}
