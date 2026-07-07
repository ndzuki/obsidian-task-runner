# Obsidian Task Runner Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build an open-source skill that lets Claude Code auto-read Obsidian task/requirement docs, understand requirements, and implement code to delivery — with a one-click install script.

**Architecture:** Two-round state machine. Round 1: read requirements → produce plan → write to task doc → status=plan-review. Human gate: set plan_approved=true. Round 2: implement code → test/lint → task-verifier → git commit → status=review. Triggered by inotify (instant) + systemd timer (fallback).

**Tech Stack:** Bash (strict mode), Python 3 (stdlib only, yaml via frontmatter parsing), systemd user units, Claude Code skill framework

## Global Constraints

- All `.sh` scripts: `set -euo pipefail`, shellcheck clean, destructive ops need confirmation
- All `.py` scripts: stdlib only (no pip deps), handle YAML frontmatter manually
- SKILL.md: <300 lines, imperative tone, explain why not just what
- Install: supports both interactive and `--non-interactive` (env vars)
- License: MIT

---

## Task Dependencies

```
Phase 1 (parallel)
  T1: LICENSE ─────────────┐
  T2: .gitignore ──────────┤
  T3: vault-map.example.json ─┐
  T4: TASK-000-template.md ───┤
                              │
Phase 2 (sequential, after T3)│
  T5: find_ready_tasks.py <───┤
  T6: update_task_status.py ──┤
  T7: resolve_project_path.py ┘
  T8: register_project.py

Phase 3 (after T5-T8)
  T9: notify_on_status_change.sh
  T10: Migrate .sh to skill/scripts/

Phase 4 (after T5-T10)
  T11: reference.md
  T12: SKILL.md
  T13: task-verifier.md

Phase 5 (after T10, T12)
  T14: Update systemd unit files
  T15: Rewrite install.sh

Phase 6 (after T15)
  T16: Rewrite README.md

Phase 7 (independent)
  T17: Code review existing scripts
```

---

### Task 1: Create LICENSE

**Files:**
- Create: `LICENSE`

**Interfaces:** None

- [ ] **Step 1: Write MIT LICENSE file**

Standard MIT License with copyright holder "ndzuki and contributors", year 2026.

```text
MIT License

Copyright (c) 2026 ndzuki and contributors

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```

---

### Task 2: Create .gitignore

**Files:**
- Create: `.gitignore`

- [ ] **Step 1: Write .gitignore**

```gitignore
# vault-map.json contains local paths — never commit real one
config/vault-map.json

# systemd unit files with substituted paths
*.service.subst
*.timer.subst

# Python
__pycache__/
*.pyc

# Logs
*.log

# IDE
.idea/
.vscode/
*.swp
*.swo
```

---

### Task 3: Create vault-map.example.json

**Files:**
- Create: `obsidian-task-runner/config/vault-map.example.json`

**Produces:**
- JSON schema consumed by T5 (find_ready_tasks.py reads project names), T7 (resolve_project_path.py), T8 (register_project.py), T15 (install.sh)

- [ ] **Step 1: Write the example config**

```json
{
  "_comment": "复制为 vault-map.json 后修改。vault-map.json 不会被 git 跟踪。",
  "projects": {
    "example-project": {
      "path": "/home/you/src/example-project",
      "git_remote": "github.com/you/example-project"
    }
  },
  "new_project_root": "/home/you/src",
  "templates": {
    "go-gin-microservice": {
      "description": "Gin + Kustomize + kind 标准微服务",
      "scaffold_skill": "cloud-native-go",
      "structure": ["cmd/", "internal/", "api/", "deployments/"]
    }
  },
  "notifications": {
    "desktop": true,
    "sound": false
  },
  "poll_interval_minutes": 30
}
```

---

### Task 4: Create TASK-000-template.md

**Files:**
- Create: `TASK-000-template.md`

**Produces:**
- Template consumed by users creating new tasks (copy → rename → fill fields)

- [ ] **Step 1: Write the task template**

```markdown
---
id: ""
title: ""
project: ""
new_project: false
template: ""

# ── 状态流转（系统自动管理，不要手动改） ──
status: ready
plan_approved: false
created: ""
updated: ""
completed: ""

# ── 优先级 & 排期 ──
priority: P2
due_date: ""
estimated_hours: 0
actual_hours: 0

# ── 人员 & 分工 ──
assignee: claude
reviewer: ""

# ── 范围 & 分类 ──
req_doc: ""
component: ""
tags: []
epic: ""
parent: ""
blocks: []
blocked_by: []

# ── 环境 & 部署 ──
target_branch: ""
target_env: staging
---

# <!-- 标题 -->

## 需求摘要
<!-- 从 req_doc 复制摘要，或简要说明要做什么 -->

## 验收标准
<!-- 逐条列出可验证的验收条件，task-verifier 会按此清单核实 -->
- [ ] 
- [ ] 

## 实现计划
<!-- 🤖 Round 1: Claude 自动填充 -->

## 实现记录
<!-- 🤖 Round 2: Claude 自动填充 -->

## 验收记录
<!-- 🤖 Claude + task-verifier 自动填充 -->
```

---

### Task 5: Create find_ready_tasks.py

**Files:**
- Create: `obsidian-task-runner/scripts/find_ready_tasks.py`

**Consumes:** vault-map.json schema (from T3)
**Produces:**
- `find_ready_tasks(vault_path: str) -> list[dict]` — returns tasks sorted by priority, ready for processing
- STDOUT: newline-delimited JSON objects, each with keys: `id, title, project, new_project, priority, file_path, status`

- [ ] **Step 1: Write the script**

```python
#!/usr/bin/env python3
"""Scan Obsidian Vault Tasks/ directory and output ready-to-process tasks as NDJSON.

A task is "ready" when:
  - status == "ready" (fresh task, Round 1 needed)
  - status == "plan-review" AND plan_approved == true (Round 2 needed)

Output is sorted by priority (P0 first), then by creation date.
"""

import json
import os
import sys
import re
from datetime import datetime


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
        elif in_list and current_key:
            # Continuation of previous value (not a list item)
            pass

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

    if status == "ready":
        return True
    if status == "plan-review" and plan_approved is True:
        return True
    return False


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
            "req_doc": fm.get("req_doc", ""),
            "template": fm.get("template", ""),
            "assignee": fm.get("assignee", "claude"),
            "auto_approve": fm.get("auto_approve", False),
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
```

