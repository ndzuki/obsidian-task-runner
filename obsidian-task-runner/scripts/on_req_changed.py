#!/usr/bin/env python3
"""Handle requirement document changes by updating related tasks.

When a Requirements/*.md file changes, find all tasks that reference it
(via the "req_doc" frontmatter field) and update them accordingly:

  - ready / plan-review → reset to ready (forces re-plan with updated reqs)
  - implementing / review / done → warn but don't change (already too far)

This ensures Claude re-reads the updated requirements on the next scan.
"""

import json
import os
import re
import sys


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
        elif status in ("implementing", "review", "done"):
            # Task is mid-execution, awaiting review, or completed —
            # mark as pending, daemon will auto-trigger new Round 1
            update_frontmatter_field(task_path, "pending_req", True)
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
        warn_count = sum(1 for a in affected if a["action"] == "warn_only")
        print(f"\n处理完成: {reset_count} 个任务已重置为 ready, {warn_count} 个任务因状态靠后仅警告")

    # Output JSON for daemon consumption
    print(json.dumps(affected, ensure_ascii=False))


if __name__ == "__main__":
    main()
