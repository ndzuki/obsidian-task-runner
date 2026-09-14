#!/usr/bin/env python3
"""修掉 obsidian-task-runner 停用后遗留在通用 skill 里的跨技能死引用。

背景：`tdd` / `grilling` / `diagnosing-bugs` / `knowledge-base` 四个日常 skill 里
有若干段落引用已停用的任务流水线（`requirement-elaborator` 的 `grill_owner` CAS 锁、
daemon 自动触发、`task-verifier` 门禁）。流水线停掉后这些引用要么指向废弃命令，
要么让 agent 误以为有后台进程在跑。

为什么是脚本而不是手改：

- **要改两份**：这四个 skill 由 chezmoi 纳管，源在
  `~/.local/share/chezmoi/dot_dsh/skills/`，运行副本在 `~/.dsh/skills/`。
  只改一份会被反向收编覆盖回旧内容。本脚本**双写**并事后校验两份一致。
- **内容寻址**：每处改动声明一对 `before`/`after` 状态串（各自唯一），据此判定
  当前处于旧态还是新态；旧态才动手，新态跳过，两者都不是就报错退出。
  锚点不唯一或对不上时**跳过整个文件**，不做"尽力而为"的部分替换
  （AGENTS.md 禁止 sed 就地改，同一道理）。
- **可恢复**：备份写到 `~/.dsh/trash/local-skill-fixes-<时间戳>/`，
  **不写进 chezmoi 源目录**（源目录里的 `.bak` 会被 chezmoi 当受管文件收编）。
- **幂等**：重复执行只报告「已应用」。

用法：
    python3 apply-local-skill-fixes.py --dry-run     # 只看会改什么
    python3 apply-local-skill-fixes.py               # 应用
    python3 apply-local-skill-fixes.py --skills-dir /tmp/x/dot_dsh/skills \
        --runtime-dir /tmp/x/dsh-skills              # 演练（不动真实环境）
"""

from __future__ import annotations

import argparse
import datetime
import io
import os
import shutil
import sys