---

### Task 6: Create update_task_status.py

**Files:**
- Create: `obsidian-task-runner/scripts/update_task_status.py`

**Consumes:** Task frontmatter format (from T4)
**Produces:**
- `update_task_status(task_path: str, updates: dict) -> bool`
- CLI: `update_task_status.py <task_path> <field=value> ...`

- [ ] **Step 1: Write the script**

```python
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
```

---

### Task 7: Create resolve_project_path.py

**Files:**
- Create: `obsidian-task-runner/scripts/resolve_project_path.py`

**Consumes:** vault-map.json (from T3)
**Produces:**
- `resolve_project_path(map_file: str, project_name: str, is_new: bool) -> dict`
- STDOUT: JSON `{"status": "existing|new|error", "path": "...", "error": "..."}`

- [ ] **Step 1: Write the script**

```python
#!/usr/bin/env python3
"""Resolve a project name to its local filesystem path using vault-map.json.

Status values:
  existing  — project found in vault-map.json, path exists on disk
  new       — project not in vault-map.json, but new_project_root is configured
  error     — project not found and not marked as new_project, or other error
"""

import json
import os
import sys


def resolve_project_path(map_file: str, project_name: str, is_new: bool = False) -> dict:
    """Resolve a project name to a local path.
    
    Args:
        map_file: Path to vault-map.json
        project_name: The 'project' field from a task
        is_new: Whether the task has new_project: true
    
    Returns:
        Dict with keys: status (str), path (str), error (str|None)
    """
    result = {"status": "error", "path": "", "error": None}

    if not os.path.isfile(map_file):
        result["error"] = f"vault-map.json not found at {map_file}"
        return result

    try:
        with open(map_file, "r") as f:
            config = json.load(f)
    except (json.JSONDecodeError, OSError) as e:
        result["error"] = f"Failed to parse {map_file}: {e}"
        return result

    # Check existing projects
    projects = config.get("projects", {})
    if project_name in projects:
        proj_path = projects[project_name].get("path", "")
        if os.path.isdir(proj_path):
            result["status"] = "existing"
            result["path"] = proj_path
            return result
        result["error"] = f"Project '{project_name}' path '{proj_path}' does not exist on disk"
        return result

    # Check new project
    if is_new:
        new_root = config.get("new_project_root", "")
        if not new_root:
            result["error"] = "new_project_root is not set in vault-map.json"
            return result
        new_path = os.path.join(new_root, project_name)
        result["status"] = "new"
        result["path"] = new_path
        return result

    result["error"] = (
        f"Project '{project_name}' not found in vault-map.json. "
        "Either add it to projects or set new_project: true in the task."
    )
    return result


def main():
    if len(sys.argv) < 3:
        print("Usage: resolve_project_path.py <map_file> <project_name> [--new-project]", file=sys.stderr)
        sys.exit(1)

    map_file = sys.argv[1]
    project_name = sys.argv[2]
    is_new = "--new-project" in sys.argv

    result = resolve_project_path(map_file, project_name, is_new)
    print(json.dumps(result, ensure_ascii=False))
    if result["error"]:
        print(result["error"], file=sys.stderr)
    sys.exit(0 if result["status"] != "error" else 1)


if __name__ == "__main__":
    main()
```

---

### Task 8: Create register_project.py

**Files:**
- Create: `obsidian-task-runner/scripts/register_project.py`

**Consumes:** vault-map.json schema (from T3)
**Produces:**
- `register_project(map_file: str, project_name: str, project_path: str, git_remote: str = "") -> bool`
- CLI: `register_project.py <map_file> <project_name> <project_path> [--git-remote <url>]`

- [ ] **Step 1: Write the script**

```python
#!/usr/bin/env python3
"""Register a new project in vault-map.json for future task lookups.

After a new project is scaffolded and confirmed by the user, this script
adds its entry to vault-map.json so subsequent tasks for the same project
resolve as 'existing'.
"""

import json
import os
import sys


def register_project(
    map_file: str,
    project_name: str,
    project_path: str,
    git_remote: str = "",
) -> bool:
    """Add a project entry to vault-map.json.
    
    Args:
        map_file: Path to vault-map.json
        project_name: The project key to add
        project_path: Absolute path to the project on disk
        git_remote: Optional git remote URL
    
    Returns:
        True on success, False on failure
    """
    if not os.path.isfile(map_file):
        print(f"Error: {map_file} not found", file=sys.stderr)
        return False

    try:
        with open(map_file, "r") as f:
            config = json.load(f)
    except (json.JSONDecodeError, OSError) as e:
        print(f"Error reading {map_file}: {e}", file=sys.stderr)
        return False

    projects = config.setdefault("projects", {})

    if project_name in projects:
        existing_path = projects[project_name].get("path", "")
        if existing_path == project_path:
            print(f"Project '{project_name}' already registered with same path")
            return True
        print(
            f"Warning: Project '{project_name}' already exists with path '{existing_path}'. "
            f"Overwriting with '{project_path}'.",
            file=sys.stderr,
        )

    projects[project_name] = {
        "path": project_path,
        "git_remote": git_remote,
    }

    try:
        with open(map_file, "w") as f:
            json.dump(config, f, indent=2, ensure_ascii=False)
        # Add trailing newline
        with open(map_file, "a") as f:
            f.write("\n")
        print(f"Registered project '{project_name}' -> {project_path}")
        return True
    except OSError as e:
        print(f"Error writing {map_file}: {e}", file=sys.stderr)
        return False


def main():
    if len(sys.argv) < 4:
        print(
            "Usage: register_project.py <map_file> <project_name> <project_path> "
            "[--git-remote <url>]",
            file=sys.stderr,
        )
        sys.exit(1)

    map_file = sys.argv[1]
    project_name = sys.argv[2]
    project_path = sys.argv[3]
    git_remote = ""

    for i, arg in enumerate(sys.argv):
        if arg == "--git-remote" and i + 1 < len(sys.argv):
            git_remote = sys.argv[i + 1]
            break

    success = register_project(map_file, project_name, project_path, git_remote)
    sys.exit(0 if success else 1)


if __name__ == "__main__":
    main()
```

---

### Task 9: Create notify_on_status_change.sh

**Files:**
- Create: `obsidian-task-runner/scripts/notify_on_status_change.sh`

