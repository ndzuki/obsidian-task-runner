#!/usr/bin/env bash
# 一键安装 obsidian-task-runner:skill、task-verifier subagent、vault-map 配置、
# 环境变量、systemd 定时兜底(claude-task-runner.timer)+ 事件触发(claude-task-watcher.service)。
#
# 用法: 在解压出来的这个目录里直接跑
#   ./install.sh
#
# 可以反复运行——每一步都做了存在性检查,不会覆盖你已经改过的 vault-map.json。
set -euo pipefail

SRC_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SKILL_HOME="$HOME/.claude/skills/obsidian-task-runner"
AGENTS_HOME="$HOME/.claude/agents"
SYSTEMD_USER_DIR="$HOME/.config/systemd/user"

say()  { printf '\033[1;36m==>\033[0m %s\n' "$1"; }
warn() { printf '\033[1;33m!!\033[0m %s\n' "$1"; }
ok()   { printf '\033[1;32mOK\033[0m %s\n' "$1"; }

# ---------- 1. 依赖检查 ----------
say "检查依赖"

missing=()
for bin in python3 git claude; do
  if ! command -v "$bin" >/dev/null 2>&1; then
    missing+=("$bin")
  else
    ok "$bin -> $(command -v "$bin")"
  fi
done

if [ "${#missing[@]}" -gt 0 ]; then
  warn "缺少以下命令,请先安装再重新运行本脚本: ${missing[*]}"
  warn "claude 的安装方式请参考 https://docs.claude.com (docs.claude.com) 的 Claude Code 安装说明"
  exit 1
fi

if ! command -v inotifywait >/dev/null 2>&1; then
  warn "缺少 inotifywait(用于事件触发,没有它只能靠定时兜底轮询)"
  if command -v pacman >/dev/null 2>&1; then
    read -r -p "检测到 pacman,现在用 sudo pacman -S inotify-tools 安装吗? [y/N] " reply
    if [[ "$reply" =~ ^[Yy]$ ]]; then
      sudo pacman -S --needed inotify-tools
    else
      warn "跳过安装,事件触发(claude-task-watcher.service)这一步会失败,只有 timer 兜底还能用"
    fi
  else
    warn "非 pacman 系统,请手动安装 inotify-tools 对应的包"
  fi
fi

# ---------- 2. 安装 skill / agent ----------
say "安装 skill 到 $SKILL_HOME"
mkdir -p "$HOME/.claude/skills"
cp -r "$SRC_DIR/obsidian-task-runner" "$HOME/.claude/skills/"
chmod +x "$SKILL_HOME"/scripts/*.sh
ok "skill 已安装(含 task-runner-daemon.sh / task-watcher.sh / 各 python 脚本)"

say "安装 task-verifier subagent 到 $AGENTS_HOME"
mkdir -p "$AGENTS_HOME"
if [ -f "$AGENTS_HOME/task-verifier.md" ]; then
  warn "$AGENTS_HOME/task-verifier.md 已存在,不覆盖(如需更新请手动比对差异)"
else
  cp "$SRC_DIR/agents/task-verifier.md" "$AGENTS_HOME/"
  ok "task-verifier.md 已安装"
fi

# ---------- 3. vault-map.json ----------
MAP_FILE="$SKILL_HOME/config/vault-map.json"
if [ -f "$MAP_FILE" ]; then
  warn "$MAP_FILE 已存在,不覆盖"
else
  cp "$SKILL_HOME/config/vault-map.example.json" "$MAP_FILE"
  warn "已从示例生成 $MAP_FILE,里面的仓库路径是占位示例,记得改成你自己的项目映射"
fi

# ---------- 4. OBSIDIAN_VAULT 环境变量 ----------
say "配置 OBSIDIAN_VAULT 环境变量"
default_vault="$HOME/Documents/Obsidian/MainVault"
read -r -p "Obsidian Vault 根目录路径 [$default_vault]: " vault_input
VAULT_PATH="${vault_input:-$default_vault}"

if [ ! -d "$VAULT_PATH" ]; then
  warn "$VAULT_PATH 目前不存在,继续安装,但记得之后要么建好这个目录要么改成正确路径"
fi

shell_rc="$HOME/.bashrc"
[ -n "${ZSH_VERSION:-}" ] && shell_rc="$HOME/.zshrc"
if grep -q "^export OBSIDIAN_VAULT=" "$shell_rc" 2>/dev/null; then
  warn "$shell_rc 里已经有 OBSIDIAN_VAULT,不重复添加,如需修改请手动编辑"
else
  echo "export OBSIDIAN_VAULT=\"$VAULT_PATH\"" >> "$shell_rc"
  ok "已写入 $shell_rc(新开的终端才会生效;当前终端可以 source $shell_rc)"
fi
mkdir -p "$VAULT_PATH/Tasks" "$VAULT_PATH/Requirements"
ok "确保 Tasks/ 和 Requirements/ 目录存在"

# ---------- 5. systemd 用户单元 ----------
say "配置 systemd --user 定时兜底 + 事件触发"

# 尽量探测常见的额外 bin 目录,拼进 systemd 服务用的 PATH(systemd --user 不读 ~/.bashrc)
extra_paths=()
for p in "$HOME/.npm-global/bin" "$HOME/go/bin" "$HOME/.local/bin" "$(go env GOPATH 2>/dev/null)/bin"; do
  [ -n "$p" ] && [ -d "$p" ] && extra_paths+=("$p")
done
runner_path="$(IFS=:; echo "${extra_paths[*]}"):/usr/local/bin:/usr/bin:/bin"

mkdir -p "$SYSTEMD_USER_DIR"
for unit in claude-task-runner.service claude-task-runner.timer claude-task-watcher.service; do
  sed -e "s#__OBSIDIAN_VAULT_PATH__#$VAULT_PATH#g" \
      -e "s#__RUNNER_PATH__#$runner_path#g" \
      "$SRC_DIR/$unit" > "$SYSTEMD_USER_DIR/$unit"
done
ok "systemd 单元已写入 $SYSTEMD_USER_DIR(PATH=$runner_path)"

systemctl --user daemon-reload
systemctl --user enable --now claude-task-runner.timer
ok "claude-task-runner.timer 已启用(30 分钟兜底轮询一次)"

if command -v inotifywait >/dev/null 2>&1; then
  systemctl --user enable --now claude-task-watcher.service
  ok "claude-task-watcher.service 已启用(Tasks/ 文件一保存就立刻触发)"
else
  warn "跳过 claude-task-watcher.service(缺 inotifywait),之后装好了再手动:"
  warn "  systemctl --user enable --now claude-task-watcher.service"
fi

# ---------- 6. 完成 ----------
echo
say "安装完成"
cat <<SUMMARY
后续要做的事:
  1. 编辑 $MAP_FILE,把 project 字段值映射到你本机的仓库绝对路径,并设置 new_project_root
  2. source $shell_rc(或开一个新终端),让 OBSIDIAN_VAULT 生效
  3. 复制 $SRC_DIR/TASK-000-template.md 到 $VAULT_PATH/Tasks/ 建第一个任务
  4. 查看运行状态:
       systemctl --user status claude-task-watcher.service
       systemctl --user list-timers | grep claude-task-runner
  5. 看日志:
       tail -f ~/.claude/logs/task-watcher.log
       tail -f ~/.claude/logs/task-runner.log
SUMMARY
