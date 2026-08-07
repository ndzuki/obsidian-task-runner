package knowledge

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

// ExtractResult holds the outcome of a project knowledge extraction run.
type ExtractResult struct {
	ADRCount     int
	NewRefs      int
	UpdatedRefs  int
	Duplicates   int // pitfall/practice notes skipped as already recorded
	Unclassified []string // ADR ids auto-archived under References/uncategorized/
	Topics       []string // matched knowledge topics (deduped) — feeds scaffold_registry
	Touched      []string // absolute paths of knowledge files created/updated
	Errors       []string
}

// AbsorbResult reports one `otg kb absorb` run (interactive-session
// knowledge sink, outside the task pipeline).
type AbsorbResult struct {
	Appended   int      // notes written into References/
	Duplicates int      // skipped: an equivalent note already exists
	Archived   []string // unclassified entries stored under References/uncategorized/
	Topics     []string // matched topics from touched documents
	Touched    []string // absolute paths written
	Errors     []string
}

// ExtractProjectKnowledge implements Step 0 of the knowledge-base skill:
// scan project ADRs and extract reusable knowledge into References/.
func ExtractProjectKnowledge(vaultDir, projectName string) (*ExtractResult, error) {
	result := &ExtractResult{}

	projectDir := filepath.Join(vaultDir, "Projects", projectName)
	if _, err := os.Stat(projectDir); os.IsNotExist(err) {
		return nil, fmt.Errorf("project not found: %s", projectName)
	}

	adrDir := filepath.Join(projectDir, "Notes", "adr")
	adrs, err := scanADRs(adrDir)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("scan ADRs: %v", err))
	} else {
		result.ADRCount = len(adrs)
	}

	refsDir := filepath.Join(vaultDir, "References")
	for _, adr := range adrs {
		written, updated, touchedPath, unclassified, err := extractADRKnowledge(adr, refsDir, projectName)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("extract %s: %v", adr.ID, err))
			continue
		}
		if unclassified {
			result.Unclassified = append(result.Unclassified, adr.ID)
			if perr := appendUnclassifiedADR(refsDir, adr, projectName); perr != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("pending %s: %v", adr.ID, perr))
			}
			continue
		}
		result.NewRefs += written
		result.UpdatedRefs += updated
		if touchedPath != "" {
			result.Touched = append(result.Touched, touchedPath)
		}
	}

	return result, nil
}

// ReclassifyUncategorized re-runs classification over References/uncategorized/
// with the current knowledge-base vocabulary. Documents that now match a topic
// (because the KB gained a document, or the ADR gained tags) are merged into
// the target document and removed from uncategorized/ — archived knowledge
// migrates to its proper place automatically as the vocabulary grows.
// Returns the number of documents reclassified.
func ReclassifyUncategorized(vaultDir string) (int, error) {
	refsDir := filepath.Join(vaultDir, "References")
	uncatDir := filepath.Join(refsDir, "uncategorized")
	entries, err := os.ReadDir(uncatDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	moved := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		path := filepath.Join(uncatDir, e.Name())
		if movedOneUncategorized(path, refsDir) {
			moved++
		}
	}
	if moved > 0 {
		InvalidateRefIndex(refsDir)
	}
	return moved, nil
}

func movedOneUncategorized(path, refsDir string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	fm, body, err := parseFrontmatter(data)
	if err != nil || fm == nil {
		return false
	}
	adr := adrInfo{
		ID:       strings.TrimSuffix(filepath.Base(path), ".md"),
		Title:    extractH1(data),
		Decision: extractSection(body, "决策摘要"),
		Tags:     toStringSlice(fm["tags"]),
	}
	project := projectFromSource(string(data))
	target := classifyADR(adr, refsDir)
	if target == "" {
		return false // still no match — keep archived
	}
	if _, _, _, _, err := extractADRKnowledge(adr, refsDir, project); err != nil {
		return false
	}
	_ = os.Remove(path)
	return true
}

// projectFromSource extracts the source project name from an archived doc's
// provenance link (Projects/<name>/Notes/adr/...).
func projectFromSource(content string) string {
	re := regexp.MustCompile(`Projects/([^/)]+)/`)
	if m := re.FindStringSubmatch(content); m != nil {
		return m[1]
	}
	return "unknown"
}

// appendUnclassifiedADR stores an unclassified ADR under
// References/uncategorized/ with a standard frontmatter so it stays searchable
// through INDEX.md — knowledge is never dropped and never waits on manual
// review. Once the knowledge base gains a matching topic (or the ADR gains
// tags), re-extraction re-classifies it into the right document.
func appendUnclassifiedADR(refsDir string, adr adrInfo, projectName string) error {
	dir := filepath.Join(refsDir, "uncategorized")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	target := filepath.Join(dir, strings.TrimSuffix(adr.ID, ".md")+".md")
	if _, err := os.Stat(target); err == nil {
		return nil // already stored
	}
	now := time.Now().Format("2006-01-02")
	content := fmt.Sprintf(`---
topics: [uncategorized, adr]
level: intermediate
updated: "%s"
source: "local"
verified: false
aliases: []
---

# %s

> 自动从项目 ADR 提取，暂无匹配主题。来源：[%s](Projects/%s/Notes/adr/%s.md)

## 决策摘要

%s

## 更新记录

- %s — 从 %s 项目 %s 自动归档
`, now, adr.Title, adr.ID, projectName, adr.ID,
		adr.Decision, now, projectName, adr.ID)
	return os.WriteFile(target, []byte(content), 0o644)
}