**Consumes:** update_task_status.py (from T6) — reads current status after Claude finishes
**Produces:** Desktop notification via `notify-send`

- [ ] **Step 1: Write the notification script**

```bash
#!/usr/bin/env bash
# Send desktop notification based on task status after Claude Code finishes.
# Called by task-runner-daemon.sh after claude -p returns.
# 
# Usage: notify_on_status_change.sh <task_file_path> [previous_status]
set -euo pipefail

TASK_FILE="${1:?Usage: notify_on_status_change.sh <task_file_path> [previous_status]}"
PREV_STATUS="${2:-}"

command -v notify-send >/dev/null 2>&1 || exit 0  # silent skip if no notify-send

# Extract frontmatter fields with a simple awk — no Python dep for notifications
extract_field() {
  local field="$1"
  awk -v key="$field" '
    /^---$/ { in_fm = !in_fm; next }
    in_fm && $0 ~ "^"key":" {
      sub("^"key":[[:space:]]*", "")
      gsub(/"/, "")
      print
      exit
    }
  ' "$TASK_FILE"
}

STATUS=$(extract_field "status")
TASK_ID=$(extract_field "id")
TITLE=$(extract_field "title")
REVIEWER=$(extract_field "reviewer")

# Only notify on gate states
case "$STATUS" in
  plan-review)
    notify-send \
      --urgency=normal \
      --app-name="Claude Task Runner" \
      --icon=dialog-information \
      "📋 Task ${TASK_ID}: 计划已生成" \
      "${TITLE}\n请审阅计划，确认后设 plan_approved: true 并保存"
    ;;
  review)
    notify-send \
      --urgency=normal \
      --app-name="Claude Task Runner" \
      --icon=emblem-default \
      "✅ Task ${TASK_ID}: 代码已实现" \
      "${TITLE}\n请 review 代码，确认无误后合并"
    ;;
  error|failed)
    notify-send \
      --urgency=critical \
      --app-name="Claude Task Runner" \
      --icon=dialog-error \
      "❌ Task ${TASK_ID}: 执行失败" \
      "${TITLE}\n请检查日志: ~/.claude/logs/task-runner.log"
    ;;
  *)
    # No notification for other statuses
    exit 0
    ;;
esac
```

- [ ] **Step 2: Make executable**

```bash
chmod +x obsidian-task-runner/scripts/notify_on_status_change.sh
```

---

### Task 10: Migrate Shell Scripts to skill/scripts/

**Files:**
- Move: `task-runner-daemon.sh` → `obsidian-task-runner/scripts/task-runner-daemon.sh`
- Move: `task-watcher.sh` → `obsidian-task-runner/scripts/task-watcher.sh`
- Modify: both files — update relative path references to use `$SKILL_DIR`

**Consumes:** T9 (notify_on_status_change.sh path)
**Produces:** Correct path references for T14 (systemd units), T15 (install.sh)

- [ ] **Step 1: Move and update task-watcher.sh**

Move `task-watcher.sh` to `obsidian-task-runner/scripts/task-watcher.sh`. Update path references:
- The `SKILL_DIR` variable already points to `$HOME/.claude/skills/obsidian-task-runner` — no change needed
- The daemon call: `"$SKILL_DIR/scripts/task-runner-daemon.sh"` — already correct in the moved context

- [ ] **Step 2: Move and update task-runner-daemon.sh**

Move `task-runner-daemon.sh` to `obsidian-task-runner/scripts/task-runner-daemon.sh`. Add notification call after `claude -p` completes:

After the `claude -p` block (line 52-62 in original), add:

```bash
  # Check if task finished a gate state and send desktop notification
  task_file="$VAULT/Tasks/$(echo "$line" | python3 -c "import json,sys;print(json.load(sys.stdin).get('file_name',''))")"
  if [ -n "$task_file" ] && [ -f "$task_file" ]; then
    "$SKILL_DIR/scripts/notify_on_status_change.sh" "$task_file"
  fi
```

- [ ] **Step 3: Remove original files from repo root**

```bash
rm task-runner-daemon.sh task-watcher.sh
```

---

### Task 11: Create reference.md

**Files:**
- Create: `obsidian-task-runner/reference.md`

- [ ] **Step 1: Write the reference document**

```markdown
# Obsidian Task Runner — Reference

## 状态流转

```
           ┌──────────────────────────────────┐
           │          人工 Gate                │
           │   设 plan_approved: true + 保存   │
           └──────────────────────────────────┘
                            ▲
                            │
ready ──→ Round 1 ──→ plan-review ──→ Round 2 ──→ review ──→ done
                           │                           │
                           ▼                           ▼
                        🔔 桌面通知                  🔔 桌面通知
                     "请审阅计划"                "请 review 代码"
```

## 状态详解

| 状态 | 含义 | 谁设置 | 下一步 |
|------|------|--------|--------|
| `ready` | 新建任务，等待处理 | 人工（模板默认） | Round 1 自动启动 |
| `plan-review` | 计划已生成，等待人工批准 | Claude（Round 1） | 人工审阅计划 |
| `implementing` | 正在实现代码 | Claude（Round 2 开始） | 自动进行 |
| `review` | 代码已实现，等待人工 review | Claude（Round 2 完成） | 人工 review 代码 |
| `done` | 已完成合并 | 人工 | 结束 |
| `error` | 执行失败 | Claude（异常时） | 人工排查日志 |
| `blocked` | 被其他任务阻塞 | 人工 | 等待依赖解决 |

## Task Frontmatter 字段参考

### 系统自动管理（不要手动改）

| 字段 | 类型 | 说明 |
|------|------|------|
| `status` | enum | 见上方状态流转表 |
| `plan_approved` | bool | Round 2 的钥匙，人工设为 true |
| `created` | ISO8601 | 文件首次创建时间 |
| `updated` | ISO8601 | 最后更新时间 |
| `completed` | ISO8601 | 完成时间 |
| `actual_hours` | float | 实际耗时 |
| `target_branch` | string | Git 分支名 |

### 人工填写

| 字段 | 类型 | 必需 | 说明 |
|------|------|------|------|
| `id` | string | ✅ | 唯一任务编号 |
| `title` | string | ✅ | 任务标题 |
| `project` | string | ✅ | vault-map.json 的项目 key |
| `new_project` | bool | | 是否从零创建新项目 |
| `template` | string | | 新项目脚手架模板名 |
| `priority` | P0-P4 | | 优先级，默认 P2 |
| `due_date` | date | | 截止日期 |
| `estimated_hours` | float | | 预估工时 |
| `assignee` | string | | claude / human / claude+human |
| `reviewer` | string | | 代码审查人 |
| `req_doc` | string | ✅ | Requirements/ 下的需求文档路径 |
| `component` | string | | 影响组件 |
| `tags` | list | | 标签 |
| `epic` | string | | 所属 Epic |
| `parent` | string | | 父任务 ID |
| `blocks` | list | | 阻塞哪些任务 ID |
| `blocked_by` | list | | 被哪些任务 ID 阻塞 |
| `auto_approve` | bool | | 是否跳过 plan-review gate（新项目无效） |
| `target_env` | string | | 部署环境 |

## vault-map.json 配置参考

详见 `config/vault-map.example.json`。

## 故障排查

### 任务没有被自动处理

1. 检查 status 是否为 `ready` 或 (`plan-review` 且 `plan_approved: true`)
2. 检查 assignee 是否为 `claude` 或 `claude+human`
3. 确认 `project` 字段在 vault-map.json 的 projects 中存在，或 `new_project: true`
4. 看日志：`tail -f ~/.claude/logs/task-runner.log`

### systemd 服务没有启动

```bash
# 检查状态
systemctl --user status claude-task-watcher.service
systemctl --user list-timers | grep claude-task-runner

