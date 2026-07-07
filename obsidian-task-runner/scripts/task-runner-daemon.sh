#!/usr/bin/env bash
# 定时扫描 Obsidian Tasks/ 目录,对每个可处理任务(status:ready,或已获批准的 plan-review)
# 拉起一次 headless Claude Code 会话。由 systemd timer 周期性调用,也可以手动跑一次做验证。
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
SKILL_DIR="$HOME/.claude/skills/obsidian-task-runner"
MAP_FILE="$SKILL_DIR/config/vault-map.json"
LOG_DIR="$HOME/.claude/logs"
LOG_FILE="$LOG_DIR/task-runner.log"
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

# 防止并发:同一时间只允许一个 daemon 实例运行
LOCKFILE="$LOG_DIR/task-runner.lock"
exec 200>"$LOCKFILE"
flock -n 200 || { echo "[$(date -Iseconds)] 已有 task-runner-daemon 实例在运行，跳过" >>"$LOG_FILE"; exit 0; }

log() { echo "[$(date -Iseconds)] $*" >>"$LOG_FILE"; }

if [ ! -f "$MAP_FILE" ]; then
  log "缺少项目映射文件: $MAP_FILE(参考 config/vault-map.example.json 创建)"
  exit 1
fi

python3 "$SKILL_DIR/scripts/find_ready_tasks.py" "$VAULT" | while IFS= read -r line; do
  task_id=$(echo "$line" | python3 -c "import json,sys;print(json.load(sys.stdin).get('id',''))")
  project=$(echo "$line" | python3 -c "import json,sys;print(json.load(sys.stdin).get('project',''))")
  new_project_flag=$(echo "$line" | python3 -c "import json,sys;print(json.load(sys.stdin).get('new_project',''))")
  task_file=$(echo "$line" | python3 -c "import json,sys;print(json.load(sys.stdin).get('file_name',''))")

  resolve_args=("$MAP_FILE" "$project")
  if [ "$new_project_flag" = "true" ]; then
    resolve_args+=(--new-project)
  fi

  resolved=$(python3 "$SKILL_DIR/scripts/resolve_project_path.py" "${resolve_args[@]}" 2>>"$LOG_DIR/task-runner.log") || {
    log "跳过 $task_id: 无法解析项目 '$project' 的路径,详情见上方日志"
    continue
  }
  repo_status=$(echo "$resolved" | python3 -c "import json,sys;print(json.load(sys.stdin).get('status',''))")
  repo_dir=$(echo "$resolved" | python3 -c "import json,sys;print(json.load(sys.stdin).get('path',''))")

  if [ "$repo_status" = "new" ]; then
    # 只创建空目录本身是安全、可幂等的操作;目录里放什么、要不要真的建这个项目,
    # 由 skill 在拿到人工确认后决定——这里绝不代替 Claude 做 git init 或写任何文件。
    mkdir -p "$repo_dir"
    log "$task_id: 项目 '$project' 是新项目,已创建空目录 $repo_dir,等待 skill 生成脚手架方案"
  elif [ "$repo_status" != "existing" ] || [ ! -d "$repo_dir" ]; then
    log "跳过 $task_id: 项目 '$project' 路径解析结果异常($resolved)"
    continue
  fi

  log "开始处理 $task_id (project=$project, repo=$repo_dir, stage=$repo_status)"

  # acceptEdits 只会自动放行文件写入和 mkdir/touch/mv/cp,
  # git/make/go/golangci-lint/goimports 仍需显式 allowedTools,否则 headless 运行到这些命令时会直接中止。
  if (
      cd "$repo_dir"
      claude -p "/obsidian-task-runner $task_id" \
        --permission-mode acceptEdits \
        --allowedTools "Bash(git *),Bash(make *),Bash(go *),Bash(golangci-lint *),Bash(goimports *),Bash(python3 *),Read,Edit,Grep,Glob" \
        --output-format json
    ) >>"$LOG_DIR/task-runner.log" 2>&1
  then
    log "$task_id 处理完成"

    # Round 1 或 Round 2 完成后检查任务状态，如果到了 gate 就发桌面通知
    if [ -n "$task_file" ] && [ -f "$VAULT/Tasks/$task_file" ]; then
      "$SKILL_DIR/scripts/notify_on_status_change.sh" "$VAULT/Tasks/$task_file" &
    fi
  else
    log "$task_id 处理失败,详情见上方日志"
  fi
done
