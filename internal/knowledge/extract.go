package knowledge

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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
	Unclassified []string // ADR ids auto-archived under References/uncategorized/
	Topics       []string // matched knowledge topics (deduped) — feeds scaffold_registry
	Touched      []string // absolute paths of knowledge files created/updated
	Errors       []string
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
	if len(adrIDs) == 0 {
		_ = markTaskExtracted(taskPath)
		return result, nil
	}

	refsDir := filepath.Join(vaultDir, "References")
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
	entry := fmt.Sprintf(`
---

## 模式 %d：%s — %s 阶段失败

**现象**：任务 TASK-%s 失败（错误码 %s，阶段 %s，日志：%s）。

**根因**：%s

**修复**：%s

**教训**：%s

**检查项**：出现 %s 错误码 → %s（自动沉淀于 %s）
`, patternNo, code, phase, taskID, code, phase, logPath,
		rc.RootCause, rc.Fix, rc.Lesson, code, rc.Fix, now)

	// 追加到"更新记录"前:把条目插到文件末尾的检查清单/更新记录之前
	if idx := strings.LastIndex(content, "## 检查清单"); idx >= 0 {
		content = content[:idx] + entry + content[idx:]
	} else {
		content += entry
	}
	return os.WriteFile(path, []byte(content), 0o644)
}
