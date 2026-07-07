#!/usr/bin/env bash
# Send desktop notification based on task status after Claude Code finishes.
# Called by task-runner-daemon.sh after claude -p returns.
#
# Usage: notify_on_status_change.sh <task_file_path> [previous_status]
set -euo pipefail

TASK_FILE="${1:?Usage: notify_on_status_change.sh <task_file_path>}"
# Note: second positional param reserved for future status-change-aware notifications

command -v notify-send >/dev/null 2>&1 || exit 0  # silent skip if no notify-send

# Extract frontmatter fields with a simple awk — no Python dep for notifications
extract_field() {
  local field="$1"
  awk -v key="$field" '
    /^---$/ { in_fm = !in_fm; next }
    in_fm && $0 ~ "^"key":" {
      sub("^"key":[[:space:]]*", "")
      gsub(/"/, "")
      print
      exit
    }
  ' "$TASK_FILE"
}

STATUS=$(extract_field "status")
TASK_ID=$(extract_field "id")
TITLE=$(extract_field "title")
REVIEWER=$(extract_field "reviewer")

# Only notify on gate states
case "$STATUS" in
  plan-review)
    notify-send \
      --urgency=normal \
      --app-name="Claude Task Runner" \
      --icon=dialog-information \
      "📋 Task ${TASK_ID}: 计划已生成" \
      "${TITLE}\n请审阅计划，确认后设 plan_approved: true 并保存"
    ;;
  review)
    notify-send \
      --urgency=normal \
      --app-name="Claude Task Runner" \
      --icon=emblem-default \
      "✅ Task ${TASK_ID}: 代码已实现" \
      "${TITLE}\n请 ${REVIEWER:-你} review 代码，确认无误后合并"
    ;;
  implementing)
    notify-send \
      --urgency=normal \
      --app-name="Claude Task Runner" \
      --icon=emblem-system \
      "⏳ Task ${TASK_ID}: 仍在执行中" \
      "${TITLE}\n任务未正常结束（可能进程中断），请检查日志"
    ;;
  error|failed)
    notify-send \
      --urgency=critical \
      --app-name="Claude Task Runner" \
      --icon=dialog-error \
      "❌ Task ${TASK_ID}: 执行失败" \
      "${TITLE}\n请检查日志: ~/.claude/logs/task-runner.log"
    ;;
  *)
    # No notification for other statuses
    exit 0
    ;;
esac
