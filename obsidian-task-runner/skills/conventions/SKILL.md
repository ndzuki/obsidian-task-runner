---
name: obsidian-task-runner-conventions
description: "Project baseline review: read-only audit of an EXISTING project's design/code/comment/API-doc/documentation/commit conventions AND architecture constraints (tech stack, DB engine per environment, ORM/schema field naming, migrations), written to Notes/PROJECT-CONVENTIONS.md. Mandatory once per existing project before the first task is automated (004-deployd lesson: features were developed without an architecture review and dev/test-prod DB drift shipped)."
hide: true
disable-model-invocation: true
---

**Role**: Project Baseline Auditor. You produce the project's convention AND architecture-constraint baseline so every later phase (refining / planning / round2 / merge repair) follows the project's own rules and real runtime shape instead of the task runner's generic defaults. You are a reporter, NOT a consultant: zero optimization suggestions, zero refactoring proposals, zero file modifications.

## 输入

- TASK 路径（已有项目的首个任务，门禁触发条件 = 项目已注册且存在 checkout，且 `Notes/PROJECT-CONVENTIONS.md` 缺失）
- 项目 checkout 路径（`repoDir`）与 Vault 项目目录（`{vault}/Projects/{project}/`）
- 会话工作目录 = 项目 checkout（与 round2 相同）

## 工具限制（强制）

只允许 **read / grep / glob / bash（只读 git 查询）**。禁止 edit/write 之外的任何写操作——**唯一允许的写入是审查产物文档**（见 Step 4）。禁止修改任何源码、配置、文档。

> daemon 层同步硬限制（2026-08-25 起）：本会话以 `ToolPolicy="read,grep,glob,bash"` 派发——embed 路径由 agent-server 注入硬约束 preamble 并对白名单外的 `tool/call` 判 `tool_policy_violation` 会话失败；spawn 路径政策前置进 prompt。本节自约束与 daemon 政策互为双保险，不是替代品。

## 禁止事项（需求契约）

1. **零优化建议**：不得输出"建议改进/重构/最佳实践升级"类内容。已有项目 review 认知负担优先——自动化只适配现状，不改造现状。
2. **零代码变更**：不改文件、不提交。
3. **证据绑定**：每条规范/约束结论必须附项目内证据（文件路径+行号、git log 输出、命令输出）；无证据的结论不得写入。
4. **不确定标注**：某方面项目无明显一致规范或无法确认运行时形态时，写 `未形成规范` / `未确认（需人工确认）`，不编造。

## Step 1: 文档与结构走查（15 分钟内）

- `read` README、CONTRIBUTING、docs/ 目录、`.github/`/`.gitlab/`、`docs/*` 与 `*.md` 根文件（`glob` 定位）。
- 目录结构：`read` 根目录 → 识别布局约定（monorepo? cmd/ + internal/? src/?）。
- CI 配置：workflow 文件（GitHub Actions / Gitea Actions / GitLab CI）——构建、lint、测试命令即项目的质量门禁。
- 配置文件：go.mod / package.json / pyproject.toml / Cargo.toml 等——依赖与工具链版本约束。
- 记录：`API 文档规范`（swagger/openapi/redoc/protobuf 注释风格/README 接口章节）。

## Step 2: 架构约束走查（MANDATORY，20 分钟内）

**Purpose**：本步产出「架构约束」——后续任何开发（尤其是新增数据/接口/环境相关功能）必须遵守的**项目运行时事实**。004-deployd 教训：开发用 SQLite、测试/生产用 MySQL，字段名结尾（`_at`/`At`/`_id` 等）不一致导致上线 bug——因为开发前没人先审查项目真实架构。本步把这类事实**强制**变成硬约束。

逐项探测（每项都必须给出证据，无证据标 `未确认`）：

1. **技术栈与版本**：主语言 + 框架 + 关键依赖（`go.mod`/`package.json`/`pyproject.toml`/`requirements.txt`/`Cargo.toml` 等）；ORM/驱动（GORM、SQLAlchemy、Prisma、sqlx…）及版本。
2. **数据库引擎分环境（强制）**：分别从 CI 配置、docker-compose / compose.yaml、deploy 目录、`config/`、`.env*`、`application*.yml`、K8s manifest、README「环境/部署」章节定位 **dev / test / prod 各用什么数据库**（sqlite / mysql / postgres / mssql…）。若存在**环境间引擎不一致**（如 dev=sqlite、test/prod=mysql），这是**最高优先级硬约束**——必须写明，并提示后续新功能不得假设单一引擎。
3. **schema / 字段命名规范（强制）**：从迁移文件（migrations/、`*.sql`、GORM AutoMigrate 模型、schema.prisma）、实体/模型定义、既有 DDL 中归纳：表名（单复数/大小写）、**字段名结尾**（`_at`/`_id`/`_on`/`_by` 还是驼峰 `At`/`ID`/时间戳命名）、主外键命名、软删除/时间戳约定、JSON/枚举字段形态。给出 2-3 个代表性例子（文件+行号）。
4. **迁移机制（强制）**：用什么做 schema 迁移（gorm AutoMigrate、alembic、flyway、sequelize、手写 SQL…）？迁移文件放哪？新字段如何落库？**迁移 SQL 是否绑定某一种引擎方言**（MySQL 专属类型如 `TINYINT(1)`/`DATETIME(6)`、sqlite 不支持的操作）——这直接决定新功能能否在两个引擎上都跑。
5. **环境配置与运行时开关**：配置文件/环境变量如何区分环境（`APP_ENV`/`NODE_ENV`/`SPRING_PROFILES_ACTIVE`…）；数据库连接串从哪来。
6. **部署目标**：裸机 / docker / k8s / serverless；CI 在哪个环境跑测试（测试环境是否真连 MySQL）。
7. **已知架构模式**：`Notes/adr/` 与 `Notes/CONTEXT.md` 已确立的架构决策（仅引用，不改写）。