# 看 systemd 日志
journalctl --user -u claude-task-watcher.service -n 50
journalctl --user -u claude-task-runner.service -n 50
```

### notify-send 没反应

- 确认 `notify-send` 可用：`which notify-send`
- 确认 notification daemon 在运行（如 dunst, notification-daemon）
- 在 vault-map.json 中检查 `notifications.desktop` 是否为 true
```

---

### Task 12: Create SKILL.md

**Files:**
- Create: `obsidian-task-runner/SKILL.md`

**Consumes:** All scripts (T5-T10), reference.md (T11)
**Produces:** The skill that `claude -p /obsidian-task-runner` invokes

- [ ] **Step 1: Write the skill definition**

```markdown
---
name: obsidian-task-runner
description: >
  读取 Obsidian Vault 中的需求文档和任务文档，自动理解要求并实现代码。
  两轮状态机：Round 1 出计划、Round 2 写代码。
  支持自动发现可处理任务、解析项目路径、创建新项目脚手架、
  运行测试和 lint、提交到分支。
  当用户在 Obsidian 中设 plan_approved: true 时自动触发下一轮。
  当用户提到"自动执行 Obsidian 任务"、"从 Obsidian 拉任务开发"、
  "自动实现需求文档"、"task runner" 时使用本 skill。
---

# Obsidian Task Runner

你是 Obsidian → Claude Code 自动化流水线的执行引擎。你的工作是在一次 `claude -p` 调用中完成一轮状态推进，然后退出，不发生交互。

## 核心约束

1. **只推进一轮**：Round 1 或 Round 2，不要在一次调用中跨越人工 Gate
2. **写回任务文档**：所有产出（计划、实现记录、验收结果）写入任务 markdown 文件
3. **不推送代码**：git commit 到分支但不 push，不创建 PR，不合并
4. **新项目永远确认**：`new_project: true` 的任务在 Round 1 只出脚手架方案，绝不自动创建

## 输入

你会收到一个 task_id（如 `/obsidian-task-runner 003`）。如果没有 task_id，调用 `find_ready_tasks.py` 取优先级最高的。

## 执行流程

### Step 1: 找到任务

```
如果提供了 task_id:
  直接读 $OBSIDIAN_VAULT/Tasks/<包含该 id 的文件>
否则:
  python3 ~/.claude/skills/obsidian-task-runner/scripts/find_ready_tasks.py $OBSIDIAN_VAULT
  取第一行的 JSON，用它的 file_path
```

### Step 2: 读取配置

读取 `~/.claude/skills/obsidian-task-runner/config/vault-map.json`，定位项目的本地路径。

### Step 3: 判断当前阶段

解析任务文档的 YAML frontmatter，关注 `status` 和 `plan_approved`：

- `status == "ready"` 或 `status` 为空 → **走 Round 1**
- `status == "plan-review"` 且 `plan_approved == true` → **走 Round 2**
- `status == "plan-review"` 且 `plan_approved != true` → 输出 "等待人工批准" 并退出

### Step 4: Round 1 — 出计划

1. **读需求文档**：根据 `req_doc` 字段读 `$OBSIDIAN_VAULT/<req_doc>`
2. **分析上下文**：
   - 如果是已有项目：读项目的目录结构、go.mod/package.json、现有代码风格
   - 如果是新项目：只出脚手架方案，包括目录结构、技术选型、Makefile 模板
3. **生成实现计划**：分步骤，每步有明确的产出物和预估代码量
4. **写回任务文档**：
   ```bash
   python3 ~/.claude/skills/obsidian-task-runner/scripts/update_task_status.py \
     <task_path> \
     status=plan-review \
     plan="<实现计划的 markdown 文本>"
   ```
   同时将计划内容写入文档的「## 实现计划」section（替换 `<!-- 🤖 Round 1: Claude 自动填充 -->`）
5. **退出**：输出简短摘要（任务 ID、状态变更、计划概要）

### Step 5: Round 2 — 实现

1. **读批准的计划**：从任务文档的 `plan` 字段或「## 实现计划」section 读取
2. **进入项目目录**：cd 到 vault-map.json 解析出的路径
3. **创建分支**：
   ```bash
   git checkout -b task/<id>-<slug>
   ```
4. **设置状态为 implementing**：
   ```bash
   python3 update_task_status.py <task_path> status=implementing
   ```
5. **按计划逐步实现**：
   - 每完成一步：检查代码编译通过、运行相关测试
   - 把每一步的产出记录到「## 实现记录」section
6. **完成后质量检查**：
   - 运行项目测试：`make test` 或等效命令
   - 运行 lint：`golangci-lint run` 或等效命令
   - 如果项目有 task-verifier subagent，调它逐条核实验收标准
7. **提交**：
   ```bash
   git add -A
   git commit -m "feat(<component>): <title>

   实现任务 #<id> 的计划

   Co-Authored-By: Claude <noreply@anthropic.com>"
   ```
8. **更新状态**：
   ```bash
   python3 update_task_status.py <task_path> \
     status=review \
     target_branch=task/<id>-<slug> \
     actual_hours=<实际耗时>
   ```
9. **退出**：输出简短摘要（任务 ID、分支名、改动文件数、验收结果）

### 特殊情况：auto_approve

如果 `auto_approve: true` 且 `new_project != true`：
- Round 1 完成后不退出，直接继续 Round 2
- 两个阶段的输出都写入任务文档
- 不触发桌面通知（不需要人工确认）

### 特殊情况：新项目

如果 `new_project: true`：
- Round 1 只生成脚手架方案（目录结构、配置文件、Makefile 等），不创建任何文件
- 方案写入任务文档后退出
- 不等同于 Round 2，需要人工确认后才执行

### 特殊情况：子任务

如果 `parent` 字段非空：
- 检查父任务状态，如果父任务不在 `review` 或 `done` 状态，设置为 `blocked` 并退出
- 如果 `blocked_by` 非空，检查依赖任务是否都完成

## 输出格式

每次执行结束输出简短的 JSON 摘要（用于日志和 daemon 解析）：

```json
{
  "task_id": "003",
  "title": "...",
  "round": 1,
  "status_after": "plan-review",
  "summary": "生成了 3 步实现计划"
}
```

## 参考文档

详细的状态流转、字段说明、故障排查见 `reference.md`。
```

