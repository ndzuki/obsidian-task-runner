#!/usr/bin/env python3
# ⚠️ 2026-09-23 起本脚本的 KB 断言已失效：它检查 `kb-preflight`/`kb-distill` 是否**在位**，
#   而这两个插件已随 KB 面归档移除（~/.dsh/archive/kb-20260922-1709/），
#   运行本脚本会给出误导性的"缺失"警告。项目已归档，本脚本不再维护。

"""归档 obsidian-task-runner：一条命令完成全部宿主侧拆除。

把之前散在文档里的 5 步手工操作收敛成一次可预演、可回滚的执行：

1. **停 + 禁用 otg 专有服务**：`otg-task-watcher.service`（daemon）、
   `dsh-agent-server.service`（它的 headless 执行地基）。只 disable 不删单元文件
   ——保留回滚能力。
2. **移走依赖 daemon 的插件**：`vault.mjs`（/vault 看板 → otg web 8787）、
   `agent-server.mjs`（headless 执行地基）及其测试与 Agent Town 面板。
3. **清掉 cordis 里的 otg 注册项**：删除 `vault-dashboard` 条目
   （`~/.dsh/cordis.patch.yml` 与 chezmoi 源**双写**——该文件被 chezmoi 纳管，
   只改一份会被反向收编回滚）。
4. **移走带凭据的 drop-in**：`otg-task-watcher.service.d/`（内含 `gh-token.conf`，
   归档后不应继续在系统里留存 token）。
5. **退役流水线 skill**：调用同目录的 `retire-local-skill.py`（不重复实现）。

**不动的东西**（归档后日常仍依赖）：`otg` 二进制（`kb-distill` 靠 `spawn('otg')`、
`otg kb` 原为检索入口——该命令已于 2026-09-29 随知识库退役）、`kb-preflight`/`kb-distill`/`dsh-commands`/`fallback` 插件、
`dsh-web`/`dsh-web-token-bridge`/`dsh-model-watch`/`chezmoi-apply-watch`/
`dsh-session-repair` 等 dsh 自身服务、`~/.dsh/storages/otg/kb.sqlite`（知识库遗留数据，其功能已于 2026-09-29 退役）、整个 vault。

用法：
    python3 teardown-otg.py --dry-run
    python3 teardown-otg.py
    # 演练（不碰真实环境、不碰 systemd）：
    python3 teardown-otg.py --dsh-dir /tmp/t/.dsh --systemd-dir /tmp/t/systemd \\
        --backup-dir /tmp/t/trash --skip-systemctl --skip-skills
"""

from __future__ import annotations

import argparse
import datetime
import io
import json
import os
import re
import shutil
import subprocess
import sys
import time

UNITS = ("otg-task-watcher.service", "dsh-agent-server.service")

# 依赖 daemon 的插件：移入 trash
PLUGIN_FILES = (
    "vault.mjs",              # /vault 看板 → otg web serve(127.0.0.1:8787)
    "agent-server.mjs",       # otg headless 执行地基
    "agent-server.kb.test.mjs",
    "agent-monitor.html",     # Agent Town 面板
    "agent-monitor-classic.html",
)

# cordis 中指向 otg web 的注册项 id
CORDIS_ENTRY_ID = "vault-dashboard"

# 含凭据的 drop-in 目录
CRED_DROPIN = "otg-task-watcher.service.d"

# systemd 的 enable 状态（*.wants 里的条目）被 chezmoi 当受管文件管理：
# 只跑 `systemctl --user disable` 会在**秒级**被 chezmoi-apply-watch 恢复
# （2026-09-14 实测：脚本 11:37:58 移走，11:38:01 复原）。这些条目必须
# 连 chezmoi 源一起移走，否则停机不持久。
WANTS_ENTRIES = (
    "default.target.wants/otg-task-watcher.service",
    "default.target.wants/dsh-agent-server.service",
)

