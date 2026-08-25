# 经验教训沉淀 — 004-deployd 近期问题复盘（2026-08-25）

> 本文件汇总 004-deployd 仓库最近（2026-08-24 ~ 08-25）暴露的问题与教训，
> 按知识库「踩坑实践」规范（现象 / 失败方案 / 根因 / 成功方案 / 相关文档）
> 整理为 `otg kb absorb` 可直接执行的命令块。
>
> **当前会话沙箱里 vault（`myNote/`）与检索库 store 只读，无法直接写入；
> 请在可写环境（daemon 机器）逐条执行以下命令**。`otg kb absorb` 自带
> 「标题/失败方案」归一化去重，重复执行不膨胀。
>
> 知识去向：目标 References 文档均已存在（`core/go/gorm-guide-condensed.md`、
> `core/databases/sql-guide.md`、`core/kubernetes/operator-development-guide.md`、
> `core/daemon-stuck-task-patterns.md`、`core/containers/cloud-native-dev-guide.md`）。

## 一、deployd 近期问题的教训总览

| # | 问题（commit） | 教训类别 | 与 task-runner 的关系 |
|---|---|---|---|
| L1 | MySQL 严格模式 1292 零日期（`778ba8c`） | SQLite/MySQL 语义差异 + GORM 整结构体写回 | 正是"开发用 SQLite、生产用 MySQL"漂移类的实证 |
| L2 | patchStatus 冲突重试丢失增量 → 无限 SyncRetry（`aa01980`） | 冲突重试必须保留 pending 增量，否则重试预算失效 | 与 task-runner 知识提炼"无限重试风暴"同构 |
| L3 | 提炼 28 条架构约束清单（`0619cf4`） | 存量项目应先提炼隐性约束再开发 | task-runner 新 conventions 门禁的同款方法论 |
| L4 | bug 修复必须在本地环境验证后交付（`719b9c5`） | 验证纪律 | 测试环境（SQLite）绿 ≠ 生产（MySQL）绿 |
| L5 | k3d JWT 铸造未对齐运行中 server（`9db4f32`） | 本地环境必须镜像生产配置 | 冒烟环境配置漂移 |
| L6 | Helm 资源名随 release 名（`195d697`） | 文档命令不得写死资源名 | — |

## 二、踩坑记录（absorb 命令块）

### L1: GORM Save 整结构体把零值时间写回，MySQL 严格模式 1292 拒绝

```bash
otg kb absorb --project deployd <<'EOF'
### 2026-08-24: registry 凭据更新在 MySQL 严格模式报 1292 零日期
- 现象: 更新 registry 凭据时报 `Error 1292: Incorrect datetime value`，生产 MySQL 拒绝写回。
- 失败方案: 只在 SQLite 单测里验证更新逻辑（SQLite 不校验零日期，测试全绿但生产炸）。
- 根因: Upsert 每次重建 RegistryCredential（仅复制 ID），CreatedAt 为 time.Time 零值；gormStore.Upsert 用 `Save(整结构体)` 全字段写回，把 `0000-00-00 00:00:00` 写进 created_at 列，被 MySQL `NO_ZERO_DATE` 拒绝。SQLite 无此校验，dev/test 全绿掩盖了差异。
- 成功方案: CreatedAt 加 `gorm:"<-:create"`（仅 INSERT 写入，UPDATE 永不落库）；补回归测试 TestStore_UpsertPreservesCreatedAt 断言更新不覆盖创建时间；更新类操作一律用 `Updates(map)`，禁止 Save 整结构体。
- 相关文档: core/go/gorm-guide-condensed.md, core/databases/sql-guide.md
EOF
```

### L2: patchStatus 冲突重试丢失 pending 增量，SyncRetries 永远到不了预算 → 无限 SyncRetry

