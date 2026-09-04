# systemd 单元（DSH 时代）

本目录不再存放 unit 模板——**单一事实来源是 `internal/install/install.go`
的 `ConfigureSystemd`**，`otg install-systemd` 与 `make install-force` 直接
生成以下三个 user 单元到 `~/.config/systemd/user/`：

| 单元 | 职责 |
|------|------|
| `dsh-agent-server.service` | `dsh --profile headless-agent-server` 常驻 RPC（dsh-embed 执行器后端，阶段会话持久化） |
| `dsh-web.service` | `dsh --profile web`（可选 Web UI） |
| `otg-task-watcher.service` | `otg daemon`（watcher + 扫描 + 状态机 + git 交付 + 知识库），`Requires+After=dsh-agent-server` |

DSH 时代无轮询 timer：daemon 是纯 watcher 服务（fsnotify + 每 10s scan）。
旧执行器时代的模板（timer / service / watcher 单元）已删除；升级路径统一为 `make deploy`。
