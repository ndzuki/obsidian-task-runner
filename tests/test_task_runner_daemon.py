#!/usr/bin/env python3
"""Regression tests for task-runner-daemon.sh orchestration rules."""

import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
DAEMON = ROOT / "obsidian-task-runner" / "scripts" / "task-runner-daemon.sh"


class TaskRunnerDaemonTests(unittest.TestCase):
    def test_blocked_task_is_marked_ready_before_agent_runs(self):
        """Agents should see ready status, not blocked, after user fills fields."""
        script = DAEMON.read_text()

        blocked_check_index = script.index('[ "$task_status" = "blocked" ]')
        ready_update_index = script.index("status=ready")
        run_agent_index = script.index('if run_task_agent "$task_id"')

        self.assertLess(blocked_check_index, run_agent_index)
        self.assertLess(ready_update_index, run_agent_index)


if __name__ == "__main__":
    unittest.main()
