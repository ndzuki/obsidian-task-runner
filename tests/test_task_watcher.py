#!/usr/bin/env python3
"""Regression tests for task-watcher.sh event handling."""

import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
WATCHER = ROOT / "obsidian-task-runner" / "scripts" / "task-watcher.sh"


class TaskWatcherEventHandlingTests(unittest.TestCase):
    def test_temporary_files_are_filtered_before_debounce_state_changes(self):
        """Temp-file events must not consume the debounce window for real files."""
        script = WATCHER.read_text()

        temp_filter_index = script.index('case "$(basename "$changed_file")" in')
        debounce_time_index = script.index("now=$(date +%s)")
        debounce_update_index = script.index("last_run=$now")

        self.assertLess(
            temp_filter_index,
            debounce_time_index,
            "temporary/hidden file filtering must happen before debounce checks",
        )
        self.assertLess(
            temp_filter_index,
            debounce_update_index,
            "temporary/hidden file events must not update last_run",
        )


if __name__ == "__main__":
    unittest.main()
