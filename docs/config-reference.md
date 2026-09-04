# vault-map.json 配置参考（单一事实源）

> 代码权威：`internal/config/config.go` 的 `Defaults()` 与 `mergeDefaults()`。
> 本文档与其保持同步；冲突时以代码为准。
> 实时查看当前生效值：`otg config show --effective`（`--redact` 可脱敏）。

## 最小配置（新用户只需写这些）

```json
{
  "obsidian_vault": "/path/to/vault",
  "new_project_root": "/path/to/repos",
  "models": { "default": "provider/model" },
  "projects": [ { "name": "demo", "path": "/path/to/repo", "git_remote": "..." } ]
}
```

其余字段全部有代码默认值，缺省即工作。`otg install` 生成的初始
vault-map.json 也只包含最小键；示例文件
`obsidian-task-runner/config/vault-map.example.json` 同理。

## 顶层字段清单

| 字段 | 类型 | 默认 | 说明 |
|---|---|---|---|
| `obsidian_vault` | string | — | Obsidian Vault 根（必填语义） |
| `new_project_root` | string | `~/src` | 新项目 checkout 根 |
| `projects` | array | `[]` | 项目注册表；`name/path/git_remote/project_id/project_type/merge_mode` |
| `models` | map | 见 `config.DefaultModels()` | assignee 键 → DSH 模型标识；`default` 缺省兜底 |
| `fallback` | object | nil | DSH 跨模型兜底链（chains/default/fallbackOnCodes），仅 daemon 自动化阶段生效 |
| `notifications.desktop` | bool | true | 桌面通知开关 |
| `poll_interval_minutes` | int | 30 | 兜底全量扫描间隔 |
| `max_concurrent_tasks` | int | 0 | round2 全局并发封顶（0=不限） |
| `max_concurrent_tasks_per_project` | int | 2 | 每项目 round2 并发上限 |
| `phase_concurrency` | map | refining 3 / planning 2 / merge 1 / priority 1 / pm 1 / audit 1 | 非 implementing 阶段并发上限（0=不限） |
| `phase_timeouts_minutes` | map | priority 5 / refining 15 / planning 45 / round2 120 / merge 15 / design 90 | 各阶段会话超时 |
| `scan_min_interval_seconds` | int | 10 | watcher 扫描节流下限 |
| `agent_server_addr` | string | `127.0.0.1:8799` | agent-server RPC 地址 |
| `agent_server_managed` | bool | true | true=daemon 拉起子进程；false=外部 systemd 管理 |
| `executor` | string | `dsh-embed` | 阶段执行后端；`dsh` 为旧 spawn 路径 |
| `dsh_cmd` | string | `dsh` | DSH 可执行文件 |
| `dsh_profile` | string | `headless` | 仅 `executor="dsh"`（spawn 路径）使用；默认 `dsh-embed` 下忽略 |
| `vault_web_addr` | string | `127.0.0.1:8787` | 只读看板 HTTP API 地址 |
| `default_assignee` | string | `""` | 新 TASK 预写 assignee；空=等人工 |
| `auto_resume_aged_after_hours` | int | 24 | 瞬态错误 blocked 任务的老化自动恢复窗口 |
| `max_overlap_wait_minutes` | int | 720 | 计划文件重叠串行等待上限 |
| `max_auto_merge_fixes` | int | 3 | 每次合并授权的 AI 修复预算 |
| `merge_poll_wait_ticks` | int | 20 | CI 轮询预算（30s/次，即 10min） |
| `max_auto_fix_conflicts` | int | 40 | 冲突文件数熔断：超过则跳过 AI 修复 |
| `upstream_stall_days` | int | 3 | 上游停滞告警阈值；**显式 0 = 关闭告警** |
| `compact_oversize_threshold_kb` | int | 60 | TASK 文档超过该体积触发历史折叠 |
| `grilling_consolidation_batch` | int | 1 | 每轮 scan 的 PM 统筹会话数 |
| `stage_min_per_phase` | int | 3 | 确定性分组：每阶段最少任务数 |
| `stage_max_phases` | int | 4 | 确定性分组：阶段数上限 |
| `replan_gate_threshold` | int | 5 | replan gate 阈值 |
| `worktree_base` | string | repo 父目录 | 任务 worktree 根覆盖 |
| `log_dir` | string | `~/.dsh/logs` | 日志目录 |
| `off_peak_timezone` | string | `""` | 低峰时段时区（opt-in） |
| `off_peak_windows` | array | nil | 低峰窗口；**未配置 = 不限制**（off_peak_only 恒可运行） |
| `memory_gate` | object | 见下 | 内存门禁（opt-in） |
| `env_cleanup` | object | **nil（禁用）** | 环境收尾（opt-in，删除 k3d 集群/registry/网络，需自备 Exclude） |
| `kb_embedding` / `kb_rerank` / `kb_chat` | object | nil | 知识库可选后端；nil=纯 BM25 / 无 RAG |
| `kb_vault` | string | 回退 obsidian_vault | 全局共享知识库根 |
| `kb_db` | string | `~/.local/share/otg/kb.sqlite` | 检索存储路径 |
| `audit` | object | enabled / max_fixes 2 / timeout 15 / concurrency 1 | 完成审计门禁 |

## 子结构

### memory_gate（默认：仅 REQ 声明触发，无自动回收）

| 键 | 默认 | 说明 |
|---|---|---|
| `mem_available_mib` | 0 | 全局内存下限（0=仅 REQ 声明触发） |
| `auto_recovery` | **false** | 自动停可重启 k3d 集群（有损操作，显式开启） |
| `max_stops` | 2 | 单轮自动回收最多停几个集群 |
| `exclude` | [] | 永不停止的名称子串白名单 |

### env_cleanup（默认：nil，不启用）

| 键 | 默认 | 说明 |
|---|---|---|
| `on_merge` / `on_block` | false | 合并完成 / 阻塞终态时删除任务自建 k3d 资源 |
| `exclude` | [] | 永不删除的名称子串白名单 |
| `dry_run` | false | 只审计不删除 |

## 已移除字段（不再解析）

`config_version`、`shutdown_grace_seconds`、`starvation_warning_days`、
`notifications.sound`。旧文件中的这些键按「未知键保留」容忍；手工删除后不会恢复。

## 环境变量覆盖

`OTG_OBSIDIAN_VAULT`（别名 `OBSIDIAN_VAULT`）、`OTG_DSH_CMD`、`OTG_DSH_PROFILE`、
`OTG_MAX_CONCURRENT_TASKS`、`OTG_MAX_CONCURRENT_TASKS_PER_PROJECT`。
安装期另有 `OBSIDIAN_VAULT` / `NEW_PROJECT_ROOT` / `NOTIFY_ENABLED` /
`POLL_INTERVAL_MINUTES` / `SKILL_INSTALL_DIR`。