```bash
otg kb absorb --project deployd <<'EOF'
### 2026-08-24: operator patchStatus 冲突重试丢失增量导致无限 SyncRetry
- 现象: 25 个同步 worker 持续冲突，Status.Sync 事件刷屏（`retry attempt 2/3` 重复数十分钟），RetryExhausted 永不触发，Sync 阶段无限重试。
- 失败方案: 冲突时 re-Get 最新对象整体覆盖本地状态再写回（把本会话未持久化的 SyncRetries 自增、新 Inventory 静默丢弃，写回陈旧状态却返回 nil）。
- 根因: 冲突重试路径丢失 pending 增量——重试计数每次都被旧值覆盖，永远到不了预算上限，重试预算形同虚设。与「知识提炼补救扫描无限重试」同构：有界重试依赖持久化的进度字段，进度被覆盖 = 无界。
- 成功方案: 深拷贝待写状态；冲突时 re-Get 到独立对象并**叠加 pending 增量**再写回。回归测试 TestPatchStatus_ConflictRetryPreservesPendingChanges（旧实现 SyncRetries 丢失为 0，修复后保留 2）。通用规则：冲突重试写回必须保留本会话未持久化的进度字段。
- 相关文档: core/kubernetes/operator-development-guide.md, core/daemon-stuck-task-patterns.md
EOF
```

### L3: SQLite 单测掩盖 MySQL 严格模式语义差异

```bash
otg kb absorb --project deployd <<'EOF'
### 2026-08-24: 全量单测跑 SQLite，MySQL 严格模式语义差异被掩盖
- 现象: CI 全绿、生产爆炸——零日期 1292、TEXT 列默认值等 MySQL 限制在 SQLite 单测中完全不暴露。
- 失败方案: 只在 SQLite 内存库上验证存储层行为变更。
- 根因: 生产 DB_DRIVER=mysql（严格模式 NO_ZERO_DATE 等），单测统一 OpenSQLite；两种引擎的约束语义差异（零日期/字符集/索引长度/TEXT 默认值）在 SQLite 侧无校验。
- 成功方案: 立规（deployd AGENTS.md/CLAUDE.md Verification）：MySQL 相关修复必须用本地 MySQL 8.4 严格模式实测并附验证证据；生产 sql_mode 与本地对齐。deployd 已把 28 条此类隐性约束提炼进 docs/architecture-constraints.md。
- 相关文档: core/databases/sql-guide.md, core/go/gorm-guide-condensed.md
EOF
```

### L4: 存量项目开发前应先提炼隐性架构约束

```bash
otg kb absorb --project deployd <<'EOF'
### 2026-08-25: 存量项目隐性架构约束未显式化，新功能开发踩坑返工
- 现象: deployd 连续出问题（1292 零日期、SyncRetry 无限重试、k3d JWT 铸造失败、Helm 资源名写死、imagePrefix 必须匹配 mapping destination 等），每次都是"代码里已经这样、违反会出故障"的隐性事实被新代码破坏。
- 失败方案: 出一次问题修一处，不提炼约束清单。
- 根因: 隐性事实约束（生产=MySQL 严格模式 vs 单测 SQLite、禁止 Save 整结构体、凭据 per-environment、CRD 先行等）只存在于代码里，开发会话看不到；task-runner 侧也没有"已有项目开发前先审查架构"的门禁（普通已有项目不过 conventions 审查，团队项目审查也只总结规范、不采集架构约束）。
- 成功方案: 双管齐下——① 项目侧：本会话按"存储/认证/operator/凭据/构建/部署/本地环境"七域遍历代码，提炼 28 条约束写入 docs/architecture-constraints.md（每条附代码证据+违反后果），AGENTS.md/CLAUDE.md 增加约束节；② task-runner 侧：conventions 门禁扩展到所有已有项目，审查内容新增「架构约束走查」（数据库分环境/schema 字段命名/迁移方言），基线注入 planning/round2 强制对齐。
- 相关文档: core/daemon-stuck-task-patterns.md
EOF
```

### L5: 本地冒烟环境必须镜像生产配置（k3d JWT 铸造）

