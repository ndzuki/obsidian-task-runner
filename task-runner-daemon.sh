#!/usr/bin/env bash
# 定时扫描 Obsidian Tasks/ 目录,对每个可处理任务(status:ready,或已获批准的 plan-review)
# 拉起一次 headless Claude Code 会话。由 systemd timer 周期性调用,也可以手动跑一次做验证。
set -euo pipefail

VAULT="${OBSIDIAN_VAULT:?请先设置环境变量 OBSIDIAN_VAULT,指向 Obsidian 仓库根目录}"
SKILL_DIR="$HOME/.claude/skills/obsidian-task-runner"
MAP_FILE="$SKILL_DIR/config/vault-map.json"
LOG_DIR="$HOME/.claude/logs"
mkdir -p "$LOG_DIR"

log() { echo "[$(date -Iseconds)] $*" >>"$LOG_DIR/task-runner.log"; }

if [ ! -f "$MAP_FILE" ]; then
  log "缺少项目映射文件: $MAP_FILE(参考 config/vault-map.example.json 创建)"
  exit 1
fi

python3 "$SKILL_DIR/scripts/find_ready_tasks.py" "$VAULT" | while IFS= read -r line; do
  task_id=$(echo "$line" | python3 -c "import json,sys;print(json.load(sys.stdin).get('id',''))")
  project=$(echo "$line" | python3 -c "import json,sys;print(json.load(sys.stdin).get('project',''))")
  new_project_flag=$(echo "$line" | python3 -c "import json,sys;print(json.load(sys.stdin).get('new_project',''))")

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
  else
    log "$task_id 处理失败,详情见上方日志"
  fi
done