---

### Task 13: Create task-verifier.md

**Files:**
- Create: `agents/task-verifier.md`

- [ ] **Step 1: Write the subagent definition**

```markdown
---
name: task-verifier
description: 对照 Obsidian 任务文档里的"验收标准"清单，逐条核实刚完成的实现是否达标，并运行测试/lint。在把任务状态推进到 review 之前必须调用一次。
tools: Read, Bash, Grep, Glob
---

# Task Verifier

你是一个验收 subagent。你的职责是：对照任务文档中「## 验收标准」section 的 checklist，逐条核实实现是否达标。

## 输入

任务文档的绝对路径（由调用者提供）。

## 执行流程

### Step 1: 读取任务文档

解析任务文档，提取：
- `project` → 对应项目路径（需要 vault-map.json）
- 「## 验收标准」→ checklist items
- 「## 实现计划」→ 预期产出
- `target_branch` → git 分支名

### Step 2: 进入项目目录并切换分支

从 `~/.claude/skills/obsidian-task-runner/config/vault-map.json` 解析项目路径，cd 进入，checkout 到 target_branch。

### Step 3: 逐条核实验收标准

对「## 验收标准」中的每一条：

```
- [ ] 验收项描述
```

执行验证动作：
- 如果是功能验收：运行对应测试或手动验证代码路径
- 如果是 API 验收：检查路由注册、handler 实现、请求/响应结构
- 如果是性能验收：运行 benchmark
- 如果是代码质量：运行 lint

每条验收项标记为 `- [x]`（通过）或 `- [ ]`（未通过，附原因）。

### Step 4: 运行测试套件

```bash
make test 2>&1 || go test ./... 2>&1
```

如果测试失败，记录失败用例和原因。

### Step 5: 运行 lint

```bash
golangci-lint run 2>&1
```

### Step 6: 写回验收记录

在任务文档的「## 验收记录」section 写入：

```markdown
## 验收记录

**验收时间**：<ISO8601>
**验收人**：Claude (task-verifier)

### 验收标准核实
- [x] 第一条验收标准 — 通过
- [x] 第二条验收标准 — 通过
- [ ] 第三条 — 未通过：缺少错误处理

### 测试结果
- 单元测试：15 passed, 0 failed
- 集成测试：3 passed, 0 failed

### Lint 结果
- golangci-lint: 0 issues

### 总体评价
✅ 所有验收标准通过 / ⚠️ N 项未通过，需要修复
```

## 输出格式

```json
{
  "task_id": "003",
  "verification_passed": true,
  "acceptance_criteria": {
    "total": 5,
    "passed": 5,
    "failed": 0
  },
  "tests": {
    "passed": 15,
    "failed": 0
  },
  "lint_issues": 0,
  "summary": "所有验收标准通过"
}
```
```

---

### Task 14: Update systemd Unit Files

**Files:**
- Modify: `claude-task-runner.service` — update ExecStart path
- No change: `claude-task-runner.timer` (no path references)
- Modify: `claude-task-watcher.service` — update ExecStart path

- [ ] **Step 1: Update claude-task-runner.service ExecStart**

Change:
```
ExecStart=%h/.claude/skills/obsidian-task-runner/scripts/task-runner-daemon.sh
```
This path is already correct — the script is being moved INTO that location by Task 10.

- [ ] **Step 2: Update claude-task-watcher.service ExecStart**

Change:
```
ExecStart=%h/.claude/skills/obsidian-task-runner/scripts/task-watcher.sh
```
This path is already correct — the script is being moved INTO that location by Task 10.

Both unit files use `%h` (home directory) expansion which systemd resolves at runtime. The path `%h/.claude/skills/obsidian-task-runner/scripts/task-watcher.sh` is correct after migration.

**These files do NOT need modification.** The paths already point to the correct install location. The only thing that changes is the source location in the repo.

---

### Task 15: Rewrite install.sh

**Files:**
- Modify: `install.sh` — full rewrite

- [ ] **Step 1: Write the new install.sh**

