#!/usr/bin/env bash
# Obsidian Task Runner — 一键安装/卸载脚本
#
# 安装（交互）:     ./install.sh
# 安装（非交互）:   OBSIDIAN_VAULT=... ./install.sh --non-interactive
# 强制覆盖安装:     ./install.sh --force
# 完全卸载:         ./install.sh --uninstall
# 卸载（含配置）:   ./install.sh --uninstall --force
set -euo pipefail

# ── 常量 ──
SRC_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SKILL_INSTALL_DIR="${SKILL_INSTALL_DIR:-$HOME/.omp/skills/obsidian-task-runner}"
AGENTS_HOME="$HOME/.omp/agents"
SYSTEMD_USER_DIR="$HOME/.config/systemd/user"
LOG_DIR="$HOME/.omp/logs"
MAP_FILE="$SKILL_INSTALL_DIR/config/vault-map.json"

say()  { printf '\033[1;36m==>\033[0m %s\n' "$1"; }
warn() { printf '\033[1;33m!!\033[0m %s\n' "$1"; }
ok()   { printf '\033[1;32mOK\033[0m %s\n' "$1"; }
err()  { printf '\033[1;31mERR\033[0m %s\n' "$1"; }

# ── 参数解析 ──
NON_INTERACTIVE=false
FORCE=false
UNINSTALL=false
for arg in "$@"; do
  case "$arg" in
    --non-interactive) NON_INTERACTIVE=true ;;
    --force) FORCE=true ;;
    --uninstall) UNINSTALL=true ;;
    --help|-h)
      cat <<'HELP'
Usage:
  ./install.sh                       交互式安装
  ./install.sh --non-interactive     非交互安装（通过环境变量配置）
  ./install.sh --force               强制覆盖所有文件（含 vault-map.json）
  ./install.sh --uninstall           卸载（保留 vault-map.json 和日志）
  ./install.sh --uninstall --force   完全卸载（删除所有配置和日志）

安装环境变量:
  OBSIDIAN_VAULT          Obsidian Vault 根目录
  NEW_PROJECT_ROOT        新项目默认创建目录（默认 $HOME/src）
  NOTIFY_ENABLED          启用桌面通知（默认 true）
  POLL_INTERVAL_MINUTES   systemd 定时器间隔分钟数（默认 30）
  SYSTEMD_ENABLED         注册 systemd 服务（默认 true）
  SKILL_INSTALL_DIR       skill 安装路径（默认 ~/.omp/skills/obsidian-task-runner）
HELP
      exit 0
      ;;
  esac
done

