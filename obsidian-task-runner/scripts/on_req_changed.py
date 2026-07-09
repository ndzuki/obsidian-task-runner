#!/usr/bin/env python3
"""Handle requirement changes by updating or auto-creating related tasks.

When a Requirements/*.md file changes:
  - If a matching task already exists (via req_doc), reset/pending as before.
  - If no matching task AND filename matches REQ-<id>-<slug>.md,
    auto-create TASK-<id>-<slug>.md. assignee is left empty — user must fill
    before any agent can pick up the task.
"""

import json
import os
import re
import sys
from datetime import datetime, timezone, timedelta

# Only auto-create for files matching REQ-<id>-<slug>.md
REQ_FILENAME_RE = re.compile(r"^REQ-(?P<id>\d+)-(?P<slug>.+)\.md$")


def parse_frontmatter(text: str) -> dict:
    """Extract YAML frontmatter from markdown text."""
    if not text.startswith("---"):
        return {}
    end = text.find("---", 3)
    if end == -1:
        return {}
    raw = text[3:end].strip()
    result = {}
    current_key = None
    current_list = None
    in_list = False

    for line in raw.split("\n"):
        stripped = line.strip()
        if not stripped or stripped.startswith("#"):
            continue
        list_match = re.match(r"^\s*-\s+(.+)$", line)
        if list_match and in_list and current_key:
            current_list.append(list_match.group(1).strip().strip('"').strip("'"))
            continue
        kv_match = re.match(r"^(\w[\w_-]*)\s*:\s*(.*)$", line)
        if kv_match:
            if in_list and current_key:
                result[current_key] = current_list
                in_list = False
                current_list = None
            key = kv_match.group(1)
            value = kv_match.group(2).strip()
            if value == "":
                current_key = key
                current_list = []
                in_list = True
                continue
            if value.startswith('"') and value.endswith('"'):
                result[key] = value[1:-1]
            elif value.lower() == "true":
                result[key] = True
            elif value.lower() == "false":
                result[key] = False
            elif value.isdigit():
                result[key] = int(value)
            elif value in ('""', "''"):
                result[key] = ""
            else:
                result[key] = value
            current_key = key
    if in_list and current_key:
        result[current_key] = current_list
    return result


# ── filename parsing ──

def parse_req_filename(req_rel_path: str) -> tuple | None:
    """Parse REQ-<id>-<slug>.md and return (id, slug) or None."""
    filename = os.path.basename(req_rel_path.replace("\\", "/"))
    m = REQ_FILENAME_RE.match(filename)
    return (m.group("id"), m.group("slug")) if m else None


def task_filename_for_req(req_rel_path: str) -> str | None:
    """Derive task filename from requirement path."""
    parsed = parse_req_filename(req_rel_path)
    if parsed is None:
        return None
    return f"TASK-{parsed[0]}-{parsed[1]}.md"


# ── markdown helpers ──

def _first_heading(content: str) -> str:
    """Extract first-level heading text."""
    for line in content.splitlines():
        if line.startswith("# ") and not line.startswith("## "):
            return line[2:].strip()
    return ""


def _extract_section(content: str, *headings: str) -> list[str]:
    """Extract lines from a named H2 section until the next H2."""
    lines = content.splitlines()
    start = None
    for i, line in enumerate(lines):
        stripped = line.strip()
        if stripped.startswith("## ") and any(
            stripped[3:].strip() == h for h in headings
        ):
            start = i + 1
            break
    if start is None:
        return []
    result = []
    for line in lines[start:]:
        if line.startswith("## "):
            break
        if line.strip():
            result.append(line.rstrip())
    return result


# ── TASK document builder ──