# router skill 目录：**不可删除**。它的 config/vault-map.json 是 kb-preflight 的
# 默认配置来源（插件里 DEFAULT_MAP_FILE 写死了这个路径），删了会让 vault 上下文
# 与知识库的自动注入整体失效。停用方式 = 替换 SKILL.md 为墓碑（退出 agent 目录）。
ROUTER_DIR = ("skills", "obsidian-task-runner")
ROUTER_TOMBSTONE = """---
name: obsidian-task-runner
description: "已归档（2026-09-14）：otg daemon 与阶段流水线已停用，本 skill 不提供任何任务编排能力。目录保留仅为承载 config/vault-map.json —— kb-preflight 插件按固定路径读取它，用于每个会话自动注入 vault 上下文与知识库命中。"
disable-model-invocation: true
---

# 已归档 — 请勿调用

本 skill 于 2026-09-14 随项目归档停用。**不要用它编排任何任务。**

## 为什么目录还在

`config/vault-map.json` 是 `~/.dsh/plugins/kb-preflight.mjs` 的默认配置来源
（其 `DEFAULT_MAP_FILE` 写死了这个路径）。它提供 **vault 根路径、知识库位置、
已注册项目清单**，供每个交互会话自动注入 `<project_context>` 与 `<knowledge_base>`。

> **删掉本目录 = 自动注入静默失效。** 归档拆除脚本保留 `config/`，
> 只把本文件替换成这份墓碑，让 skill 退出 agent 目录。

归档后的交互式日常能力见仓库 `skills-standalone/`：
`project-baseline-audit` / `risk-aware-planning` / `design-pass` / `incremental-delivery`。
"""


def find_entry_span(lines: list[str], entry_id: str) -> tuple[int, int] | None:
    """定位 `- id: <entry_id>` 条目（含紧邻其上的注释块）的行区间 [start, end)。

    向前吞掉连续的注释行，向后吞掉比 `- id:` 缩进更深的续行——不依赖条目位置，
    用户重排 YAML 后依然正确。
    """
    idx = None
    for i, line in enumerate(lines):
        if re.match(rf"^\s*-\s*id:\s*{re.escape(entry_id)}\s*$", line):
            idx = i
            break
    if idx is None:
        return None
    indent = len(lines[idx]) - len(lines[idx].lstrip())
    start = idx
    while start > 0 and lines[start - 1].lstrip().startswith("#"):
        start -= 1
    end = idx + 1
    while end < len(lines):
        cur = lines[end]
        if cur.strip() == "":
            end += 1
            continue
        cur_indent = len(cur) - len(cur.lstrip())
        if cur_indent <= indent:
            break
        end += 1
    # 回退掉尾部多吞的空行，避免留下多余空行
    while end > idx + 1 and lines[end - 1].strip() == "":
        end -= 1
    return start, end


def strip_entry(text: str, entry_id: str) -> tuple[str, bool]:
    """返回 (新文本, 是否有改动)。"""
    lines = text.splitlines(keepends=True)
    span = find_entry_span(lines, entry_id)
    if span is None:
        return text, False
    start, end = span
    del lines[start:end]
    return "".join(lines), True


def chezmoi_source(target: str) -> str | None:
    """返回 target 的 chezmoi 源路径；未纳管 / chezmoi 不可用 → None。

    这是本脚本最关键的修正点：`~/.dsh/plugins`、`~/.config/systemd/user` 都是
    chezmoi 纳管路径，**只把 home 那份移走会被 reverse-sync 在秒级恢复**（实测 3s）。
    移动/删除类动作必须连源一起处理；只有内容修改才能靠双写解决。
    """
    try:
        r = subprocess.run(["chezmoi", "source-path", target],
                           capture_output=True, text=True, timeout=20)
    except (FileNotFoundError, subprocess.TimeoutExpired):
        return None
    if r.returncode != 0:
        return None
    p = (r.stdout or "").strip()
    return p if p and os.path.exists(p) else None