# ══════════════════════════════════════════════════════════════════════════════
# 卸载逻辑
# ══════════════════════════════════════════════════════════════════════════════
if [ "$UNINSTALL" = true ]; then
  echo ""
  say "Obsidian Task Runner — 卸载"
  echo ""

  # 确认（交互模式下）
  if [ "$NON_INTERACTIVE" = false ] && [ "$FORCE" = false ]; then
    read -r -p "确认卸载 obsidian-task-runner? [y/N] " reply
    [[ "$reply" =~ ^[Yy]$ ]] || { ok "已取消"; exit 0; }
  fi

  # 1. 停用 systemd 单元
  say "停用 systemd 单元"
  for unit in omp-task-runner.timer omp-task-watcher.service; do
    if systemctl --user is-enabled "$unit" &>/dev/null; then
      systemctl --user disable --now "$unit" 2>/dev/null || true
      ok "已停用 $unit"
    else
      ok "$unit 未启用，跳过"
    fi
  done
  systemctl --user daemon-reload 2>/dev/null || true

  # 2. 删除 systemd 单元文件
  say "清理 systemd 单元文件"
  for unit in omp-task-runner.service omp-task-runner.timer omp-task-watcher.service; do
    if [ -f "$SYSTEMD_USER_DIR/$unit" ]; then
      rm "$SYSTEMD_USER_DIR/$unit"
      ok "已删除 $SYSTEMD_USER_DIR/$unit"
    else
      ok "$unit 不存在，跳过"
    fi
  done

  # 3. 删除 skill
  say "删除 skill"
  if [ -d "$SKILL_INSTALL_DIR" ]; then
    rm -rf "$SKILL_INSTALL_DIR"
    ok "已删除 $SKILL_INSTALL_DIR"
  else
    ok "skill 目录不存在，跳过"
  fi

  # 4. 删除 task-verifier
  say "删除 task-verifier subagent"
  if [ -f "$AGENTS_HOME/task-verifier.md" ]; then
    rm "$AGENTS_HOME/task-verifier.md"
    ok "已删除 $AGENTS_HOME/task-verifier.md"
  else
    ok "task-verifier.md 不存在，跳过"
  fi

  # 5. 清理日志（--force 时删除）
  if [ "$FORCE" = true ]; then
    say "清理日志"
    if [ -d "$LOG_DIR" ]; then
      rm -rf "$LOG_DIR"
      ok "已删除 $LOG_DIR"
    fi
  else
    ok "保留日志目录 $LOG_DIR（用 --force 同时删除）"
  fi

  # 6. 提示清理 shell rc 中的环境变量
  say "环境变量"
  found_in_rc=false
  for rc in "$HOME/.bashrc" "$HOME/.zshrc" "$HOME/.config/fish/config.fish"; do
    if [ -f "$rc" ] && grep -q "OBSIDIAN_VAULT" "$rc" 2>/dev/null; then
      warn "$rc 中包含 OBSIDIAN_VAULT，请手动删除那一行"
      found_in_rc=true
    fi
  done
  [ "$found_in_rc" = false ] && ok "shell rc 中未发现 OBSIDIAN_VAULT"

  echo ""
  say "卸载完成"
  [ "$FORCE" = false ] && warn "vault-map.json 和日志已保留，如需完全清除请用 --uninstall --force"
  exit 0
fi

# ══════════════════════════════════════════════════════════════════════════════
# 安装逻辑
# ══════════════════════════════════════════════════════════════════════════════

echo ""
say "Obsidian Task Runner — 安装"
echo ""

# 检查是否已安装（重运行时给出清晰提示）
ALREADY_INSTALLED=false
[ -d "$SKILL_INSTALL_DIR" ] && ALREADY_INSTALLED=true

if [ "$ALREADY_INSTALLED" = true ] && [ "$FORCE" = false ]; then
  warn "检测到已有安装: $SKILL_INSTALL_DIR"
  warn "将增量更新 skill 文件，但保留 vault-map.json 和 task-verifier.md"
  warn "如需强制覆盖全部文件请用 --force，如需卸载请用 --uninstall"
  echo ""
fi

# ── 1. 依赖检查 ──
say "Step 1/7: 检查依赖"

missing=()
for bin in python3 git omp; do
  if ! command -v "$bin" >/dev/null 2>&1; then
    missing+=("$bin")
  else
    ok "$bin -> $(command -v "$bin")"
  fi
done

if [ "${#missing[@]}" -gt 0 ]; then
  err "缺少以下命令: ${missing[*]}"
  err "请先安装缺失的依赖:"
  err "  arch: pacman -S python git"
  err "  omp: 请参考项目文档安装 Oh My Pi"
  exit 1
fi

if ! command -v inotifywait >/dev/null 2>&1; then
  warn "缺少 inotifywait（没有它只能用定时轮询，做不到文件保存后即时触发）"
  if [ "$NON_INTERACTIVE" = false ] && command -v pacman >/dev/null 2>&1; then
    read -r -p "现在用 sudo pacman -S inotify-tools 安装? [y/N] " reply
    [[ "$reply" =~ ^[Yy]$ ]] && sudo pacman -S --needed inotify-tools
  fi
fi

if ! command -v notify-send >/dev/null 2>&1; then
  warn "缺少 notify-send（桌面通知不可用，状态变更时不会弹提醒）"
fi

# ── 2. 询问或读取配置 ──
say "Step 2/7: 配置"