def build_task_markdown(
    req_path: str, req_content: str, req_fm: dict, now_iso: str
) -> str:
    """Render a complete TASK-<id>-<slug>.md from a requirement document."""
    parsed = parse_req_filename(req_path)
    if parsed is None:
        return ""
    task_id, slug = parsed

    project = req_fm.get("project", "")
    priority = req_fm.get("priority", "P2")
    epic = req_fm.get("epic", "")
    tags = req_fm.get("tags", [])
    reviewer = req_fm.get("reviewer", "")
    target_env = req_fm.get("target_env", "staging")

    title = _first_heading(req_content) or slug.replace("-", " ").title()
    summary_lines = _extract_section(req_content, "要做什么")
    ac_lines = _extract_section(req_content, "完成标准", "验收标准")

    # Tags as YAML list
    tags_block = "\n".join(f"  - {t}" for t in tags) if tags else "  - "

    # Auto-created tasks always start blocked because assignee is intentionally
    # left empty. Once the user fills required fields, the daemon auto-promotes
    # the task to ready and starts Round 1.
    status = "blocked"

    body = f"""# TASK-{task_id}: {title}

## 需求摘要

{chr(10).join(summary_lines) if summary_lines else '<!-- 请从需求文档补充摘要 -->'}

## 验收标准

{chr(10).join(ac_lines) if ac_lines else '- [ ] 请补充可验证的验收标准'}

## 人工 Review 提醒

自动从 `Requirements/{os.path.basename(req_path)}` 生成。请确认以下字段：

| 字段 | 当前值 | 需要填？ |
|------|--------|---------|
| `project` | `{project or '（空）'}` | {'✅' if project else '🔴 必填'} |
| `assignee` | （空） | 🔴 必填（codex / claude / claude+human） |
| `off_peak_only` | `false` | 可选 |
| `plan_approved` | `false` | 人工 Gate #1 |
| `merge_approved` | `false` | 人工 Gate #2 |

> ⚠️ **任务已暂停在 `blocked`。** 请在 frontmatter 中补齐必填字段；补齐后保存，daemon 会自动进入 Round 1，无需手动把 `status` 改成 `ready`。{'' if project else chr(10) + chr(10) + '> ⚠️ **`project` 为空。** 请填写 `project` 字段。'}

## 实现计划
<!-- 🤖 Round 1: agent 自动填充 -->

## 实现记录
<!-- 🤖 Round 2: agent 自动填充 -->

## 验收记录
<!-- 🤖 agent + task-verifier 自动填充 -->
"""

    # Build frontmatter
    tags_fm = (
        "tags:\n" + "\n".join(f"  - {t}" for t in tags)
        if tags
        else "tags: []"
    )
    fm = f"""---
id: "{task_id}"
title: "{title}"
project: "{project}"
new_project: false
template: ""
status: "{status}"
plan_approved: false
merge_approved: false
pending_req: false
off_peak_only: false
created: "{now_iso}"
updated: "{now_iso}"
completed: ""
priority: {priority}
due_date: ""
estimated_hours: 0
actual_hours: 0
assignee: ""
reviewer: "{reviewer}"
req_doc: Requirements/{os.path.basename(req_path)}
component: ""
{tags_fm}
epic: "{epic}"
parent: ""
blocks: []
blocked_by: []
target_branch: ""
target_env: {target_env}
---
"""

    return fm + body


# ── TASK file I/O ──

def create_task_for_requirement(vault_path: str, req_rel_path: str) -> dict | None:
    """Auto-create a TASK file for a new requirement. Returns action dict or None."""
    parsed = parse_req_filename(req_rel_path)
    if parsed is None:
        return None

    tasks_dir = os.path.join(vault_path, "Tasks")
    os.makedirs(tasks_dir, exist_ok=True)

    target_name = task_filename_for_req(req_rel_path)
    task_path = os.path.join(tasks_dir, target_name)
    if os.path.exists(task_path):
        return None  # don't overwrite

    # Read requirement
    req_path = os.path.join(vault_path, req_rel_path)
    try:
        with open(req_path, "r") as f:
            req_content = f.read()
    except OSError:
        return None

    req_fm = parse_frontmatter(req_content)

    cst = timezone(timedelta(hours=8))
    now_iso = datetime.now(cst).strftime("%Y-%m-%dT%H:%M:%S%z")

    task_md = build_task_markdown(req_rel_path, req_content, req_fm, now_iso)
    if not task_md:
        return None

    # Atomic write via temp file
    tmp_path = task_path + ".tmp." + str(os.getpid())
    try:
        with open(tmp_path, "w") as f:
            f.write(task_md)
        os.replace(tmp_path, task_path)
    except OSError:
        if os.path.exists(tmp_path):
            os.remove(tmp_path)
        return None

    task_id = parsed[0]
    print(f"  {task_id} ({target_name}): 自动创建任务文档（status=blocked）")
    return {"task_id": task_id, "file": target_name, "action": "create_task"}


def update_frontmatter_field(file_path: str, field: str, new_value) -> bool:
    """Replace a single field in a markdown file's YAML frontmatter."""
    try:
        with open(file_path, "r") as f:
            content = f.read()
    except (OSError, UnicodeDecodeError):
        return False

    if not content.startswith("---"):
        return False
    end = content.find("---", 3)
    if end == -1:
        return False

    fm_text = content[3:end]
    body = content[end + 3:]

    new_fm_lines = []
    found = False
    for line in fm_text.split("\n"):
        stripped = line.strip()
        if re.match(rf"^{field}\s*:", stripped):
            if isinstance(new_value, bool):
                new_fm_lines.append(f"{field}: {str(new_value).lower()}")
            elif isinstance(new_value, str):
                new_fm_lines.append(f'{field}: "{new_value}"')
            else:
                new_fm_lines.append(f"{field}: {new_value}")
            found = True
        else:
            new_fm_lines.append(line)

    # Append new field at end of frontmatter if not found
    if not found:
        if isinstance(new_value, bool):
            new_fm_lines.append(f"{field}: {str(new_value).lower()}")
        elif isinstance(new_value, str):
            new_fm_lines.append(f'{field}: "{new_value}"')
        else:
            new_fm_lines.append(f"{field}: {new_value}")

    new_content = "---\n" + "\n".join(new_fm_lines) + "\n---" + body
    try:
        with open(file_path, "w") as f:
            f.write(new_content)
        return True
    except OSError:
        return False


