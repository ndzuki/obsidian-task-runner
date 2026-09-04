# 贡献指南

欢迎 issue / PR / 讨论。这个仓库本身由本项目流水线自动开发（自举），
你的 PR 会走同一条：需求 → 计划 → 实现 → 独立审计 → 合并。

## 开发环境

- Go 1.24+
- `make build`：构建 `otg` 与 `kitty-grill`（注意知识库需要 `-tags sqlite_fts5`）
- `make test`：Go 全量测试（`-tags sqlite_fts5`）+ agent-server/kb-preflight 的 node 单测

## 测试门禁

```bash
make test        # 全量：Go + node
make test-cover  # 带覆盖率
```

要点：

1. **对齐测试**（`pkg/yamlfrontmatter/frontmatter_alignment_test.go`）：reference.md
   字段附录、TASK 模板、taskFieldOrder 三者必须一致；新增/删除 frontmatter 字段
   必须同步三处，否则 CI 失败。
2. **契约测试**：agent-server RPC 契约（`/agents` `/agent/run` `/agent/chat`）
   的字段形状在 `deploy/dsh-plugins/*.test.mjs` 与 daemon 侧镜像结构中双向钉住。
3. 写操作测试一律 mock，不触碰真实 vault 与远端。

## Skill 约定

`obsidian-task-runner/skills/` 是随包安装的阶段 Skill（refining / round1 / round2 /
merge / conventions / priority / pm / split / design）。改动约定：

- `name` + `description` 必须写清触发条件与输入输出契约；
- 引用外部 Skill 必须在 `config/skill-registry.json` 登记（bundled 或 external required）；
- 阶段会话的硬性契约（环境清理、禁停用户服务等）在 SKILL.md 内显式声明。

## 提交与发布

- 提交信息用 conventional commits（feat/fix/docs/chore/refactor + scope）；
- 发版打 annotated tag（`vX.Y.Z`），CHANGELOG 从 tag 生成；
- 破坏性变更必须在 REQ 侧标注 breaking 并附带迁移说明。

## 文档口径（写给所有贡献者）

- 公开文档（README/docs/CHANGELOG/ROADMAP/CONTRIBUTING）**不出现内部语境**
  （任务编号、私有故障记录、个人环境路径）；
- 配置与字段的单一事实源是 `docs/config-reference.md` 与 `reference.md` §4.9，
  改代码必须同步文档，且由对齐测试拦截漂移。