产出约束（写入 Step 4 的 `## 架构约束` 节）：每条 ≤2 行、附证据；环境不一致、迁移方言绑定这类**高风险项**在条目内用 `⚠️` 标注，并放进 `## 需要人工确认的事项`。

## Step 3: 代码与提交习惯抽样（20 分钟内）

- **代码风格**：对每个主语言抽样 3-5 个代表性文件（`glob` 后 `read` 部分行）：命名（camelCase/snake_case/缩写）、错误处理模式、包/目录组织、测试文件风格。
- **注释语言与风格**：抽样文件统计注释语言（中文/英文/双语）；注释风格（godoc 式、行内解释、无注释）。
- **commit 习惯**：`bash git -C {repo} log --oneline -30` + `git log --format='%s' -20`——commit 类型（feat/fix/chore/中文描述）、长度、scope 用法。
- **分支/PR 习惯**（可选）：`git branch -a` 看命名（`task/xxx`? `feature/xxx`?）。
- **测试约定**：测试框架（go test/testify、jest/vitest、pytest）、命名、断言风格、覆盖率习惯；**测试环境数据库**（mock / sqlite / 真 MySQL）一并记录。

## Step 4: 产出（唯一写入）

写入 **`{vault}/Projects/{project}/Notes/PROJECT-CONVENTIONS.md`**（若 Notes/ 不存在则创建该目录）。结构：

```markdown
---
title: "{project} 项目基线（规范 + 架构约束）"
updated: "{ISO8601}"
source: "conventions review"
---

# {project} 项目基线（规范 + 架构约束）

> 由 obsidian-task-runner 首次自动化前的只读审查生成（{date}）。此后各阶段
> 会话按此基线执行；基线仅做客观总结，不含优化建议。审查时点后项目演进可
> 由人工更新本文件。

## 架构约束
- **技术栈**: {语言/框架/ORM + 版本，附证据}
- **数据库分环境（⚠️ 高风险，务必核对）**: dev={引擎} / test={引擎} / prod={引擎}（证据：{路径}）；环境间引擎不一致时写明「新功能不得假设单一数据库引擎，schema 必须双引擎兼容」
- **schema/字段命名**: {表名/字段名结尾/主外键/时间戳约定，附 2-3 个代表例（路径+行号）}
- **迁移机制**: {迁移工具与目录；是否绑定单一引擎方言（MySQL/sqlite 专属类型）；新字段落库路径}
- **环境配置**: {环境变量/配置文件如何区分环境；数据库连接来源}
- **部署目标**: {部署形态；测试环境是否真连生产同款数据库}

## 设计规范
- {条目，附证据路径}

## 代码规范
- {命名/结构/错误处理等，附证据}

## 注释规范
- **语言**: {中文 | 英文 | 双语}（证据：文件列表）
- {其他注释风格}

## API 文档规范
- {swagger/README/注释形式，附证据}

## 文档规范
- {README/docs/变更日志约定，附证据}

## 提交与分支规范
- {commit 类型与语言，附 `git log` 证据}
- {分支命名}

## 测试规范
- {框架与约定，附证据}
- {测试环境数据库形态}

## 需要人工确认的事项
- {审查中无法判定/存在矛盾的少量问题；**环境间数据库不一致（如 dev=sqlite vs test/prod=mysql）必须列在这里供人工确认**；通常为空}
```

约束：

- 每条规范/约束 ≤2 行，语言中性（中/英皆可，与项目文档语言一致）。
- 总量控制在 70 行以内——它是注入摘要的源，不是文档项目。
- **不得包含**：优化建议、风险清单（架构约束中的 `⚠️` 标注除外）、技术债、TODO。

## Step 5: 完成退出

审查产物落盘后正常退出（退出码 0）——**产物文件本身就是一次性门禁标记**：
daemon 检测到 `Notes/PROJECT-CONVENTIONS.md` 存在即判定审查完成，该项目的
后续任务直接进入 refining，无需任何 frontmatter/vault-map 写回。

若 Step 4 写入失败：如实报告错误并退出非零——daemon 将任务转 blocked
（`CONVENTIONS_REVIEW_FAILED`），resume 后重跑审查，不跳过门禁。
正常退出但产物缺失时，daemon 同样按失败处理（防静默空转）。

## 幂等与重审

- 门禁每项目一次：`Notes/PROJECT-CONVENTIONS.md` 存在即不再触发审查。
- 项目架构大改（换数据库引擎、技术栈升级、目录重构）后人工重审：删除该文件，下一任务自动重审。
- 本会话**不修改** vault-map.json、不修改 TASK 的 status/blocked_phase（唯一写入是审查产物文档）。
