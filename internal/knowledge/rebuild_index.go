// Package knowledge implements the knowledge-base maintenance operations
// (INDEX.md rebuild, project knowledge extraction) in pure Go — no Python/bash.
package knowledge

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// RefEntry represents one document in the knowledge base.
type RefEntry struct {
	Path     string   // relative to References/ (e.g. "core/go/connect-rpc.md")
	Title    string   // first h1 in the document
	Summary  string   // first blockquote after h1 — KB v2 mandatory abstract
	Topics   []string // from frontmatter
	Level    string   // beginner|intermediate|advanced|reference
	Updated  string   // ISO 8601
	Verified bool
	Noisy    bool     // contains non-knowledge content (chat links, project file lists)
	Activity string   // high|normal|low — usage frequency, metadata not directory
}

// layerOrder defines display order for the three layers.
var layerOrder = []string{"core", "extended", "archived"}

// RebuildINDEX scans References/ and regenerates INDEX.md.
// Returns the number of entries written.
func RebuildINDEX(vaultDir string) (int, error) {
	refsDir := filepath.Join(vaultDir, "References")
	entries, err := scanReferences(refsDir)
	if err != nil {
		return 0, fmt.Errorf("scan References/: %w", err)
	}

	content := buildINDEX(entries)
	indexPath := filepath.Join(refsDir, "INDEX.md")
	if err := os.WriteFile(indexPath, []byte(content), 0o644); err != nil {
		return 0, fmt.Errorf("write INDEX.md: %w", err)
	}
	return len(entries), nil
}

// scanReferences walks References/ and parses every .md file's frontmatter.
func scanReferences(refsDir string) ([]RefEntry, error) {
	var entries []RefEntry
	err := filepath.WalkDir(refsDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".md") || d.Name() == "INDEX.md" {
			return nil
		}
		rel, _ := filepath.Rel(refsDir, path)
		entry, parseErr := parseRefFile(path, rel)
		if parseErr != nil {
			// Non-fatal: log but continue
			fmt.Fprintf(os.Stderr, "knowledge: skip %s: %v\n", rel, parseErr)
			return nil
		}
		entries = append(entries, *entry)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, nil
}

// parseRefFile reads one References/ .md file and extracts frontmatter + title.
func parseRefFile(path, rel string) (*RefEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	fm, _, err := parseFrontmatter(data)
	if err != nil {
		return nil, err
	}

	entry := &RefEntry{
		Path:    rel,
		Title:   extractH1(data),
		Summary: extractSummary(data),
		Noisy:   hasNoise(data),
	}
	if v, ok := fm["topics"]; ok {
		entry.Topics = toStringSlice(v)
	}
	if v, ok := fm["level"]; ok {
		entry.Level = fmt.Sprint(v)
	}
	if v, ok := fm["updated"]; ok {
		entry.Updated = fmt.Sprint(v)
	}
	if v, ok := fm["verified"]; ok {
		switch vv := v.(type) {
		case bool:
			entry.Verified = vv
		case string:
			entry.Verified = vv == "true"
		}
	}
	if v, ok := fm["activity"]; ok {
		switch vv := v.(type) {
		case string:
			if vv == "high" || vv == "low" {
				entry.Activity = vv
			}
		}
	}
	if entry.Activity == "" {
		entry.Activity = "normal"
	}
	return entry, nil
}

// parseFrontmatter extracts YAML between --- delimiters. Returns the raw map,
// the body after the closing ---, and any error.
func parseFrontmatter(data []byte) (map[string]any, string, error) {
	content := string(data)
	if !strings.HasPrefix(content, "---\n") {
		return nil, "", fmt.Errorf("no frontmatter")
	}
	closeIdx := strings.Index(content[4:], "\n---")
	if closeIdx < 0 {
		return nil, "", fmt.Errorf("unclosed frontmatter")
	}
	fmText := content[4 : 4+closeIdx]
	body := content[4+closeIdx+4:]

	var fm map[string]any
	if err := yaml.Unmarshal([]byte(fmText), &fm); err != nil {
		return nil, "", fmt.Errorf("yaml: %w", err)
	}
	return fm, body, nil
}

// extractH1 returns the first # heading from markdown content.
func extractH1(data []byte) string {
	re := regexp.MustCompile(`(?m)^# ([^#\n].*)$`)
	m := re.FindSubmatch(data)
	if m != nil {
		return strings.TrimSpace(string(m[1]))
	}
	return ""
}

