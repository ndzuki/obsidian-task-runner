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

    projects = config.setdefault("projects", [])

    # Check for existing entry by name
    for proj in projects:
        if proj.get("name") == project_name:
            existing_path = proj.get("path", "")
            if existing_path == project_path:
                print(f"Project '{project_name}' already registered with same path")
                return True
            print(
                f"Warning: Project '{project_name}' already exists with path "
                f"'{existing_path}'. Overwriting with '{project_path}'.",
                file=sys.stderr,
            )
            proj["path"] = project_path
            proj["git_remote"] = git_remote
            break
    else:
        # Not found — append new entry
        projects.append({
            "name": project_name,
            "path": project_path,
            "git_remote": git_remote,
        })

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
