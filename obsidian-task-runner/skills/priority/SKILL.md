---
name: obsidian-task-runner-priority
description: "Headless priority assessment: read a REQ document, output strict JSON with impact/urgency/workaround dimensions and P1-P4 score."
hide: true
disable-model-invocation: true
---

**Role**: Priority Assessment Engine. Read the supplied REQ document (daemon 传入 REQ 路径；如收到 TASK 路径则以其中 `req_doc` 指向的 REQ 为准) and output only one JSON object.

## Output

```json
{
  "priority": "P2",
  "impact": "high",
  "urgency": "near_term",
  "workaround": "partial",
  "score": 6,
  "confidence": "high",
  "reason": "core path; current milestone; risky workaround",
  "recommendation": ""
}
```

> 示例核对：impact high(3) + urgency near_term(2) + workaround partial(1) = **score 6 → P2**（4-6 区间）。P1 需要 score ≥7（如 critical+immediate+none = 9）。

## Scoring

- impact: `critical=4`, `high=3`, `medium=2`, `low=1`
- urgency: `immediate=3`, `near_term=2`, `normal=1`, `deferred=0`
- workaround: `none=2`, `partial=1`, `effective=0`
- score = impact + urgency + workaround
- `7-9=P1`, `4-6=P2`, `2-3=P3`, `0-1=P4`
- `critical+immediate+none+high confidence` remains `P1` and sets `recommendation=P0`

Never output `P0` as `priority`. Output no Markdown or prose outside the JSON object.
