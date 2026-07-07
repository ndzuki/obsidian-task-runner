#!/usr/bin/env python3
"""Update YAML frontmatter fields in an Obsidian Task document.

Handles the automatic timestamp fields:
  - updated: always set to now() on any change
  - created: set to now() if currently empty
  - completed: set to now() when status changes to 'done'
"""

import sys
import os
import re
from datetime import datetime, timezone


def now_iso() -> str:
    return datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%S")


def update_task_status(task_path: str, updates: dict) -> bool:
    """Update frontmatter fields in a task markdown file.

    Args:
        task_path: Absolute path to the task .md file
        updates: Dict of field_name -> new_value

    Returns:
        True on success, False on failure
    """
    if not os.path.isfile(task_path):
        print(f"Error: {task_path} not found", file=sys.stderr)
        return False

    try:
        with open(task_path, "r") as f:
            content = f.read()
    except OSError as e:
        print(f"Error reading {task_path}: {e}", file=sys.stderr)
        return False

    # Locate frontmatter boundaries
    if not content.startswith("---"):
        print(f"Error: {task_path} has no frontmatter", file=sys.stderr)
        return False

    end = content.find("---", 3)
    if end == -1:
        print(f"Error: {task_path} frontmatter not closed", file=sys.stderr)
        return False

    fm_text = content[3:end]
    body = content[end + 3:]

    # Auto-timestamps
    updates["updated"] = now_iso()
    if "created" in updates:
        pass  # explicitly set
    else:
        # Set created if empty
        created_match = re.search(r"^created\s*:\s*(.*)$", fm_text, re.MULTILINE)
        if created_match and created_match.group(1).strip() in ('""', "''", ""):
            updates["created"] = now_iso()
        elif not created_match:
            updates["created"] = now_iso()

    if updates.get("status") == "done" and "completed" not in updates:
        updates["completed"] = now_iso()

    # Apply updates
    new_fm_lines = []
    updated_keys = set()
    for line in fm_text.split("\n"):
        stripped = line.strip()
        if not stripped or stripped.startswith("#"):
            new_fm_lines.append(line)
            continue

        match = re.match(r"^(\w[\w_-]*)\s*:", stripped)
        if match:
            key = match.group(1)
            if key in updates:
                value = updates[key]
                new_fm_lines.append(format_field(key, value))
                updated_keys.add(key)
                continue

        new_fm_lines.append(line)

    # Append new fields that weren't in the original frontmatter
    for key, value in updates.items():
        if key not in updated_keys:
            new_fm_lines.append(format_field(key, value))

    new_content = "---\n" + "\n".join(new_fm_lines) + "\n---" + body

    try:
        with open(task_path, "w") as f:
            f.write(new_content)
        return True
    except OSError as e:
        print(f"Error writing {task_path}: {e}", file=sys.stderr)
        return False


def format_field(key: str, value) -> str:
    """Format a frontmatter field value."""
    if isinstance(value, bool):
        return f"{key}: {str(value).lower()}"
    elif isinstance(value, (int, float)):
        return f"{key}: {value}"
    elif isinstance(value, list):
        if not value:
            return f"{key}: []"
        items = ", ".join(f'"{v}"' if isinstance(v, str) else str(v) for v in value)
        return f"{key}: [{items}]"
    elif isinstance(value, str):
        if value == "":
            return f'{key}: ""'
        return f'{key}: "{value}"'
    else:
        return f'{key}: {value}'


def main():
    if len(sys.argv) < 2:
        print("Usage: update_task_status.py <task_path> [field=value ...]", file=sys.stderr)
        print("Examples:", file=sys.stderr)
        print('  update_task_status.py task.md status=plan-review', file=sys.stderr)
        print('  update_task_status.py task.md status=done plan_approved=false', file=sys.stderr)
        sys.exit(1)

    task_path = sys.argv[1]
    updates = {}
    for arg in sys.argv[2:]:
        if "=" not in arg:
            print(f"Warning: skipping invalid arg '{arg}' (expected key=value)", file=sys.stderr)
            continue
        key, value = arg.split("=", 1)
        # Type coercion
        if value.lower() == "true":
            value = True
        elif value.lower() == "false":
            value = False
        elif value.isdigit():
            value = int(value)
        updates[key] = value

    if not updates:
        print("Error: no fields to update", file=sys.stderr)
        sys.exit(1)

    success = update_task_status(task_path, updates)
    sys.exit(0 if success else 1)


if __name__ == "__main__":
    main()
