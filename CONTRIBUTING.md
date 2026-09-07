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

## 用户资产所有权（强制约束）

> 这两条是硬约束，违反的 PR 不合并。

1. **仓库不带个性化定制**：模型路由/网关地址/时区偏好/常驻服务名/个人路径一律
   不进仓库。任何「对我好用」的默认值必须先评估「对陌生使用者是否成立」——不成立
   就做成 opt-in 配置，并在 `docs/config-reference.md` 与 quickstart 告知使用者。
2. **自动化命令绝不覆盖用户配置与环境**：任何脚本/命令只能写「项目自有资产」，
   对「用户资产」只读或只补缺失键。

资产分级与行为表：

| 资产 | 示例 | 自动化行为 |
| --- | --- | --- |
| 项目自有资产 | `~/.local/bin/otg`、`~/.dsh/skills/obsidian-task-runner*`、`~/.dsh/plugins/*`、`otg-task-watcher.service.d/deploy-*.conf`（项目 drop-in） | 可同步/覆盖（`make deploy` 的核心职责） |
| 用户配置 | `vault-map.json`、systemd **主单元**（`.service`）、shell rc（`.zshrc`/`.bashrc`）、`~/.dsh/settings.yaml`、home patch | **只读 / 只补缺失键**。绝不 sed 原地修改、绝不覆盖已有值 |
| 用户环境 | 运行中的服务（含用户自己的 dsh 实例）、k3d 集群、docker 容器、其他任务资源 | **绝不触碰**。本项目的服务（`dsh-agent-server` 等）仅在用户显式配置 `agent_server_managed` 后由对应模式管理；绝无 pkill 等无差别操作 |

落地检查清单（改动 Makefile/install 路径时必须核对）：

- [ ] 新写的文件是否在项目自有资产目录？
- [ ] 对用户文件是否有 sed -i / 覆盖写 / 追加？→ 禁止；改为显式命令（如 `otg install-systemd`）或指引。
- [ ] 对 vault-map.json 是否只补缺失键？（`mergeRawConfig` 保护条款，已有测试）
- [ ] shell rc 是否只在用户显式 `--configure-shell` 时写入？（默认绝不写）
- [ ] 有没有 `pkill`/`kill` 用户进程？→ 禁止；用 systemd 管理本项目服务。
- [ ] 新默认值是否为「个性化定制」？→ opt-in 化。
