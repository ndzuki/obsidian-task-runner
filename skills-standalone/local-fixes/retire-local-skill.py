#!/usr/bin/env python3
"""退役本机个人 skill：从 chezmoi 源与运行副本两处移除，移入 trash（不真删）。

## 为什么必须两处一起动

`~/.dsh/skills/<name>/` 是运行副本，`~/.local/share/chezmoi/dot_dsh/skills/<name>/`
是 chezmoi 的**事实源**。只删其中一处：

- 只删运行副本 → 下次 `chezmoi apply` 或反向收编会把它**恢复**回来；
- 只删源 → home 里那份会残留，agent 仍能加载（并可能被反向收编回源）。

## 安全设计

- **白名单**：只允许退役 `ALLOWED` 里列出的名字。防止打错一个字就删掉 `grilling`。
- **移入 trash 而不是删**：`~/.dsh/trash/retired-skills-<时间戳>/`，可整目录搬回。
- **先 dry-run**：`--dry-run` 打印会动哪些目录，不写盘。
- **报告残留引用**：删完扫描全部 skill，列出仍提及该名字的文件——这些是**有意保留**的
  流水线 skill，若流水线要复活需把它们重新指向 `grilling`。

用法：
    python3 retire-local-skill.py --dry-run
    python3 retire-local-skill.py
    python3 retire-local-skill.py --skills-dir /tmp/x/skills --runtime-dir /tmp/x/rt \\
        --backup-dir /tmp/x/bak      # 演练
"""

from __future__ import annotations

import argparse
import datetime
import os
import shutil
import sys

# 允许退役的 skill 名（白名单）。想退役新的，先在这里加名字——这是刻意的摩擦。
ALLOWED: tuple[str, ...] = (
    # 已退役：职责由 grilling 承担，且内含 OMP 时代死命令
    "requirement-elaborator",
    # 流水线阶段 skill：daemon 停用后不再有任何触发源
    #
    # ⚠️ 不要把 "obsidian-task-runner"（router 目录）加进来。它的目录承载
    # config/vault-map.json，而 ~/.dsh/plugins/kb-preflight.mjs 的 DEFAULT_MAP_FILE
    # 写死了这个路径——移走它会让 vault 上下文与知识库的**自动注入整体失效**
    # （插件判定 kb_vault/obsidian_vault 皆空 → 注入关闭）。
    # 该目录由 teardown-otg.py 以「墓碑」方式停用：只替换 SKILL.md 让它退出
    # agent 目录，保留 config/。
    "obsidian-task-runner-refining",
    "obsidian-task-runner-round1",
    "obsidian-task-runner-round2",
    "obsidian-task-runner-merge",
    "obsidian-task-runner-conventions",
    "obsidian-task-runner-priority",
    "obsidian-task-runner-pm",
    "obsidian-task-runner-split",
    "obsidian-task-runner-design",
    # 流水线运维 skill：停 daemon→对齐 TASK→重启冒烟的流程已不存在
    "project-rebaseline",
)

TARGETS: tuple[str, ...] = ALLOWED


def main() -> int:
    home = os.path.expanduser("~")
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--skills-dir", default=f"{home}/.local/share/chezmoi/dot_dsh/skills",
                    help="chezmoi 源目录（事实源）")
    ap.add_argument("--runtime-dir", default=f"{home}/.dsh/skills",
                    help="运行副本目录")
    ap.add_argument("--backup-dir", default=f"{home}/.dsh/trash",
                    help="退役目录根（不要放在 chezmoi 源里，否则会被收编）")
    ap.add_argument("--dry-run", action="store_true")
    args = ap.parse_args()

    unknown = [t for t in TARGETS if t not in ALLOWED]
    if unknown:
        print(f"拒绝执行：{unknown} 不在白名单 {ALLOWED} 内", file=sys.stderr)
        return 2

    stamp = datetime.datetime.now().strftime("%Y%m%d-%H%M%S")
    trash = os.path.join(args.backup_dir, f"retired-skills-{stamp}")

    moves: list[tuple[str, str]] = []
    for name in TARGETS:
        for label, root in (("chezmoi源", args.skills_dir), ("运行副本", args.runtime_dir)):
            path = os.path.join(root, name)
            if os.path.isdir(path):
                moves.append((f"{label}:{name}", path))
            else:
                print(f"  (跳过) {label} 无此目录: {path}")

    if not moves:
        print("没有可退役的目录——可能已经退役过了。")
        return 0

    print("将退役以下目录：")
    for label, path in moves:
        print(f"  {label:12s} {path}")

    if args.dry_run:
        print(f"\n[dry-run] 共 {len(moves)} 个目录；将移入 {trash}；未写盘。")
        return 0

    os.makedirs(trash, exist_ok=True)
    for label, path in moves:
        dest = os.path.join(trash, f"{label.replace(':', '-')}-{os.path.basename(path)}")
        shutil.move(path, dest)
        print(f"  已移出: {path} → {dest}")

    # 残留引用扫描：删完还有谁提到它
    print("\n=== 仍提及该名字的文件（有意保留的流水线 skill）===")
    hits: list[str] = []
    for root in (args.skills_dir, args.runtime_dir):
        if not os.path.isdir(root):
            continue
        for dirpath, _dirnames, filenames in os.walk(root):
            for fn in filenames:
                if not fn.endswith(".md"):
                    continue
                fp = os.path.join(dirpath, fn)
                try:
                    text = open(fp, encoding="utf-8", errors="ignore").read()
                except OSError:
                    continue
                for name in TARGETS:
                    if name in text:
                        rel = os.path.relpath(fp, root)
                        if rel not in hits:
                            hits.append(rel)
                        break
    for h in sorted(hits):
        print(f"  {h}")
    if hits:
        print("\n  这些是流水线 skill 的内部叙述。流水线已暂停，保留原文不影响日常交互；")
        print("  若日后复活流水线，需把其中的指向改为 `grilling`（本目录 README 记录了该待办）。")
    else:
        print("  （无）")

    print(f"\n完成：{len(moves)} 个目录已移入 {trash}")
    print(f"回滚：mv {trash}/<目录> 回原位（文件名前缀标了 chezmoi源- / 运行副本-）")
    return 0


if __name__ == "__main__":
    sys.exit(main())
