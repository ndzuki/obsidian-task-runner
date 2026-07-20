# Obsidian Task Runner — 业务流程

> 从 Obsidian 需求到代码交付的自动化流水线

## 1. 整体架构

```mermaid
flowchart TD
    subgraph "触发层"
        A[fsnotify 监听<br/>Projects/ 文件变化]
        B[systemd timer<br/>每 30 分钟兜底]
    end

    subgraph "调度层"
        C[otg daemon<br/>常驻守护进程]
        D[on-req-changed<br/>需求变更处理]
    end

    subgraph "执行层"
        E[find-ready<br/>发现可处理任务]
        F[resolve-path<br/>项目名 → 本地路径]
        G{omp<br/>headless 执行 SKILL.md}
    end

    A --> C
    C --> D
    D --> C
    B --> C
    C --> E
    E --> F
    F --> G
```

## 2. 状态机

```mermaid
stateDiagram-v2
    blocked --> ready: 补齐 project + assignee<br/>且 blocked_by 为空
    ready --> needs-grilling: daemon 触发 Grilling 通知<br/>Kitty tab 弹出
    needs-grilling --> plan-review: 你完成交互式 grilling<br/>生成计划
    plan-review --> ready: 需求变更 pending_req

    state needs-grilling {
        [*] --> Round1: 需求对齐 grilling
        Round1 --> [*]: status → plan-review
        --
        [*] --> Round2: 实现阻塞 grilling
        Round2 --> [*]: status → implementing
        Round2 --> plan-review: plan_version=0<br/>（无计划，先出计划）
    }

    state plan-review {
        [*] --> 等待人工审阅
        等待人工审阅 --> [*]: plan_approved=true
    }

    plan-review --> implementing: Round 2 实现代码
    implementing --> needs-grilling: 测试失败 / 设计决策 /<br/>依赖冲突 / 架构摩擦<br/>（plan_version=0 时自动转 plan-review）
    implementing --> review: 测试/lint/验收通过<br/>git commit 到本地分支
    review --> ready: 需求变更 pending_req

    state review {
        [*] --> 等待人工Review
        等待人工Review --> [*]: merge_approved=true
    }

    review --> done: Merge Phase<br/>git push → PR → merge
    review --> conflict: 合并冲突
    conflict --> review: 人工解决冲突<br/>merge_approved=true
    conflict --> done: Merge Phase 重试成功

    note right of blocked
        自动创建的任务初始为 blocked
        用户填 assignee + project 后
        daemon 自动解除 → Grilling 通知
    end note
```

## 3. 主流程：从需求到交付

```mermaid
sequenceDiagram
    participant User as 你 (Obsidian)
    participant Watch as task-watcher
    participant Daemon as task-runner-daemon
    participant Agent as OMP (model)
    Daemon->>Agent: omp --auto-approve -m deepseek-v4-pro

    Note over Agent: ═══ Grilling 对齐 ═══

    Agent->>Agent: 加载需求上下文
    Agent->>Daemon: status = needs-grilling
    Daemon->>User: 🖥️ Kitty 新 tab + 🔔 桌面通知

    Note over User: 切换到 Kitty tab，与 OMP 交互
    User->>Agent: 对话式 grilling（requirement-elaborator）
    Agent->>Agent: 生成技术规格 + 实现计划
    Agent->>Daemon: status = plan-review
    Daemon->>User: 🔔 桌面通知: 计划已生成

    Note over User: 审阅计划 + ADR 提议
    User->>Daemon: plan_approved = true，保存

    Daemon->>Agent: omp --auto-approve -m deepseek-v4-pro
    Note over Agent: ═══ Round 2: 实现 ═══
    Note over Daemon,Git: 同仓库并发时，daemon 为每个任务创建独立 Git worktree<br/>target_branch 已存在则绑定/校验该分支

    Agent->>Git: 在任务 worktree 创建或恢复 task/001-slug
    Agent->>Agent: 按计划逐步实现代码
    Agent->>Agent: 每步测试 + lint
    Agent->>Agent: git commit
    Agent->>Agent: task-verifier 验收
    Agent->>User: 写回 ## 实现记录 + ## 验收记录
    Agent->>Daemon: status = review, target_branch = ...
    Daemon->>User: 🔔 桌面通知: 代码已实现，请 review

    Note over User: Review 代码
    User->>Daemon: merge_approved = true，保存

    Daemon->>Agent: omp --approval-mode yolo -m deepseek-v4-pro

    Note over Agent: ═══ Merge Phase ═══

    Agent->>Git: git push -u origin task/001-slug
    Agent->>Git: gh pr create
    Agent->>Git: gh pr merge --delete-branch
    Agent->>Git: git checkout main && git pull
    Agent->>Daemon: status = done
    Daemon->>User: 🔔 桌面通知: 已完成
```