// ExtractTaskKnowledge implements per-task Step 0 extraction: only the ADRs
// authored by this delivered task are extracted into References/, idempotently
// (the task's knowledge_extracted marker prevents re-extraction on repeated
// merges). Unlike the project-level scan, repeated deliveries do not re-touch
// knowledge files for unrelated ADRs.
func ExtractTaskKnowledge(vaultDir, projectName, taskPath string) (*ExtractResult, error) {
	result := &ExtractResult{}
	data, err := os.ReadFile(taskPath)
	if err != nil {
		return nil, fmt.Errorf("read task %s: %w", taskPath, err)
	}
	fm, err := yamlfrontmatter.Parse(data)
	if err != nil || fm == nil {
		return nil, fmt.Errorf("parse task %s: %w", taskPath, err)
	}
	if fm.KnowledgeExtracted {
		return result, nil // idempotent: extracted on a previous merge
	}

	adrIDs := collectADRIDs(fm.AdrWritten)
	_, body, _ := parseFrontmatter(data)

	refsDir := filepath.Join(vaultDir, "References")
	if len(adrIDs) > 0 {
		adrs, err := scanADRs(filepath.Join(vaultDir, "Projects", projectName, "Notes", "adr"))
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("scan ADRs: %v", err))
		} else {
			result.ADRCount = len(adrs)
		}
		for _, adr := range adrs {
			if !matchesADRID(adr.ID, adrIDs) {
				continue
			}
			written, updated, touchedPath, unclassified, err := extractADRKnowledge(adr, refsDir, projectName)
			if err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("extract %s: %v", adr.ID, err))
				continue
			}
			if unclassified {
				result.Unclassified = append(result.Unclassified, adr.ID)
				if perr := appendUnclassifiedADR(refsDir, adr, projectName); perr != nil {
					result.Errors = append(result.Errors, fmt.Sprintf("pending %s: %v", adr.ID, perr))
				}
				continue
			}
			result.NewRefs += written
			result.UpdatedRefs += updated
			if touchedPath != "" {
				result.Touched = append(result.Touched, touchedPath)
				result.Topics = appendUniqueTopics(result.Topics, topicsForTarget(refsDir, touchedPath))
			}
		}
	}

	// Pitfall extraction: the task body's "## 踩坑记录" section captures
	// failed-then-succeeded attempts (tried X, failed, switched to Y). These
	// negative lessons are sunk into the matching knowledge document so later
	// rounds can avoid re-treading the failed path. Runs even when the task
	// has no ADR (a deliverable can be pitfall-rich and decision-free).
	for _, p := range parsePitfalls(body) {
		target, unclassified, duplicate, err := extractTaskPitfall(p, refsDir, projectName, fm.ID)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("pitfall %q: %v", p.Title, err))
			continue
		}
		if unclassified {
			result.Unclassified = append(result.Unclassified, "TASK-"+fm.ID+" pitfall")
			if perr := appendUnclassifiedPitfall(refsDir, p, projectName, fm.ID); perr != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("pending pitfall %q: %v", p.Title, perr))
			}
			continue
		}
		if duplicate {
			result.Duplicates++
			// The lesson was re-encountered and already recorded — the
			// repeated encounter itself is a heat signal.
			if target != "" {
				if _, herr := IncrementHits(vaultDir, []string{target}); herr != nil {
					result.Errors = append(result.Errors, fmt.Sprintf("pitfall hit %q: %v", p.Title, herr))
				}
			}
			continue
		}
		result.UpdatedRefs++
		result.Touched = append(result.Touched, filepath.Join(refsDir, target))
		result.Topics = appendUniqueTopics(result.Topics, topicsForTarget(refsDir, target))
	}
	if err := markTaskExtracted(taskPath); err != nil {
		result.Errors = append(result.Errors, err.Error())
	}
	return result, nil
}

// topicsForTarget returns the frontmatter topics of a knowledge document.
func topicsForTarget(refsDir, target string) []string {
	for _, e := range loadRefIndex(refsDir) {
		if e.Path == target {
			return e.Topics
		}
	}
	return nil
}

// appendUniqueTopics appends topics that are not already present.
func appendUniqueTopics(dst, src []string) []string {
	seen := make(map[string]bool, len(dst)+len(src))
	for _, t := range dst {
		seen[t] = true
	}
	for _, t := range src {
		if t != "" && !seen[t] {
			dst = append(dst, t)
			seen[t] = true
		}
	}
	return dst
}

// collectADRIDs normalizes the adr_written frontmatter value (string, []any of
// strings, or a map keyed by ADR id) into a flat list of id strings.
func collectADRIDs(v any) []string {
	var ids []string
	switch vv := v.(type) {
	case string:
		if vv != "" {
			ids = append(ids, vv)
		}
	case []any:
		for _, item := range vv {
			ids = append(ids, collectADRIDs(item)...)
		}
	case []string:
		ids = append(ids, vv...)
	case map[string]any:
		for k := range vv {
			ids = append(ids, k)
		}
	case map[any]any:
		for k := range vv {
			ids = append(ids, fmt.Sprint(k))
		}
	}
	return ids
}

// matchesADRID reports whether an ADR file id (e.g. "ADR-012-model-x") matches
// a referenced id, allowing short references like "ADR-012".
func matchesADRID(id string, refs []string) bool {
	for _, r := range refs {
		norm := strings.TrimSuffix(r, ".md")
		if norm == "" {
			continue
		}
		if id == norm || strings.HasPrefix(id, norm+"-") {
			return true
		}
	}
	return false
}

func markTaskExtracted(taskPath string) error {
	return yamlfrontmatter.Update(taskPath, map[string]interface{}{"knowledge_extracted": true})
}

type adrInfo struct {
	ID       string
	Title    string
	Status   string
	Decision string
	Tags     []string // daemon-managed taxonomy tags (user-reviewable)
	FilePath string
}