```bash
otg kb absorb --project deployd <<'EOF'
### 2026-08-24: k3d release-demo JWT 铸造用错 secret，冒烟环境验证无效
- 现象: release-demo 脚本铸造的 JWT 被运行中 server 拒绝（invalid token），演示环境空跑。
- 失败方案: 假设 shell 环境变量与运行中 server 的配置一致；凭据 PUT 失败仅打印告警继续跑（容错打印掩盖失败）。
- 根因: JWT 铸造必须用**运行中 server 实际配置的 JWT_SECRET**（从 Deployment env 读取），不能假设与 shell 一致；k3d server 注入的是 dev REGISTRY_SECRET_ENCRYPTION_KEY，与手工构造不同。
- 成功方案: release-demo.sh 改为从 Deployment env 读取 JWT_SECRET 再铸造；凭据 PUT 失败 fail-fast；k3d 写入的 kubeconfig API server 是 0.0.0.0:6443，本地工具需修正 127.0.0.1。
- 相关文档: core/containers/cloud-native-dev-guide.md
EOF
```

### L6: Helm 资源名随 release 名生成，文档命令不得写死

```bash
otg kb absorb --project deployd <<'EOF'
### 2026-08-24: Helm 部署文档写死 Deployment/ClusterRole 资源名，升级核对命令失效
- 现象: 按文档命令核对升级结果时找不到 Deployment/ClusterRole。
- 失败方案: 在文档里写死 `deployd-operator` / `deployd-operator-role`。
- 根因: Helm 资源名随 release 名生成（`<release>-deployd-operator`，release 名已含 chart 名时无前缀），不是常量。
- 成功方案: 文档改为 `helm list` + `kubectl get clusterrole | grep deployd` / label 选择器核对。
- 相关文档: extended/helm
EOF
```

## 三、执行清单（在可写环境执行）

1. 逐条执行上面 6 个 absorb 命令（`otg kb absorb` 自动去重 + 重建 INDEX + 增量同步检索库）。
2. 验证：`otg kb search "MySQL 1292"`、`otg kb search "SyncRetry"` 应命中上述记录。
3. **部署今天的 task-runner 修复**：已安装 skill 仍是 2026-08-24 19:32 旧版（`~/.dsh/skills/obsidian-task-runner-conventions/SKILL.md` 尚无「架构约束」、主 SKILL.md 还写着 merge/pm 槽位不可达）——执行 `make install-force`（或 `otg install --force`）部署新门禁与退避修复，否则 004-deployd 后续任务仍走旧流程。
4. **重审 004-deployd 基线**：现有 `Notes/PROJECT-CONVENTIONS.md` 是旧版（无 `## 架构约束` 节）。删除该文件 → 下一个 ready 任务自动触发新版基线审查；审查会话应读取 deployd 仓库已提炼的 `docs/architecture-constraints.md`（28 条约束，现成证据）写回基线。deployd 仓库侧已有约束清单，task-runner 门禁是双保险。
5. deployd 仓库本身已有防复发机制：`docs/architecture-constraints.md` + AGENTS.md/CLAUDE.md 约束节与验证规则（bug 修复必须本地环境实测）——**不要重复造**，任务自动化引用即可。

## 四、第二批教训（2026-08-25 TDD 失败场景验证批次）

### L7: 失败场景必须逐项 TDD 验证，禁止只验主成功路径

```bash
otg kb absorb --project deployd <<'EOF'
### 2026-08-25: 每个新功能点做多种失败场景验证（TDD）——翻出 8 个真实 bug
- 现象: 对每个功能点（Sync/Build/Warmup/RBAC/凭据/Web）逐一构造失败场景，用 TDD 先写测试暴露问题再修复，最终翻出 8 个真实 bug；其中一半只在某一种失败场景或某一种环境才暴露。
- 失败方案: 只验证主成功路径 + 全量单测（SQLite）全绿就当交付。
- 根因: 失败场景（401/403、并发竞态、重试耗尽、吊销、序列化 null vs []、跨引擎方言、失败被容错掩盖）各有独立触发面，主路径绿不代表负向行为正确。
- 成功方案: 三层验证（单测失败测试 → 生产级环境实测（MySQL 严格模式/k3d）→ e2e 故障注入）；测试驱动真实实现（httptest 内存 registry 而非 mock）；并发类 `-count=3+` 复跑抓 flaky；失败信息可断言；错误要 fail-fast 不被容错输出掩盖。
- 相关文档: core/daemon-stuck-task-patterns.md
EOF
```

