package yamlfrontmatter

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// 对齐测试：reference.md §4.9 完整字段附录、TASK 模板与 taskFieldOrder
// 三者必须一致。漂移在 merge 前被 CI 拦截，而不是等文档陈旧后再人工发现。
// 文件不存在时跳过（安装包/精简 checkout 不含 docs 与 templates）。

const repoRootRel = ".." + string(filepath.Separator) + ".."

func readRepoFile(t *testing.T, rel string) string {
	t.Helper()
	path := filepath.Join(repoRootRel, rel)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		t.Skipf("repo file %s not present — skipping alignment test", rel)
	}
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(data)
}

// TestReferenceFieldAppendixMatchesTaskFieldOrder pins reference.md §4.9：
// 附录键集合必须与 taskFieldOrder 完全一致（一个不多、一个不少）。
func TestReferenceFieldAppendixMatchesTaskFieldOrder(t *testing.T) {
	ref := readRepoFile(t, filepath.Join("obsidian-task-runner", "reference.md"))
	start := strings.Index(ref, "### 4.9 完整字段附录")
	if start < 0 {
		t.Skip("reference.md §4.9 appendix not found")
	}
	end := strings.Index(ref[start:], "分组含义")
	seg := ref[start : start+end]
	appendixKeys := map[string]bool{}
	for _, m := range regexp.MustCompile(`\| `+"`"+`([a-z_0-9]+)`+"`"+` \|`).FindAllStringSubmatch(seg, -1) {
		appendixKeys[m[1]] = true
	}
	orderKeys := map[string]bool{}
	for _, k := range taskFieldOrder {
		orderKeys[k] = true
	}
	for k := range appendixKeys {
		if !orderKeys[k] {
			t.Errorf("appendix key %q not in taskFieldOrder", k)
		}
	}
	for k := range orderKeys {
		if !appendixKeys[k] {
			t.Errorf("taskFieldOrder key %q missing from appendix", k)
		}
	}
	if len(appendixKeys) != len(taskFieldOrder) {
		t.Fatalf("appendix=%d order=%d", len(appendixKeys), len(taskFieldOrder))
	}
}

// TestTaskTemplateKeysMatchSchema pins templates/TASK-000-template.md：
// frontmatter 段内的所有键（含注释掉的可选键）必须是 taskFieldOrder 的
// 成员（或 scaffold 子键 kind/capabilities/preferences/notes），必填身份键
// 必须齐全。死字段回流会在 merge 前被拦下。
func TestTaskTemplateKeysMatchSchema(t *testing.T) {
	tpl := readRepoFile(t, filepath.Join("templates", "TASK-000-template.md"))
	fm := ""
	if a := strings.Index(tpl, "---"); a >= 0 {
		if b := strings.Index(tpl[a+3:], "---"); b >= 0 {
			fm = tpl[a+3 : a+3+b]
		}
	}
	if fm == "" {
		t.Skip("TASK template frontmatter not found")
	}
	allowed := map[string]bool{}
	for _, k := range taskFieldOrder {
		allowed[k] = true
	}
	for _, k := range []string{"kind", "capabilities", "preferences", "notes"} {
		allowed[k] = true // scaffold 子键
	}
	for _, m := range regexp.MustCompile(`(?m)^[ \t]*#?[ \t]*([a-z_][a-z_0-9]*)[ \t]*:`).FindAllStringSubmatch(fm, -1) {
		k := m[1]
		if !allowed[k] {
			t.Errorf("template key %q not in taskFieldOrder (dead field in template?)", k)
		}
	}
	for _, required := range []string{"id", "title", "project", "project_id", "assignee", "req_doc", "status"} {
		if !regexp.MustCompile(`(?m)^[ \t]*` + required + `[ \t]*:`).MatchString(fm) {
			t.Errorf("required identity key %q missing from template", required)
		}
	}
}

// TestRemovedFieldsNotInSchema pins the 2026-09-04 cleanup: the removed keys
// must stay out of struct, order and backfill table.
func TestRemovedFieldsNotInSchema(t *testing.T) {
	removed := []string{"template", "estimated_hours", "actual_hours", "component", "parent", "target_env", "due_date"}
	for _, k := range removed {
		for _, o := range taskFieldOrder {
			if o == k {
				t.Errorf("removed key %q still in taskFieldOrder", k)
			}
		}
		if _, ok := taskFieldDefaults[k]; ok {
			t.Errorf("removed key %q still backfilled by taskFieldDefaults", k)
		}
	}
}