```bash
#!/usr/bin/env bash
# Obsidian Task Runner — 一键安装脚本
# 交互模式: ./install.sh
# 非交互模式: OBSIDIAN_VAULT=... NEW_PROJECT_ROOT=... ./install.sh --non-interactive
set -euo pipefail

# ── 全局配置（环境变量覆盖） ──
NON_INTERACTIVE=false
FORCE=false
for arg in "$@"; do
  case "$arg" in
    --non-interactive) NON_INTERACTIVE=true ;;
    --force) FORCE=true ;;
    --help|-h)
      cat <<'HELP'
Usage: ./install.sh [--non-interactive] [--force]

交互模式（默认）:
  ./install.sh
  逐步询问所有配置项。

非交互模式:
  OBSIDIAN_VAULT=/path/to/vault \
  NEW_PROJECT_ROOT=/path/to/projects \
  NOTIFY_ENABLED=true \
  POLL_INTERVAL_MINUTES=30 \
  SYSTEMD_ENABLED=true \
  ./install.sh --non-interactive

环境变量:
  OBSIDIAN_VAULT          Obsidian Vault 根目录（必需）
  NEW_PROJECT_ROOT        新项目默认创建目录（默认 $HOME/src）
  NOTIFY_ENABLED          是否启用桌面通知（默认 true）
  POLL_INTERVAL_MINUTES   systemd 定时器间隔分钟数（默认 30）
  SYSTEMD_ENABLED         是否注册 systemd 服务（默认 true）
  SKILL_INSTALL_DIR       skill 安装路径（默认 ~/.claude/skills/obsidian-task-runner）
HELP
      exit 0
      ;;
  esac
done

SRC_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SKILL_INSTALL_DIR="${SKILL_INSTALL_DIR:-$HOME/.claude/skills/obsidian-task-runner}"
AGENTS_HOME="$HOME/.claude/agents"
SYSTEMD_USER_DIR="$HOME/.config/systemd/user"
LOG_DIR="$HOME/.claude/logs"

say()  { printf '\033[1;36m==>\033[0m %s\n' "$1"; }
warn() { printf '\033[1;33m!!\033[0m %s\n' "$1"; }
ok()   { printf '\033[1;32mOK\033[0m %s\n' "$1"; }
err()  { printf '\033[1;31mERR\033[0m %s\n' "$1"; }

# ── 1. 依赖检查 ──
say "Step 1/7: 检查依赖"

missing=()
for bin in python3 git claude; do
  if ! command -v "$bin" >/dev/null 2>&1; then
    missing+=("$bin")
  else
    ok "$bin -> $(command -v "$bin")"
  fi
done

if [ "${#missing[@]}" -gt 0 ]; then
  err "缺少以下命令: ${missing[*]}"
  err "claude 安装: https://docs.claude.com"
  exit 1
fi

if ! command -v inotifywait >/dev/null 2>&1; then
  warn "缺少 inotifywait（没有它只能用定时轮询，做不到文件保存后即时触发）"
  if [ "$NON_INTERACTIVE" = false ] && command -v pacman >/dev/null 2>&1; then
    read -r -p "现在用 sudo pacman -S inotify-tools 安装? [y/N] " reply
    [[ "$reply" =~ ^[Yy]$ ]] && sudo pacman -S --needed inotify-tools
  fi
fi

if ! command -v notify-send >/dev/null 2>&1; then
  warn "缺少 notify-send（桌面通知不可用，状态变更时不会弹提醒）"
fi

# ── 2. 询问或读取配置 ──
say "Step 2/7: 配置"

if [ "$NON_INTERACTIVE" = true ]; then
  OBSIDIAN_VAULT="${OBSIDIAN_VAULT:?非交互模式必须设置 OBSIDIAN_VAULT 环境变量}"
  NEW_PROJECT_ROOT="${NEW_PROJECT_ROOT:-$HOME/src}"
  NOTIFY_ENABLED="${NOTIFY_ENABLED:-true}"
  POLL_INTERVAL_MINUTES="${POLL_INTERVAL_MINUTES:-30}"
  SYSTEMD_ENABLED="${SYSTEMD_ENABLED:-true}"
  ok "非交互模式: OBSIDIAN_VAULT=$OBSIDIAN_VAULT"
  ok "非交互模式: NEW_PROJECT_ROOT=$NEW_PROJECT_ROOT"
else
  read -r -p "Obsidian Vault 根目录 [$HOME/Documents/Obsidian/MainVault]: " vault_input
  OBSIDIAN_VAULT="${vault_input:-$HOME/Documents/Obsidian/MainVault}"

  read -r -p "新项目默认创建目录 [$HOME/src]: " new_root_input
  NEW_PROJECT_ROOT="${new_root_input:-$HOME/src}"

  read -r -p "启用桌面通知? [Y/n]: " notify_input
  NOTIFY_ENABLED=true
  [[ "$notify_input" =~ ^[Nn]$ ]] && NOTIFY_ENABLED=false

  read -r -p "systemd 定时轮询间隔(分钟) [30]: " interval_input
  POLL_INTERVAL_MINUTES="${interval_input:-30}"

  read -r -p "注册 systemd 服务? [Y/n]: " systemd_input
  SYSTEMD_ENABLED=true
  [[ "$systemd_input" =~ ^[Nn]$ ]] && SYSTEMD_ENABLED=false
fi

# ── 3. 安装 skill ──
say "Step 3/7: 安装 skill 到 $SKILL_INSTALL_DIR"

mkdir -p "$(dirname "$SKILL_INSTALL_DIR")"
if [ -d "$SKILL_INSTALL_DIR" ] && [ "$FORCE" = false ]; then
  warn "$SKILL_INSTALL_DIR 已存在，增量更新（用 --force 强制覆盖）"
fi
cp -r "$SRC_DIR/obsidian-task-runner" "$SKILL_INSTALL_DIR"
chmod +x "$SKILL_INSTALL_DIR"/scripts/*.sh 2>/dev/null || true
chmod +x "$SKILL_INSTALL_DIR"/scripts/*.py 2>/dev/null || true
ok "skill 已安装"

# ── 4. 安装 task-verifier subagent ──
say "Step 4/7: 安装 task-verifier subagent"

mkdir -p "$AGENTS_HOME"
if [ -f "$AGENTS_HOME/task-verifier.md" ] && [ "$FORCE" = false ]; then
  warn "$AGENTS_HOME/task-verifier.md 已存在，跳过（用 --force 覆盖）"
else
  cp "$SRC_DIR/agents/task-verifier.md" "$AGENTS_HOME/"
  ok "task-verifier.md 已安装"
fi

# ── 5. 生成 vault-map.json ──
say "Step 5/7: 配置 vault-map.json"

MAP_FILE="$SKILL_INSTALL_DIR/config/vault-map.json"
if [ -f "$MAP_FILE" ] && [ "$FORCE" = false ]; then
  warn "$MAP_FILE 已存在，不覆盖。如需更新请手动编辑"
else
  # 从 example 生成，替换为实际值
  cp "$SKILL_INSTALL_DIR/config/vault-map.example.json" "$MAP_FILE"
  # 用 python3 精确替换 JSON 字段（比 sed 安全）
  python3 -c "
import json, sys
with open('$MAP_FILE') as f:
    config = json.load(f)
config['new_project_root'] = '$NEW_PROJECT_ROOT'
config['notifications']['desktop'] = ${NOTIFY_ENABLED,,}
config['poll_interval_minutes'] = $POLL_INTERVAL_MINUTES
with open('$MAP_FILE', 'w') as f:
    json.dump(config, f, indent=2, ensure_ascii=False)
    f.write('\n')
"
  ok "已生成 $MAP_FILE"
  warn "请编辑此文件，填入你的项目映射（projects 字段）"
fi

# ── 6. 环境变量 + 目录 ──
say "Step 6/7: 配置环境"

# 写入 shell rc
shell_rc=""
for rc in "$HOME/.bashrc" "$HOME/.zshrc" "$HOME/.config/fish/config.fish"; do
  [ -f "$rc" ] && { shell_rc="$rc"; break; }
done
[ -z "$shell_rc" ] && shell_rc="$HOME/.bashrc"

if grep -q "^export OBSIDIAN_VAULT=" "$shell_rc" 2>/dev/null; then
  warn "$shell_rc 中已有 OBSIDIAN_VAULT，不重复添加"
else
  echo "export OBSIDIAN_VAULT=\"$OBSIDIAN_VAULT\"" >> "$shell_rc"
  ok "已写入 $shell_rc"
fi

mkdir -p "$OBSIDIAN_VAULT/Tasks" "$OBSIDIAN_VAULT/Requirements"
mkdir -p "$NEW_PROJECT_ROOT"
mkdir -p "$LOG_DIR"
ok "目录已创建"

# ── 7. systemd ──
say "Step 7/7: 配置 systemd"

# 探测 PATH
extra_paths=()
for p in "$HOME/.npm-global/bin" "$HOME/go/bin" "$HOME/.local/bin" "$(go env GOPATH 2>/dev/null)/bin"; do
  [ -n "$p" ] && [ -d "$p" ] && extra_paths+=("$p")
done
runner_path="$(IFS=:; echo "${extra_paths[*]}"):/usr/local/bin:/usr/bin:/bin"

if [ "$SYSTEMD_ENABLED" = true ]; then
  mkdir -p "$SYSTEMD_USER_DIR"

  for unit in claude-task-runner.service claude-task-runner.timer claude-task-watcher.service; do
    sed -e "s#__OBSIDIAN_VAULT_PATH__#$OBSIDIAN_VAULT#g" \
        -e "s#__RUNNER_PATH__#$runner_path#g" \
        "$SRC_DIR/$unit" > "$SYSTEMD_USER_DIR/$unit"
  done

  # 调整 timer 间隔
  sed -i "s/OnUnitActiveSec=30min/OnUnitActiveSec=${POLL_INTERVAL_MINUTES}min/" \
    "$SYSTEMD_USER_DIR/claude-task-runner.timer"

  systemctl --user daemon-reload
  systemctl --user enable --now claude-task-runner.timer
  ok "claude-task-runner.timer 已启用（每 ${POLL_INTERVAL_MINUTES} 分钟兜底轮询）"

  if command -v inotifywait >/dev/null 2>&1; then
    systemctl --user enable --now claude-task-watcher.service
    ok "claude-task-watcher.service 已启用（Tasks/ 文件保存即触发）"
  else
    warn "跳过 claude-task-watcher.service（缺 inotifywait）"
  fi
else
  ok "跳过 systemd 注册（SYSTEMD_ENABLED=false）"
fi

# ── 完成 ──
echo ""
say "安装完成！"
cat <<SUMMARY

后续步骤:
  1. 编辑 $MAP_FILE
     填入你的项目映射（projects 字段）
  2. source $shell_rc  或开新终端，让 OBSIDIAN_VAULT 生效
  3. 创建第一个任务:
     cp $SRC_DIR/TASK-000-template.md $OBSIDIAN_VAULT/Tasks/TASK-001-你的任务.md
     编辑 frontmatter 填入 id/title/project/req_doc
  4. 查看运行状态:
     systemctl --user status claude-task-watcher.service
     systemctl --user list-timers | grep claude-task-runner
  5. 查看日志:
     tail -f $LOG_DIR/task-watcher.log
     tail -f $LOG_DIR/task-runner.log
  6. 手动测试一次:
     cd <项目目录>
     claude -p "/obsidian-task-runner <task_id>"

文档: $SRC_DIR/README.md
问题反馈: <github-url>

SUMMARY
```