# ---------------------------------------------------------------- 改动清单
# 每处改动三个要素：
#   before / after : 各须在文件中唯一；before 命中 = 旧态，after 命中 = 新态
#   old/new        : 子串精确替换（old 须唯一）
#   start/end/new  : 区间替换（start 到 end 之前；两锚点各须唯一）
EDITS: list[dict] = [
    # ============ tdd：Seams 节引用流水线 Round 1 / Round 2 + grilling 阻塞流
    dict(
        path="tdd/SKILL.md",
        before="在 obsidian-task-runner 流程中，Round 1 计划",
        after="**写测试前先写下被测 seam 并确认**",
        start="## Seams — 测试打在哪（预确认制）",
        end="## Anti-patterns",
        new="""## Seams — 测试打在哪（预确认制）

**Seam** = 观察行为的公共边界：HTTP handler、Repository/Service 接口、纯函数层。
**写测试前先写下被测 seam 并确认**——"测试打在哪层？" 回答不了，说明还没想清被测契约。
已有计划时（如 `skill://risk-aware-planning` 产出的计划的「测试 Seam」行），按计划声明的 seam 执行：

1. **只测已确认的 seam**：行为必须通过该层公共接口表达。测不到 → 不是换内部测，
   而是架构信号（seam 放错层或 Step 边界错误）。
2. **计划外 seam 必须上报**：需要测内部/私有方法、或新增 mock 边界才能表达
   行为 → 停下来与用户对齐，不静默绕过。
3. **修 bug 也一样**：先写 seam 再写测试——这行字是一次性思考工具，
   想清楚了就可以删掉。

参考：`tests.md`（好/坏测试 Go 示例）、`mocking.md`（mock 边界 Go 示例）。

""",
    ),
    # ============ grilling：删掉 grill_owner CAS 锁（停用流水线的专属协议）
    dict(
        path="grilling/SKILL.md",
        before="> **并发控制**",
        after="---\n\n# Grilling — Relentless Alignment Interview",
        old="> **并发控制**: 本 skill 自身不管理并发——互斥由调用方（`requirement-elaborator`）"
        "通过 TASK frontmatter 的 `grill_owner` / `grill_started_at` / `grill_timeout_minutes` "
        "字段实现 CAS 所有权协议。你不需要关心锁——只追问。\n\n",
        new="",
    ),
    # ============ grilling：调用方举例去掉已停用技能
    dict(
        path="grilling/SKILL.md",
        before="requirement-elaborator, triage, code review",
        after="a planning, design, or review skill",
        old="(e.g. requirement-elaborator, triage, code review)",
        new="(e.g. a planning, design, or review skill)",
    ),
    # ============ diagnosing-bugs：description 指向 requirement-elaborator
    dict(
        path="diagnosing-bugs/SKILL.md",
        before="use requirement-elaborator instead.",
        after="use grilling instead.",
        old="NOT for requirement gaps — use requirement-elaborator instead.",
        new="NOT for requirement gaps — use grilling instead.",
    ),
    # ============ diagnosing-bugs：正文指向 requirement-elaborator
    dict(
        path="diagnosing-bugs/SKILL.md",
        before="use `skill://requirement-elaborator` instead.",
        after="use `skill://grilling` instead.",
        old="use `skill://requirement-elaborator` instead.",
        new="use `skill://grilling` instead.",
    ),
    # ============ diagnosing-bugs：适用门里的 Task-runner 语境
    dict(
        path="diagnosing-bugs/SKILL.md",
        before="- Task-runner Round 2 pause: 阻塞类型",
        after="- 阻塞类型 =「代码逻辑错误」",
        old="- Task-runner Round 2 pause: 阻塞类型 =「代码逻辑错误」(root cause is NOT a requirement gap)",
        new="- 阻塞类型 =「代码逻辑错误」(root cause is NOT a requirement gap)",
    ),
    # ============ knowledge-base：加「运行前提」，标出哪些段落需要 daemon
    dict(
        path="knowledge-base/SKILL.md",
        before="工程实践资产。\n\n## 双向知识流",
        after="工程实践资产。\n\n## 运行前提（先读）",
        old="工程实践资产。\n\n## 双向知识流",
        new="""工程实践资产。

## 运行前提（先读）

本 skill 分两半，**可用性不同**：

- **检索半**（Step 1 及检索类步骤）：**任何会话都能用**，不依赖后台进程。
  `otg kb search` 或直接读 `References/` 均可。故障排查：若报
  `enable WAL: unable to open database file`，那是 home 只读（沙箱）导致的写失败，
  **不是知识库损坏**——把 `kb.sqlite` 拷到可写路径，再用 `--db` 指过去即可正常检索。
- **沉淀半**（Step 0 / Step 6 的自动机制）：由 otg daemon 与 agent-server 实现。
  **daemon 未运行时这些自动触发不会发生**，需走手动路径：踩坑 → `otg kb absorb`；
  结论/架构决策 → 追加对应文档的「实践经验」小节；热度 → `otg kb hit`。

下文凡标 **〔需 daemon〕** 的段落都属第二类。

## 双向知识流""",
    ),
    dict(
        path="knowledge-base/SKILL.md",
        before="`otg kb search`。\n\n- 技术名词",
        after="**〔需 daemon〕** 上两段注入由 daemon 自管的 agent-server 完成",
        old="`otg kb search`。\n\n",
        new="`otg kb search`。\n"
        "> **〔需 daemon〕** 上两段注入由 daemon 自管的 agent-server 完成；"
        "daemon 未运行时注入不发生，靠 Step 1 的人工检索路径。\n\n",
    ),
    dict(
        path="knowledge-base/SKILL.md",
        before="> **触发方**：daemon MUST invoke this skill on Merge 成功",
        after="**这条路径不依赖 daemon**",
        old='> **触发方**：daemon MUST invoke this skill on Merge 成功（PR 合入后状态转 `done`）。'
        'Agent MAY also execute on user request ("沉淀项目经验").',
        new='> **触发方**：**〔需 daemon〕** 流水线入口——daemon 在 Merge 成功'
        '（PR 合入后状态转 `done`）时调用本 skill。Agent 也可在用户要求时执行'
        '（"沉淀项目经验"），**这条路径不依赖 daemon**。',
    ),
    dict(
        path="knowledge-base/SKILL.md",
        before="**自动机制（daemon 代码实现，零人工）**：\n\n- **按任务提取**",
        after="以下四条由 daemon 在 merge 后自动执行",
        old="**自动机制（daemon 代码实现，零人工）**：\n\n- **按任务提取**",
        new="**自动机制（daemon 代码实现，零人工）**：\n\n"
        "> **〔需 daemon〕** 以下四条由 daemon 在 merge 后自动执行；daemon 未运行时"
        "改走「运行前提」里的手动路径。\n\n- **按任务提取**",
    ),
    dict(
        path="knowledge-base/SKILL.md",
        before="**触发时机**：\n",
        after="**触发时机**（第 1 条 **〔需 daemon〕**）：",
        old="**触发时机**：",
        new="**触发时机**（第 1 条 **〔需 daemon〕**）：",
    ),
    dict(
        path="knowledge-base/SKILL.md",
        before="- Round 2 验收通过（task-verifier 全部 AC PASS）后",
        after="daemon 未运行时，由用户显式",
        old="- Round 2 验收通过（task-verifier 全部 AC PASS）后，daemon 调用本 Skill "
        "扫描 TASK `## 验收记录`。",
        new="- **〔需 daemon〕** Round 2 验收通过（task-verifier 全部 AC PASS）后，"
        "daemon 调用本 Skill 扫描 TASK `## 验收记录`。daemon 未运行时，由用户显式"
        '要求"标记已验证"，并人工核对验收记录后再翻转。',
    ),
]


