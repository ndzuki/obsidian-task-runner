#!/usr/bin/env python3
"""Tests for task discovery readiness rules."""

import importlib.util
import tempfile
import unittest
from pathlib import Path


SCRIPTS = Path(__file__).resolve().parents[1] / "obsidian-task-runner" / "scripts"
spec = importlib.util.spec_from_file_location(
    "find_ready_tasks", SCRIPTS / "find_ready_tasks.py"
)
find_ready_tasks = importlib.util.module_from_spec(spec)
spec.loader.exec_module(find_ready_tasks)


class BlockedTaskAutoReadyTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.vault = Path(self.tmp.name)
        self.tasks_dir = self.vault / "Tasks"
        self.tasks_dir.mkdir()

    def tearDown(self):
        self.tmp.cleanup()

    def _write_task(self, filename: str, frontmatter: str):
        self.tasks_dir.joinpath(filename).write_text(
            f"---\n{frontmatter.strip()}\n---\n# Task\n"
        )

    def test_blocked_task_with_project_and_valid_assignee_is_discovered(self):
        """A task blocked only for missing fields should run after fields are filled."""
        self._write_task(
            "TASK-004-chezmoi-auto-sync.md",
            """
id: "004"
title: "Chezmoi Auto Sync"
project: autoworkflow
status: blocked
assignee: codex
priority: P2
blocked_by: []
""",
        )

        tasks = find_ready_tasks.find_ready_tasks(str(self.vault))

        self.assertEqual(len(tasks), 1)
        self.assertEqual(tasks[0]["id"], "004")
        self.assertEqual(tasks[0]["status"], "blocked")
        self.assertEqual(tasks[0]["assignee"], "codex")

    def test_blocked_task_without_assignee_is_not_discovered(self):
        self._write_task(
            "TASK-004-chezmoi-auto-sync.md",
            """
id: "004"
title: "Chezmoi Auto Sync"
project: autoworkflow
status: blocked
assignee: ""
priority: P2
blocked_by: []
""",
        )

        tasks = find_ready_tasks.find_ready_tasks(str(self.vault))

        self.assertEqual(tasks, [])

    def test_blocked_task_with_dependency_is_not_auto_unblocked(self):
        self._write_task(
            "TASK-004-chezmoi-auto-sync.md",
            """
id: "004"
title: "Chezmoi Auto Sync"
project: autoworkflow
status: blocked
assignee: codex
priority: P2
blocked_by:
  - "001"
""",
        )

        tasks = find_ready_tasks.find_ready_tasks(str(self.vault))

        self.assertEqual(tasks, [])

    def test_blocked_task_with_unknown_assignee_is_not_auto_unblocked(self):
        self._write_task(
            "TASK-004-chezmoi-auto-sync.md",
            """
id: "004"
title: "Chezmoi Auto Sync"
project: autoworkflow
status: blocked
assignee: robot
priority: P2
blocked_by: []
""",
        )

        tasks = find_ready_tasks.find_ready_tasks(str(self.vault))

        self.assertEqual(tasks, [])


if __name__ == "__main__":
    unittest.main()
