#!/usr/bin/env python3
"""Register a new project in vault-map.json for future task lookups.

After a new project is scaffolded and confirmed by the user, this script
adds its entry to vault-map.json so subsequent tasks for the same project
resolve as 'existing'.

Write strategy:
  - read config → modify in memory → write to .tmp → validate → rename
  - this prevents corruption if the write is interrupted mid-stream
  - comma/newline/indentation all handled by json.dump()
"""

import json
import os
import sys
import tempfile


def register_project(
    map_file: str,
    project_name: str,
    project_path: str,
    git_remote: str = "",
    dry_run: bool = False,
) -> bool:
    """Add a project entry to vault-map.json.

    Args:
        map_file: Path to vault-map.json
        project_name: The project key to add
        project_path: Absolute path to the project on disk
        git_remote: Optional git remote URL
        dry_run: If True, print what would be written but don't modify the file

    Returns:
        True on success, False on failure
    """
    if not os.path.isfile(map_file):
        print(f"Error: {map_file} not found", file=sys.stderr)
        return False

    # 1. Read & parse existing config (validate input JSON)
    try:
        with open(map_file, "r") as f:
            raw = f.read()
        config = json.loads(raw)
    except json.JSONDecodeError as e:
        print(f"Error: {map_file} is not valid JSON: {e}", file=sys.stderr)
        return False
    except OSError as e:
        print(f"Error reading {map_file}: {e}", file=sys.stderr)
        return False

    projects = config.setdefault("projects", [])

    # 2. Modify in memory — append or update
    updated = False
    for proj in projects:
        if proj.get("name") == project_name:
            existing_path = proj.get("path", "")
            if existing_path == project_path:
                print(f"Project '{project_name}' already registered with same path, no change needed")
                return True
            print(
                f"Updating project '{project_name}': '{existing_path}' -> '{project_path}'",
                file=sys.stderr,
            )
            proj["path"] = project_path
            proj["git_remote"] = git_remote
            updated = True
            break
    else:
        projects.append({
            "name": project_name,
            "path": project_path,
            "git_remote": git_remote,
        })
        updated = True

    if not updated:
        return True  # nothing changed

    # 3. Serialize to JSON with consistent formatting
    new_content = json.dumps(config, indent=2, ensure_ascii=False) + "\n"

    if dry_run:
        print(f"[DRY RUN] Would write to {map_file}:")
        print(new_content)
        return True

    # 4. Atomic write: temp file → validate → rename
    tmp_file = map_file + ".tmp"
    try:
        with open(tmp_file, "w") as f:
            f.write(new_content)
            f.flush()
            os.fsync(f.fileno())  # ensure data hits disk before rename

        # 5. Validate: re-read tmp file to verify it's valid JSON
        with open(tmp_file, "r") as f:
            parsed = json.load(f)
        # Sanity: check our project is actually in there
        found = any(p.get("name") == project_name for p in parsed.get("projects", []))
        if not found:
            print(f"Error: validation failed — project '{project_name}' not found after write", file=sys.stderr)
            os.unlink(tmp_file)
            return False

        # 6. Atomic rename (POSIX guarantees this is atomic)
        os.replace(tmp_file, map_file)
        print(f"Registered project '{project_name}' -> {project_path}")
        return True

    except (json.JSONDecodeError, OSError) as e:
        print(f"Error: write/validation failed: {e}", file=sys.stderr)
        # Clean up temp file on failure
        if os.path.exists(tmp_file):
            os.unlink(tmp_file)
        return False


def main():
    if len(sys.argv) < 4:
        print(
            "Usage: register_project.py <map_file> <project_name> <project_path> "
            "[--git-remote <url>] [--dry-run]",
            file=sys.stderr,
        )
        sys.exit(1)

    map_file = sys.argv[1]
    project_name = sys.argv[2]
    project_path = sys.argv[3]
    git_remote = ""
    dry_run = "--dry-run" in sys.argv

    for i, arg in enumerate(sys.argv):
        if arg == "--git-remote" and i + 1 < len(sys.argv):
            git_remote = sys.argv[i + 1]
            break

    success = register_project(map_file, project_name, project_path, git_remote, dry_run)
    sys.exit(0 if success else 1)


if __name__ == "__main__":
    main()