### L8: 并发/排队类测试受迭代序影响 flaky，必须抓根因修复

```bash
otg kb absorb --project deployd <<'EOF'
### 2026-08-25: checkBuilds 排队测试 5 次中挂 2 次（flaky）
- 现象: TestCheckBuilds_QueuesServicesBehindGlobalSemaphore 随机失败（3/5 绿）。
- 失败方案: 加 `-count` 重试/放大超时"压过去"。
- 根因: 排队服务与终态服务的处理顺序受 YAML map 迭代序影响，槽位未释放时本轮无法启动，单次 observe 不收敛。
- 成功方案: 先加 debug 输出定位（只看到 api 已终态、web 未创建），再在代码层加"收尾补偿"（终态释放槽位后再给排队服务一次启动机会），30/30 稳定。教训：flaky 必须抓到根因，禁止重试到绿。
- 相关文档: core/daemon-stuck-task-patterns.md
EOF
```

### L9: 同 revision 重跑不更新 warmup DS → 新 digest 永远 pending

```bash
otg kb absorb --project deployd <<'EOF'
### 2026-08-25: 同 revision 重跑（新构建 digest）warmup DaemonSet 不更新，Status.Warmup 永远 pending
- 现象: k3d-smoke 长生命周期集群（16h）中 warmup 断言失败：新 digest 镜像从不被预热，Status.Warmup[] 卡 pending。
- 失败方案: warmup.Start 对已存在 DS 直接返回 nil（幂等 no-op），以为同 revision 无需更新。
- 根因: 同 revision 定制构建重新执行产生新 digest，但 DS 名按 revision 生成、已存在则不更新——新镜像无 pod 拉取。
- 成功方案: Start 改为按镜像集合比较、有变化则原地 Update DS 容器（幂等仅当镜像一致）。教训：幂等创建要处理"同名资源内容变化"场景，生命周期残留会永久卡状态。
- 相关文档: core/kubernetes/operator-development-guide.md
EOF
```

### L10: 测试环境"恰好能用"≠ 生产 RBAC 正确

```bash
otg kb absorb --project deployd <<'EOF'
### 2026-08-25: secrets/configmaps 缺 delete verb，Helm 部署吊销/回收被 Forbidden
- 现象: AC-8 凭据吊销、buildkitd 空闲回收在 Helm 部署的客户集群失败（Forbidden），k3d 却全绿。
- 失败方案: 只在 k3d 冒烟验证（k3d 自建 ClusterRole 手写了 delete），未核对 Helm chart 的 RBAC。
- 根因: chart clusterrole 只给 secrets/configmaps create/get/list/patch/update/watch，缺 delete；测试环境与生产 manifest 两套 RBAC 漂移。
- 成功方案: helm chart + kubebuilder marker + 生成 role 三处补齐 delete；make manifests 校验漂移。教训：e2e 环境的权限 manifest 必须与交付物（chart）一致，测试"恰好能用"不证明生产正确。
- 相关文档: core/kubernetes/operator-development-guide.md
EOF
```

### L11: 幂等/状态机要处理生命周期残留（events TTL 过期、长生命周期复用）