func scanADRs(adrDir string) ([]adrInfo, error) {
	entries, err := os.ReadDir(adrDir)
	if err != nil {
		return nil, err
	}
	var adrs []adrInfo
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, "ADR-") || !strings.HasSuffix(name, ".md") {
			continue
		}
		if name == "ADR-INDEX.md" || name == "ADR-COVERAGE.md" {
			continue
		}
		fullPath := filepath.Join(adrDir, name)
		data, err := os.ReadFile(fullPath)
		if err != nil {
			continue
		}
		fm, body, err := parseFrontmatter(data)
		if err != nil {
			continue
		}
		id := strings.TrimSuffix(name, ".md")
		status := "accepted"
		if v, ok := fm["status"]; ok {
			status = fmt.Sprint(v)
		}
		if status != "accepted" && status != "superseded" {
			continue
		}
		title := ""
		if v, ok := fm["title"]; ok {
			title = fmt.Sprint(v)
		}
		decision := extractSection(body, "Decision")
		if idx := strings.Index(decision, "。"); idx > 0 {
			decision = decision[:idx]
		}
		if idx := strings.Index(decision, ".\n"); idx > 0 {
			decision = decision[:idx]
		}

		var tags []string
		if v, ok := fm["tags"]; ok {
			tags = toStringSlice(v)
		}
		adrs = append(adrs, adrInfo{
			ID:       id,
			Title:    title,
			Status:   status,
			Decision: strings.TrimSpace(decision),
			Tags:     tags,
			FilePath: fullPath,
		})
	}
	return adrs, nil
}

func extractADRKnowledge(adr adrInfo, refsDir, projectName string) (int, int, string, bool, error) {
	newCount, updateCount := 0, 0
	target := classifyADR(adr, refsDir)
	if target == "" {
		return 0, 0, "", true, nil // unclassified — caller records it for review
	}

	targetPath := filepath.Join(refsDir, target)
	if _, err := os.Stat(targetPath); os.IsNotExist(err) {
		if err := createRefScaffold(targetPath, adr, projectName); err != nil {
			return 0, 0, "", false, err
		}
		newCount++
	} else {
		if err := appendPracticeNote(targetPath, adr, projectName); err != nil {
			return 0, 0, "", false, err
		}
		updateCount++
	}

	return newCount, updateCount, targetPath, false, nil
}

// refIndexCache avoids re-scanning References/ for every ADR in an extraction
// run. Entries are dropped by InvalidateRefIndex (called after RebuildINDEX
// and by the daemon watcher on References/ writes).
var refIndexCache sync.Map // refsDir → []RefEntry

// InvalidateRefIndex drops the cached knowledge-base index (after
// RebuildINDEX or on References/ file events).
func InvalidateRefIndex(refsDir string) {
	refIndexCache.Delete(refsDir)
}

// loadRefIndex returns the knowledge-base index, scanning and caching on
// first use.
func loadRefIndex(refsDir string) []RefEntry {
	if cached, ok := refIndexCache.Load(refsDir); ok {
		return cached.([]RefEntry)
	}
	scanned, err := scanReferences(refsDir)
	if err != nil {
		return nil
	}
	refIndexCache.Store(refsDir, scanned)
	return scanned
}

// classifyADR maps an ADR to a knowledge-base document by scoring the ADR's
// decision/title text against every document's topics, aliases and Obsidian
// tags (all from the knowledge-base frontmatter). The classification rule is
// the knowledge base itself: adding a new topic is just adding a document —
// no code or config changes. Ties prefer the higher layer (core > extended >
// archived), then the lexicographically smaller path for determinism. Empty
// when nothing matches.
func classifyADR(adr adrInfo, refsDir string) string {
	var entries []RefEntry
	if cached, ok := refIndexCache.Load(refsDir); ok {
		entries = cached.([]RefEntry)
	} else {
		scanned, err := scanReferences(refsDir)
		if err != nil {
			return ""
		}
		entries = scanned
		refIndexCache.Store(refsDir, scanned)
	}

	// Tag hits are authoritative: daemon-managed, user-reviewable exact keys
	// beat text matching. Documents matching a tag rank first regardless of
	// how many text keywords hit.
	tagSet := make(map[string]bool, len(adr.Tags))
	for _, tg := range adr.Tags {
		tagSet[strings.ToLower(tg)] = true
	}
	hay := strings.ToLower(adr.Decision + " " + adr.Title)
	best := ""
	bestScore := 0
	bestTagHits := 0
	bestLongest := 0
	bestLayer := 3
	for _, e := range entries {
		score := 0
		tagHits := 0
		longest := 0
		hit := func(kw string) {
			if kw == "" {
				return
			}
			key := strings.ToLower(kw)
			if strings.Contains(hay, key) {
				score++
				if len(key) > longest {
					longest = len(key)
				}
			}
			if tagSet[key] {
				tagHits++
			}
		}
		for _, t := range e.Topics {
			hit(t)
		}
		for _, a := range e.Aliases {
			hit(a)
		}
		for _, tg := range e.Tags {
			hit(tg)
		}
		if score == 0 && tagHits == 0 {
			continue
		}
		layer := layerRank(e.Path)
		if tagHits > bestTagHits ||
			(tagHits == bestTagHits && score > bestScore) ||
			(tagHits == bestTagHits && score == bestScore && layer < bestLayer) ||
			(tagHits == bestTagHits && score == bestScore && layer == bestLayer && e.Path < best) {
			best = e.Path
			bestScore = score
			bestTagHits = tagHits
			bestLongest = longest
			bestLayer = layer
		}
	}
	if best == "" {
		return ""
	}
	// Confidence gate: auto-writing into a KB file requires more than a single
	// generic short keyword (go/ci/sdk/jwt match too much text). Tag hits (user
	// reviewable), multi-keyword matches, and precise terms of ≥4 bytes (helm,
	// redis, connect, postgres, 数据库) are trusted — purely data-driven, no
	// stop-word table.
	if bestTagHits == 0 && bestScore < 2 && bestLongest < 4 {
		return ""
	}
	return best
}

// SuggestTags derives candidate taxonomy tags for an ADR from the knowledge
// base's own topics/aliases/tags vocabulary — the KB defines the tag space, so
// new topics become new tags with no code changes.
func SuggestTags(adr adrInfo, refsDir string) []string {
	entries := loadRefIndex(refsDir)
	if entries == nil {
		return nil
	}

	hay := strings.ToLower(adr.Decision + " " + adr.Title)
	seen := make(map[string]bool)
	var tags []string
	for _, e := range entries {
		for _, t := range append(append(append([]string{}, e.Topics...), e.Aliases...), e.Tags...) {
			key := strings.ToLower(strings.TrimSpace(t))
			if key == "" || seen[key] || !strings.Contains(hay, key) {
				continue
			}
			seen[key] = true
			tags = append(tags, strings.TrimSpace(t))
		}
	}
	return tags
}

