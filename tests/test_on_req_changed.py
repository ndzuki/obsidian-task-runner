#!/usr/bin/env python3
"""Tests for auto-create TASK from new REQ documents."""

import importlib.util
import os
import sys
import tempfile
import unittest
from pathlib import Path

SCRIPTS = Path(__file__).resolve().parents[1] / "obsidian-task-runner" / "scripts"
spec = importlib.util.spec_from_file_location(
    "on_req_changed", SCRIPTS / "on_req_changed.py"
)
on_req_changed = importlib.util.module_from_spec(spec)
spec.loader.exec_module(on_req_changed)


class AutoCreateTaskTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.vault = Path(self.tmp.name)
        self.tasks_dir = self.vault / "Tasks"
        self.reqs_dir = self.vault / "Requirements"
        self.tasks_dir.mkdir()
        self.reqs_dir.mkdir()

    def tearDown(self):
        self.tmp.cleanup()

    def _write_req(self, name, content):
        p = self.reqs_dir / name
        p.write_text(content)
        return p

    def _read_task(self, name):
        p = self.tasks_dir / name
        return p.read_text() if p.exists() else None

    # ── filename mapping ──

    def test_parse_req_filename_standard(self):
        r = on_req_changed.parse_req_filename("Requirements/REQ-042-demo.md")
        self.assertEqual(r, ("042", "demo"))

    def test_parse_req_filename_no_match(self):
        self.assertIsNone(on_req_changed.parse_req_filename("Requirements/random.md"))
        self.assertIsNone(on_req_changed.parse_req_filename("REQ-no-slug.md"))

    def test_task_filename_for_req(self):
        f = on_req_changed.task_filename_for_req("Requirements/REQ-007-foo.md")
        self.assertEqual(f, "TASK-007-foo.md")

    # ── auto-create with project → status ready ──

    def test_create_task_from_new_req_with_project(self):
        self._write_req(
            "REQ-042-demo.md",
            '---\nid: "042"\ntitle: Test\nproject: my-project\npriority: P0\n---\n'
            "# Test\n\n## 要做什么\n\nBuild it.\n\n## 完成标准\n\n- [ ] AC-1: works\n",
        )
        result = on_req_changed.on_req_changed(
            str(self.vault), "Requirements/REQ-042-demo.md"
        )
        self.assertEqual(len(result), 1)
        self.assertEqual(result[0]["action"], "create_task")

        task = self._read_task("TASK-042-demo.md")
        self.assertIsNotNone(task)
        self.assertIn('id: "042"', task)
        self.assertIn('project: "my-project"', task)
        self.assertIn('priority: P0', task)
        self.assertIn('status: "ready"', task)
        self.assertIn('plan_approved: false', task)
        self.assertIn('merge_approved: false', task)
        self.assertIn('assignee: ""', task)   # user must fill
        self.assertIn("Build it.", task)
        self.assertIn("AC-1: works", task)

    # ── no project → status blocked ──

    def test_create_task_from_new_req_without_project(self):
        self._write_req(
            "REQ-043-noproj.md",
            '---\nid: "043"\ntitle: NoProj\n---\n'
            "# NoProj\n\n## 要做什么\n\nSomething.\n",
        )
        result = on_req_changed.on_req_changed(
            str(self.vault), "Requirements/REQ-043-noproj.md"
        )
        self.assertEqual(len(result), 1)
        self.assertEqual(result[0]["action"], "create_task")

        task = self._read_task("TASK-043-noproj.md")
        self.assertIsNotNone(task)
        self.assertIn('status: "blocked"', task)

    # ── idempotent: existing task not duplicated ──

    def test_existing_task_not_duplicated(self):
        self._write_req(
            "REQ-044-dup.md",
            '---\nid: "044"\ntitle: Dup\nproject: p\n---\n# Title\n## 要做什么\n\nX.\n',
        )
        self.tasks_dir.joinpath("TASK-044-dup.md").write_text(
            '---\nid: "044"\nproject: "p"\nstatus: "done"\n'
            'req_doc: Requirements/REQ-044-dup.md\n---\n# TASK-044\n'
        )
        result = on_req_changed.on_req_changed(
            str(self.vault), "Requirements/REQ-044-dup.md"
        )
        # done status → pending_req (auto re-plan) — correct, not duplicated
        self.assertEqual(result[0]["action"], "pending_req")
        self.assertEqual(
            len(list(self.tasks_dir.glob("TASK-044*"))), 1,
            "must not create duplicate file"
        )

    # ── non-REQ file pattern → no auto-create ──

    def test_non_req_filename_no_auto_create(self):
        self._write_req(
            "some-notes.md",
            "# notes\n\n## 要做什么\n\nStuff.\n",
        )
        result = on_req_changed.on_req_changed(
            str(self.vault), "Requirements/some-notes.md"
        )
        for a in result:
            self.assertNotEqual(a.get("action"), "create_task")


if __name__ == "__main__":
    unittest.main()
