# Obsidian → Claude Code 自动化任务流水线

## 目录里有什么

```
obsidian-task-runner/          # Claude Code skill(个人级)
├── SKILL.md
├── reference.md
├── scripts/
│   ├── find_ready_tasks.py
│   ├── update_task_status.py
│   ├── resolve_project_path.py
│   ├── register_project.py
│   ├── task-runner-daemon.sh     # 兜底轮询:扫一遍所有可处理任务
│   └── task-watcher.sh           # 事件触发:Tasks/ 文件一保存就立刻跑
└── config/
    └── vault-map.example.json

agents/task-verifier.md        # 验收 subagent(个人级,或放到具体项目 .claude/agents/ 下)
TASK-000-template.md           # 新任务时复制这个模板
install.sh                     # 一键安装脚本
claude-task-runner.service     # systemd 用户服务(兜底轮询,由 timer 触发)
claude-task-runner.timer       # systemd 定时器(默认 30 分钟一次)
claude-task-watcher.service    # systemd 用户服务(事件触发,常驻监听)
```

## 一键安装

```bash
./install.sh
```

会做这些事:检查 `claude`/`python3`/`git`/`inotifywait` 是否都在;把 skill 装到 `~/.claude/skills/obsidian-task-runner`;把 `task-verifier` 装到 `~/.claude/agents/`(如果你已经有自己的版本不会覆盖);生成 `vault-map.json`(如果还没有);交互式问你 Obsidian Vault 路径,写进 `~/.bashrc`/`~/.zshrc` 并建好 `Tasks/`/`Requirements/` 目录;探测 `claude`/`go` 等命令的实际路径,填进 systemd 单元的 `PATH`;注册并启动 `claude-task-runner.timer`(兜底轮询)和 `claude-task-watcher.service`(事件触发,前提是装了 `inotify-tools`)。

跑完之后还有两件事需要你自己做:编辑 `vault-map.json` 填真实的项目路径映射,以及 `source` 一下你的 shell rc 文件让 `OBSIDIAN_VAULT` 生效。脚本可以重复运行,不会覆盖你已经改过的 `vault-map.json` 或已存在的 `task-verifier.md`。

## Tasks/ 一保存就触发,是怎么做到的

`claude-task-watcher.service` 是个常驻进程,用 `inotifywait` 盯着 `Tasks/` 目录的 `close_write`(文件保存完成)事件——你在 Obsidian 里把某个任务的 `plan_approved` 改成 `true` 并保存,几秒内(5 秒防抖,避免编辑器一次保存触发好几次)就会拉起一次 `claude -p`。`claude-task-runner.timer` 保留作为**兜底**,每 30 分钟整体扫一遍,防的是:inotify 事件在服务重启的间隙丢失、或者 Vault 通过 Syncthing 从另一台机器同步过来时,文件变化没有经过这台机器的本地写入路径。两者不冲突,可以同时开着。

## 手动安装(不想用脚本,或想理解每一步在干什么)

<details>
<summary>展开查看手动步骤</summary>

1. **安装 skill**
   ```bash
   mkdir -p ~/.claude/skills
   cp -r obsidian-task-runner ~/.claude/skills/
   chmod +x ~/.claude/skills/obsidian-task-runner/scripts/*.sh
   ```

2. **配置项目映射**
   ```bash
   cp ~/.claude/skills/obsidian-task-runner/config/vault-map.example.json \
      ~/.claude/skills/obsidian-task-runner/config/vault-map.json
   # 编辑 vault-map.json:
   #   projects        把已有项目的 project 字段值映射到本机仓库绝对路径
   #   new_project_root 新项目统一建在哪个目录下(任务标了 new_project: true 时用)
   ```

3. **安装/更新 task-verifier subagent**
   ```bash
   mkdir -p ~/.claude/agents
   cp agents/task-verifier.md ~/.claude/agents/
   ```
   如果你在某个项目里已经有更定制化的 `task-verifier`,把项目版本(`.claude/agents/task-verifier.md`)留着即可——项目级会覆盖个人级同名 subagent。

4. **在 Obsidian Vault 里建好目录**
   ```
   YourVault/
   ├── Requirements/
   └── Tasks/
   ```
   新任务复制 `TASK-000-template.md` 改名放进 `Tasks/`。

5. **设置环境变量**(写进 `~/.bashrc` 或 `~/.zshrc`)
   ```bash
   export OBSIDIAN_VAULT="$HOME/Documents/Obsidian/MainVault"
   ```

6. **手动验证一次流程**(先不要接 systemd,确认跑得通)
   ```bash
   cd /path/to/some/project   # 对应 vault-map.json 里某个 project 的仓库
   claude -p "/obsidian-task-runner" --permission-mode acceptEdits
   ```
   这一步会:找到优先级最高的可处理任务 → 读需求文档 → 出计划、写进任务文档、状态改成 `plan-review` → 结束(除非该任务 `auto_approve: true` 且不是新项目)。打开任务文件确认计划没问题后,把 `plan_approved` 改成 `true`,再跑一次同样的命令,这次会真正开始实现。

