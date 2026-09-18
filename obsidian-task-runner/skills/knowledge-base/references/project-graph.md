# Step -1：项目应用知识图谱

> `knowledge-base` 的按需参考（2026-09-18 从 SKILL.md 拆出）。需要时再 `read`，不要预加载。

---

### Step -1: 项目应用知识图谱（进入项目时自动执行）

当 Agent 进入任何 Projects/ 下的项目时，合成 CONTEXT.md + ADR + References 三源交叉引用：

1. 读取 `Notes/CONTEXT.md` → 提取领域语言、约束、反模式。
2. 读取 `Notes/adr/ADR-INDEX.md` 和所有 `accepted` 状态 ADR → 提取技术选型和取舍。
3. 读取 `$OBSIDIAN_VAULT/References/INDEX.md` → 将 ADR 中引用的技术匹配到知识库文档。
4. 输出项目技术全景表：

```markdown
## 项目应用知识图谱

| ADR | 决策 | 知识库来源 | verified | 项目实践 |
|-----|------|-----------|----------|---------|
| ADR-002 | Connect + protobuf 统一协议 | core/go/connect-rpc.md | true | 3 个服务稳定运行 |
| ADR-004 | Go SDK-only 集群执行 | core/go/solid-principles.md | false | 待验证 |

### 知识缺口
- ADR-008 (ordered fail-closed preflight) 无对应 References 文档 → 建议从实现中提取
- `core/networking/k8s-gateway-api-guide.md` verified:false → 项目使用了但未标记验证

### 跨项目模式
- 3 个项目选用 Connect + Wire → 已标注为强推荐模式
```

此输出在 Agent 执行 Round 1 或 Round 2 时自动注入 `[Project Context]`，Agent 第一屏即见项目技术全景。