// EnsureADRTags merges suggested tags into an ADR's frontmatter without
// removing user-curated tags. Called by the daemon before extraction so ADRs
// carry reviewable taxonomy tags.
func EnsureADRTags(adrPath, refsDir string) error {
	data, err := os.ReadFile(adrPath)
	if err != nil {
		return err
	}
	fm, _, err := parseFrontmatter(data)
	if err != nil {
		return err
	}
	existing := toStringSlice(fm["tags"])
	adr := adrInfo{
		ID:       strings.TrimSuffix(filepath.Base(adrPath), ".md"),
		Title:    fmt.Sprint(fm["title"]),
		Decision: extractSection(string(data), "Decision"),
	}
	suggested := SuggestTags(adr, refsDir)
	if len(suggested) == 0 {
		return nil
	}
	have := make(map[string]bool, len(existing))
	for _, t := range existing {
		have[strings.ToLower(t)] = true
	}
	merged := append([]string{}, existing...)
	for _, t := range suggested {
		if !have[strings.ToLower(t)] {
			merged = append(merged, t)
		}
	}
	if len(merged) == len(existing) {
		return nil
	}
	return yamlfrontmatter.Update(adrPath, map[string]interface{}{"tags": merged})
}

// ── Pitfall extraction ──────────────────────────────────────────────
//
// A task pitfall is a failed-then-succeeded attempt recorded by Round 2:
// the agent believed approach X was correct, hit failure, and only
// succeeded after switching to Y. Recording X is what prevents re-treading
// it — the success alone (which may also live in an ADR) cannot.

// taskPitfall is one parsed "## 踩坑记录" entry from a task body.
type taskPitfall struct {
	Time      string // ISO date from the entry heading, "" when missing
	Title     string // entry heading text
	Symptom   string
	Failed    string // approach that did not work + observed failure
	RootCause string
	Success   string // approach that worked
	Refs      string // optional comma-separated References/ paths
}

// parsePitfalls extracts "## 踩坑记录" entries from a task body.
// Format (one entry per "### " heading):
//
//	### 2026-08-07: {phenomenon}
//	- 现象: ...
//	- 失败方案: ...
//	- 根因: ...
//	- 成功方案: ...
//	- 相关文档: extended/tools/kulala-http-client.md (optional)
func parsePitfalls(body string) []taskPitfall {
	idx := strings.Index(body, "## 踩坑记录")
	if idx < 0 {
		return nil
	}
	rest := body[idx+len("## 踩坑记录"):]
	if end := strings.Index(rest, "\n## "); end >= 0 {
		rest = rest[:end]
	}
	var out []taskPitfall
	for _, block := range strings.Split(rest, "### ") {
		block = strings.TrimSpace(block)
		if block == "" || strings.HasPrefix(block, "<!--") {
			continue
		}
		lines := strings.SplitN(block, "\n", 2)
		title := strings.TrimSpace(lines[0])
		p := taskPitfall{Title: title}
		if len(lines) == 2 {
			for _, line := range strings.Split(lines[1], "\n") {
				line = strings.TrimSpace(line)
				switch {
				case strings.HasPrefix(line, "- 现象:"):
					p.Symptom = strings.TrimSpace(strings.TrimPrefix(line, "- 现象:"))
				case strings.HasPrefix(line, "- 失败方案:"):
					p.Failed = strings.TrimSpace(strings.TrimPrefix(line, "- 失败方案:"))
				case strings.HasPrefix(line, "- 根因:"):
					p.RootCause = strings.TrimSpace(strings.TrimPrefix(line, "- 根因:"))
				case strings.HasPrefix(line, "- 成功方案:"):
					p.Success = strings.TrimSpace(strings.TrimPrefix(line, "- 成功方案:"))
				case strings.HasPrefix(line, "- 相关文档:"):
					p.Refs = strings.TrimSpace(strings.TrimPrefix(line, "- 相关文档:"))
				}
			}
		}
		// ISO date prefix ("2026-08-07: ...") becomes the entry time.
		if len(title) >= 11 && title[4] == '-' && title[7] == '-' {
			if t, terr := time.Parse("2006-01-02", title[:10]); terr == nil {
				p.Time = t.Format("2006-01-02")
			}
		}
		if p.Title != "" && (p.Symptom != "" || p.Failed != "" || p.Success != "") {
			out = append(out, p)
		}
	}
	return out
}

// extractTaskPitfall routes one pitfall to its knowledge document and sinks
// the negative lesson. An explicit 相关文档 reference wins; otherwise the
// pitfall is classified against the knowledge-base vocabulary like an ADR.
// Returns the relative target path, whether it remained unclassified,
// whether it was a duplicate (lesson already recorded), and an error.
// duplicate=true carries the target so callers can bump the document's heat.
func extractTaskPitfall(p taskPitfall, refsDir, projectName, taskID string) (target string, unclassified, duplicate bool, err error) {
	sink := func(targetPath, rel string) (string, bool, bool, error) {
		appended, aerr := appendPitfallNote(targetPath, p, projectName, taskID)
		if aerr != nil {
			return "", false, false, aerr
		}
		if !appended {
			return rel, false, true, nil // duplicate — already recorded
		}
		return rel, false, false, nil
	}
	if p.Refs != "" {
		for _, ref := range strings.Split(p.Refs, ",") {
			ref = strings.TrimSpace(ref)
			ref = strings.TrimPrefix(ref, "References/")
			if ref == "" || strings.Contains(ref, "..") {
				continue
			}
			target := filepath.Join(refsDir, filepath.FromSlash(ref))
			if _, serr := os.Stat(target); serr == nil {
				return sink(target, filepath.ToSlash(ref))
			}
		}
	}
	adr := adrInfo{
		Title:    p.Title,
		Decision: strings.Join([]string{p.Symptom, p.RootCause, p.Success}, " "),
	}
	target = classifyADR(adr, refsDir)
	if target == "" {
		return "", true, false, nil
	}
	return sink(filepath.Join(refsDir, filepath.FromSlash(target)), target)
}