7. **接上 systemd(定时兜底 + 事件触发)**
   ```bash
   mkdir -p ~/.config/systemd/user
   # 把两个 unit 文件里的 __OBSIDIAN_VAULT_PATH__ 和 __RUNNER_PATH__ 替换成实际值
   sed -e "s#__OBSIDIAN_VAULT_PATH__#$OBSIDIAN_VAULT#g" \
       -e "s#__RUNNER_PATH__#$HOME/.npm-global/bin:/usr/local/bin:/usr/bin:/bin#g" \
       claude-task-runner.service > ~/.config/systemd/user/claude-task-runner.service
   sed -e "s#__OBSIDIAN_VAULT_PATH__#$OBSIDIAN_VAULT#g" \
       -e "s#__RUNNER_PATH__#$HOME/.npm-global/bin:/usr/local/bin:/usr/bin:/bin#g" \
       claude-task-watcher.service > ~/.config/systemd/user/claude-task-watcher.service
   cp claude-task-runner.timer ~/.config/systemd/user/

   systemctl --user daemon-reload
   systemctl --user enable --now claude-task-runner.timer
   systemctl --user enable --now claude-task-watcher.service   # 需要先装 inotify-tools
   ```
   检查状态:`systemctl --user list-timers | grep claude-task-runner`、`systemctl --user status claude-task-watcher.service`。日志在 `~/.claude/logs/task-runner.log` 和 `~/.claude/logs/task-watcher.log`。

</details>

## 这一版修复了什么(自查记录)

对照 Claude Code 官方文档核实后,发现并修复了几处会导致流水线在无人值守时直接卡死或行为不符预期的问题:

1. **命令名不对**:skill 目录叫 `obsidian-task-runner`,实际生效的命令是 `/obsidian-task-runner`,而不是文档里原来写的 `/scan-tasks`(命令名由 skill 所在目录名决定,`name` 字段只是展示名)。已统一替换。
2. **headless 权限不够**:`--permission-mode acceptEdits` 只会自动放行文件写入和 `mkdir/touch/mv/cp`,`git`/`make`/`go`/`golangci-lint`/`goimports` 这类命令仍需要显式 `--allowedTools`,否则无人值守跑到这些命令时会直接中止。已在 `task-runner-daemon.sh` 里补上。
3. **systemd 环境缺 PATH**:`systemctl --user` 启动的进程不会读取 `~/.bashrc`,如果 `claude`/`go`/`golangci-lint` 装在非系统默认路径(比如 npm 全局前缀),定时任务会因为找不到命令而失败。已在 `claude-task-runner.service` 里加了 `PATH`,但你需要根据自己机器上 `which claude`、`which golangci-lint` 的实际路径核对一下这行是否需要调整。
4. **"等你确认"这个设计在 headless 下根本不成立**:`claude -p` 是一次性调用,进程跑完就退出,不存在"停在对话里等人回复"这回事。已经改成两轮状态机:第一轮出计划、写进任务文档、状态改 `plan-review` 就结束;人工把 `plan_approved` 改成 `true` 后,下一轮才真正实现。详见 `reference.md`。
5. **没有"从零建项目"的路径**:原来 `project` 不在 `vault-map.json` 里就直接跳过,没有任何创建新仓库的机制。新增 `resolve_project_path.py` / `register_project.py` 和任务的 `new_project` 字段来补这个缺口——新建项目的脚手架方案永远需要人工确认,不受 `auto_approve` 影响。
6. **发现延迟**:最初只有 30 分钟/15 分钟一次的定时轮询,`plan_approved` 改成 `true` 后最多要等到下个整点才会被捡起来。新增 `claude-task-watcher.service`,用 `inotifywait` 盯 `Tasks/` 目录,文件一保存(几秒防抖后)就立刻触发,定时轮询降级为兜底。同时提供 `install.sh` 把这几处安装、路径探测、systemd 注册都自动化掉,减少手动踩坑的机会。

## 关于"全自动"的边界

- 定时任务会自动:发现可处理任务 → 解析项目路径(已有项目直接用,全新项目先建空目录)→ 读需求 → 出计划、写进任务文档、状态改 `plan-review`(`auto_approve:true` 且非新项目时跳过这一步)→ 人工确认后的下一轮:写代码 → 跑测试/lint → 调 task-verifier → 提交到 `task/<id>-<slug>` 分支 → 把状态推进到 `review`。
- **不会自动**:`git push`、发起/合并 PR、把状态标记成 `done`;新建项目的脚手架方案也永远需要人工确认。这几步留给你在 `review`(或 `plan-review`)里手动确认。
- 如果某个已有项目的任务你确实信得过、想全程无人值守跑到 review,把它的 frontmatter 设 `auto_approve: true` 即可,单独控制,不影响其他任务;新建项目不受此开关影响。

## 后续可以加的东西

- 想要"完成后自动发 PR、等 CI 通过再提醒你 review",可以在第 9 步之后加一个 `gh pr create --draft`,但仍建议保留 draft PR 而不是直接合并。
- 如果之后想让审查这一步也交给 Claude Code 的 code review 能力,可以在人工确认前先跑一遍 `/code-review` 或接入 Security Review 插件,作为 task-verifier 的补充,而不是替代。
