# local-fixes — 修掉通用 skill 里残留的流水线死引用

**这是本机个人环境的修复脚本，不是 obsidian-task-runner 的产品产物。** 用完可以整个目录删掉。

## 为什么需要它

`tdd` / `grilling` / `diagnosing-bugs` / `knowledge-base` 这四个日常 skill 里有若干段落
引用已停用的任务流水线。流水线停掉后，这些引用要么指向废弃命令，要么让 agent 误以为
有后台进程在跑：

| 文件 | 问题 | 处理 |
|---|---|---|
| `tdd` | Seams 节写"在 obsidian-task-runner 流程中，Round 1 计划已声明 seam"——日常用 tdd 时这段悬空 | 改成通用的「先写 seam 再写测试」，计划存在时才对计划 |
| `grilling` | 开头整段讲 `requirement-elaborator` 的 `grill_owner` CAS 锁 | 整段删除（单用户交互没有并发问题） |
| `grilling` | 调用方举例点名 `requirement-elaborator` | 改成通用的"规划/设计/评审类 skill" |
| `diagnosing-bugs` | description 与正文都指向 `requirement-elaborator` | 改指 `grilling`；适用门里的 `Task-runner Round 2 pause` 语境去掉 |
| `knowledge-base` | 三段写死 daemon 自动触发（`OTR_KB_VAULT`、`daemon MUST invoke on Merge`、`task-verifier`） | **不删**——daemon 若重启这些描述依然正确——而是加「运行前提」章节，并用 **〔需 daemon〕** 标出哪些段落依赖后台进程，同时给出手动等价路径 |

`knowledge-base` 额外补了一条实测结论：`enable WAL: unable to open database file`
**不是知识库损坏**，是 home 只读（沙箱）导致的写失败；把 `kb.sqlite` 拷到可写路径再用
`--db` 指过去即可正常检索。

## 为什么要用脚本而不是手改

这四个 skill 由 **chezmoi 纳管**：

- 事实源：`~/.local/share/chezmoi/dot_dsh/skills/`
- 运行副本：`~/.dsh/skills/`

**只改一份会被反向收编覆盖回旧内容**。脚本双写两份并事后校验一致。
另外：备份写到 `~/.dsh/trash/` 而**不是**源目录——源目录里放 `.bak` 会被 chezmoi
当成受管文件收编到 home。

## 用法

```bash
cd ~/src/repos/github.com/ndzuki/obsidian-task-runner

# 1) 先看会改什么（不写盘）
python3 skills-standalone/local-fixes/apply-local-skill-fixes.py --dry-run

# 2) 应用（自动备份 + 双写 + 校验）
python3 skills-standalone/local-fixes/apply-local-skill-fixes.py

# 3) 检查 diff，然后在 chezmoi 源仓库提交
cd ~/.local/share/chezmoi && git diff && git add -A && git commit -m "chore(skills): 移除流水线停用后的死引用"
```

可重复执行：已是目标状态时只报告「已应用」，不重复改。

## 安全性

- **内容寻址**：每处改动声明一对唯一的状态串（`before` / `after`）。旧态才动手，
  新态跳过，两者都不是就报错退出——不做"尽力而为"的部分替换。
- **锚点不唯一即跳过整个文件**，报错而不是猜。
- **静态自检**：启动时校验状态串确实能区分改前改后（这条检查是在演练中真的抓到过
  bug 才加的——`before` 一度选成了新文本会原样保留的句子）。
- **备份可恢复**：`~/.dsh/trash/local-skill-fixes-<时间戳>/<skill>/SKILL.md.{source,runtime}`。

## 退役 `requirement-elaborator`

它已被 `grilling` 完全覆盖，且内含 OMP 时代的死命令（`hub list` / `hub send`）与
TASK frontmatter 的 `grill_owner` CAS 锁。退役用第二个脚本：

```bash
python3 skills-standalone/local-fixes/retire-local-skill.py --dry-run
python3 skills-standalone/local-fixes/retire-local-skill.py
```

- **两处一起删**：只删一处会被另一个方向同步回来（见上文 chezmoi 说明）。
- **移入 trash 不真删**：`~/.dsh/trash/retired-skills-<时间戳>/`，文件名前缀标明
  来自 chezmoi 源还是运行副本，可整目录搬回。
- **白名单**：脚本内 `ALLOWED` 只允许 `requirement-elaborator`；打错名字会被拒绝，
  不会误删 `grilling`。