## 4. 需求变更流程

```mermaid
sequenceDiagram
    participant User as 你
    participant Watch as watcher
    participant D as daemon
    participant A as OMP

    User->>Watch: 编辑已有 REQ-xxx.md，保存
    Watch->>D: on_req_changed.py 处理

    alt 任务在 ready/plan-review
        D->>D: 直接重置 status = ready
        D->>A: 立即进入 Round 1（重新出计划）
    else 任务在 implementing
        D->>D: 标记 pending_req = true
        Note over D: 当前 Round 2 完成后自动<br/>链式进入 Round 1
    else 任务在 review/done
        D->>D: 标记 pending_req = true
        Note over D: 下次 daemon 扫描时拾起
    end

    A->>A: Round 1 重新出计划
    Note over A: 即使 auto_approve = true<br/>也停在 plan-review
    A->>D: status = plan-review
    D->>User: 🔔 桌面通知: 需求变更已并入
```

## 5. 模型映射与权限

```mermaid
flowchart LR
    subgraph "任务配置"
        A[assignee: field]
    end

    subgraph "模型选择"
        B{assignee?}
        C[deepseek]
        D[gpt]
        E[default / 其他]
    end

    subgraph "执行"
        F[deepseek-v4-pro<br/>Round 1 + Round 2 + Merge]
        G[gpt-5.6-sol<br/>Round 1 + Round 2 + Merge]
        H[default → deepseek-v4-flash<br/>默认回退]
    end

    subgraph "权限"
        I[--auto-approve<br/>Round 1 / Round 2]
        J[--approval-mode yolo<br/>Merge Phase]
    end

    A --> B
    B -->|deepseek| C
    B -->|gpt| D
    B -->|default / 其他| E
    C --> F
    D --> G
    E --> H
    F --> I
    G --> I
    H --> I
    F --> J
    G --> J
```

| assignee | Round 1 | Round 2 | Merge Phase | 轻量任务 |
|----------|---------|---------|-------------|----------|
| `deepseek` | deepseek-v4-pro | deepseek-v4-pro | deepseek-v4-pro | — |
| `gpt` | gpt-5.6-sol | gpt-5.6-sol | gpt-5.6-sol | — |
| `default` / 未知 | deepseek-v4-flash | deepseek-v4-flash | deepseek-v4-flash | deepseek-v4-flash |

| 阶段 | OMP 权限 | 允许的操作 |
|------|----------|-----------|
| Round 1: 出计划 | `--auto-approve` | 读/写文件、读代码、创建 git 分支 |
| Round 2: 实现 | `--auto-approve` | 文件操作、运行测试/lint、git commit |
| Merge Phase | `--approval-mode yolo` | git push、gh pr create/merge、分支清理 |


## 6. 实现说明

现已用 Go 重写成单一二进制（详见 [`go-rewrite-plan.md`](go-rewrite-plan.md)）。以下功能均封装在 `otg` 子命令中：

