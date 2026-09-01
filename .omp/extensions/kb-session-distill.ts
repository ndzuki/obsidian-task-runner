/**
 * KB Session Distill — 会话结束知识提炼扩展
 *
 * 监听 session_stop（主交互会话停止钩子；task/subagent 会话不触发），
 * 会话有实质工作时注入提炼指令，让 agent 委派 subagent 分析本次会话
 * 转录并把可复用经验（踩坑/验证结论/架构决策）录入知识库
 * （knowledge-base SKILL.md Step 0.7）。
 *
 * 安装：复制到 ~/.omp/agent/extensions/（用户级，全项目生效）
 * 或 <repo>/.omp/extensions/（项目级自动发现）。
 * 禁用：config.yml 加 disabledExtensions: [extension-module:kb-session-distill]
 *
 * 频率控制（防退出延迟与重复提炼）：
 * - 同会话只触发一次（session entry 内写 custom 标记，branch 可查）
 * - 每个有实质工作的主会话都触发一次——不再做「同日一次」的跨会话去重：
 *   用户多轮 /new 会话各自沉淀（TASK-071 教训：同日只触发一次导致后续会话
 *   的修复经验全部丢失）；重复内容由 `otg kb absorb` 内置归一化去重兜底，
 *   INDEX 不会膨胀。
 * - 兜底：session_stop 的 continue 有 8 次上限且仅主会话触发
 *
 * D2 预过滤（省「无可提炼」判空成本）：触发前提增加「有工作证据」——
 * 会话出现工具调用（tool/call 事件 / tool_use content 块 / 结构化字段探针）
 * 才值得提炼；纯聊天的 ≥3 条消息会话不再白跑一轮 subagent 判空。
 * 误判方向保守：探针只会把结构化字段里恰好以 `"bash"` 等独立字符串出现
 * 的内容误判为「有工作」→ 多跑一轮判空，成本可控，不漏真实经验。
 */
import type { ExtensionAPI } from "@oh-my-pi/pi-coding-agent";

export const DISTILL_MARK = "com.otg.kb-distilled";

/** 统计 branch 中的用户侧消息数（entry 结构因版本而异，宽松匹配多种形态）。 */
export function countUserMessages(branch: unknown[]): number {
  let userMsgs = 0;
  for (const entry of branch) {
    const e = entry as {
      type?: string;
      role?: string;
      message?: { role?: string };
    };
    if (
      e?.role === "user" ||
      e?.type === "user" ||
      (e?.type === "message" && e?.message?.role === "user")
    ) {
      userMsgs++;
      if (userMsgs >= 3) break;
    }
  }
  return userMsgs;
}

/** 工作证据探测：会话里出现过工具调用才算有实质工作。
 *  - 结构直测：tool/call 事件、assistant 消息里的 tool_use/tool_call/tool_result 块；
 *  - 兜底探针：整个 entry 序列化后匹配工具名——工具名在结构化字段里以
 *    独立字符串出现才命中（闲聊文本里的普通单词不会以 `"bash"` 形式出现）。 */
export function hasWorkEvidence(branch: unknown[]): boolean {
  for (const entry of branch) {
    const e = entry as { type?: string; message?: { content?: unknown[] } };
    if (e?.type === "tool/call" || e?.type === "tool_call" || e?.type === "tool_use") return true;
    if (Array.isArray(e?.message?.content)) {
      for (const b of e.message.content) {
        const bt = (b as { type?: string })?.type;
        if (bt === "tool_use" || bt === "tool_call" || bt === "tool_result") return true;
      }
    }
  }
  for (const entry of branch) {
    const s = JSON.stringify(entry ?? {});
    if (
      s.includes("tool/call") ||
      s.includes("tool_use") ||
      s.includes('"bash"') ||
      s.includes('"edit"') ||
      s.includes('"write"') ||
      s.includes("str_replace_editor") ||
      s.includes("kb absorb")
    ) {
      return true;
    }
  }
  return false;
}

/** 提炼触发判定（纯函数，可单测）：
 *  branch 长度 ≥ minBranchLen → 无已提炼标记 → 用户消息 ≥ minUserMsgs
 *  → 有工作证据（D2）。任何一项不满足即跳过。 */
export function shouldDistill(
  branch: unknown[],
  opts?: { minBranchLen?: number; minUserMsgs?: number; mark?: string }
): boolean {
  const entries = Array.isArray(branch) ? branch : [];
  const mark = opts?.mark ?? DISTILL_MARK;
  if (entries.length < (opts?.minBranchLen ?? 12)) return false;
  for (const entry of entries) {
    const custom = entry as { customType?: string };
    if (custom?.customType === mark) return false;
  }
  if (countUserMessages(entries) < (opts?.minUserMsgs ?? 3)) return false;
  return hasWorkEvidence(entries);
}

export default function (pi: ExtensionAPI) {
  pi.setLabel("KB Session Distill");

  pi.on("session_stop", async (_event, ctx) => {
    const branch = ctx.sessionManager.getBranch();
    if (!shouldDistill(branch)) return;

    // 会话内标记（不写跨会话状态文件——每个会话独立沉淀）。
    pi.appendEntry(DISTILL_MARK, { at: new Date().toISOString() });

    return {
      continue: true,
      additionalContext:
        "[会话知识提炼] 本次交互会话已结束。请按 knowledge-base SKILL.md Step 0.7 执行：\n" +
        "1. 委派 subagent（task）读取本会话转录（history://<id>），分析踩坑经验" +
        "（现象/失败方案/根因/成功方案）、验证结论（实测数据）与架构决策；\n" +
        "2. 入库前先 `otg kb search` 检索知识库去重——已有文档则追加小节/更新记录，" +
        "新主题才新建 References 文档（标准 6 字段 frontmatter）；踩坑经验统一用" +
        " `otg kb absorb`（内置归一化去重）；\n" +
        "3. 无可复用知识则回复「无可提炼」并结束，不硬造知识。\n" +
        "完成后正常结束，不要继续无关工作。",
    };
  });
}