```bash
otg kb absorb --project deployd <<'EOF'
### 2026-08-25: k3d-smoke warmup 事件断言在长生命周期集群失效
- 现象: smoke 断言"warmup DaemonSet activity appears in events"在 16h 集群失败。
- 失败方案: 断言依赖 K8s events（1h TTL），未考虑复用/老化集群。
- 根因: events 过期后、且 revision 未变化不再产生新事件，断言无法满足。
- 成功方案: 断言改为"事件 或 DaemonSet 存在"双通道；测试环境要模拟长生命周期复用。教训：测试断言要按"可复用、可老化"设计，不要依赖会过期的副作用。
- 相关文档: core/daemon-stuck-task-patterns.md
EOF
```

### L12: 序列化契约要锁定 null vs [] 等形状

```bash
otg kb absorb --project deployd <<'EOF'
### 2026-08-25: agent sync destinations 空值序列化为 null，破坏 wire 契约
- 现象: destinations 为空时 JSON 输出 null 而非 []，operator 按数组解析易错。
- 失败方案: 未初始化切片（nil slice 序列化为 null），假设空数组等价。
- 根因: Go nil slice 与空 slice 的 JSON 形状不同；wire 契约要求恒为数组。
- 成功方案: 构造时初始化 `Destinations: []registrySyncDestination{}`，加测试断言 `"destinations":[]`。教训：对外 JSON 契约要明确 null 与 [] 的语义，用测试锁住形状。
- 相关文档: core/go/gorm-guide-condensed.md
EOF
```

### L13: mutateAsync 拒绝未捕获 → unhandled rejection（失败测试暴露）

```bash
otg kb absorb --project deployd <<'EOF'
### 2026-08-25: Web 提交 mutateAsync 拒绝未捕获导致 unhandled rejection
- 现象: 环境创建/编辑时凭据 PUT 失败，控制台 unhandled rejection（Vitest 报 Errors）。
- 失败方案: `await mutation.mutateAsync()` 未 try/catch，依赖 onError 兜底。
- 根因: mutateAsync 的拒绝未被捕获，onError 只负责 UI 文案不负责吞掉 Promise 拒绝。
- 成功方案: handleFinish 内 try/catch 吞掉拒绝（错误仍由 onError 展示）。教训：写失败场景测试能暴露"错误未捕获"这类非 UI 缺陷。
- 相关文档: extended/frontend
EOF
```

### L14: 构建失败分类必须回读需求原文，不凭"合理"偏离

```bash
otg kb absorb --project deployd <<'EOF'
### 2026-08-25: 构建失败把 git 凭据错误判为"立即失败"，违反 REQ AC-6/L216
- 现象: classifyFailure 把 authentication required/unauthorized/denied 判为非重试 → 立即 BuildFailed；REQ 要求 git 凭据错误 backoff 重试。
- 失败方案: 按"确定性错误重试无意义"的设计直觉分类，未回读需求原文。
- 根因: REQ 错误模型明确"构建 Job 失败（含 Dockerfile 错误与 git 凭据错误）统一 backoff 重试→耗尽 Failed"；凭据可轮换恢复，重试有价值。
- 成功方案: 对齐 REQ 全量可重试（仅"Job 成功但 digest 缺失"不可重试）；TDD 先改测试期望（红）再改实现（绿）。教训：失败分类这类语义必须回读需求原文与错误模型表，不能凭"合理"偏离。
- 相关文档: core/daemon-stuck-task-patterns.md
EOF
```

## 五、执行清单（第二批）

1. 逐条执行 L7-L14 共 8 个 absorb 命令（可写环境）。
2. 验证：`otg kb search "失败场景"`、`otg kb search "flaky"`、`otg kb search "warmup"` 应命中。
3. **部署 skill 更新**：`make sync-docs`（或 `make install-force`）把 round2 SKILL.md（失败场景验证节 + 完成检查 + Review Bundle）同步到 `~/.dsh/skills/`；代码改动（audit prompt 失败场景复核）随下一次 `make build`/`otg install` 生效。
4. **回看 004-deployd 后续任务**：新实现任务将按 round2「失败场景验证」执行，独立审计（audit prompt 第 7 条）会复核负向测试与 prod 引擎实测证据。