class Refuse(Exception):
    """任何"说不清当前状态"的情况都抛这个——报错而不是猜。"""


def state(text: str, e: dict) -> str:
    """返回 'before' / 'after' / 抛 Refuse。"""
    b, a = e["before"] in text, e["after"] in text
    if b and not a:
        return "before"
    if a and not b:
        return "after"
    if b and a:
        raise Refuse(
            f"before 与 after 同时命中（状态串选得不够独特）: "
            f"before={e['before'][:40]!r} after={e['after'][:40]!r}"
        )
    raise Refuse(
        f"before 与 after 都不在文件里（文件已被改过，或本编辑不适用于此版本）: "
        f"before={e['before'][:40]!r}"
    )


def transform(text: str, e: dict) -> str:
    if "old" in e:
        n = text.count(e["old"])
        if n != 1:
            raise Refuse(f"old 锚点出现 {n} 次（期望 1 次）: {e['old'][:60]!r}")
        return text.replace(e["old"], e["new"], 1)
    start, end = e["start"], e["end"]
    for label, anchor in (("start", start), ("end", end)):
        if text.count(anchor) != 1:
            raise Refuse(f"{label} 锚点出现 {text.count(anchor)} 次: {anchor!r}")
    i = text.index(start)
    j = text.index(end, i)
    return text[:i] + e["new"] + text[j:]


def validate_spec() -> None:
    """静态自检：before 不能出现在本次改动产出的新文本里。

    否则改完之后 before 与 after 同时命中，state() 永远判不出新态——幂等失效。
    （这是实测踩到的坑：before 选成了"新文本会原样保留的那句话"。）

    纯删除（new 为空）时 after 是**接缝串**（如 '---\\n\\n# 标题'），跨越被删区间
    之外，不可能出现在 new 里——这类跳过 after 检查，其正确性由运行期 state() 验证。
    """
    for e in EDITS:
        if e["before"] in e["new"]:
            raise SystemExit(
                f"改动清单自检失败：{e['path']} 的 before 出现在 new 中——"
                f"状态串无法区分改前改后。before={e['before'][:50]!r}"
            )
        if e["new"] and e["after"] not in e["new"]:
            raise SystemExit(
                f"改动清单自检失败：{e['path']} 的 after 不在 new 中——"
                f"应用后状态串不会出现。after={e['after'][:50]!r}"
            )


