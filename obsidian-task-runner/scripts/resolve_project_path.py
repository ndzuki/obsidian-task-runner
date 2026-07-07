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

    # Check existing projects (list format: [{name, path, git_remote}, ...])
    projects = config.get("projects", [])
    for proj in projects:
        if proj.get("name") == project_name:
            proj_path = proj.get("path", "")
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
        print("Usage: resolve_project_path.py <map_file> <project_name> [--new-project]",
              file=sys.stderr)
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
