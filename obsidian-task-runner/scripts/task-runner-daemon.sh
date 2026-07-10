#!/usr/bin/env bash
# 定时扫描 Obsidian Tasks/ 目录,对每个可处理任务(status:ready,或已获批准的 plan-review)
# 拉起一次 OMP 会话。由 systemd timer 周期性调用,也可以手动跑一次做验证。
set -euo pipefail

SKILL_DIR="${SKILL_INSTALL_DIR:-$HOME/.omp/skills/obsidian-task-runner}"

# OBSIDIAN_VAULT 优先从环境变量取；如果没设（比如手动跑），从 vault-map.json 读
if [ -z "${OBSIDIAN_VAULT:-}" ]; then
  _map="$SKILL_DIR/config/vault-map.json"
  if [ -f "$_map" ]; then
    OBSIDIAN_VAULT="$(python3 -c "import json,sys;print(json.load(open('$_map')).get('obsidian_vault',''))" 2>/dev/null)"
  fi
  [ -z "$OBSIDIAN_VAULT" ] && { echo "请设置 OBSIDIAN_VAULT 环境变量或在 vault-map.json 中配置 obsidian_vault" >&2; exit 1; }
fi
VAULT="$OBSIDIAN_VAULT"
MAP_FILE="$SKILL_DIR/config/vault-map.json"
LOG_DIR="$HOME/.omp/logs"
LOG_FILE="$LOG_DIR/task-runner.log"
mkdir -p "$LOG_DIR"

# OMP 模型映射. 每个任务通过 frontmatter 的 `assignee` 字段决定使用的模型：
#   assignee: deepseek     → deepseek-v4-pro (deepseek/deepseek-v4-pro:xhigh)
#   assignee: gpt          → gpt-5.5 (gateway/gpt-5.5:xhigh)
#   未设置 / 无法识别时，回退到 TASK_RUNNER_AGENT 环境变量（默认 deepseek）。
#
# 轻量任务（状态更新、通知等）使用 deepseek-v4-flash：
TASK_RUNNER_AGENT="${TASK_RUNNER_AGENT:-deepseek}"
OMP_MODEL_DEEPSEEK="${OMP_MODEL_DEEPSEEK:-deepseek/deepseek-v4-pro:xhigh}"
OMP_MODEL_GPT="${OMP_MODEL_GPT:-gateway/gpt-5.5:xhigh}"
OMP_MODEL_FLASH="${OMP_MODEL_FLASH:-deepseek/deepseek-v4-flash}"
# OMP 命令路径
OMP_CMD="${OMP_CMD:-omp}"

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

make_omp_prompt() {
  local task_id="$1"
  local model="$2"
  cat <<EOF
你正在以 OMP 后台任务模式运行，目标是执行 Obsidian Task Runner skill：

  $SKILL_DIR/SKILL.md

请先完整读取该 SKILL.md，再严格按其中的 Obsidian Task Runner 流程执行：

  /obsidian-task-runner $task_id

执行要求：
- 只推进当前任务的一轮：Round 1、Round 2 或 Merge Phase，不要跨越人工 gate。
- 按任务文档 frontmatter 判断状态，并把计划、实现记录、验收记录、合并记录写回 Obsidian 任务 markdown。
- 使用本地系统时间；需要时间戳时执行 date 命令，不要使用 UTC 推断。
- 后台非交互执行，不要向用户提问；缺失信息按 skill 要求写入“需人工补充”。
- 遵守 skill 的核心约束：Round 1 / Round 2 不推送、不创建 PR、不合并；只有 Merge Phase 在 merge_approved=true 授权后执行 git push、gh pr create、merge 和分支清理。
- 结束时只输出简短 JSON 摘要，便于日志解析。
EOF
run_task_agent() {
  local task_id="$1"
  local repo_dir="$2"
  local task_assignee="$3"
  local task_status="$4"
  local task_merge_approved="$5"

  # 按任务 assignee 字段选择模型；未设置或无法识别时回退到 TASK_RUNNER_AGENT
  local model
  local model_name
  case "$task_assignee" in
    deepseek)
      model="$OMP_MODEL_DEEPSEEK"
      model_name="deepseek-v4-pro"
      ;;
    gpt)
      model="$OMP_MODEL_GPT"
      model_name="gpt-5.5"
      ;;
    *)
      model="$OMP_MODEL_FLASH"
      model_name="deepseek-v4-flash"
      ;;
  esac

  local approval_flag="--auto-approve"
  local approval_label="auto-approve"
  if [ "$task_merge_approved" = "True" ] \
    && { [ "$task_status" = "review" ] || [ "$task_status" = "conflict" ]; }; then
    approval_flag="--approval-mode yolo"
    approval_label="yolo（完整 git/gh/merge 权限）"
    log "$task_id: Merge Phase — 授权执行 git push/gh pr create/merge"
  fi

  log "$task_id: 使用 OMP headless 执行（assignee=$task_assignee, model=$model_name, approval=$approval_label）"

  # OMP 调用：cd 到项目目录，用对应模型 headless 执行 task
  (
    cd "$repo_dir"
    $OMP_CMD -m "$model" $approval_flag -p "/obsidian-task-runner $task_id"
  )
}

