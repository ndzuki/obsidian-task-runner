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
	Topics   []string // from frontmatter
	Level    string   // beginner|intermediate|advanced|reference
	Updated  string   // ISO 8601
	Verified bool
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
		Path:  rel,
		Title: extractH1(data),
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
	b.WriteString(fmt.Sprintf("> 自动生成于 %s\n", time.Now().Format("2006-01-02")))
	b.WriteString(fmt.Sprintf("> 总计 %d 篇\n", len(entries)))

	layers := groupByLayer(entries)
	for _, layer := range layerOrder {
		group := layers[layer]
		if len(group) == 0 {
			continue
		}
		label := map[string]string{
			"core":     "Core（项目高频）",
			"extended": "Extended（偶尔使用）",
			"archived": "Archived（备份不检索）",
		}[layer]
		b.WriteString(fmt.Sprintf("\n## %s\n\n", label))
		b.WriteString("| 文件 | 标题 | topics | level | updated | verified |\n")
		b.WriteString("|------|------|--------|-------|---------|----------|\n")
		for _, e := range group {
			title := e.Title
			if title == "" {
				title = "⚠️"
			}
			topics := strings.Join(e.Topics, ", ")
			verified := "false"
			if e.Verified {
				verified = "true"
			}
			b.WriteString(fmt.Sprintf("| %s | %s | %s | %s | %s | %s |\n",
				e.Path, title, topics, e.Level, e.Updated, verified))
		}
	}
	return b.String()
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
