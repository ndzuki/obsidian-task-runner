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
 * - 同一天只触发一次（状态文件 ~/.local/share/otg/kb-distill-state.json）
 * - 兜底：session_stop 的 continue 有 8 次上限且仅主会话触发
 */
import type { ExtensionAPI } from "@oh-my-pi/pi-coding-agent";
import { readFileSync, writeFileSync, mkdirSync } from "node:fs";
import { homedir } from "node:os";
import { join, dirname } from "node:path";

const DISTILL_MARK = "com.otg.kb-distilled";

function distillStatePath(): string {
  return join(homedir(), ".local", "share", "otg", "kb-distill-state.json");
}

// lastDistillDate reads the persisted date of the last triggered distillation
// ("" when never / unreadable).
function lastDistillDate(): string {
  try {
    const raw = readFileSync(distillStatePath(), "utf8");
    return (JSON.parse(raw) as { date?: string }).date ?? "";
  } catch {
    return "";
  }
}

function markDistilled(date: string): void {
  try {
    const p = distillStatePath();
    mkdirSync(dirname(p), { recursive: true });
    writeFileSync(p, JSON.stringify({ date }), "utf8");
  } catch {
    // state persistence is best-effort; the in-session mark still dedupes
  }
}

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

    // 同日已触发过（跨会话）→ 跳过。
    const today = new Date().toISOString().slice(0, 10);
    if (lastDistillDate() === today) return;

    // 持久化标记（会话内 + 跨会话）后再 continue。
    pi.appendEntry(DISTILL_MARK, { date: today });
    markDistilled(today);

    return {
      continue: true,
      additionalContext:
        "[会话知识提炼] 本次交互会话已结束。请委派 subagent 读取本会话转录（history://），" +
        "分析其中的踩坑经验（现象/失败方案/根因/成功方案）、验证结论（实测数据）与架构决策；" +
        "有可复用知识则按 knowledge-base Step 0.7 流程入库（踩坑用 `otg kb absorb`，新主题新建 References 文档 + `otg kb rebuild-index`）；" +
        "无可复用知识则回复「无可提炼」并结束。完成后正常结束，不要继续无关工作。",
    };
  });
}
