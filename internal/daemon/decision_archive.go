package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ndzuki/obsidian-task-runner/internal/notify"
	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

// decisionArchiveThresholdBytes bounds the main decision list: beyond it the
// daemon archives answered points deterministically (PM Step 4.5 remains the
// primary path; this is the no-session fallback so a stale list can never
// grow unbounded). Archiving is deferred while more than this many points
// are still unanswered — the list must stay readable while questions are
// open.
const (
	decisionArchiveThresholdBytes = 50 * 1024
	decisionArchiveMaxPending     = 3
)

// decisionArchiveName is the audit file for answered decision points.
const decisionArchiveName = "Grilling-Decisions-archive.md"

// autoArchiveDecisions is the deterministic fallback for the PM Step 4.5
// archive: when the main decision list exceeds the size threshold and at
// most a few points remain unanswered, answered D-n blocks move to
// Grilling-Decisions-archive.md and the main list is rewritten to
// frontmatter + pointer + pending blocks. The distributed_answers_hash is
// refreshed so the change-detection never re-dispatches on the archive's
// own rewrite.
func (r *Runner) autoArchiveDecisions() int {
	projectsDir := filepath.Join(r.cfg.ObsidianVault, "Projects")
	projects, err := os.ReadDir(projectsDir)
	if err != nil {
		return 0
	}
	archived := 0
	for _, projectEntry := range projects {
		if !projectEntry.IsDir() {
			continue
		}
		projDir := filepath.Join(projectsDir, projectEntry.Name())
		listPath := filepath.Join(projDir, "Notes", grillingDecisionListName)
		data, err := os.ReadFile(listPath)
		if err != nil || len(data) < decisionArchiveThresholdBytes {
			continue
		}
		_, pending := grillingDecisionCounts(listPath)
		if pending > decisionArchiveMaxPending {
			continue
		}

		content := string(data)
		blocks := decisionBlockRE.FindAllStringIndex(content, -1)
		if len(blocks) == 0 {
			continue
		}
		var answered, pendingBlocks []string
		for i, loc := range blocks {
			end := len(content)
			if i+1 < len(blocks) {
				end = blocks[i+1][0]
			}
			block := content[loc[0]:end]
			if m := decisionLineRE.FindStringSubmatch(block); m != nil && decisionAnswered(m[1]) {
				answered = append(answered, block)
			} else {
				pendingBlocks = append(pendingBlocks, block)
			}
		}
		if len(answered) == 0 {
			continue
		}

		// Append to the archive file (create with skeleton on first use).
		archivePath := filepath.Join(projDir, "Notes", decisionArchiveName)
		now := time.Now()
		archiveEntry := strings.TrimRight(strings.Join(answered, ""), "\n") + "\n"
		if archData, err := os.ReadFile(archivePath); err == nil {
			archData = append([]byte(strings.TrimRight(string(archData), "\n")+"\n\n"), []byte(archiveEntry)...)
			if werr := os.WriteFile(archivePath, archData, 0o644); werr != nil {
				r.logger.Printf("auto-archive %s: append archive: %v", projectEntry.Name(), werr)
				continue
			}
		} else {
			skeleton := fmt.Sprintf(`---
id: "grilling-decisions-archive"
project: %s
type: archive
created: %s
updated: %s
---

# Grilling Decisions Archive — %s

> 已答决策点归档（daemon 确定性兜底 + PM Step 4.5）。D-n 编号全局单调递增的
> 审计来源：新决策点编号 = max(主清单, 本归档) + 1。

`, projectEntry.Name(), now.Format(time.RFC3339), now.Format(time.RFC3339), projectEntry.Name())
			if werr := os.WriteFile(archivePath, []byte(skeleton+archiveEntry), 0o644); werr != nil {
				r.logger.Printf("auto-archive %s: create archive: %v", projectEntry.Name(), werr)
				continue
			}
		}

		// Rewrite the main list: original frontmatter block + pointer + pending.
		rest := content[3:] // skip the leading "---"
		fmEnd := strings.Index(rest, "\n---")
		if fmEnd < 0 {
			r.logger.Printf("auto-archive %s: malformed list frontmatter, skipping", projectEntry.Name())
			continue
		}
		// Guard against a frontmatter scalar value containing a "---" line:
		// the closing delimiter must be followed by the body heading.
		afterFm := strings.TrimSpace(content[3+fmEnd+4:])
		if !strings.HasPrefix(afterFm, "#") {
			r.logger.Printf("auto-archive %s: frontmatter boundary suspect (no heading after close), skipping", projectEntry.Name())
			continue
		}
		mainFm := content[:3+fmEnd+4] // through the closing "---" line
		pointer := fmt.Sprintf(`# Grilling Decisions — %s

> 历史决策已归档至 [[Grilling-Decisions-archive]]（daemon 自动，%s）。
> 当前待答决策点如下；回答「决策:」后设置 frontmatter `+"`grill_continue: true`"+`，daemon 自动分发。

`, projectEntry.Name(), now.Format(time.RFC3339))
		newMain := mainFm + pointer + strings.Join(pendingBlocks, "")
		if werr := os.WriteFile(listPath, []byte(newMain), 0o644); werr != nil {
			r.logger.Printf("auto-archive %s: rewrite main list: %v", projectEntry.Name(), werr)
			continue
		}
		_ = yamlfrontmatter.Update(listPath, map[string]interface{}{
			"answered_count":          len(answered),
			"pending_count":           len(pendingBlocks),
			"distributed_answers_hash": grillingAnswersHash(newMain),
			"last_distributed_at":      now.Format(time.RFC3339),
		})

		archived += len(answered)
		r.logger.Printf("auto-archive %s: archived %d answered decision(s), %d pending remain", projectEntry.Name(), len(answered), len(pendingBlocks))
		if !r.diagNotified("archive|" + projectEntry.Name() + "|" + now.Format("2006-01-02")) {
			notify.SendTaskAction("grilling", "Grilling-Decisions", "🗄️", "决策清单自动归档",
				fmt.Sprintf("%d 条已答决策移入 Grilling-Decisions-archive.md，主清单已收敛（daemon 兜底归档）。", len(answered)), r.cfg.Notifications.Desktop)
		}
		r.updateRoadmap(projectEntry.Name(), "决策归档", fmt.Sprintf("%d 条已答决策自动归档（daemon 兜底）", len(answered)))
	}
	return archived
}