// normalizeKey folds a string for dedup comparisons: lowercase, trimmed,
// whitespace collapsed.
func normalizeKey(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(s))), " ")
}

// hasRecordedKey reports whether the document already carries a normalized
// key as a `**label**：value` line — the dedup check for practice notes.
func hasRecordedKey(content, label, key string) bool {
	if key == "" {
		return false
	}
	want := normalizeKey(key)
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "**"+label+"**") {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(line, "**"+label+"**"))
		rest = strings.TrimPrefix(rest, "：")
		rest = strings.TrimPrefix(rest, ":")
		if normalizeKey(rest) == want {
			return true
		}
	}
	return false
}

// appendPitfallNote appends one pitfall practice note before the document's
// "## 更新记录" section (creating the section when absent). Returns false
// when an equivalent note (same normalized title or failed approach) already
// exists — the dedup store is the document itself, so repeated deliveries or
// interactive absorbs of the same lesson never duplicate index/token cost.
func appendPitfallNote(path string, p taskPitfall, projectName, taskID string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	content := string(data)
	if hasRecordedKey(content, "标题", p.Title) || hasRecordedKey(content, "失败方案", p.Failed) {
		return false, nil
	}
	now := time.Now().Format("2006-01-02")
	note := fmt.Sprintf("\n### %s 踩坑实践（TASK-%s）\n\n**时间**：%s\n\n**标题**：%s\n\n**现象**：%s\n\n**失败方案**：%s\n\n**根因**：%s\n\n**成功方案**：%s\n",
		projectName, taskID, p.Time, p.Title, p.Symptom, p.Failed, p.RootCause, p.Success)
	if idx := strings.LastIndex(content, "## 更新记录"); idx >= 0 {
		content = content[:idx] + note + content[idx:]
	} else {
		content += "\n" + note + "\n## 更新记录\n\n- `" + now + "` — 从 " + projectName + " 项目 TASK-" + taskID + " 提取踩坑实践\n"
	}
	return true, os.WriteFile(path, []byte(content), 0o644)
}

// appendUnclassifiedPitfall archives a pitfall with no knowledge match under
// References/uncategorized/ so the lesson is never dropped; Reclassify
// vocabulary growth can migrate it later.
func appendUnclassifiedPitfall(refsDir string, p taskPitfall, projectName, taskID string) error {
	dir := filepath.Join(refsDir, "uncategorized")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	slug := strings.ToLower(p.Title)
	re := regexp.MustCompile(`[^\p{L}\p{N}]+`)
	slug = strings.Trim(re.ReplaceAllString(slug, "-"), "-")
	if len(slug) > 30 {
		slug = slug[:30]
	}
	if slug == "" {
		slug = "pitfall"
	}
	target := filepath.Join(dir, fmt.Sprintf("TASK-%s-pitfall-%s.md", taskID, slug))
	if _, err := os.Stat(target); err == nil {
		return nil // already stored
	}
	now := time.Now().Format("2006-01-02")
	content := fmt.Sprintf(`---
topics: [uncategorized, pitfall]
level: intermediate
updated: "%s"
source: "local"
verified: false
aliases: []
---

# %s

> 自动从项目 TASK-%s 提取，暂无匹配主题。来源：%s 项目 TASK-%s

## 踩坑实践

**时间**：%s

**现象**：%s

**失败方案**：%s

**根因**：%s

**成功方案**：%s

## 更新记录

- %s — 从 %s 项目 TASK-%s 自动归档
`, now, p.Title, taskID, projectName, taskID,
		p.Time, p.Symptom, p.Failed, p.RootCause, p.Success, now, projectName, taskID)
	return os.WriteFile(target, []byte(content), 0o644)
}

// ── Interactive absorb (`otg kb absorb`) ─────────────────────────────
//
// Daily OMP conversations (outside the obsidian-task-runner pipeline) also
// produce failed-then-succeeded lessons. AbsorbKnowledge gives those sessions
// the same sink as task merges: classify → append to the matching References
// document (deduped) or archive under uncategorized/. Summary mode absorbs
// free-text project lessons as 「实践经验」 notes.

// AbsorbKnowledge sinks interactive-session knowledge into the vault.
// With summary=false the input text is a "## 踩坑记录"-style block (with
// "### {date}: {title}" heading and 现象/失败方案/根因/成功方案 keys);
// with summary=true it is free-text project experience appended as a
// 「实践经验」 note under the best-matching document.
func AbsorbKnowledge(vaultDir, projectName, text string, summary bool) (*AbsorbResult, error) {
	res := &AbsorbResult{}
	if vaultDir == "" || strings.TrimSpace(text) == "" {
		return res, nil
	}
	if projectName == "" {
		projectName = "interactive"
	}
	refsDir := filepath.Join(vaultDir, "References")

	if summary {
		title := firstNonEmptyLine(text)
		adr := adrInfo{Title: title, Decision: text}
		target := classifyADR(adr, refsDir)
		if target == "" {
			if err := appendUnclassifiedSummary(refsDir, title, text, projectName); err != nil {
				res.Errors = append(res.Errors, err.Error())
			} else {
				res.Archived = append(res.Archived, title)
			}
			return res, nil
		}
		targetPath := filepath.Join(refsDir, filepath.FromSlash(target))
		appended, err := appendSummaryNote(targetPath, title, text, projectName)
		if err != nil {
			res.Errors = append(res.Errors, err.Error())
			return res, nil
		}
		if !appended {
			res.Duplicates++
			return res, nil
		}
		res.Appended++
		res.Touched = append(res.Touched, targetPath)
		res.Topics = appendUniqueTopics(res.Topics, topicsForTarget(refsDir, target))
		return res, nil
	}

	for _, p := range parsePitfalls("## 踩坑记录\n" + text) {
		target, unclassified, duplicate, err := extractTaskPitfall(p, refsDir, projectName, "interactive")
		if err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("pitfall %q: %v", p.Title, err))
			continue
		}
		if unclassified {
			if perr := appendUnclassifiedPitfall(refsDir, p, projectName, "interactive"); perr != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("pending pitfall %q: %v", p.Title, perr))
			} else {
				res.Archived = append(res.Archived, p.Title)
			}
			continue
		}
		if duplicate {
			res.Duplicates++
			// Re-encountering an already-recorded lesson is a heat signal:
			// the same failure keeps surfacing, so the experience deserves
			// higher retrieval priority.
			if target != "" {
				if _, herr := IncrementHits(vaultDir, []string{target}); herr != nil {
					res.Errors = append(res.Errors, fmt.Sprintf("pitfall hit %q: %v", p.Title, herr))
				}
			}
			continue
		}
		res.Appended++
		res.Touched = append(res.Touched, filepath.Join(refsDir, target))
		res.Topics = appendUniqueTopics(res.Topics, topicsForTarget(refsDir, target))
	}
	return res, nil
}

