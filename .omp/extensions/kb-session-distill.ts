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
 */
import type { ExtensionAPI } from "@oh-my-pi/pi-coding-agent";

const DISTILL_MARK = "com.otg.kb-distilled";

export default function (pi: ExtensionAPI) {
  pi.setLabel("KB Session Distill");

  pi.on("session_stop", async (_event, ctx) => {
    const branch = ctx.sessionManager.getBranch();
    if (!branch || branch.length < 12) return; // 太短，无提炼价值

    // 同会话已触发过提炼（含上一轮 continue 后再 stop）→ 跳过。
    for (const entry of branch) {
      const custom = entry as { customType?: string };
      if (custom?.customType === DISTILL_MARK) return;
    }

    // 有实质工作：统计用户侧消息（entry 结构因版本而异，宽松匹配多种形态；
    // 误判最坏情况 = 多跑一轮判空提炼，成本可控）。
    let userMsgs = 0;
    for (const entry of branch) {
      const e = entry as {
        type?: string;
        role?: string;
        message?: { role?: string };
      };
      if (e?.role === "user" || e?.type === "user" || e?.type === "message" && e?.message?.role === "user") {
        userMsgs++;
        if (userMsgs >= 3) break;
      }
    }
    if (userMsgs < 3) return;

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