def main() -> int:
    home = os.path.expanduser("~")
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--skills-dir", default=f"{home}/.local/share/chezmoi/dot_dsh/skills",
                    help="chezmoi 源目录（事实源）")
    ap.add_argument("--runtime-dir", default=f"{home}/.dsh/skills",
                    help="运行副本目录（双写，防止反向收编回滚）")
    ap.add_argument("--backup-dir", default=f"{home}/.dsh/trash",
                    help="备份根目录（不要放在 chezmoi 源里，否则会被收编）")
    ap.add_argument("--dry-run", action="store_true")
    ap.add_argument("--no-runtime", action="store_true", help="只改源，不写运行副本")
    args = ap.parse_args()

    validate_spec()

    by_file: dict[str, list[dict]] = {}
    for e in EDITS:
        by_file.setdefault(e["path"], []).append(e)

    stamp = datetime.datetime.now().strftime("%Y%m%d-%H%M%S")
    backup_root = os.path.join(args.backup_dir, f"local-skill-fixes-{stamp}")

    changed: list[str] = []
    skipped: list[str] = []
    failed: list[str] = []

    for path, edits in sorted(by_file.items()):
        src = os.path.join(args.skills_dir, path)
        if not os.path.isfile(src):
            failed.append(f"{path}: 源文件不存在 {src}")
            continue

        text = io.open(src, encoding="utf-8").read()
        try:
            todo = [e for e in edits if state(text, e) == "before"]
        except Refuse as exc:
            failed.append(f"{path}: {exc} —— 该文件未改动")
            continue

        if not todo:
            skipped.append(f"{path}: {len(edits)} 处均已是目标状态")
            continue

        try:
            for e in todo:
                text = transform(text, e)
        except Refuse as exc:
            failed.append(f"{path}: {exc} —— 该文件未改动")
            continue

        print(f"  {path}: 待应用 {len(todo)}/{len(edits)} 处")
        if args.dry_run:
            changed.append(path)
            continue

        bdir = os.path.join(backup_root, os.path.dirname(path))
        os.makedirs(bdir, exist_ok=True)
        shutil.copy2(src, os.path.join(bdir, "SKILL.md.source"))

        with io.open(src, "w", encoding="utf-8") as fh:
            fh.write(text)

        if args.no_runtime:
            print("      仅改源（--no-runtime）")
        else:
            dst = os.path.join(args.runtime_dir, path)
            if os.path.isfile(dst):
                shutil.copy2(dst, os.path.join(bdir, "SKILL.md.runtime"))
                with io.open(dst, "w", encoding="utf-8") as fh:
                    fh.write(text)
                if io.open(src, encoding="utf-8").read() != io.open(dst, encoding="utf-8").read():
                    failed.append(f"{path}: 双写后源与运行副本不一致，请人工核对")
                    continue
                print("      源 + 运行副本已双写并校验一致")
            else:
                print(f"      ⚠ 运行副本缺失，仅改源：{dst}")
        changed.append(path)

    print()
    for line in skipped:
        print(f"  已应用/跳过: {line}")
    for line in failed:
        print(f"  失败: {line}")
    print()
    if args.dry_run:
        print(f"[dry-run] {len(changed)} 个文件将有改动；未写盘。")
        return 1 if failed else 0
    if changed:
        print(f"完成：{len(changed)} 个文件已修改，备份在 {backup_root}")
        print("下一步：检查 diff → 在 chezmoi 源仓库提交 →（可选）chezmoi apply")
    elif not failed:
        print("无需改动：全部改动已是目标状态。")
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
