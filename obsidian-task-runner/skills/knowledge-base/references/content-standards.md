# 内容规范：文档体裁与入库质量标准

> `knowledge-base` 的按需参考（2026-09-18 从 SKILL.md 拆出）。需要时再 `read`，不要预加载。

---

## 内容规范

### 文档体裁

| 体裁 | 用途 | 结构要求 | 示例 |
|---|---|---|---|
| **参考手册** (reference) | 完整 API/命令/配置参数查阅 | TOC → 分类章节 → 速查表 | Connect-Go 完整手册 |
| **实战指南** (advanced) | 从零到一的项目级教程 | 背景→环境→步骤→验证→踩坑 | KEDA 完全指南 |
| **概念精讲** (intermediate) | 单一技术点深度讲解 | 痛点→原理→示例→对比→最佳实践 | Go 核心设计哲学 |
| **快速入门** (beginner) | 15 分钟上手 | 安装→最小示例→核心概念→下一步 | Docker CLI 完全参考 |

体裁在 frontmatter 的 `level` 字段体现：`reference` / `advanced` / `intermediate` / `beginner`。

