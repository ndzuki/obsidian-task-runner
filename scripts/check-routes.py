#!/usr/bin/env python3
"""ROUTES.md 体积门禁（M11 其二）。

为什么需要它
------------
`project-context.mjs` 只把每个项目的 `Notes/ROUTES.md` 当作**常驻指针索引**注入，
且有一条硬上限 `PROJECT_ROUTES_MAX`（UTF-8 字节）。超限时 `routesDigest()` 会**按行边界
截断**并附一句提示——即**超出的指针会从注入面消失**，而 agent 不再知道那些笔记的存在。

实测教训（2026-09-25）：`001-release-manager/Notes/ROUTES.md` 被增补到 2779 B，
在当时的 2048 B 上限下，`## 基线` 三件套（CONTEXT.md / PROJECT-CONVENTIONS.md /
ADR-INDEX.md）被**整段吞掉**——正是本项目最核心的三条常驻指针。事后才发现。

所以本门禁的判据是：**每份 ROUTES.md 的字节数 ≤ 插件当前的 PROJECT_ROUTES_MAX**。

单一事实源
----------
上限**不在这里硬编码**——它从 `project-context.mjs` 里读出来。插件改上限，这里自动跟随，
不会出现「门禁说 2048、插件实际 4096」的漂移。

用法
----
    make check-routes                 # 检查真实 vault（vault-map 的 obsidian_vault）
    python3 scripts/check-routes.py --vault /tmp/x   # 检查指定 vault（供测试/演示）
    python3 scripts/check-routes.py --limit 100      # 指定上限（供测试/演示）

退出码：0 = 全部合规；1 = 有文件超限（或找不到上限/vault）；2 = 参数错误。
"""

from __future__ import annotations

import argparse
import json
import os
import re
import sys
from glob import glob

HOME = os.path.expanduser("~")
DEFAULT_PLUGIN = os.path.join(HOME, ".dsh", "plugins", "project-context.mjs")
DEFAULT_MAP = os.path.join(HOME, ".dsh", "config", "vault-map.json")


def read_limit(plugin_path: str) -> int:
    """从插件源码读出 PROJECT_ROUTES_MAX —— 上限的单一事实源。"""
    try:
        src = open(plugin_path, encoding="utf-8").read()
    except OSError as e:
        sys.exit(f"[check-routes] 读不到插件源码 {plugin_path}: {e}")
    m = re.search(r"^\s*const\s+PROJECT_ROUTES_MAX\s*=\s*(\d+)", src, re.M)
    if not m:
        sys.exit(f"[check-routes] 在 {plugin_path} 里找不到 `const PROJECT_ROUTES_MAX = <数字>`")
    return int(m.group(1))


# 条目数上限（DESIGN.md §2.4「其它硬约束」：每项目条目 **20 行**）。
# 2026-09-25 同步：此前它只是 DESIGN 里的**文字约定**、无任何可执行强制 —— 属「两套说法」隐患，
# 现将同一数值落成真门禁（本脚本）。数值本身仍以 DESIGN §2.4 为准，改 DESIGN 时同步改这里。
MAX_ENTRIES = 20
# ⚠️ 已知到顶：**001-release-manager 已达 20 行**（2026-09-25：17 表格数据行 + 3 基线项；
#   3,033 B / 4,096 B ⇒ **行先到顶**）。向 001 新增条目**必须先合并**（DESIGN §2.4 取舍顺序），
#   否则本门禁会因行数失败。
# ⚠️ 另一条独立约束（本脚本**查不到**）：`assembleParts` 的段预算 =
#   `PROJECT_CONTEXT_MAX − reserved(路由表段) − 300` ⇒ 路由表越长，其余段可用预算越小。
#   实测 001 曾因 ROUTES 3,033 B 把 `## Language / 术语` 挤掉（已由裁决把上限提到 5400 修复）。
#   ⇒ **动过 ROUTES 体积后要另外复测「有无段被丢弃」**，别以为本脚本 PASS 就万事大吉。

# 条目计数口径（必须唯一，否则又会出现「两套说法」）：
#   = 「表格数据行」+「`## 基线` 段下的列表项」
#   · 表格数据行 = 以 `|` 开头、**排除**分隔行（`|---|---|`）与表头行（`| 触发 | 读 |` 等）
#   · `## 基线` 段下的 `- ` 列表项计入（DESIGN §2.4 规则 6：绝不删基线行）
#   · 其它散落列表项不计（本项目现有 ROUTES 中为 0）
# 实测锚点（2026-09-25 H-6）：001 = 16 表格数据行 + 3 基线项 = **20**（恰好到顶）
TABLE_HEADER_RE = re.compile(r"^\|\s*(触发|路径 glob|关键词|信号)\s*\|")
TABLE_SEP_RE = re.compile(r"^\|[\s\-:|]+\|$")


