#!/usr/bin/env bash
# Send desktop notification based on task status after OMP finishes.
# Called by task-runner-daemon.sh after OMP processing.
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
PR_URL=$(extract_field "pr_url")

# Only notify on gate states
case "$STATUS" in
  plan-review)
    notify-send \
      --urgency=normal \
      --app-name="OMP Task Runner" \
      --icon=dialog-information \
      "📋 Task ${TASK_ID}: 计划已生成" \
      "${TITLE}\n请审阅计划，确认后设 plan_approved: true 并保存"
    ;;
  review)
    notify-send \
      --urgency=normal \
      --app-name="OMP Task Runner" \
      --icon=emblem-default \
      "✅ Task ${TASK_ID}: 代码已实现" \
      "${TITLE}\n请 ${REVIEWER:-你} review 代码，确认无误后设 merge_approved: true 自动合并"
    # If a PR was created, also send PR notification
    if [ -n "${PR_URL:-}" ]; then
      notify-send \
        --urgency=normal \
        --app-name="OMP Task Runner" \
        --icon=emblem-shared \
        "📬 Task ${TASK_ID}: PR 已创建" \
        "${TITLE}\n${PR_URL}\n请 review PR，确认后设 merge_approved: true"
    fi
    ;;
  conflict)
    notify-send \
      --urgency=critical \
      --app-name="OMP Task Runner" \
      --icon=emblem-important \
      "⚠️ Task ${TASK_ID}: 合并冲突" \
      "${TITLE}\n自动合并失败，存在冲突文件，请手动解决后重新设置 merge_approved: true"
    ;;
  done)
    notify-send \
      --urgency=normal \
      --app-name="OMP Task Runner" \
      --icon=emblem-favorite \
      "🎉 Task ${TASK_ID}: 已完成" \
      "${TITLE}\n代码已合并并推送至远程仓库"
    ;;
  implementing)
    notify-send \
      --urgency=normal \
      --app-name="OMP Task Runner" \
      --icon=emblem-system \
      "⏳ Task ${TASK_ID}: 仍在执行中" \
      "${TITLE}\n任务未正常结束（可能进程中断），请检查日志"
    ;;
  error|failed)
    notify-send \
      --urgency=critical \
      --app-name="OMP Task Runner" \
      --icon=dialog-error \
      "❌ Task ${TASK_ID}: 执行失败" \
      "${TITLE}\n请检查日志: ~/.omp/logs/task-runner.log"
    ;;
  *)
    # No notification for other statuses
    exit 0
    ;;
esac
