// Package adr manages project architecture decision records.
package adr

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

var requiredSections = []string{
	"## Status",
	"## Context",
	"## Decision",
	"## Alternatives Considered",
	"## Consequences",
}

func Write(projectDir, taskID, title, body string) (string, error) {
	adrDir := filepath.Join(projectDir, "Notes", "adr")
	if err := os.MkdirAll(adrDir, 0o755); err != nil {
		return "", fmt.Errorf("create ADR directory: %w", err)
	}
	id, err := nextID(adrDir)
	if err != nil {
		return "", err
	}
	slug := slugify(title)
	fileName := fmt.Sprintf("ADR-%03d-%s.md", id, slug)
	path := filepath.Join(adrDir, fileName)
	date := time.Now().Format("2006-01-02")
	content := fmt.Sprintf(`---
adr_id: "ADR-%03d"
title: "%s"
status: "accepted"
decision_scope: "project"
created: "%s"
updated: "%s"
requirements:
  - "REQ-%s"
tasks:
  - "TASK-%s"
---

%s`, id, title, date, date, taskID, taskID, strings.TrimSpace(body)+"\n")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("write ADR: %w", err)
	}
	if err := Validate(path); err != nil {
		return "", err
	}
	return path, nil
}

func Validate(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	content := string(data)
	if !strings.HasPrefix(content, "---\n") || !strings.Contains(content, "\n---\n") {
		return fmt.Errorf("ADR frontmatter is missing")
	}
	for _, section := range requiredSections {
		if !strings.Contains(content, section) {
			return fmt.Errorf("ADR missing section %s", section)
		}
	}
	if !strings.Contains(content, "status: \"accepted\"") && !strings.Contains(content, "\naccepted\n") {
		return fmt.Errorf("ADR status must be accepted")
	}
	return nil
}

func BuildIndex(projectDir string) error {
	adrDir := filepath.Join(projectDir, "Notes", "adr")
	entries, err := os.ReadDir(adrDir)
	if err != nil {
		return err
	}
	var rows []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "ADR-") || filepath.Ext(entry.Name()) != ".md" || entry.Name() == "ADR-INDEX.md" || entry.Name() == "ADR-COVERAGE.md" {
			continue
		}
		path := filepath.Join(adrDir, entry.Name())
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			continue
		}
		title := firstValue(string(data), "title")
		tasks := listValues(string(data), "tasks")
		rows = append(rows, fmt.Sprintf("| [[%s]] | %s | %s |", strings.TrimSuffix(entry.Name(), ".md"), title, strings.Join(tasks, ", ")))
	}
	sort.Strings(rows)
	content := "# ADR-INDEX\n\n| ADR | Decision | Tasks |\n|---|---|---|\n" + strings.Join(rows, "\n") + "\n"
	return os.WriteFile(filepath.Join(adrDir, "ADR-INDEX.md"), []byte(content), 0o644)
}

func BuildCoverage(projectDir string) error {
	adrDir := filepath.Join(projectDir, "Notes", "adr")
	entries, err := os.ReadDir(adrDir)
	if err != nil {
		return err
	}
	taskADRs := map[string][]string{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "ADR-") || filepath.Ext(entry.Name()) != ".md" || entry.Name() == "ADR-INDEX.md" || entry.Name() == "ADR-COVERAGE.md" {
			continue
		}
		data, readErr := os.ReadFile(filepath.Join(adrDir, entry.Name()))
		if readErr != nil {
			continue
		}
		for _, task := range listValues(string(data), "tasks") {
			taskADRs[task] = append(taskADRs[task], "[["+strings.TrimSuffix(entry.Name(), ".md")+"]]")
		}
	}
	var tasks []string
	for task := range taskADRs {
		tasks = append(tasks, task)
	}
	sort.Strings(tasks)
	var rows []string
	for _, task := range tasks {
		rows = append(rows, fmt.Sprintf("| %s | %s |", task, strings.Join(taskADRs[task], ", ")))
	}
	content := "# ADR-COVERAGE\n\n| Task | ADRs |\n|---|---|\n" + strings.Join(rows, "\n") + "\n"
	return os.WriteFile(filepath.Join(adrDir, "ADR-COVERAGE.md"), []byte(content), 0o644)
}

func nextID(dir string) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	maxID := 0
	for _, entry := range entries {
		name := entry.Name()
		if len(name) < 7 || !strings.HasPrefix(name, "ADR-") {
			continue
		}
		id, parseErr := strconv.Atoi(name[4:7])
		if parseErr == nil && id > maxID {
			maxID = id
		}
	}
	return maxID + 1, nil
}

func slugify(title string) string {
	slug := strings.ToLower(strings.TrimSpace(title))
	slug = nonSlug.ReplaceAllString(slug, "-")
	slug = strings.Trim(slug, "-")
	if slug == "" {
		return "decision"
	}
	return slug
}

func firstValue(content, key string) string {
	prefix := key + ":"
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), prefix) {
			return strings.Trim(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), prefix)), "\"")
		}
	}
	return ""
}

func listValues(content, key string) []string {
	lines := strings.Split(content, "\n")
	prefix := key + ":"
	var values []string
	inList := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == prefix {
			inList = true
			continue
		}
		if inList {
			if !strings.HasPrefix(trimmed, "-") {
				break
			}
			values = append(values, strings.Trim(strings.TrimSpace(strings.TrimPrefix(trimmed, "-")), "\""))
		}
	}
	return values
}