def count_entries(path: str) -> tuple[int, int, int]:
    """返回 (表格数据行, 基线列表项, 合计条目)。"""
    lines = open(path, encoding="utf-8").read().split("\n")
    table = 0
    for line in lines:
        if not line.startswith("|"):
            continue
        if TABLE_SEP_RE.match(line) or TABLE_HEADER_RE.match(line):
            continue
        table += 1
    base = 0
    in_base = False
    for line in lines:
        if line.startswith("## "):
            in_base = line.startswith("## 基线")
        elif in_base and line.startswith("- "):
            base += 1
    return table, base, table + base



def read_vault(map_path: str) -> str:
    v = os.environ.get("OBSIDIAN_VAULT", "")
    if v:
        return v
    try:
        cfg = json.load(open(map_path, encoding="utf-8"))
    except OSError as e:
        sys.exit(f"[check-routes] 读不到 vault-map {map_path}: {e}")
    return cfg.get("obsidian_vault") or ""


def main() -> int:
    ap = argparse.ArgumentParser(description="ROUTES.md 体积门禁")
    ap.add_argument("--plugin", default=DEFAULT_PLUGIN, help="project-context.mjs 路径（上限来源）")
    ap.add_argument("--map", default=DEFAULT_MAP, help="vault-map.json 路径")
    ap.add_argument("--vault", default="", help="直接指定 vault 根（覆盖 vault-map）")
    ap.add_argument("--limit", type=int, default=0, help="直接指定上限（覆盖插件读取）")
    args = ap.parse_args()

    limit = args.limit or read_limit(args.plugin)
    vault = args.vault or read_vault(args.map)
    if not vault:
        sys.exit("[check-routes] 无法确定 vault 根（vault-map 无 obsidian_vault，且未传 --vault）")
    if not os.path.isdir(vault):
        sys.exit(f"[check-routes] vault 不存在: {vault}")

    files = sorted(glob(os.path.join(vault, "Projects", "*", "Notes", "ROUTES.md")))
    if not files:
        print(f"[check-routes] 未在 {vault}/Projects/*/Notes/ 找到任何 ROUTES.md —— 无对象可查")
        return 0

    width = max(len(os.path.basename(os.path.dirname(os.path.dirname(f)))) for f in files)
    over = []
    too_many = []
    print(f"[check-routes] 上限 PROJECT_ROUTES_MAX = {limit} B（来自 {args.plugin}）"
          f" · 条目上限 {MAX_ENTRIES} 行（DESIGN §2.4）")
    for f in files:
        proj = os.path.basename(os.path.dirname(os.path.dirname(f)))
        size = os.path.getsize(f)
        tbl, base, total = count_entries(f)
        bflag = "OK" if size <= limit else "OVER"
        eflag = "OK" if total <= MAX_ENTRIES else "OVER"
        if size > limit:
            over.append((proj, size, f))
        if total > MAX_ENTRIES:
            too_many.append((proj, total, tbl, base, f))
        print(f"  {proj:<{width}}  {size:>6} B {bflag:<4} 条目 {total:>3} 行 ({tbl} 表 + {base} 基线) {eflag}")

    if over:
        print(f"\n[check-routes] ✗ 有 {len(over)} 份 ROUTES.md 超**字节**上限 —— 超出的指针会被**静默截断**"
              f"（`routesDigest` 按行边界截断，agent 再也看不到被截掉的那些笔记）：")
        for proj, size, f in over:
            print(f"    {proj}: {size} B > {limit} B  (超出 {size - limit} B)  {f}")
        print("  修法：压缩该 ROUTES.md（合并过长条目/把低频触发项移到正文笔记），或经裁决上调上限。")
    if too_many:
        print(f"\n[check-routes] ✗ 有 {len(too_many)} 份 ROUTES.md 超**条目**上限 {MAX_ENTRIES} 行"
              f"（DESIGN §2.4：超出说明该项目的笔记没做减法，应回 §4 重判并合并/下沉低价值条目）：")
        for proj, total, tbl, base, f in too_many:
            print(f"    {proj}: {total} 行（{tbl} 表格数据行 + {base} 基线项）> {MAX_ENTRIES} 行  {f}")
        print("  修法：按 §2.4 取舍顺序合并同笔记行 / 下沉「能从代码几分钟读出」的条目，"
              "**基线两行永不删**。")
    if over or too_many:
        return 1

    print(f"\n[check-routes] ✓ {len(files)} 份 ROUTES.md 全部 ≤ {limit} B 且 ≤ {MAX_ENTRIES} 行")
    return 0


if __name__ == "__main__":
    sys.exit(main())