def on_req_changed(vault_path: str, req_rel_path: str) -> list[dict]:
    """Process requirement change. Returns list of affected tasks."""
    tasks_dir = os.path.join(vault_path, "Tasks")
    if not os.path.isdir(tasks_dir):
        return []

    affected = []
    for filename in os.listdir(tasks_dir):
        if not filename.endswith(".md"):
            continue
        task_path = os.path.join(tasks_dir, filename)
        try:
            with open(task_path, "r") as f:
                fm = parse_frontmatter(f.read())
        except OSError:
            continue

        task_req = fm.get("req_doc", "")
        if not task_req:
            continue

        # Normalize: strip .md suffix on both sides, backslash → slash
        def norm(p: str) -> str:
            p = p.replace("\\", "/")
            if p.endswith(".md"):
                p = p[:-3]
            return p

        task_req_normalized = norm(task_req)
        req_normalized = norm(req_rel_path)

        # Match by name (ignoring extension), or by full relative path
        task_name = os.path.basename(task_req_normalized)
        req_name = os.path.basename(req_normalized)
        if not (task_req_normalized == req_normalized or
                task_name == req_name or
                task_req_normalized.endswith("/" + req_name)):
            continue

        task_id = fm.get("id", "?")
        status = fm.get("status", "ready")

        if status in ("ready", "plan-review"):
            # Reset to ready — Claude will re-read updated req in next scan
            update_frontmatter_field(task_path, "status", "ready")
            update_frontmatter_field(task_path, "plan_approved", False)
            affected.append({
                "task_id": task_id,
                "file": filename,
                "action": "reset_to_ready",
                "old_status": status,
            })
            print(f"  {task_id} ({filename}): {status} → ready（需求已更新，重新出计划）")
        elif status in ("implementing", "review", "conflict", "done"):
            # Task is mid-execution, awaiting review, or completed —
            # mark as pending, daemon will auto-trigger new Round 1.
            # Also clear merge_approved to prevent stale approval from
            # auto-triggering merge after re-implementation.
            update_frontmatter_field(task_path, "pending_req", True)
            update_frontmatter_field(task_path, "merge_approved", False)
            affected.append({
                "task_id": task_id,
                "file": filename,
                "action": "pending_req",
                "old_status": status,
            })
            print(f"  {task_id} ({filename}): status={status}，已标记 pending_req（自动重新出计划）")
        else:
            affected.append({
                "task_id": task_id,
                "file": filename,
                "action": "warn_only",
                "old_status": status,
            })
            print(f"  {task_id} ({filename}): status={status}，已跳过（请手动评估）",
                  file=sys.stderr)

    # Fallback: if no existing task, try auto-creating one
    if not affected:
        created = create_task_for_requirement(vault_path, req_rel_path)
        if created:
            affected = [created]

    return affected


def main():
    if len(sys.argv) < 3:
        print("Usage: on_req_changed.py <vault_path> <req_file_path>",
              file=sys.stderr)
        sys.exit(1)

    vault_path = sys.argv[1]
    req_file = sys.argv[2]

    # Make path relative to vault
    req_rel = req_file
    if req_rel.startswith(vault_path):
        req_rel = os.path.relpath(req_file, vault_path)

    print(f"需求文档变更: {req_rel}")

    affected = on_req_changed(vault_path, req_rel)

    if not affected:
        print("  没有关联的任务")
    else:
        reset_count = sum(1 for a in affected if a["action"] == "reset_to_ready")
        created_count = sum(1 for a in affected if a["action"] == "create_task")
        warn_count = sum(1 for a in affected if a["action"] == "warn_only")
        parts = []
        if reset_count:
            parts.append(f"{reset_count} 个任务已重置为 ready")
        if created_count:
            parts.append(f"{created_count} 个任务已新建")
        if warn_count:
            parts.append(f"{warn_count} 个任务因状态靠后仅警告")
        print(f"\n处理完成: {', '.join(parts)}")

    # Output JSON for daemon consumption
    print(json.dumps(affected, ensure_ascii=False))


if __name__ == "__main__":
    main()
