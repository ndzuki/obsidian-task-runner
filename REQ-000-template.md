---
id: ""
title: ""
epic: ""
priority: P2
created: ""
updated: ""
author: ""
tags: []
---

# <!-- 标题：一句话描述要做什么 -->

## 背景 & 动机
<!-- 为什么要做这个需求？解决什么问题？给谁用？ -->
- **问题**：
- **目标用户**：
- **预期收益**：

## 功能需求

### FR-1: <!-- 功能名称 -->
<!-- 用 Given-When-Then 或 checklist 描述 -->
- **Given** <!-- 前置条件 -->
- **When** <!-- 触发动作 -->
- **Then** <!-- 预期结果 -->

### FR-2: <!-- 功能名称 -->

## 非功能性需求
<!-- 性能、安全、可用性、兼容性等 -->

| 维度 | 要求 | 指标 |
|------|------|------|
| 性能 | | |
| 安全 | | |
| 可用性 | | |
| 兼容性 | | |

## 技术约束
<!-- 语言、框架、数据库、部署环境等硬性约束 -->
- **语言/框架**：
- **数据库**：
- **部署环境**：
- **依赖服务**：
- **API 协议**：

## 验收标准
<!-- 逐条列出可验证的验收条件，task-verifier 会逐条核实 -->
- [ ] AC-1: <!-- 条件描述 -->
- [ ] AC-2: <!-- 条件描述 -->
- [ ] AC-3: <!-- 条件描述 -->

## API 规格（如涉及）
<!-- 如果需求涉及 API，在这里描述接口契约 -->

### `POST /api/v1/example`
```json
// Request
{
  "field": "value"
}

// Response 200
{
  "id": "xxx",
  "status": "ok"
}

// Response 400
{
  "error": "invalid field",
  "code": "INVALID_INPUT"
}
```

## 数据模型（如涉及）
<!-- 如果需求涉及新的数据结构，在这里定义 -->

```yaml
EntityName:
  id: string (UUIDv4)
  name: string (required, 1-200 chars)
  status: enum[active, inactive, archived]
  created_at: datetime (auto)
  updated_at: datetime (auto)
```

## 依赖 & 参考
<!-- 依赖的其他需求、参考文档、外部链接 -->
- 依赖需求：
- 参考文档：
- 外部链接：

## 风险 & 边界
<!-- 已知风险、什么不做（out of scope）、什么假设 -->
- **风险**：
- **不在范围内**：
- **假设**：
