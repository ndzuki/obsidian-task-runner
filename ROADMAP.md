# Roadmap

> 现状：核心流水线（v0.49）已完整跑通自己的开发全流程（本仓库的 89 个 tag
> 即历史证据）。以下为公开路线，欢迎 issue 提案与 PR。

## 近期（进行中）

| 项 | 说明 |
| --- | --- |
| 文档门面 | README 重写、quickstart/CHANGELOG/ROADMAP/CONTRIBUTING 补齐（本次） |
| 历史文档归档 | 五篇迁移期架构文档合并为单一现势 `docs/architecture.md`，旧文入 `docs/archive/` |

## 计划（欢迎认领）

| 项 | 说明 |
| --- | --- |
| **Agent Monitor TUI**（dshtui 子命令） | 把 Agent Town 搬到 Kitty tab，Rust TUI 渲染（ratatui + kitty 像素协议），快捷键随时调出，低内存（<20MB）。数据源 `/agents` 契约不变，零后端改动。需求草案在 dshtui 项目 REQ-009 |
| 多平台 | 非 systemd 环境（macOS launchd / Windows）的守护进程方案 |
| 更多模型渠道 | 社区驱动的 provider 配置与测试 |
| 配置 UI | `otg config` 的交互式向导（或 Web 面板配置页） |
| 国际化 | 文档与通知文案英文版 |

## 原则

- 数据契约（`/agents`、frontmatter schema、vault-map）向后兼容；破坏性变更走 breaking 标注与迁移
- 所有新功能默认 off（opt-in），不改变开箱行为
- 每个功能带对齐测试（文档 ↔ 代码 1:1），漂移在 CI 被拦截