// firstNonEmptyLine returns the first non-blank line of a text block.
func firstNonEmptyLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if s := strings.TrimSpace(line); s != "" {
			return s
		}
	}
	return "经验总结"
}

// appendSummaryNote appends a free-text experience note (interactive summary
// mode) before the document's "## 更新记录" section. Deduplicated by
// normalized title. Returns false when an equivalent note already exists.
func appendSummaryNote(path, title, text, projectName string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	content := string(data)
	if hasRecordedKey(content, "标题", title) {
		return false, nil
	}
	now := time.Now().Format("2006-01-02")
	note := fmt.Sprintf("\n### %s 经验总结（interactive）\n\n**时间**：%s\n\n**标题**：%s\n\n**要点**：%s\n",
		projectName, now, title, strings.TrimSpace(text))
	if idx := strings.LastIndex(content, "## 更新记录"); idx >= 0 {
		content = content[:idx] + note + content[idx:]
	} else {
		content += "\n" + note + "\n## 更新记录\n\n- `" + now + "` — 从 " + projectName + " 交互会话吸收经验总结\n"
	}
	return true, os.WriteFile(path, []byte(content), 0o644)
}

// appendUnclassifiedSummary archives an interactive summary with no knowledge
// match under References/uncategorized/.
func appendUnclassifiedSummary(refsDir, title, text, projectName string) error {
	dir := filepath.Join(refsDir, "uncategorized")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	slug := strings.ToLower(title)
	re := regexp.MustCompile(`[^\p{L}\p{N}]+`)
	slug = strings.Trim(re.ReplaceAllString(slug, "-"), "-")
	if len(slug) > 30 {
		slug = slug[:30]
	}
	if slug == "" {
		slug = "summary"
	}
	target := filepath.Join(dir, "interactive-summary-"+slug+".md")
	if _, err := os.Stat(target); err == nil {
		return nil // already stored
	}
	now := time.Now().Format("2006-01-02")
	content := fmt.Sprintf(`---
topics: [uncategorized, summary]
level: intermediate
updated: "%s"
source: "local"
verified: false
aliases: []
---

# %s

> 从 %s 项目交互会话自动吸收，暂无匹配主题。

## 要点

%s

## 更新记录

- %s — 从 %s 项目交互会话自动归档
`, now, title, projectName, strings.TrimSpace(text), now, projectName)
	return os.WriteFile(target, []byte(content), 0o644)
}

// ── Experience heat & core promotion ─────────────────────────────────
//
// Knowledge that keeps proving itself rises: every successful application
// (merge hit, repeated absorb, manual `otg kb hit`) bumps the document's
// `hits` counter, which boosts retrieval ranking. Documents whose hits pass
// the promotion threshold move from extended/ into core/ so the
// core → extended → archived retrieval cascade prefers them.

// IncrementHits bumps the `hits` frontmatter counter of each referenced
// knowledge document by one. Missing documents are skipped. Returns the
// number of documents bumped.
//
// The counter is updated with a field-preserving frontmatter rewrite: the KB
// v2 schema pins `updated` to YYYY-MM-DD, which yamlfrontmatter.Update (task
// semantics, timestamp refresh) would violate.
func IncrementHits(vaultDir string, refPaths []string) (int, error) {
	if vaultDir == "" || len(refPaths) == 0 {
		return 0, nil
	}
	refsDir := filepath.Join(vaultDir, "References")
	bumped := 0
	type syncHit struct {
		path string
		hits int
	}
	var syncHits []syncHit
	for _, ref := range refPaths {
		path := filepath.Join(refsDir, filepath.FromSlash(ref))
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		fm, _, err := parseFrontmatter(data)
		if err != nil || fm == nil {
			continue
		}
		hits := 0
		switch vv := fm["hits"].(type) {
		case int:
			hits = vv
		case float64:
			hits = int(vv)
		}
		if err := os.WriteFile(path, []byte(bumpHitsField(string(data), hits+1)), 0o644); err != nil {
			return bumped, fmt.Errorf("bump hits on %s: %w", ref, err)
		}
		// Keep the in-process classification cache hot: bump the cached entry
		// in place instead of invalidating (a full rescan at 10k docs is what
		// this optimization exists to avoid).
		if cached, ok := refIndexCache.Load(refsDir); ok {
			entries := cached.([]RefEntry)
			clean := strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(ref), "References/"), "/")
			for i := range entries {
				if entries[i].Path == clean {
					entries[i].Hits++
					break
				}
			}
			refIndexCache.Store(refsDir, entries)
		}
		bumped++
		clean := strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(ref), "References/"), "/")
		syncHits = append(syncHits, syncHit{path: clean, hits: hits + 1})
	}
	// Mirror the bump into the retrieval store (frontmatter is the source of
	// truth; a hits change does not alter content_hash, so no later sync
	// would pick it up). Only when the store already exists — avoids
	// creating the real XDG store from contexts without a config (tests,
	// embedders); CLI/daemon hit paths run with the store present.
	dbPath := KBPath(vaultDir, "")
	if len(syncHits) > 0 {
		if _, err := os.Stat(dbPath); err == nil {
			if db, err := openKB(dbPath); err == nil {
				for _, s := range syncHits {
					if _, err := db.Exec(`UPDATE kb_docs SET hits=? WHERE path=?`, s.hits, s.path); err != nil {
						db.Close()
						break
					}
				}
				db.Close()
			}
		}
	}
	return bumped, nil
}