- **残留引用扫描**：删完列出仍提及它的文件——这些全是**已冻结的流水线 skill**
  （`obsidian-task-runner-refining` / `-split` / `-pm` / `reference.md`），
  流水线暂停期间保留原文无害；**若日后复活流水线，需把这些指向改为 `grilling`**。

### 产品侧配套改动（已完成）

退役一个 skill 会牵动仓库里三处对它的引用，全部已同步：

| 文件 | 改动 | 为什么必须改 |
|---|---|---|
| `internal/install/install.go` | `validateRequiredSkills()` 去掉该条目 | 原本缺失即 `otg install` **硬失败**（`make deploy` 连带失败） |
| `internal/notify/notify.go` | grilling 通知文案改指 `grilling` | 那是**用户可见**的提示，会显示一个不存在的 skill |
| `cmd/kitty-grill/main.go` | 注入的 prompt 改指 `skill://grilling`；doc/flag 文案同步 | 那是**运行期 prompt**，会让模型去遵循已退役 skill 的方法论 |
| `obsidian-task-runner/reference.md` | fail-fast 依赖清单 | 文档与代码 1:1 对齐 |
| `obsidian-task-runner/SKILL.md` | 外部依赖清单 | 同上 |
| `cmd/kitty-grill/main_test.go` | 测试注释里的旧名 | 注释漂移 |

验证：`go build -tags sqlite_fts5 ./...` 通过；全量 `go test -race -tags sqlite_fts5 ./...`
通过（16 包 ok，0 失败）；`internal/{install,notify}`、`cmd/kitty-grill` 单包均绿。

> **沙箱注意**：在只读沙箱里跑测试会假失败——任务锁目录是 `~/.cache/otg/locks/`，
> 只读时 59 个测试报 `read-only file system`。加
> `XDG_CACHE_HOME=/tmp/cache GOCACHE=/tmp/gocache` 后全绿，那些失败与代码无关。

**若你打算保留流水线复活性，`git checkout` 这几处即可撤销。**

### 有意保留的引用

`obsidian-task-runner/skills/{refining,split,pm}/SKILL.md` 与 `reference.md` 里仍有
该名字的内部叙述。流水线已暂停，保留原文无害；**若日后复活流水线，需把这些指向
改为 `grilling`**（agent 运行时找不到 `skill://requirement-elaborator` 会 fail-fast）。

## 归档：一条命令

```bash
cd ~/src/repos/github.com/ndzuki/obsidian-task-runner
python3 skills-standalone/local-fixes/teardown-otg.py --dry-run   # 先看会做什么
python3 skills-standalone/local-fixes/teardown-otg.py             # 执行
```

把五步收敛成一次可预演、可回滚的执行：

1. **停 + 禁用** `otg-task-watcher.service`、`dsh-agent-server.service`——只 disable
   不删单元文件，保留回滚能力；
2. **移走依赖 daemon 的插件**：`vault.mjs`、`agent-server.mjs`（含测试）、
   `agent-monitor*.html`；
3. **删除 cordis 里的 `vault-dashboard` 注册项**——`~/.dsh/cordis.patch.yml` 与
   chezmoi 源**双写**（该文件被 chezmoi 纳管，只改一份会被反向收编回滚）；
4. **移走带凭据的 drop-in** `otg-task-watcher.service.d/`（内含 `gh-token.conf` 的
   两个 `Environment=` 凭据行——归档后不该继续留在系统里）；
5. **退役流水线 skill**（调用 `retire-local-skill.py`，不重复实现）。

全部移入 `~/.dsh/trash/otg-archive-<时间戳>/`，可整目录搬回。

**演练记录**：已在假 HOME 上完整跑过 dry-run → 执行 → 幂等重跑 → 校验
（插件该留的留、cordis 两份一致且 YAML 合法、skill 只剩 `grilling`、
凭据 drop-in 已移走、测试 profile 的引用被报出）。

### ⚠️ 必须保留 `obsidian-task-runner/config/`（否则自动注入失效）

`~/.dsh/skills/obsidian-task-runner/` **不能删**。它的 `config/vault-map.json` 是
`~/.dsh/plugins/kb-preflight.mjs` 的默认配置来源——插件的 `DEFAULT_MAP_FILE` 把这个
路径写死了：

```js
const DEFAULT_MAP_FILE = join(homedir(), ".dsh", "skills", "obsidian-task-runner", "config", "vault-map.json")
```

它提供 **vault 根路径、知识库位置、已注册项目清单**；缺了它插件就判定「注入整体关闭」，
于是每个交互会话的 `<project_context>` 与 `<knowledge_base>` **自动注入会静默消失**。

所以 teardown 对它用「墓碑」而不是删除：

