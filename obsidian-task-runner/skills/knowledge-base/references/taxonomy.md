# 分类体系 与 交互经验归类规则

> `knowledge-base` 的按需参考（2026-09-18 从 SKILL.md 拆出）。需要时再 `read`，不要预加载。

---

## 分类体系（主题域 + 频率元数据）

目录按**主题域**划分——分类稳定、可预期，不随项目活跃度变化；使用频率是 frontmatter 元数据（`activity: high|normal|low`），由 INDEX 展示层排序（`verified → activity → updated`），**不移动文件、不破坏引用链接**：

```
References/
├── INDEX.md
├── core/                       # 平台与架构技术（决定系统形态）
│   ├── go/                     # Go 语言与生态
│   ├── kubernetes/             # K8s 核心
│   ├── gitops/                 # ArgoCD, Flux, Flagger
│   ├── containers/             # Docker, containerd, nerdctl, crictl
│   └── networking/             # Nginx, Gateway API, Istio, APISIX
├── extended/                   # 运维与工具（支撑运营）
│   ├── cicd/                   # Jenkins, GitHub Actions, Makefile
│   ├── observability/          # Prometheus, VictoriaMetrics/Logs, OpenSearch
│   ├── databases/              # SQL, MySQL, GORM
│   ├── helm/                   # Helm Charts
│   ├── linux/                  # awk, sed, journalctl, ssh, perf
│   └── tools/                  # Obsidian, Git, Supervisor
└── archived/                   # 已废弃技术（仅人工归档，不自动移动）
    ├── languages/              # Rust, Lua
    ├── infrastructure/         # LDAP, cert-manager, KeePassXC, Jumpserver
    └── ai-ml/                  # AI Agent, 向量数据库
```

**层级规则**：
- 目录归属 = 主题域，一旦确定不随项目活跃度移动；新增主题按域归类。
- `activity` 元数据：初始按项目引用计数标注（≥5 引用 = high，其余 normal）；长期无引用由人工或引用扫描降为 `low`；`archived/` 仅人工确认废弃后放入。
- 检索优先级：`verified=true` > `activity=high` > 更新日期（INDEX 已按此排序）。

## 交互经验归类规则

对话/排障过程中产生的新知识，按**知识形态**归类（不是都进 core/）：

| 知识形态 | 定义 | 存放位置 | 写入路径（全部手动） |
|----------|------|----------|----------|
| **技术要点** | 主题明确的技术知识（API 用法、配置、模式） | 按主题域：`core/<域>/` 或 `extended/<域>/` 对应文件，追加「实践经验」小节 | `otg kb absorb --summary`，或 `write`/`edit` 直接补小节后 `otg kb rebuild-index` |
| **领域踩坑** | 实现中"以为方案 X 对 → 失败 → 换 Y 成功"的负向经验（失败方案+根因+成功方案） | 对应主题文档的「踩坑实践」小节；未命中归档 `References/uncategorized/` | `otg kb absorb`（踩坑格式，内置归一化去重；按 `相关文档` 引用优先，否则按 topics/aliases/tags 分类） |
| **系统运维模式** | 跨主题的排障模式（错误码、卡死、重启） | `core/daemon-stuck-task-patterns.md`（系统模式文件，otg 时代遗留但仍是排障模式库） | 手动追加（按错误码+阶段去重） |
| **方法论/流程** | 工作方法、模型、流程 | `extended/tools/` | agent 按需，`write`/`edit` + `otg kb rebuild-index` |

> 原「自动路径」列描述的是 otg 流水线的 `classifyADR` / `ExtractTaskKnowledge` /
> `AppendFailurePattern` 机制，**已随流水线停用（2026-09-14）**，现全部手动。

判断顺序：主题是否明确 → 明确按域归类；跨主题/系统级 → 系统模式文件；方法论 → tools/。

**层级规则**：
- `extended/`：项目引用但非高频技术。Agent 检索时降权，排在 core 结果之后。
- `archived/`：从未被项目引用。默认不检索，仅当用户明确指定或 core/extended 无结果时才搜索。
- 升级路径：`archived` → `extended`（项目引用时）→ `core`（多项目验证且 verified: true）。