if [ ! -f "$MAP_FILE" ]; then
  log "缺少项目映射文件: $MAP_FILE(参考 config/vault-map.example.json 创建)"
  exit 1
fi

# 循环重扫:处理完当前批次后重新扫描,如果 on_req_changed 在 daemon 运行期间
# 重置了任务,下一轮扫描会拾起——避免用户更新需求文档后因 daemon 忙碌而被忽略。
# 最多重扫 3 轮,防止异常情况下无限循环。
scan_round=0
max_rounds=3

while [ $scan_round -lt $max_rounds ]; do
  scan_round=$((scan_round + 1))
  tasks_json=$(python3 "$SKILL_DIR/scripts/find_ready_tasks.py" "$VAULT" 2>>"$LOG_FILE")

  if [ -z "$tasks_json" ]; then
    log "第 $scan_round 轮扫描: 无待处理任务,退出"
    break
  fi

  task_count=$(echo "$tasks_json" | wc -l)
  log "第 $scan_round 轮扫描: 发现 $task_count 个待处理任务"

  echo "$tasks_json" | while IFS= read -r line; do
    [ -z "$line" ] && continue
  task_id=$(echo "$line" | python3 -c "import json,sys;print(json.load(sys.stdin).get('id',''))" 2>/dev/null || true)
  project=$(echo "$line" | python3 -c "import json,sys;print(json.load(sys.stdin).get('project',''))" 2>/dev/null || true)
  new_project_flag=$(echo "$line" | python3 -c "import json,sys;print(json.load(sys.stdin).get('new_project',''))" 2>/dev/null || true)
  task_file=$(echo "$line" | python3 -c "import json,sys;print(json.load(sys.stdin).get('file_name',''))" 2>/dev/null || true)

  resolve_args=("$MAP_FILE" "$project")
  # Python print(True) → "True" (capital T), not "true"
  if [ "$new_project_flag" = "True" ]; then
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
    # 由 skill 在拿到人工确认后决定——这里绝不代替 AI 做 git init 或写任何文件。
    mkdir -p "$repo_dir"
    log "$task_id: 项目 '$project' 是新项目,已创建空目录 $repo_dir,等待 skill 生成脚手架方案"
  elif [ "$repo_status" != "existing" ] || [ ! -d "$repo_dir" ]; then
    log "跳过 $task_id: 项目 '$project' 路径解析结果异常($resolved)"
    continue
  fi

  task_title=$(echo "$line" | python3 -c "import json,sys;print(json.load(sys.stdin).get('title',''))" 2>/dev/null || true)
  task_status=$(echo "$line" | python3 -c "import json,sys;print(json.load(sys.stdin).get('status',''))" 2>/dev/null || true)
  task_pending=$(echo "$line" | python3 -c "import json,sys;print(json.load(sys.stdin).get('pending_req',''))" 2>/dev/null || true)
  task_plan_approved=$(echo "$line" | python3 -c "import json,sys;print(json.load(sys.stdin).get('plan_approved',''))" 2>/dev/null || true)
  task_merge_approved=$(echo "$line" | python3 -c "import json,sys;print(json.load(sys.stdin).get('merge_approved',''))" 2>/dev/null || true)
  task_assignee=$(echo "$line" | python3 -c "import json,sys;print(json.load(sys.stdin).get('assignee','deepseek'))" 2>/dev/null || true)
  task_path="$VAULT/Tasks/$task_file"

  # Auto-created tasks start as blocked while required fields are missing.
  # find_ready_tasks.py only returns blocked tasks when the user has filled
  # project + supported assignee and there are no dependency blockers. Promote
  # them to ready before invoking the agent so the skill enters Round 1.
  if [ "$task_status" = "blocked" ]; then
    log "$task_id: 必填字段已补齐，自动解除 blocked → ready"
    python3 "$SKILL_DIR/scripts/update_task_status.py" "$task_path" \
      status=ready 2>>"$LOG_DIR/task-runner.log" || true
    task_status="ready"
  fi

  # pending_req 检查必须在 plan_approved 之前——
  # 避免先弹 "开始实现" 再弹 "需求变更已并入"，误导用户
  if [ "$task_pending" = "True" ] && [ "$task_status" != "ready" ] && [ "$task_status" != "plan-review" ]; then
    log "$task_id 因 pending_req 唤醒（当前 status=$task_status），重置为 ready 并重新出计划"
    python3 "$SKILL_DIR/scripts/update_task_status.py" "$task_path" \
      status=ready pending_req=false plan_approved=false merge_approved=false 2>>"$LOG_DIR/task-runner.log" || true
    if command -v notify-send >/dev/null 2>&1; then
      notify-send --urgency=normal --app-name="OMP Task Runner" --icon=emblem-refresh \
        "🔄 Task ${task_id}: 需求变更已并入" \
        "${task_title:-}\n自动根据新需求重新出计划" &
    fi
  elif [ "$task_merge_approved" = "True" ] && { [ "$task_status" = "review" ] || [ "$task_status" = "conflict" ]; }; then
    log "检测到 Merge 任务（merge_approved=true），即将执行自动合并"
    if command -v notify-send >/dev/null 2>&1; then
      notify-send --urgency=normal --app-name="OMP Task Runner" --icon=emblem-system \
        "🔀 Task ${task_id}: 开始合并" \
        "${task_title:-}\n正在将功能分支合并到主分支" &
    fi
  elif [ "$task_plan_approved" = "True" ] && [ "$task_status" = "plan-review" ]; then
    log "检测到 Round 2 任务（plan_approved=true），即将开始实现"
    if command -v notify-send >/dev/null 2>&1; then
      notify-send --urgency=normal --app-name="OMP Task Runner" --icon=emblem-system \
        "🚀 Task ${task_id}: 开始实现" \
        "${task_title:-}\nOMP 正在执行，完成后会通知你" &
    fi
  fi

  log "开始处理 $task_id (project=$project, repo=$repo_dir, stage=$repo_status)"

  if run_task_agent "$task_id" "$repo_dir" "$task_assignee" "$task_status" "$task_merge_approved" >>"$LOG_DIR/task-runner.log" 2>&1
  then
    log "$task_id 处理完成"

    # Round 1 或 Round 2 完成后检查任务状态，如果到了 gate 就发桌面通知
    if [ -n "$task_file" ] && [ -f "$VAULT/Tasks/$task_file" ]; then
      "$SKILL_DIR/scripts/notify_on_status_change.sh" "$VAULT/Tasks/$task_file" &
    fi

    # 检查是否有待处理的需求变更（Round 2 期间需求文档被更新了）
    task_path="$VAULT/Tasks/$task_file"
    if [ -n "$task_file" ] && [ -f "$task_path" ]; then
      pending=$(python3 -c "
import re
with open('$task_path') as f:
    content = f.read()
fm = content.split('---', 2)[1] if content.startswith('---') else ''
m = re.search(r'pending_req\s*:\s*true', fm, re.IGNORECASE)
print('yes' if m else 'no')
" 2>/dev/null || echo "no")

      if [ "$pending" = "yes" ]; then
        log "$task_id 检测到 pending_req（需求在此期间有更新），自动触发新 Round 1"
        if command -v notify-send >/dev/null 2>&1; then
          notify-send --urgency=normal --app-name="OMP Task Runner" --icon=emblem-refresh \
            "🔄 Task ${task_id}: 需求变更已并入" \
            "${task_title:-}\n当前工作已完成，自动根据新需求重新出计划" &
        fi

        # 重置任务状态，清除 pending_req
        python3 "$SKILL_DIR/scripts/update_task_status.py" "$task_path" \
          status=ready plan_approved=false pending_req=false merge_approved=false 2>>"$LOG_DIR/task-runner.log" || true

        # 立即进入新 Round 1（在同一次 daemon 运行中链式处理）
        log "$task_id 开始链式处理（pending_req → Round 1）"

        if run_task_agent "$task_id" "$repo_dir" "$task_assignee" "$task_status" "$task_merge_approved" >>"$LOG_DIR/task-runner.log" 2>&1
        then
          log "$task_id 链式 Round 1 完成（新计划已生成）"
          if [ -f "$task_path" ]; then
            "$SKILL_DIR/scripts/notify_on_status_change.sh" "$task_path" &
          fi
        else
          log "$task_id 链式 Round 1 失败"
        fi
      fi
    fi
  else
    log "$task_id 处理失败,详情见上方日志"
  fi
  done  # end inner: while read line (tasks in current batch)

  log "第 $scan_round 轮扫描完成"
done  # end outer: while scan_round < max_rounds (re-scan loop)
