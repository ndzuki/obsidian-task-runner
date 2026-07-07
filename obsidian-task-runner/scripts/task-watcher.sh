#!/usr/bin/env bash
# 监听 Obsidian Vault 的 Tasks/ 和 Requirements/ 目录:
# 文件一旦被保存就立刻跑一次 task-runner-daemon.sh。
# systemd timer 保留作为兜底——防止 inotify 事件丢失或同步延迟。
set -euo pipefail

SKILL_DIR="${SKILL_INSTALL_DIR:-$HOME/.claude/skills/obsidian-task-runner}"

# OBSIDIAN_VAULT 优先从环境变量取；如果没设（比如手动跑），从 vault-map.json 读
if [ -z "${OBSIDIAN_VAULT:-}" ]; then
  _map="$SKILL_DIR/config/vault-map.json"
  if [ -f "$_map" ]; then
    OBSIDIAN_VAULT="$(python3 -c "import json,sys;print(json.load(open('$_map')).get('obsidian_vault',''))" 2>/dev/null)"
  fi
  [ -z "$OBSIDIAN_VAULT" ] && { echo "请设置 OBSIDIAN_VAULT 环境变量或在 vault-map.json 中配置 obsidian_vault" >&2; exit 1; }
fi
VAULT="$OBSIDIAN_VAULT"
TASKS_DIR="$VAULT/Tasks"
REQS_DIR="$VAULT/Requirements"
LOG_DIR="$HOME/.claude/logs"
LOG_FILE="$LOG_DIR/task-watcher.log"
mkdir -p "$LOG_DIR"

# ── 日志轮转：超过 10MB 时归档，保留最近 5 个 ──
rotate_log() {
  local log="$1" max_size=$((10 * 1024 * 1024))  # 10MB
  if [ -f "$log" ] && [ "$(stat -c%s "$log" 2>/dev/null || echo 0)" -gt "$max_size" ]; then
    for i in 4 3 2 1; do
      [ -f "${log}.$i" ] && mv "${log}.$i" "${log}.$((i+1))"
    done
    mv "$log" "${log}.1"
    touch "$log"
  fi
}
rotate_log "$LOG_FILE"

log() { echo "[$(date -Iseconds)] $*" >>"$LOG_FILE"; }

command -v inotifywait >/dev/null 2>&1 || {
  log "缺少 inotifywait,请先安装 inotify-tools(Arch: sudo pacman -S inotify-tools)"
  exit 1
}

WATCH_DIRS=()
[ -d "$TASKS_DIR" ] && WATCH_DIRS+=("$TASKS_DIR")
[ -d "$REQS_DIR" ] && WATCH_DIRS+=("$REQS_DIR")

if [ "${#WATCH_DIRS[@]}" -eq 0 ]; then
  log "没有可监听的目录: $TASKS_DIR 和 $REQS_DIR 都不存在"
  exit 1
fi

log "开始监听 ${WATCH_DIRS[*]}"

DEBOUNCE_SECONDS=5
last_run=0

# 同时监听 Tasks/ 和 Requirements/ 的 close_write + moved_to 事件
# --format 输出完整路径，方便区分是哪个目录
inotifywait -m -q -e close_write -e moved_to --format '%w%f' "${WATCH_DIRS[@]}" | while read -r changed_file; do
  now=$(date +%s)
  if (( now - last_run < DEBOUNCE_SECONDS )); then
    continue
  fi
  last_run=$now

  # 判断变更来源
  case "$changed_file" in
    "$TASKS_DIR"/*)
      log "检测到 Tasks 变化: $(basename "$changed_file"),触发扫描"
      ;;
    "$REQS_DIR"/*)
      log "检测到 Requirements 变化: $(basename "$changed_file")"
      # 需求文档更新 → 找到关联的任务，重置为 ready → 触发扫描
      python3 "$SKILL_DIR/scripts/on_req_changed.py" "$VAULT" "$changed_file" >>"$LOG_FILE" 2>&1 || true
      ;;
    *)
      log "检测到变化: $changed_file,触发扫描"
      ;;
  esac

  if ! "$SKILL_DIR/scripts/task-runner-daemon.sh"; then
    log "本轮扫描出错,详情见 task-runner.log"
  fi
done