if [ "$NON_INTERACTIVE" = true ]; then
  OBSIDIAN_VAULT="${OBSIDIAN_VAULT:?非交互模式必须设置 OBSIDIAN_VAULT 环境变量}"
  NEW_PROJECT_ROOT="${NEW_PROJECT_ROOT:-$HOME/src}"
  NOTIFY_ENABLED="${NOTIFY_ENABLED:-true}"
  POLL_INTERVAL_MINUTES="${POLL_INTERVAL_MINUTES:-30}"
  SYSTEMD_ENABLED="${SYSTEMD_ENABLED:-true}"
  ok "非交互模式: OBSIDIAN_VAULT=$OBSIDIAN_VAULT"
  ok "非交互模式: NEW_PROJECT_ROOT=$NEW_PROJECT_ROOT"
  ok "非交互模式: NOTIFY_ENABLED=$NOTIFY_ENABLED"
  ok "非交互模式: POLL_INTERVAL_MINUTES=${POLL_INTERVAL_MINUTES}min"
  ok "非交互模式: SYSTEMD_ENABLED=$SYSTEMD_ENABLED"
else
  # 如果已安装且 vault-map.json 存在，读取旧值作为默认值
  default_vault="$HOME/Documents/Obsidian/MainVault"
  default_new_root="$HOME/src"
  if [ -f "$MAP_FILE" ]; then
    read_old_vault="$(python3 -c "import json;print(json.load(open('$MAP_FILE')).get('obsidian_vault',''))" 2>/dev/null || true)"
    read_old_root="$(python3 -c "import json;print(json.load(open('$MAP_FILE')).get('new_project_root',''))" 2>/dev/null || true)"
    [ -n "$read_old_vault" ] && default_vault="$read_old_vault"
    [ -n "$read_old_root" ] && default_new_root="$read_old_root"
    ok "检测到已有配置，回车使用旧值"
  fi

  read -r -p "Obsidian Vault 根目录 [$default_vault]: " vault_input
  OBSIDIAN_VAULT="${vault_input:-$default_vault}"

  read -r -p "新项目默认创建目录 [$default_new_root]: " new_root_input
  NEW_PROJECT_ROOT="${new_root_input:-$default_new_root}"

  read -r -p "启用桌面通知? [Y/n]: " notify_input
  NOTIFY_ENABLED=true
  [[ "$notify_input" =~ ^[Nn]$ ]] && NOTIFY_ENABLED=false

  read -r -p "systemd 定时轮询间隔(分钟) [30]: " interval_input
  POLL_INTERVAL_MINUTES="${interval_input:-30}"

  read -r -p "注册 systemd 服务? [Y/n]: " systemd_input
  SYSTEMD_ENABLED=true
  [[ "$systemd_input" =~ ^[Nn]$ ]] && SYSTEMD_ENABLED=false
fi

# ── 3. 安装 skill ──
say "Step 3/7: 安装 skill 到 $SKILL_INSTALL_DIR"

mkdir -p "$(dirname "$SKILL_INSTALL_DIR")"
if [ -d "$SKILL_INSTALL_DIR" ]; then
  if [ "$FORCE" = true ]; then
    warn "强制覆盖 $SKILL_INSTALL_DIR"
    rm -rf "$SKILL_INSTALL_DIR"
  else
    ok "$SKILL_INSTALL_DIR 已存在，增量更新（保留 vault-map.json）"
  fi
