#!/usr/bin/env python3
"""Scan Obsidian Vault Tasks/ directory and output ready-to-process tasks as NDJSON.

A task is "ready" when:
  - status == "ready" (fresh task, Round 1 needed)
  - status == "plan-review" AND plan_approved == true (Round 2 needed)
  - status == "review" AND merge_approved == true (merge phase needed)
  - status == "conflict" AND merge_approved == true (retry merge)

Round 2 tasks with off_peak_only: true are deferred during Beijing peak hours
(09:00-12:00, 14:00-18:00 CST) to reduce token costs.

Output is sorted by priority (P0 first), then by creation date.
"""

import json
import os
import sys
import re
from datetime import datetime, timezone, timedelta


def parse_frontmatter(text: str) -> dict:
    """Extract YAML frontmatter from markdown text. Handles basic YAML types."""
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
        # Skip empty lines and comments
        stripped = line.strip()
        if not stripped or stripped.startswith("#"):
            continue

        # List item
        list_match = re.match(r"^\s*-\s+(.+)$", line)
        if list_match and in_list and current_key:
            val = list_match.group(1).strip().strip('"').strip("'")
            current_list.append(val)
            continue

        # Key-value pair
        kv_match = re.match(r"^(\w[\w_-]*)\s*:\s*(.*)$", line)
        if kv_match:
            # Flush previous list
            if in_list and current_key:
                result[current_key] = current_list
                in_list = False
                current_list = None

            key = kv_match.group(1)
            value = kv_match.group(2).strip()

            # Empty value — could be start of a list on next line
            if value == "":
                current_key = key
                current_list = []
                in_list = True
                continue

            # Quoted string
            if value.startswith('"') and value.endswith('"'):
                result[key] = value[1:-1]
            elif value.startswith("'") and value.endswith("'"):
                result[key] = value[1:-1]
            # Boolean
            elif value.lower() == "true":
                result[key] = True
            elif value.lower() == "false":
                result[key] = False
            # Number
            elif value.isdigit():
                result[key] = int(value)
            # Float
            elif re.match(r"^\d+\.\d+$", value):
                result[key] = float(value)
            # Empty string
            elif value in ('""', "''"):
                result[key] = ""
            else:
                result[key] = value

            current_key = key

    # Flush last list
    if in_list and current_key:
        result[current_key] = current_list

    return result


def priority_order(priority: str) -> int:
    """Convert P0-P4 to sortable integer."""
    order = {"P0": 0, "P1": 1, "P2": 2, "P3": 3, "P4": 4}
    return order.get(priority.upper(), 2)


def is_ready(frontmatter: dict) -> bool:
    """Check if a task is ready for processing."""
    status = frontmatter.get("status", "ready")
    plan_approved = frontmatter.get("plan_approved", False)
    merge_approved = frontmatter.get("merge_approved", False)
    pending_req = frontmatter.get("pending_req", False)
    assignee = frontmatter.get("assignee", "")

    # assignee must be set — daemon won't pick up tasks without one
    if not assignee or not assignee.strip():
        return False

    if status == "ready":
        return True
    if status == "plan-review" and plan_approved is True:
        return True
    if status == "review" and merge_approved is True:
        return True
    if status == "conflict" and merge_approved is True:
        return True
    # Req doc was updated while task was implementing/review/done —
    # needs re-planning regardless of current status
    if pending_req is True:
        return True
    return False


def is_off_peak() -> bool:
    """Check if current Beijing time (CST, UTC+8) is in off-peak hours.

    DeepSeek peak pricing: 09:00-12:00 and 14:00-18:00 CST.
    Returns True only during off-peak (cheaper) hours.
    """
    cst = timezone(timedelta(hours=8))
    now_cst = datetime.now(cst)
    hour = now_cst.hour
    if 9 <= hour < 12:   # morning peak
        return False
    if 14 <= hour < 18:  # afternoon peak
        return False
    return True


def find_ready_tasks(vault_path: str) -> list[dict]:
    """Find all ready tasks in the vault, sorted by priority."""
    tasks_dir = os.path.join(vault_path, "Tasks")
    if not os.path.isdir(tasks_dir):
        return []

    ready_tasks = []
    for filename in os.listdir(tasks_dir):
        if not filename.endswith(".md"):
            continue
        file_path = os.path.join(tasks_dir, filename)
        try:
            with open(file_path, "r") as f:
                content = f.read()
        except (OSError, UnicodeDecodeError):
            continue

        fm = parse_frontmatter(content)
        if not is_ready(fm):
            continue

        # Skip templates — tasks with empty project field
        if not fm.get("project", ""):
            continue

        # Defer off-peak-only Round 2 tasks during peak hours
        status = fm.get("status", "ready")
        plan_approved = fm.get("plan_approved", False)
        off_peak_only = fm.get("off_peak_only", False)
        if (status == "plan-review" and plan_approved is True
                and off_peak_only is True and not is_off_peak()):
            task_id = fm.get("id", "?")
            print(f"  {task_id} ({filename}): Round 2 因 off_peak_only 延迟"
                  f"（当前北京时间 {datetime.now(timezone(timedelta(hours=8))).strftime('%H:%M')}"
                  f"，高峰时段）", file=sys.stderr)
            continue

        ready_tasks.append({
            "id": fm.get("id", ""),
            "title": fm.get("title", ""),
            "project": fm.get("project", ""),
            "new_project": fm.get("new_project", False),
            "priority": fm.get("priority", "P2"),
            "file_path": file_path,
            "file_name": filename,
            "status": fm.get("status", "ready"),
            "plan_approved": fm.get("plan_approved", False),
            "merge_approved": fm.get("merge_approved", False),
            "req_doc": fm.get("req_doc", ""),
            "template": fm.get("template", ""),
            "assignee": fm.get("assignee", ""),
            "auto_approve": fm.get("auto_approve", False),
            "pending_req": fm.get("pending_req", False),
            "off_peak_only": fm.get("off_peak_only", False),
            "switch_settings": fm.get("switch_settings", False),
        })

    ready_tasks.sort(key=lambda t: (priority_order(t["priority"]), t["id"]))
    return ready_tasks


def main():
    if len(sys.argv) < 2:
        print("Usage: find_ready_tasks.py <vault_path>", file=sys.stderr)
        sys.exit(1)

    vault_path = sys.argv[1]
    tasks = find_ready_tasks(vault_path)
    for task in tasks:
        print(json.dumps(task, ensure_ascii=False))


if __name__ == "__main__":
    main()
