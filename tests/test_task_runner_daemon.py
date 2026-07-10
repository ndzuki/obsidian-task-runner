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

    def test_omp_merge_phase_detection(self):
        """merge_approved=true triggers Merge Phase detection for review/conflict status."""
        script = DAEMON.read_text()

        # The daemon should detect merge-approved tasks
        self.assertIn('[ "$task_merge_approved" = "True" ]', script)
        self.assertIn('[ "$task_status" = "review" ]', script)
        self.assertIn('[ "$task_status" = "conflict" ]', script)
        # It should log the merge phase start
        self.assertIn("Merge Phase", script)
        self.assertIn('case "$task_assignee" in', script)
        self.assertIn('deepseek)', script)
        self.assertIn('gpt)', script)
        self.assertIn('model="$OMP_MODEL_DEEPSEEK"', script)
        self.assertIn('model="$OMP_MODEL_GPT"', script)
        # Headless approval modes
        self.assertIn("--auto-approve", script)
        self.assertIn("--approval-mode yolo", script)


if __name__ == "__main__":
    unittest.main()