- 保留整个目录与 `config/`（`vault-map.json` 逐字节不动）；
- 只把 `SKILL.md` 换成墓碑（带 `disable-model-invocation: true`，让它退出 agent 技能目录）；
- 该 `SKILL.md` 被 chezmoi 纳管，因此**双写**（`~/.dsh` 与 chezmoi 源），否则会被反向收编回滚；
- 收尾**自动校验** `vault-map.json` 仍在且可解析，不通过就明确列出问题——防"拆完看起来成功、
  实际注入已死"这类静默失效（这正是本项目被归档的那类错误）。

`retire-local-skill.py` 的白名单里刻意**不含** `obsidian-task-runner`：单独退役它会被
拒绝并打印原因。

### 为什么"移动/删除"必须连 chezmoi 源一起动

`~/.dsh/plugins`、`~/.config/systemd/user`、`~/.dsh/skills/obsidian-task-runner` 都是
**chezmoi 纳管**路径。对这些路径，两类动作的正确做法不同：

| 动作 | 只动 home 的后果 | 正确做法 |
|---|---|---|
| **改内容**（cordis 条目、router 墓碑） | 内容被反向收编回滚 | **双写** home + chezmoi 源 |
| **移动/删除**（插件、凭据 drop-in、`*.wants` enable 项） | 文件被反向收编**恢复** | **连源一起移走**，且**先源后 home** |

**2026-09-14 实测**：第一版脚本自报"已移走 5 个插件 + 凭据 drop-in + 2 个 enable 项"，
**3 秒后**它们全部被 `chezmoi-apply-watch` 恢复了（`mtime` 11:38:01 vs 脚本执行 11:37:58）。
更糟的是收尾校验查得太早，给出了**假 PASS**——正是本项目被归档的那类"静默失效"。

修正后：

- `move()` 先用 `chezmoi source-path` 解析源，**先移源、再移 home**；
- 收尾校验加 **5 秒等待窗口**（历史上 3 秒即恢复），并额外检查 `*.wants` enable 项与凭据 drop-in；
- 新增 `--verify`：随时复检，专门用来发现"过一会儿又被恢复了"。

> 一句话教训：**在 chezmoi 环境下，"删掉一个文件"不是一次操作，而是两次（源 + home），
> 而且顺序不能反。**

### 防止归档被撤销（重要）

`make deploy` 会**整套装回来**：`sync-docs` 装回 9 个阶段 skill、`sync-plugins` 装回
`agent-server.mjs`/`agent-monitor.html`、[3/6] 重建 service drop-in、[5/6]
`restart otg-task-watcher.service`。`otg install` 同理会 `enable+start` 服务并装回 skill。
即：**归档后任何一次 `make deploy` 或 `otg install` 都会前功尽弃。**

已在仓库侧加两道守卫（默认拒绝，可显式覆盖）：

| 入口 | 行为 | 覆盖方式 |
|---|---|---|
| `make deploy` / `install` / `sync-docs` / `sync-plugins` / `daemon-recover` / `deploy-dryrun` | Makefile `_archive-guard` 前置目标，拦截后**不触发编译** | `make deploy FORCE_ARCHIVED=1` |
| `otg install` | CLI 边界守卫（`internal/cli/install.go`），打印归档说明并退出 1 | `OTG_ALLOW_ARCHIVED_INSTALL=1 otg install` |

不受影响：`make install-standalone`（唯一仍受支持的安装目标）、`build` / `test` / `lint` /
`clean`、`otg kb *`（知识库 CLI）。

> 守卫只加在 **CLI/Makefile 边界**，不放进 `install.Run` / `ConfigureSystemd` 内部——
> 策略在入口，机制保持可测（`internal/install` 的既有测试不受影响）。
> `otg install-systemd` 只重写单元文件、不 enable，不构成复活路径，故未加守卫。

### 手工等价操作（脚本不可用时的兜底）

**要停的（otg 专有）**：

```bash
# 1) 停 + 禁用（enabled = 还会开机自启，必须 disable，否则重启后又回来）
systemctl --user stop    otg-task-watcher.service dsh-agent-server.service
systemctl --user disable otg-task-watcher.service dsh-agent-server.service

# 2) 确认无残留
systemctl --user is-active otg-task-watcher.service dsh-agent-server.service
pgrep -af 'otg daemon|headless-agent-server' || echo "无残留"

# 3) 退役依赖 daemon 的插件（移入 trash，不真删）
mv ~/.dsh/plugins/vault.mjs           ~/.dsh/trash/   # /vault 看板 → otg web serve(8787)
mv ~/.dsh/plugins/agent-server.mjs    ~/.dsh/trash/   # otg 的 headless 执行地基
mv ~/.dsh/plugins/agent-monitor*.html ~/.dsh/trash/   # Agent Town 面板
```