// extractSummary returns the first blockquote line following the h1 — the
// KB v2 mandatory abstract (a "> ..." line directly after the title).
// Empty when the document predates KB v2 or lacks the abstract.
func extractSummary(data []byte) string {
	re := regexp.MustCompile(`(?m)^# [^#\n].*$\n{1,2}>\s*(.+)$`)
	m := re.FindSubmatch(data)
	if m != nil {
		return strings.TrimSpace(string(m[1]))
	}
	return ""
}

// kbNoisePatterns match non-knowledge content that pollutes entries: AI chat
// session links and project-specific file listings. Such files fail the KB v2
// quality bar and are flagged in INDEX instead of silently kept.
var kbNoisePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)https?://(claude\.ai|chatgpt\.com|poe\.com)/`),
	regexp.MustCompile(`(?m)^##? .*文件清单`),
	regexp.MustCompile(`(?m)^##? .*项目结构`),
}

func hasNoise(data []byte) bool {
	for _, pat := range kbNoisePatterns {
		if pat.Match(data) {
			return true
		}
	}
	return false
}

// toStringSlice converts a frontmatter value to []string.
func toStringSlice(v any) []string {
	switch vv := v.(type) {
	case []any:
		var out []string
		for _, item := range vv {
			out = append(out, fmt.Sprint(item))
		}
		return out
	case []string:
		return vv
	case string:
		return []string{vv}
	}
	return nil
}

// buildINDEX generates the INDEX.md markdown from entries.
func buildINDEX(entries []RefEntry) string {
	var b strings.Builder
	b.WriteString("# References INDEX\n\n")
	fmt.Fprintf(&b, "> 自动生成于 %s\n", time.Now().Format("2006-01-02"))
	fmt.Fprintf(&b, "> 总计 %d 篇\n", len(entries))
	verifiedCount, highCount := 0, 0
	staleCount := 0
	for _, e := range entries {
		if e.Verified {
			verifiedCount++
		}
		if e.Activity == "high" {
			highCount++
		}
		if t, err := time.Parse("2006-01-02", e.Updated); err == nil && time.Since(t) > 365*24*time.Hour {
			staleCount++
		}
	}
	fmt.Fprintf(&b, "> 可信度：verified %d/%d；活跃：high %d；可能过期(>365d)：%d\n",
		verifiedCount, len(entries), highCount, staleCount)

	layers := groupByLayer(entries)
	for _, layer := range layerOrder {
		group := layers[layer]
		if len(group) == 0 {
			continue
		}
		// Retrieval priority: verified → activity(high>normal>low) → updated desc.
		sort.SliceStable(group, func(i, j int) bool {
			if group[i].Verified != group[j].Verified {
				return group[i].Verified
			}
			a, bv := activityRank(group[i].Activity), activityRank(group[j].Activity)
			if a != bv {
				return a > bv
			}
			return group[i].Updated > group[j].Updated
		})
		label := map[string]string{
			"core":     "Core（平台与架构技术）",
			"extended": "Extended（运维与工具）",
			"archived": "Archived（已废弃，人工归档）",
		}[layer]
		fmt.Fprintf(&b, "\n## %s\n\n", label)
		b.WriteString("| 文件 | 标题 | 摘要 | topics | activity | level | updated | verified |\n")
		b.WriteString("|------|------|------|--------|----------|-------|---------|----------|\n")
		for _, e := range group {
			title := e.Title
			if title == "" {
				title = "⚠️"
			}
			summary := e.Summary
			if summary == "" {
				summary = "⚠️"
			}
			if e.Noisy {
				summary = "⚠️ 含噪音待清理: " + summary
			}
			summary = strings.ReplaceAll(summary, "|", "\\|")
			if r := []rune(summary); len(r) > 80 {
				summary = string(r[:77]) + "..."
			}
			topics := strings.Join(e.Topics, ", ")
			verified := "false"
			if e.Verified {
				verified = "true"
			}
			activity := e.Activity
			if activity == "" {
				activity = "normal"
			}
			if t, err := time.Parse("2006-01-02", e.Updated); err == nil && time.Since(t) > 365*24*time.Hour {
				activity += " ⚠️可能过期"
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s | %s | %s |\n",
				e.Path, title, summary, topics, activity, e.Level, e.Updated, verified)
		}
	}
	return b.String()
}

// activityRank maps the activity metadata to a sort weight.
func activityRank(a string) int {
	switch a {
	case "high":
		return 2
	case "low":
		return 0
	default:
		return 1
	}
}

// groupByLayer partitions entries by their top-level directory.
func groupByLayer(entries []RefEntry) map[string][]RefEntry {
	m := make(map[string][]RefEntry)
	for _, e := range entries {
		parts := strings.SplitN(e.Path, "/", 2)
		layer := parts[0]
		m[layer] = append(m[layer], e)
	}
	return m
}