| 子命令 | 替代原名 | 作用 |
|--------|---------|------|
| `otg daemon` | `task-runner-daemon.sh` | 常驻守护进程（fsnotify + 定时兜底） |
| `otg daemon --once` | systemd oneshot | 单次扫描 |
| `otg find-ready` | `find_ready_tasks.py` | 列出就绪任务（NDJSON） |
| `otg on-req-changed` | `on_req_changed.py` | 需求变更处理 |
| `otg update-status` | `update_task_status.py` | 原子更新 frontmatter |
| `otg resolve-path` | `resolve_project_path.py` | 项目名 → 路径 |
| `otg register-project` | `register_project.py` | 注册新项目到 vault-map |
| `otg install` | `install.sh` | 一键安装（Skill + systemd + 看板） |

`otg daemon` 内置了通知和日志功能，无需额外的 shell 脚本。

## 7. 关键规则

### 安全边界

1. **Round 1 / Round 2 不推送**：只本地 git commit，不 push、不创建 PR、不 merge
2. **Merge Phase 才授权远程操作**：`merge_approved: true` 是人工明确授权信号
3. **新项目永远停在 Round 1**：只出脚手架方案，人工确认后才创建文件
4. **重新出计划停在 plan-review**：需求变更后即使 `auto_approve` 也需人工确认

### auto_approve 规则

- `auto_approve: true` + 非新项目 → Round 1 后无缝进 Round 2
- **例外**：`## 实现记录` 有内容（重新出计划）→ 停在 plan-review

### 低峰执行

- `off_peak_only: true` → Round 2 仅北京低峰（00-09, 12-14, 18-24）
- Round 1 和 Merge Phase 不受影响

### 并发控制

- daemon 使用单实例文件锁：同一 Vault 同时只运行一个调度 daemon；watcher 或 timer 的重复触发不会启动第二个调度循环。
- `max_concurrent_tasks` 控制真正执行中的 OMP headless 数量，默认 `2`；小于 `1` 时按 `1` 执行。等待仓库独占许可的任务不占用全局槽位。
- 调度分为两个阶段：先解析仓库并准备执行目录，再派发 OMP。Round 2 的 worktree 创建或校验在仓库短锁内完成，完成后立即释放锁，不把准备时间计入 OMP 并发。
- 如果 TASK 已有 `target_branch`，daemon 创建 worktree 时直接 checkout 该本地分支；分支不存在则创建并绑定。复用已有 worktree 时必须验证当前分支与 `target_branch` 一致，否则跳过任务并记录错误。
- 如果 `target_branch: ""`，说明任务尚未形成实现分支；worktree 可以暂时从 `HEAD` detached 创建，Round 2 agent 随后在该 worktree 中创建 `task/<id>-<slug>`，完成后写回 frontmatter。
- Round 2 在任务专属 worktree 中运行，可与同仓库的其他 Round 2、Round 1 或 Merge 并行；它们不共享工作目录和 Git index。
- Round 1、Merge 和新项目任务使用主工作区，必须获取仓库独占许可；同一仓库的这些阶段严格串行，防止并发修改默认分支、初始化文件或共享配置。
- 调度器按就绪任务顺序查找当前可运行项。某个独占任务因同仓库正在执行而等待时，调度器会跳过它，继续派发后续可在 worktree 中运行的 Round 2，避免等待者提前占满全局槽位。
- 示例：同仓库队列为 `Merge A → Merge B → Round 2 C`，`max_concurrent_tasks: 2`。调度器先运行 `Merge A + Round 2 C`；`Merge B` 不占槽位，等待 `Merge A` 释放主工作区许可。
- 运行去重、PID 恢复文件和审计日志按任务文件路径区分；不同项目中相同 `id` 的任务不会互相阻塞。
- 每批完成后最多重扫 3 轮，拾取执行期间变为 ready 的任务；新 watcher 触发遇到 daemon 锁会退出，不丢任务。