并从 `~/.dsh/cordis.patch.yml` 删掉 `vault-dashboard` 注册项（唯一指向 otg web 的入口）。

> `dsh-agent-server.service` 的 Description 明确写着
> `headless-agent-server for obsidian-task-runner` —— 它是 otg 专有，不需要犹豫。

**必须留的**：

| 留什么 | 为什么 |
|---|---|
| `otg` 二进制（`~/.local/bin/otg`） | `otg kb search`/`absorb` 是日常检索与沉淀入口，`kb-distill.mjs` 靠 `spawn('otg', …)`。**归档的是 daemon，不是 CLI。** |
| `kb-preflight.mjs` | daemon 之外的自动上下文注入主力（web profile 注册） |
| `kb-distill.mjs` / `dsh-commands.mjs` / `fallback.mjs` | 会话沉淀、斜杠命令、模型降级 |
| `dsh-web` / `dsh-web-token-bridge` / `dsh-model-watch` | dsh web GUI 本体 |
| `chezmoi-apply-watch` | 配置同步（`~/.dsh/skills` 靠它收编） |
| `dsh-session-repair.service` + `.timer` | 会话健康修复，与 otg 无关 |
| `~/.dsh/storages/otg/kb.sqlite` + `<vault>/References/` | 知识库本体 |
| `<vault>/Projects/` 全部 | **上下文与任务记录的第一者**，改由交互会话维护 |

**流水线 skill 的退役由 teardown 脚本第 5 步调用**（白名单含 router + 9 个阶段 skill +
`project-rebaseline`）。只想单独退役 skill 时，直接跑
`python3 skills-standalone/local-fixes/retire-local-skill.py`（支持 `--dry-run`）。

## 归档记录应写进 vault

归档决策属于**任务记录**，落点应是 vault（第一者），不是仓库：

在 `Projects/003-obsidian-task-runner/Notes/` 下追加一节：

```markdown
## 2026-09-14 归档

- 决策：停止 daemon 与阶段流水线；所有工作改由 dsh web 交互会话完成。
- vault 的 `Projects/` 结构保留，继续作为上下文与任务记录的第一者。
- 保留：`otg` CLI 的 kb 子命令、`kb-preflight`/`kb-distill` 插件、全部 vault 内容。
- 停用：`otg-task-watcher.service`、`dsh-agent-server.service`、9 个阶段 skill。
- 提炼：通用方法移出为独立 skill（project-baseline-audit / risk-aware-planning /
  design-pass / incremental-delivery），不依赖 daemon，见仓库 `skills-standalone/`。
- 未收口：`Notes/Stage-Review.md`（Phase 2）的「评审决策」自 2026-08-11 起为空——
  归档前如实记为未收口项，不假装已完成。
```

## 回滚

```bash
# 从备份恢复（两份都恢复）
cp ~/.dsh/trash/local-skill-fixes-<ts>/tdd/SKILL.md.source \
   ~/.local/share/chezmoi/dot_dsh/skills/tdd/SKILL.md
cp ~/.dsh/trash/local-skill-fixes-<ts>/tdd/SKILL.md.runtime \
   ~/.dsh/skills/tdd/SKILL.md
```

## 演练记录

脚本在交付前已在工作区对四个 skill 的**真实副本**完整演练过：dry-run → 应用
（12 处）→ 幂等重跑 → 死引用清零（`requirement-elaborator` / `grill_owner` = 0）
→ 双写一致 → frontmatter 合法 → 备份落盘。演练中还修掉了两个自身缺陷：

1. 一处 `marker` 撞上原文既有句子，导致 `tdd` 被误判为"已应用"；
2. `tdd` 的区间替换**过度删除**了指向自带参考文件的一行
   （`参考：tests.md、mocking.md`）——已补回。

退役脚本同样演练过：dry-run → 两处目录移入 trash → 幂等重跑（报告"已退役过"）
→ 白名单拦下未授权的 `grilling`（该目录完好无损）→ 残留引用扫描正确列出
`obsidian-task-runner-refining`。

> 修复脚本已于 2026-09-14 在宿主执行完毕（备份
> `~/.dsh/trash/local-skill-fixes-20260914-111018`）：四个文件的
> `requirement-elaborator` 与 `grill_owner` 引用均归零，`knowledge-base` 落下
> 6 处 〔需 daemon〕 标记。本文档保留作为改动依据与回滚索引。