def verify_postconditions(dsh_dir: str, systemd_dir: str) -> list[str]:
    """归档后必须成立的后置条件。

    「静默失效」是最坏的失败模式——拆完看起来成功，但自动上下文注入已经不工作
    而无人察觉（这正是本项目被归档的那类错误）。所以这里显式检查并列出问题。
    """
    problems: list[str] = []
    map_path = os.path.join(dsh_dir, *ROUTER_DIR, "config", "vault-map.json")
    if not os.path.isfile(map_path):
        problems.append(
            f"vault-map.json 缺失：{map_path} → kb-preflight 的 vault 上下文/KB 注入会整体关闭")
    else:
        try:
            cfg = json.loads(io.open(map_path, encoding="utf-8").read())
        except Exception as exc:  # noqa: BLE001 - 任何解析失败都算问题
            problems.append(f"vault-map.json 无法解析：{exc}")
        else:
            if not (cfg.get("kb_vault") or cfg.get("obsidian_vault")):
                problems.append("vault-map.json 的 kb_vault / obsidian_vault 均为空 → 注入会关闭")
    for name in ("kb-preflight.mjs", "kb-distill.mjs"):
        if not os.path.isfile(os.path.join(dsh_dir, "plugins", name)):
            problems.append(f"{name} 缺失 → 自动注入/沉淀链路断了（本不该被移走）")
    for name in PLUGIN_FILES:
        if os.path.isfile(os.path.join(dsh_dir, "plugins", name)):
            problems.append(f"应已移走但仍存在：{name}")
    for rel in WANTS_ENTRIES:
        if os.path.lexists(os.path.join(systemd_dir, rel)):
            problems.append(f"enable 项仍在（重启后会自启）：{rel}")
    if os.path.lexists(os.path.join(systemd_dir, CRED_DROPIN)):
        problems.append(f"凭据 drop-in 仍在：{CRED_DROPIN}")
    router_skill = os.path.join(dsh_dir, *ROUTER_DIR, "SKILL.md")
    if os.path.isfile(router_skill):
        if "disable-model-invocation: true" not in io.open(router_skill, encoding="utf-8").read():
            problems.append("router SKILL.md 未停用 → 仍会出现在 agent 的技能目录里")
    return problems