fi
cp -rT "$SRC_DIR/obsidian-task-runner" "$SKILL_INSTALL_DIR"
chmod +x "$SKILL_INSTALL_DIR"/scripts/*.sh 2>/dev/null || true
chmod +x "$SKILL_INSTALL_DIR"/scripts/*.py 2>/dev/null || true
ok "skill 已更新"

# ── 4. 安装 task-verifier subagent ──
say "Step 4/7: 安装 task-verifier subagent"

mkdir -p "$AGENTS_HOME"
if [ -f "$AGENTS_HOME/task-verifier.md" ] && [ "$FORCE" = false ]; then
  ok "$AGENTS_HOME/task-verifier.md 已存在，跳过（用 --force 覆盖）"
else
  cp "$SRC_DIR/agents/task-verifier.md" "$AGENTS_HOME/"
  if [ "$FORCE" = true ]; then
    ok "task-verifier.md 已强制覆盖"
  else
    ok "task-verifier.md 已安装"
  fi
fi

# ── 5. 生成 vault-map.json ──
say "Step 5/7: 配置 vault-map.json"

if [ -f "$MAP_FILE" ] && [ "$FORCE" = false ]; then
  ok "$MAP_FILE 已存在，不覆盖。如需重新生成请用 --force"
else
  if [ "$FORCE" = true ] && [ -f "$MAP_FILE" ]; then
    warn "强制覆盖 $MAP_FILE（旧文件备份到 ${MAP_FILE}.bak）"
    cp "$MAP_FILE" "${MAP_FILE}.bak"
  fi
  cp "$SKILL_INSTALL_DIR/config/vault-map.example.json" "$MAP_FILE"
  # bash bool → Python bool
  if [ "${NOTIFY_ENABLED,,}" = "true" ]; then
    py_notify="True"
  else
    py_notify="False"
  fi
  # Pass values via argv to avoid shell injection into Python string literal
  python3 -c "
import json, sys
with open(sys.argv[1]) as f:
    config = json.load(f)
config['obsidian_vault'] = sys.argv[2]
config['new_project_root'] = sys.argv[3]
config['notifications']['desktop'] = sys.argv[4] == 'True'
config['poll_interval_minutes'] = int(sys.argv[5])
with open(sys.argv[1], 'w') as f:
    json.dump(config, f, indent=2, ensure_ascii=False)
    f.write('\n')
" -- "$MAP_FILE" "$OBSIDIAN_VAULT" "$NEW_PROJECT_ROOT" "$py_notify" "$POLL_INTERVAL_MINUTES"
  ok "已生成 $MAP_FILE"
  warn "请编辑此文件，填入你的项目映射（projects 字段）"
fi

# ── 6. 环境变量 + 目录 ──
say "Step 6/7: 配置环境"

# 根据当前 $SHELL 判断 shell 类型，写入对应的 rc 文件和语法
detected_shell="$(basename "${SHELL:-bash}")"
shell_rc=""
shell_type="bash"

case "$detected_shell" in
  fish)
    shell_rc="$HOME/.config/fish/config.fish"
    shell_type="fish"
    ;;
  zsh)
    shell_rc="$HOME/.zshrc"
    shell_type="zsh"
    ;;
  *)
    shell_rc="$HOME/.bashrc"
    shell_type="bash"
    ;;
esac

ok "检测到当前 shell: $detected_shell → 写入 $shell_rc"

# 确保 rc 文件所在目录存在
mkdir -p "$(dirname "$shell_rc")"

if [ "$shell_type" = "fish" ]; then
  if grep -q "set -gx OBSIDIAN_VAULT" "$shell_rc" 2>/dev/null; then
    ok "$shell_rc 中已有 OBSIDIAN_VAULT，不重复添加"
  else
    echo "set -gx OBSIDIAN_VAULT \"$OBSIDIAN_VAULT\"" >> "$shell_rc"
    ok "已写入 $shell_rc（fish 语法: set -gx OBSIDIAN_VAULT \"$OBSIDIAN_VAULT\"）"
  fi
else
  if grep -q "^export OBSIDIAN_VAULT=" "$shell_rc" 2>/dev/null; then
    ok "$shell_rc 中已有 OBSIDIAN_VAULT，不重复添加"
  else
    echo "export OBSIDIAN_VAULT=\"$OBSIDIAN_VAULT\"" >> "$shell_rc"
    ok "已写入 $shell_rc（bash/zsh 语法，当前终端可 source $shell_rc）"
  fi
fi

mkdir -p "$OBSIDIAN_VAULT/Tasks" "$OBSIDIAN_VAULT/Requirements"
mkdir -p "$NEW_PROJECT_ROOT"
mkdir -p "$LOG_DIR"
ok "目录已创建: Tasks/ Requirements/ $NEW_PROJECT_ROOT/ ~/.omp/logs/"

# ── 7. systemd ──
say "Step 7/7: 配置 systemd"

# 尽量探测常见的额外 bin 目录,拼进 systemd 服务用的 PATH(systemd --user 不读 ~/.bashrc)
extra_paths=()
for p in "$HOME/.npm-global/bin" "$HOME/go/bin" "$HOME/.local/bin"; do
  [ -d "$p" ] && extra_paths+=("$p")
done
# 如果 Go 可用，追加 GOPATH/bin
if go_bin="$(go env GOPATH 2>/dev/null)/bin" && [ -d "$go_bin" ]; then
  extra_paths+=("$go_bin")
fi
runner_path="$(IFS=:; echo "${extra_paths[*]}"):/usr/local/bin:/usr/bin:/bin"

if [ "$SYSTEMD_ENABLED" = true ]; then
  mkdir -p "$SYSTEMD_USER_DIR"

  for unit in omp-task-runner.service omp-task-runner.timer omp-task-watcher.service; do
    if [ -f "$SRC_DIR/$unit" ]; then
      # Use Python for safe string replacement (avoids sed injection via path chars).
      # Pass values via environment variables to avoid shell quoting issues.
      _SRC_UNIT="$SRC_DIR/$unit" \
      _OBSIDIAN_VAULT="$OBSIDIAN_VAULT" \
      _RUNNER_PATH="$runner_path" \
      _POLL_INTERVAL="$POLL_INTERVAL_MINUTES" \
      _DEST_UNIT="$SYSTEMD_USER_DIR/$unit" \
      python3 -c "
import os
with open(os.environ['_SRC_UNIT']) as f:
    content = f.read()
content = content.replace('__OBSIDIAN_VAULT_PATH__', os.environ['_OBSIDIAN_VAULT'])
content = content.replace('__RUNNER_PATH__', os.environ['_RUNNER_PATH'])
content = content.replace('__POLL_INTERVAL_MINUTES__', os.environ['_POLL_INTERVAL'])
content = content.replace('OnUnitActiveSec=30min', f\"OnUnitActiveSec={os.environ['_POLL_INTERVAL']}min\")
with open(os.environ['_DEST_UNIT'], 'w') as f:
    f.write(content)
"
    else
      warn "$SRC_DIR/$unit 不存在，跳过"
    fi
  done

  if command -v systemctl >/dev/null 2>&1; then
    systemctl --user daemon-reload
    systemctl --user enable --now omp-task-runner.timer 2>/dev/null || \
      warn "omp-task-runner.timer 注册失败，请手动检查"
    ok "omp-task-runner.timer 已启用（每 ${POLL_INTERVAL_MINUTES} 分钟兜底轮询）"

    if command -v inotifywait >/dev/null 2>&1; then
      systemctl --user enable --now omp-task-watcher.service 2>/dev/null || \
        warn "omp-task-watcher.service 注册失败，请手动检查"
      ok "omp-task-watcher.service 已启用（Tasks/ 文件保存即触发）"
    else
      warn "跳过 omp-task-watcher.service（缺 inotifywait）"
    fi
  else
    warn "systemctl 不可用（非 systemd 环境），单元文件已生成但未注册"
    warn "请手动运行: cd <项目目录> && omp -p \"/obsidian-task-runner\""
  fi
  ok "跳过 systemd 注册（SYSTEMD_ENABLED=false），手动运行方式:"
  ok "  cd <项目目录> && omp -m \"\$OMP_MODEL\" -p \"/obsidian-task-runner\""
fi

# ── 完成 ──
echo ""
say "安装完成！"
cat <<SUMMARY

后续步骤:
  1. 编辑 $MAP_FILE
     填入你的项目映射（projects 字段），把示例改成实际路径
  2. source $shell_rc  或开新终端，让 OBSIDIAN_VAULT 生效
  3. 创建第一个任务:
     cp $SRC_DIR/TASK-000-template.md $OBSIDIAN_VAULT/Tasks/TASK-001-你的任务.md
     编辑 frontmatter: id, title, project, req_doc
  4. 写需求文档:
     cp $SRC_DIR/REQ-000-template.md $OBSIDIAN_VAULT/Requirements/你的需求.md
  5. 查看运行状态:
     systemctl --user status omp-task-watcher.service
     systemctl --user list-timers | grep omp-task-runner
  6. 查看日志:
     tail -f $LOG_DIR/task-watcher.log
     tail -f $LOG_DIR/task-runner.log
  7. 手动测试一次（不依赖 systemd）:
     cd <某个项目目录>
     omp -m "deepseek/deepseek-v4-pro:xhigh" -p "/obsidian-task-runner <task_id>"

卸载:
  ./install.sh --uninstall          保留配置和日志
  ./install.sh --uninstall --force  完全清除

SUMMARY