// bumpHitsField rewrites only the `hits:` line inside the frontmatter block,
// preserving every other field, their order, and the exact `updated` value.
// Appends the line when absent.
func bumpHitsField(content string, hits int) string {
	if !strings.HasPrefix(content, "---\n") {
		return content
	}
	rest := content[4:]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return content
	}
	fmText := rest[:end]
	body := rest[end+4:]
	replaced := false
	lines := strings.Split(fmText, "\n")
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "hits:") {
			lines[i] = "hits: " + strconv.Itoa(hits)
			replaced = true
		}
	}
	if !replaced {
		lines = append(lines, "hits: "+strconv.Itoa(hits))
	}
	return "---\n" + strings.Join(lines, "\n") + "\n---" + body
}

// PromoteToCore moves extended/ documents whose hits reach minHits into
// core/ (same subdirectory), so frequently reused experience joins the
// primary retrieval layer. A document already existing at the destination is
// skipped (no auto-merge). Returns the moved relative paths.
//
// Candidates come from the in-process ref index cache (kept hot by
// IncrementHits), so at 10k-document scale the check is O(candidates) instead
// of a full walk.
func PromoteToCore(vaultDir string, minHits int) ([]string, error) {
	if vaultDir == "" || minHits <= 0 {
		return nil, nil
	}
	refsDir := filepath.Join(vaultDir, "References")
	entries := loadRefIndex(refsDir)
	var moved []string
	for _, e := range entries {
		if !strings.HasPrefix(e.Path, "extended/") || e.Hits < minHits {
			continue
		}
		sub := strings.TrimPrefix(e.Path, "extended/")
		src := filepath.Join(refsDir, filepath.FromSlash(e.Path))
		dest := filepath.Join(refsDir, "core", filepath.FromSlash(sub))
		if _, serr := os.Stat(dest); serr == nil {
			continue // destination occupied — no auto-merge
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return moved, err
		}
		// Leave a migration trail in the document's own 更新记录 so the
		// move is traceable without external logs.
		if uerr := appendPromoteNote(src, e.Hits); uerr != nil {
			return moved, uerr
		}
		if err := os.Rename(src, dest); err != nil {
			return moved, err
		}
		moved = append(moved, e.Path+" → core/"+sub)
	}
	if len(moved) > 0 {
		InvalidateRefIndex(refsDir)
	}
	return moved, nil
}