def main() -> int:
    home = os.path.expanduser("~")
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--dsh-dir", default=f"{home}/.dsh")
    ap.add_argument("--systemd-dir", default=f"{home}/.config/systemd/user")
    ap.add_argument("--backup-dir", default=f"{home}/.dsh/trash")
    ap.add_argument("--chezmoi-skills-dir", default=f"{home}/.local/share/chezmoi/dot_dsh/skills")
    ap.add_argument("--chezmoi-cordis", default=f"{home}/.local/share/chezmoi/dot_dsh/private_cordis.patch.yml")
    ap.add_argument("--chezmoi-router",
                    default=f"{home}/.local/share/chezmoi/dot_dsh/skills/obsidian-task-runner/SKILL.md",
                    help="router SKILL.md 的 chezmoi 源（纳管文件，须与 ~/.dsh 双写）")
    ap.add_argument("--runtime-dir", default=f"{home}/.dsh/skills",
                    help="skill 运行副本目录（透传给 retire-local-skill.py）")
    ap.add_argument("--dry-run", action="store_true")
    ap.add_argument("--verify", action="store_true",
                    help="只跑后置校验（拆完/过一会儿再确认没被反向收编恢复）")
    ap.add_argument("--skip-systemctl", action="store_true", help="不碰 systemd（演练用）")
    ap.add_argument("--skip-skills", action="store_true", help="不退役 skill（演练用）")
    args = ap.parse_args()

    if args.verify:
        print("=== 归档后置校验 ===")
        problems = verify_postconditions(args.dsh_dir, args.systemd_dir)
        if problems:
            print("  ⚠ 发现问题（可能被 chezmoi 反向收编恢复）：")
            for p in problems:
                print(f"    - {p}")
            return 1
        print("  ✓ vault-map.json 完好、kb-preflight/kb-distill 在位、daemon 插件已移走、"
              "enable 项已移除、凭据 drop-in 已移除、router 已退出 agent 目录")
        return 0

    stamp = datetime.datetime.now().strftime("%Y%m%d-%H%M%S")
    trash = os.path.join(args.backup_dir, f"otg-archive-{stamp}")
    planned: list[str] = []
    done: list[str] = []
    skipped: list[str] = []

    def move(src: str, note: str) -> None:
        """把 home 路径与其 chezmoi 源一起移入 trash。

        顺序很关键：**先移源**。若先移 home，`chezmoi-apply-watch` 会在秒级把源
        恢复回 home（本脚本第一版就栽在这里：自报"已移走"，3 秒后文件全回来了）。
        源不在后，home 的移除才是稳定的。
        """
        src_source = chezmoi_source(src)
        if not os.path.lexists(src) and not src_source:
            skipped.append(f"{note}：不存在（已处理过）")
            return
        planned.append(f"移入 trash：{src}"
                       + (f"\n             + chezmoi 源：{src_source}" if src_source else ""))
        if args.dry_run:
            return
        os.makedirs(trash, exist_ok=True)
        if src_source:
            shutil.move(src_source, os.path.join(trash, "SOURCE--" + os.path.basename(src_source.rstrip("/"))))
            done.append(f"已移走 chezmoi 源（{note}）")
        if os.path.lexists(src):
            shutil.move(src, os.path.join(trash, os.path.basename(src.rstrip("/"))))
            done.append(f"已移走 {note}")

    # ---- 1. systemd ----
    if args.skip_systemctl:
        skipped.append("systemd：--skip-systemctl，未触碰")
    else:
        for unit in UNITS:
            for verb in ("stop", "disable"):
                cmd = ["systemctl", "--user", verb, unit]
                planned.append("执行：" + " ".join(cmd))
                if args.dry_run:
                    continue
                try:
                    r = subprocess.run(cmd, capture_output=True, text=True)
                except FileNotFoundError:
                    skipped.append("systemd：找不到 systemctl，跳过（请手工停用）")
                    break
                if r.returncode != 0:
                    msg = (r.stderr or r.stdout).strip().splitlines()
                    skipped.append(f"systemctl {verb} {unit} 返回 {r.returncode}"
                                   f"（{'无 user bus' if 'bus' in (r.stderr or '') else (msg[0] if msg else '未知')}）")
                else:
                    done.append(f"systemd {verb} {unit}")

    # ---- 2. 插件 ----
    for name in PLUGIN_FILES:
        move(os.path.join(args.dsh_dir, "plugins", name), f"插件 {name}")

    # ---- 2b. systemd enable 项（chezmoi 纳管 → 必须连源一起移，否则 disable 不持久）----
    for rel in WANTS_ENTRIES:
        move(os.path.join(args.systemd_dir, rel), f"enable 项 {rel}")

    # ---- 4. 凭据 drop-in（放在插件后，避免与 3 的日志顺序混淆）----
    move(os.path.join(args.systemd_dir, CRED_DROPIN), f"凭据 drop-in {CRED_DROPIN}")

    # ---- 3. cordis 注册项（双写）----
    cordis_home = os.path.join(args.dsh_dir, "cordis.patch.yml")
    cordis_src = args.chezmoi_cordis
    for label, path in (("~/.dsh 副本", cordis_home), ("chezmoi 源", cordis_src)):
        if not os.path.isfile(path):
            skipped.append(f"cordis {label}：不存在 {path}")
            continue
        text = io.open(path, encoding="utf-8").read()
        new, changed = strip_entry(text, CORDIS_ENTRY_ID)
        if not changed:
            skipped.append(f"cordis {label}：已无 {CORDIS_ENTRY_ID} 条目")
            continue
        planned.append(f"删除 cordis 条目 {CORDIS_ENTRY_ID}（{label}）：{path}")
        if args.dry_run:
            continue
        bak = os.path.join(trash, f"cordis-{label.replace('~/.dsh 副本', 'home').replace('chezmoi 源', 'source')}.yml")
        os.makedirs(trash, exist_ok=True)
        shutil.copy2(path, bak)
        with io.open(path, "w", encoding="utf-8") as fh:
            fh.write(new)
        done.append(f"cordis 条目已删（{label}），备份 {bak}")

    # ---- 4b. router 墓碑：保留 config/，只让 skill 退出 agent 目录 ----
    router_home = os.path.join(args.dsh_dir, *ROUTER_DIR, "SKILL.md")
    map_home = os.path.join(args.dsh_dir, *ROUTER_DIR, "config", "vault-map.json")
    if not os.path.isfile(map_home):
        skipped.append(f"⚠ router 目录下没有 config/vault-map.json（{map_home}）——"
                       f"kb-preflight 的自动 vault 上下文/KB 注入可能已经失效，请先确认")
    for key, label, path in (("home", "~/.dsh 副本", router_home),
                             ("source", "chezmoi 源", args.chezmoi_router)):
        if not os.path.isfile(path):
            skipped.append(f"router SKILL.md（{label}）：不存在 {path}")
            continue
        text = io.open(path, encoding="utf-8").read()
        if "已归档 — 请勿调用" in text:
            skipped.append(f"router SKILL.md（{label}）：已是墓碑")
            continue
        planned.append(f"写入 router 墓碑（{label}）：{path}")
        if args.dry_run:
            continue
        os.makedirs(trash, exist_ok=True)
        shutil.copy2(path, os.path.join(trash, f"router-SKILL.md.{key}"))
        with io.open(path, "w", encoding="utf-8") as fh:
            fh.write(ROUTER_TOMBSTONE)
        done.append(f"router 墓碑已写入（{label}），config/ 原样保留")

    # ---- 5. 退役流水线 skill ----
    if args.skip_skills:
        skipped.append("skill 退役：--skip-skills，未执行")
    else:
        rs = os.path.join(os.path.dirname(os.path.abspath(__file__)), "retire-local-skill.py")
        if not os.path.isfile(rs):
            skipped.append(f"skill 退役：找不到 {rs}")
        else:
            cmd = [sys.executable, rs,
                   "--skills-dir", args.chezmoi_skills_dir,
                   "--runtime-dir", args.runtime_dir,
                   "--backup-dir", args.backup_dir]
            planned.append("执行：" + " ".join(cmd))
            if not args.dry_run:
                r = subprocess.run(cmd, capture_output=True, text=True)
                lines = [l for l in (r.stdout or "").strip().splitlines() if l.strip()]
                summary = next((l for l in lines if l.startswith("完成：")),
                               lines[-1] if lines else f"rc={r.returncode}")
                done.append("skill 退役：" + summary)
                if r.returncode != 0:
                    skipped.append(f"skill 退役返回 {r.returncode}，请查看其输出")

    # ---- 报告 ----
    print("=== 计划 ===")
    for line in planned:
        print("  " + line)
    if args.dry_run:
        print(f"\n[dry-run] 共 {len(planned)} 项，未写盘。去掉 --dry-run 执行。")
        return 0

    print("\n=== 已完成 ===")
    for line in done or ["（无）"]:
        print("  " + line)
    if skipped:
        print("\n=== 跳过 / 需人工 ===")
        for line in skipped:
            print("  " + line)

    # 其它 profile 是否仍引用被移走的插件
    refs: list[str] = []
    profiles = os.path.join(args.dsh_dir, "profiles")
    if os.path.isdir(profiles):
        for dirpath, _dirnames, filenames in os.walk(profiles):
            for fn in filenames:
                if not fn.endswith((".yml", ".yaml")):
                    continue
                fp = os.path.join(dirpath, fn)
                try:
                    text = io.open(fp, encoding="utf-8", errors="ignore").read()
                except OSError:
                    continue
                for name in PLUGIN_FILES:
                    if name in text:
                        refs.append(f"{os.path.relpath(fp, args.dsh_dir)} 引用 {name}")
                        break

    print("\n=== 后置校验（自动注入链路是否还活着）===")
    time.sleep(5)  # 给 reverse-sync 一个暴露窗口：历史上它 3 秒就把移走的文件恢复了
    problems = verify_postconditions(args.dsh_dir, args.systemd_dir)
    if problems:
        print("  ⚠ 发现问题：")
        for p in problems:
            print(f"    - {p}")
    else:
        print("  ✓ vault-map.json 完好、kb-preflight/kb-distill 在位、daemon 插件已移走、"
              "router 已退出 agent 目录")

    print("\n=== 收尾提示 ===")
    if refs:
        print("  以下**测试用** profile 仍引用已移走的插件（正式 profile 未引用，不影响日常）：")
        for r in refs:
            print(f"    - {r}")
    print(f"  备份/退役目录：{trash}")
    print("  仍需手工完成：")
    standalone_ok = all(
        os.path.isfile(os.path.join(args.runtime_dir, n, "SKILL.md"))
        for n in ("project-baseline-audit", "risk-aware-planning",
                  "design-pass", "incremental-delivery"))
    if standalone_ok:
        print("    1) ✅ 交互式 skill 已安装（make install-standalone 已完成）")
    else:
        print("    1) cd <repo> && make install-standalone    # 安装 vault-first 版技能")
    print("    2) cd ~/.local/share/chezmoi && git add -A && git commit -m "
          "\"chore: 归档 obsidian-task-runner（停 daemon、退役阶段 skill）\"")
    print("    3) 在 vault 的 Projects/003-obsidian-task-runner/Notes/ 记录归档决策")
    print("       （模板见 skills-standalone/local-fixes/README.md）")
    return 0


if __name__ == "__main__":
    sys.exit(main())