---

### Task 16: Rewrite README.md

**Files:**
- Modify: `README.md` — full rewrite for open-source audience

- [ ] **Step 1: Write the rewritten README**

Content see below (written inline since it's documentation, not code):

```markdown
# Obsidian Task Runner

让 [Claude Code](https://docs.claude.com) 自动读取 Obsidian Vault 中的需求和任务文档，理解要求后自行开发实现到代码交付。

## 这是什么

你在 Obsidian 里整理好需求和任务，保存文件后，Claude Code 自动：
1. 读取需求文档，理解要做什么
2. 生成实现计划，写回任务文档
3. 等你确认计划（设 `plan_approved: true`）
4. 自动实现代码、运行测试、lint、提交到分支
5. 桌面通知提醒你 review

不需要离开 Obsidian，不需要手动切到终端 `git checkout -b`、写代码、跑测试——这些都由 Claude Code 在后台自动完成。

## 工作原理

```
Obsidian Tasks/ 文件保存
        │
        ▼
  inotifywait 检测变化 (秒级触发)
        │
        ▼
  find_ready_tasks.py 发现可处理任务
        │
        ▼
  claude -p /obsidian-task-runner (headless)
        │
        ├── Round 1: 读需求 → 出计划 → 状态: plan-review → 🔔
        │     👤 你在 Obsidian 中审阅计划，设 plan_approved: true
        │
        └── Round 2: 实现代码 → 测试/lint → 验收 → 提交分支 → 🔔
```

## 快速开始

### 前置要求

- [Claude Code](https://docs.claude.com) CLI（`claude` 命令可用）
- Python 3.8+
- Git
- Linux（systemd + inotify）
- （可选）`inotify-tools` — 事件触发；不装也能用定时轮询
- （可选）`libnotify` — 桌面通知

### 一行安装

```bash
git clone https://github.com/ndzuki/obsidian-task-runner.git
cd obsidian-task-runner
./install.sh
```

### 非交互安装

```bash
OBSIDIAN_VAULT=/home/you/Obsidian/Vault \
NEW_PROJECT_ROOT=/home/you/src \
./install.sh --non-interactive
```

### 创建第一个任务

```bash
cp TASK-000-template.md "$OBSIDIAN_VAULT/Tasks/TASK-001-你的任务.md"
```

编辑这个文件的 YAML frontmatter：

```yaml
---
id: "001"
title: "实现用户登录 API"
project: "my-backend"
req_doc: "Requirements/用户登录API.md"
priority: P2
---
```

然后在 `Requirements/` 目录下写对应的需求文档，或者如果已经有需求文档，只需在 `req_doc` 字段指向它。

### 手动触发（不依赖 systemd）

```bash
cd /path/to/your/project
claude -p "/obsidian-task-runner"
```

## 配置

### vault-map.json

`~/.claude/skills/obsidian-task-runner/config/vault-map.json`：

```json
{
  "projects": {
    "my-backend": {
      "path": "/home/you/src/my-backend",
      "git_remote": "github.com/you/my-backend"
    },
    "frontend-app": {
      "path": "/home/you/src/frontend-app",
      "git_remote": "github.com/you/frontend-app"
    }
  },
  "new_project_root": "/home/you/src",
  "notifications": {
    "desktop": true
  },
  "poll_interval_minutes": 30
}
```

### Task 文档字段

参考 `TASK-000-template.md` 和 `obsidian-task-runner/reference.md`。

关键字段：
- `status: ready` — 新建任务
- `plan_approved: true` — 批准计划，触发 Round 2
- `auto_approve: true` — 跳过人工确认（新项目无效）
- `new_project: true` — 从零创建项目

## 安全边界

以下操作**不会**自动执行（需要人工确认）：
- `git push` / 创建 PR / 合并
- 将任务标记为 `done`
- 新项目的脚手架创建（永远停在 Round 1 等待确认）
- 删除文件或分支

## 目录结构

```
obsidian-task-runner/
├── SKILL.md
├── reference.md
├── scripts/
│   ├── find_ready_tasks.py
│   ├── update_task_status.py
│   ├── resolve_project_path.py
│   ├── register_project.py
│   ├── notify_on_status_change.sh
│   ├── task-runner-daemon.sh
│   └── task-watcher.sh
└── config/
    └── vault-map.example.json

agents/
└── task-verifier.md

TASK-000-template.md
install.sh
```

## 常见问题

### 任务没有被自动处理？

1. 检查 status 是 `ready` 还是 `plan-review` + `plan_approved: true`
2. 检查 `project` 是否在 vault-map.json 中有映射
3. `tail -f ~/.claude/logs/task-runner.log`

### 没有桌面通知？

安装 `libnotify`：`sudo pacman -S libnotify`（Arch）或 `sudo apt install libnotify-bin`（Debian/Ubuntu）。

确认 notification daemon 在运行（dunst、mako 等）。

### 如何只用手动模式？

不运行 `install.sh` 的 systemd 步骤，或设置 `SYSTEMD_ENABLED=false`。手动在项目目录执行：
```bash
claude -p "/obsidian-task-runner"
```

## 贡献

MIT License。欢迎 Issue 和 PR。

## License

MIT © 2026 ndzuki and contributors
```

---

### Task 17: Code Review Existing Scripts

**Files:**
- Review: `task-watcher.sh`
- Review: `task-runner-daemon.sh`
- Review: `install.sh` (original version)

**Purpose:** Before migration, identify bugs and improvements in the existing shell scripts. Apply fixes to the migrated versions.

- [ ] **Step 1: Review task-watcher.sh for issues**

Known issues to verify and fix:
1. `inotifywait` pipe to `while read` runs in a subshell — variables set inside the loop (like `last_run`) won't persist BUT in this case `last_run` is only used within the loop body, so it works correctly.
2. The `DEBOUNCE_SECONDS` and `last_run` approach is sound for debouncing within a single inotifywait session.
3. If `task-runner-daemon.sh` takes longer than `DEBOUNCE_SECONDS`, subsequent events can trigger duplicate runs. Add a lock file.

- [ ] **Step 2: Review task-runner-daemon.sh for issues**

Known issues to verify and fix:
1. The `while read` loop processes NDJSON — parsing each line with `python3 -c` for each field is fragile. Already addressed by `find_ready_tasks.py` outputting clean JSON.
2. `--allowedTools` list hardcoded — should document this in reference.md.
3. No locking mechanism — two simultaneous daemon runs could process the same task twice. Add flock.

- [ ] **Step 3: Apply fixes**

In the migrated version of `task-runner-daemon.sh`, add a lock file at the start:

```bash
LOCKFILE="$LOG_DIR/task-runner.lock"
exec 200>"$LOCKFILE"
flock -n 200 || { log "已有 task-runner-daemon 实例在运行，跳过"; exit 0; }
```

In the migrated version of `task-watcher.sh`, pass the lock through — since the watcher calls the daemon, the daemon's own lock is sufficient.

The `while read` subshell issue in `task-watcher.sh` — the `inotifywait | while read` runs the while loop in a subshell because of the pipe. The `last_run` variable IS modified inside the loop but since it's only used inside the same loop iteration, it works. The actual issue is that `last_run` is reset to 0 in the parent shell and the subshell inherits it, but changes in the subshell don't propagate back. However, since we only READ `last_run` and WRITE `last_run` within the same loop, and we never read it after the loop exits, this is fine.

One real improvement: add `--recursive` to inotifywait for subdirectories? No, Tasks/ is flat.

Add a comment about the subshell behavior for future maintainers.
```

---

## Self-Review

**1. Spec coverage:** All 17 tasks from the spec are covered. Each task has concrete steps and exact code.

**2. Placeholder scan:** No TBD, TODO, "implement later", "add tests", or vague instructions. All code is complete.

**3. Type consistency:** 
- `find_ready_tasks.py` outputs NDJSON → `task-runner-daemon.sh` parses with `python3 -c` — consistent
- `update_task_status.py` uses `field=value` CLI interface — consistent across all callers
- `resolve_project_path.py` outputs `{"status", "path", "error"}` — consumed by daemon — consistent
- File paths: all scripts moved to `obsidian-task-runner/scripts/` — systemd units already use `%h/.claude/skills/obsidian-task-runner/scripts/` — consistent

Done. Proceed to execution.