// appendPromoteNote records the extended/ → core/ promotion in the
// document's "## 更新记录" section before the file is moved.
func appendPromoteNote(path string, hits int) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	content := string(data)
	now := time.Now().Format("2006-01-02")
	line := "- `" + now + "` — 经验热度 hits=" + strconv.Itoa(hits) + "，从 extended/ 自动升级至 core/（成功应用复用）\n"
	if idx := strings.LastIndex(content, "## 更新记录"); idx >= 0 {
		content = content[:idx+len("## 更新记录")] + "\n" + line + content[idx+len("## 更新记录"):]
	} else {
		content += "\n## 更新记录\n\n" + line
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

// tailLogTail returns the last meaningful line of a phase log as failure
// evidence, collapsed and length-capped. Empty when the log is unreadable.
func tailLogTail(logPath string, maxBytes int64) string {
	if logPath == "" {
		return ""
	}
	f, err := os.Open(logPath)
	if err != nil {
		return ""
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.Size() == 0 {
		return ""
	}
	off := st.Size() - maxBytes
	if off < 0 {
		off = 0
	}
	buf := make([]byte, st.Size()-off)
	if _, err := f.ReadAt(buf, off); err != nil {
		return ""
	}
	s := strings.TrimSpace(string(buf))
	if i := strings.LastIndex(s, "\n"); i >= 0 {
		if tail := strings.TrimSpace(s[i+1:]); tail != "" {
			s = tail
		} else {
			s = strings.TrimSpace(s[:i])
		}
	}
	s = strings.Join(strings.Fields(s), " ")
	// Failure evidence lives at the tail of the log — keep the end, not the head.
	if len(s) > 200 {
		s = "…" + s[len(s)-199:]
	}
	return s
}

// layerRank maps the top-level References/ directory to a priority
// (core=0, extended=1, archived=2).
func layerRank(path string) int {
	parts := strings.SplitN(path, "/", 2)
	switch parts[0] {
	case "core":
		return 0
	case "extended":
		return 1
	case "archived":
		return 2
	}
	return 3
}

func createRefScaffold(path string, adr adrInfo, projectName string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	now := time.Now().Format("2006-01-02")
	relPath, _ := filepath.Rel(filepath.Dir(filepath.Dir(path)), path)
	parts := strings.Split(relPath, "/")
	topics := parts[len(parts)-2:]
	if len(topics) > 1 {
		topics = topics[:1]
	}
	topicStr := strings.TrimSuffix(strings.Join(topics, ", "), ".md")

	content := fmt.Sprintf(`---
topics: [%s]
level: intermediate
updated: "%s"
source: "local"
verified: false
aliases: []
---

# %s

> 自动从项目 ADR 提取。来源：[%s](Projects/%s/Notes/adr/%s.md)

## 决策摘要

%s

## 更新记录

- %s — 从 %s 项目 %s 自动提取
`, topicStr, now, adr.Title, adr.ID, projectName, adr.ID,
		adr.Decision, now, projectName, adr.ID)

	return os.WriteFile(path, []byte(content), 0o644)
}

func appendPracticeNote(path string, adr adrInfo, projectName string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	content := string(data)
	now := time.Now().Format("2006-01-02")

	note := fmt.Sprintf("\n### %s 项目实践\n\n**来源**：[%s](Projects/%s/Notes/adr/%s.md)（%s）\n\n**决策**：%s\n",
		projectName, adr.ID, projectName, adr.ID, now, adr.Decision)

	if idx := strings.LastIndex(content, "## 更新记录"); idx >= 0 {
		content = content[:idx] + note + content[idx:]
	} else {
		content += "\n" + note + "\n## 更新记录\n\n- `" + now + "` — 从 " + projectName + " 项目 " + adr.ID + " 自动提取实践经验\n"
	}

	return os.WriteFile(path, []byte(content), 0o644)
}

func extractSection(content, heading string) string {
	header := "## " + heading
	idx := strings.Index(content, header)
	if idx < 0 {
		header = "## " + heading + "\n"
		idx = strings.Index(content, header)
	}
	if idx < 0 {
		return ""
	}
	start := idx + len(header)
	if start >= len(content) {
		return ""
	}
	rest := content[start:]
	nextHeader := regexp.MustCompile(`(?m)^## `).FindStringIndex(rest)
	if nextHeader != nil {
		return rest[:nextHeader[0]]
	}
	return rest
}

// MarkVerified sets verified: true on the given knowledge files. Called after
// a merge→done delivery: the ADR decisions extracted from that delivery have
// been validated by real project practice, so the file-level verified flag
// flips. Content added later (unverified ADRs) keeps the per-section notes
// honest — the flag is file-level, practice notes stay section-level.
// All errors are collected and reported together; files that succeeded stay
// flipped (partial success is not rolled back).
func MarkVerified(paths []string) error {
	var errs []string
	for _, p := range paths {
		if err := yamlfrontmatter.Update(p, map[string]interface{}{"verified": true}); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", p, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("mark verified: %s", strings.Join(errs, "; "))
	}
	return nil
}

// failureRootCause maps stable error codes to known root causes / fixes used
// by the auto-sink of daemon phase failures into the knowledge base.
type failureRootCause struct {
	RootCause string
	Fix       string
	Lesson    string
}

var failureRootCauses = map[string]failureRootCause{
	"API_KEY_UNAVAILABLE": {
		RootCause: "OMP 无法获取模型 API Key（KeePassXC 未解锁或 secret service 不可达；systemd 单元需 XDG_RUNTIME_DIR/DBUS_SESSION_BUS_ADDRESS）",
		Fix:       "解锁 KeePassXC（或配置 DEEPSEEK_API_KEY/CODEX_API_KEY 环境变量）；daemon 探测到 key 后自动恢复",
		Lesson:    "外部依赖不可用是可恢复条件，不应与真失败混为一谈；key 失败不重试不 fallback",
	},
	"PHASE_INTERRUPTED": {
		RootCause: "daemon 停机/重启中断了执行中的 OMP（SIGTERM → 保存 session 退出）",
		Fix:       "无需处理：重启后下一轮 scan 自动重新调度，阶段成功后标记自动清除",
		Lesson:    "部署重启是运维常态，任务状态保持 + PHASE_INTERRUPTED 标记使重启无损",
	},
	"MODEL_FAILED": {
		RootCause: "模型调用失败（进程异常退出、网络、模型服务错误）",
		Fix:       "查看 phase_log 定位具体错误（key/quota/网络）；必要时手动 resume",
		Lesson:    "先看日志再判断失败类别；MODEL_FAILED 是兜底分类",
	},
	"PHASE_TIMEOUT": {
		RootCause: "模型在阶段超时内无响应",
		Fix:       "检查网络与模型服务状态；fallback 模型会自动重试",
		Lesson:    "超时走独立分类，不与其他失败混淆",
	},
	"MODEL_QUOTA_EXHAUSTED": {
		RootCause: "模型 token 配额耗尽",
		Fix:       "前往模型平台充值后续航",
		Lesson:    "配额错误优先识别，避免误判为网络问题",
	},
}

// AppendFailurePattern sinks a daemon phase failure into the knowledge base as
// a bug pattern (现象/根因/修复/教训), deduplicated by error code + phase.
// First occurrence per code+phase is recorded; later occurrences are skipped
// (the file itself is the dedup store, surviving daemon restarts). Missing
// knowledge base is silently skipped.
func AppendFailurePattern(vaultDir, code, phase, taskID, logPath string) error {
	if vaultDir == "" {
		return nil
	}
	path := filepath.Join(vaultDir, "References", "core", "daemon-stuck-task-patterns.md")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // knowledge base not set up
		}
		return err
	}
	content := string(data)

	marker := code + " — " + phase + " 阶段失败"
	if strings.Contains(content, marker) {
		return nil // already recorded
	}

	rc, known := failureRootCauses[code]
	if !known {
		rc = failureRootCause{
			RootCause: "见 phase_log 定位具体原因",
			Fix:       "分析日志后修复，必要时手动 resume_approved",
			Lesson:    "未知错误码先归类再处理",
		}
	}

	patternNo := strings.Count(content, "## 模式") + 1
	now := time.Now().Format("2006-01-02")
	tail := tailLogTail(logPath, 4096)
	entry := fmt.Sprintf(`
---

## 模式 %d：%s — %s 阶段失败

**现象**：任务 TASK-%s 失败（错误码 %s，阶段 %s，日志：%s）。

**根因**：%s

**修复**：%s

**教训**：%s

**日志现场**：%s

**检查项**：出现 %s 错误码 → %s（自动沉淀于 %s）
`, patternNo, code, phase, taskID, code, phase, logPath,
		rc.RootCause, rc.Fix, rc.Lesson, tail, code, rc.Fix, now)

	// 追加到"更新记录"前:把条目插到文件末尾的检查清单/更新记录之前
	if idx := strings.LastIndex(content, "## 检查清单"); idx >= 0 {
		content = content[:idx] + entry + content[idx:]
	} else {
		content += entry
	}
	return os.WriteFile(path, []byte(content), 0o644)
}
