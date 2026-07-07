#!/usr/bin/env bash
# 监听 Obsidian Vault 的 Tasks/ 目录:文件一旦被保存(close_write,覆盖 Obsidian
# 保存和常见的编辑器原子写入)就立刻跑一次 task-runner-daemon.sh,而不用等
# systemd timer 的下一个触发点。timer 仍然保留作为兜底——防止 inotify 事件丢失、
# 这台机器上 Claude Code 进程重启期间错过事件、或 Syncthing 同步延迟到达等情况。
set -euo pipefail

# OBSIDIAN_VAULT 优先从环境变量取；如果没设（比如手动跑），从 vault-map.json 读
if [ -z "${OBSIDIAN_VAULT:-}" ]; then
  _map="$HOME/.claude/skills/obsidian-task-runner/config/vault-map.json"
  if [ -f "$_map" ]; then
    OBSIDIAN_VAULT="$(python3 -c "import json,sys;print(json.load(open('$_map')).get('obsidian_vault',''))" 2>/dev/null)"
  fi
  [ -z "$OBSIDIAN_VAULT" ] && { echo "请设置 OBSIDIAN_VAULT 环境变量或在 vault-map.json 中配置 obsidian_vault" >&2; exit 1; }
fi
VAULT="$OBSIDIAN_VAULT"
TASKS_DIR="$VAULT/Tasks"
SKILL_DIR="$HOME/.claude/skills/obsidian-task-runner"
LOG_DIR="$HOME/.claude/logs"
mkdir -p "$LOG_DIR"

log() { echo "[$(date -Iseconds)] $*" >>"$LOG_DIR/task-watcher.log"; }

command -v inotifywait >/dev/null 2>&1 || {
  log "缺少 inotifywait,请先安装 inotify-tools(Arch: sudo pacman -S inotify-tools)"
  exit 1
}

if [ ! -d "$TASKS_DIR" ]; then
  log "目录不存在: $TASKS_DIR"
  exit 1
fi

log "开始监听 $TASKS_DIR"

DEBOUNCE_SECONDS=5
last_run=0

# 注意: inotifywait | while read 创建了一个子 shell 管道。
# last_run 变量在子 shell 内的修改不会传回父 shell,
# 但因为 last_run 只在 while 循环体内读写,不需要在循环结束后使用,所以这里没问题。
# 如果以后要在这里加循环后的逻辑并引用 last_run,需要改用 process substitution 或其他方式。
#
# close_write:文件保存完成(内容修改,比如 Obsidian 改了 status/plan_approved)
# moved_to:一些编辑器用"写临时文件再 rename"的方式保存,表现为 moved_to
inotifywait -m -q -e close_write -e moved_to --format '%f' "$TASKS_DIR" | while read -r changed_file; do
  now=$(date +%s)
  if (( now - last_run < DEBOUNCE_SECONDS )); then
    continue
  fi
  last_run=$now
  log "检测到 $changed_file 变化,触发一次扫描"
  if ! "$SKILL_DIR/scripts/task-runner-daemon.sh"; then
    log "本轮扫描出错,详情见 task-runner.log"
  fi
done
