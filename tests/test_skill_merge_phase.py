#!/usr/bin/env python3
"""Regression tests for Merge Phase instructions in SKILL.md."""

import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SKILL = ROOT / "obsidian-task-runner" / "SKILL.md"


class SkillMergePhaseTests(unittest.TestCase):
    def test_github_merge_path_restores_default_branch_after_merge(self):
        """gh pr merge must not leave the local repo on the feature branch."""
        skill = SKILL.read_text()

        merge_index = skill.index('gh pr merge "$TARGET_BRANCH" --merge --delete-branch')
        checkout_index = skill.index('git checkout "$DEFAULT_BRANCH"', merge_index)
        pull_index = skill.index('git pull --ff-only origin "$DEFAULT_BRANCH"', merge_index)
        cleanup_index = skill.index('git branch -d "$TARGET_BRANCH"', merge_index)

        self.assertLess(merge_index, checkout_index)
        self.assertLess(checkout_index, pull_index)
        self.assertLess(pull_index, cleanup_index)


if __name__ == "__main__":
    unittest.main()
