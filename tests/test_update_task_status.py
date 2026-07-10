#!/usr/bin/env python3
"""Tests for task frontmatter status updates."""

import importlib.util
import os
import re
import time
import unittest
from pathlib import Path


SCRIPTS = Path(__file__).resolve().parents[1] / "obsidian-task-runner" / "scripts"
spec = importlib.util.spec_from_file_location(
    "update_task_status", SCRIPTS / "update_task_status.py"
)
update_task_status = importlib.util.module_from_spec(spec)
spec.loader.exec_module(update_task_status)


class UpdateTaskStatusTimestampTests(unittest.TestCase):
    def test_now_iso_uses_local_timezone_offset(self):
        """Timestamps written to task files must use local time, not UTC."""
        old_tz = os.environ.get("TZ")
        os.environ["TZ"] = "Asia/Shanghai"
        time.tzset()
        try:
            timestamp = update_task_status.now_iso()
        finally:
            if old_tz is None:
                os.environ.pop("TZ", None)
            else:
                os.environ["TZ"] = old_tz
            time.tzset()

        self.assertRegex(timestamp, r"^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\+0800$")
        self.assertNotRegex(timestamp, r"^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}$")


if __name__ == "__main__":
    unittest.main()
