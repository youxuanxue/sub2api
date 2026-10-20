---
title: Response usage.cost（推理响应带回标准官方价）
status: approved
approved_by: "user (2026-09-30 conversation: cost 应为标准官方价格而非 actual_cost; 2026-10-20 conversation: 修复已确认的计费缺陷并开 PR)"
approved_at: 2026-09-30
created: 2026-09-29
authors: [agent]
risk: high
related_prs: ["#2384", "#2388"]
---

# Response usage.cost（推理响应带回标准官方价）

## Intent

Universal Key / 直连 Key 的推理响应在已有 `usage` token 字段上，追加本笔用量的
**标准官方价** `usage.cost`（USD number），便于客户侧按牌价对账；客户实扣
（分组倍率 / 高峰因子等）仍只落在账本 `actual_cost`，不放进响应。

## Contract

- **字段**：在已有 `usage` 对象上增加 `cost`（float64，USD）。语义 = 用量账本
  `total_cost`（倍率前标准官方价 / 价格表结算价），**不是**客户实扣 `actual_cost`，
  也不是上游原价拆分。
- **非流式**：整包 JSON 的 `usage.cost`。
- **流式**：
  - OpenAI Chat Completions：含 `usage` 的 chunk（通常为末帧）写入 `usage.cost`。
  - Anthropic Messages：在 `message_delta` 的 `usage` 上写入（此时已具备完整用量）；
    `message_start` 不写（输入未齐）。
- **缺省**：无 `usage`、无法算定（缺鉴权快照 / 非 token 计价媒体等）时 **不新增**
  `cost` 字段，不发明 0。
- **一致性**：响应里的 `cost` 与随后落库的 `usage_logs.total_cost` 同源结算；响应侧
  预计算的完整 `CostBreakdown` 挂在 ForwardResult 上供 RecordUsage 复用（实扣仍走
  `ActualCost`）。粘性会话 `ForceCacheBilling`（input→cache_read）必须在响应预览侧
  同样应用后再 settle。

## Out of scope（v1 不做）

- OpenAI Responses / 除下述 NewAPI bridge 外的非 raw Chat Completions 写出路径（后续追加）。
- 新响应头、HTTP trailer、新查询 endpoint。
- `cost_details` / upstream 成本拆分。
- 改写客户端可见的 token 数字。
- 强制媒体 / 按次计费路径在响应里返回 cost。
- 在响应里暴露 `actual_cost` / 分组倍率后的实扣。
- 初始 v1 覆盖面：**Claude Messages** + **OpenAI Chat Completions raw** 的 token 路径。
- 2026-10-13 用户明确要求修复阿里成本统计漏项，授权增加 **NewAPI bridge Chat Completions**
  token 路径，沿用 `TotalCost` 语义。bridge 以 adaptor 返回的最终规范化 usage 结算，
  JSON 或流式最后一个 terminal usage 帧在写出前注入；内容 SSE 仍即时透传。
  失败、缺 usage、无法定价及媒体路径不补字段；缓冲超过 8 MiB 时原样透传并由账本正常计费。

## Owners

| 关注点 | Owner |
| --- | --- |
| 契约（本文件） | `docs/approved/response-usage-cost.md` |
| JSON/SSE 注入 | `backend/internal/service/response_usage_cost_tk.go`（`InjectUsageCost*`；入参为 `TotalCost`） |
| Claude 客户实扣结算 | `settleClaudeCustomerFacingCost`（`gateway_usage_billing.go`）— RecordUsage 与响应预览共用 |
| OpenAI 客户实扣结算 | `settleOpenAICustomerFacingCost`（`openai_gateway_usage.go`）— 含 response_model / Free Fast；RecordUsage 与响应预览共用 |
| Claude Messages 写出 | `gateway_upstream_response.go`（注入 `TotalCost`） |
| OpenAI Chat Completions raw 写出 | `openai_gateway_chat_completions_raw.go`（注入 `TotalCost`） |
| NewAPI bridge Chat 写出 | `openai_gateway_bridge_dispatch.go` + `response_usage_cost_bridge_tk.go`（等待 canonical usage、调用共享 preview、返回 `PrecomputedCost`） |
| 同请求防漂移 | 响应侧结算结果挂 `PrecomputedCost`，`RecordUsage` 复用，禁止二次编排 |

禁止在 preview 路径再镜像一份 response_model / Free Fast / 倍率编排；新增结算规则只改 `settle*`。
响应写出必须传 `CostBreakdown.TotalCost`，禁止再传 `ActualCost`。

## Validation

- 非流式 chat/messages：响应 `usage.cost` 与同请求落库 `total_cost` 一致（容差 1e-12）；
  当分组倍率 ≠ 1 时，`usage.cost` ≠ `actual_cost`。
- 流式：末帧 / `message_delta` 含 `usage.cost`；无 usage 时不出现 cost。
- 既有无 usage 的错误/中断路径行为不变。

- bridge 回归：真实 Ali adaptor 对本地供应端的流式/非流式请求验证缓存 token、
  `usage.cost == usage_logs.total_cost` 及倍率分离；分片 SSE、Flush、尾帧顺序、
  `usage:null`、无定价、断连及缓冲上限由 `response_usage_cost_bridge_tk_test.go` 验证。

## 结算快照与倍率查询失败

2026-10-20 用户要求修复 cn-prod 已确认的计费缺陷：客户倍率查询超时后按 `1` 计算费用，异步账单重查到 `0.3`，导致实际扣款和记录倍率分裂。本修复范围仅代码和 PR；不授权部署、退款或改写历史账单。

OpenAI / Claude 的预计算费用携带同次结算的倍率、计费模型及所需 token 分类元数据。响应 `usage.cost`、扣费和账单复用该快照，禁止复用旧费用却重新查倍率或模型。没有可信元数据的旧裸费用不用于扣款，重新走结算 owner。正常管理员变价仍可影响尚未成功预计算的请求。

正式结算使用 `ResolveForBilling`：仅成功缓存或成功查询得到的倍率可用于扣款；成功查询但无用户覆盖继续使用组默认倍率。查询错误向上传播，preview 不缓存错误费用，异步结算可在依赖恢复后重新查询。失败不扣款、不伪造零费用成功账单；OpenAI 的既有 deferred hold release 仍执行。准入、候选比较和余额预估保留原先的 `Resolve` 降级契约，本修复不新增请求拒绝条件。

持续依赖故障沿用现有错误返回和 handler 错误日志；当前没有持久化待结算队列和自动重放，因此已服务请求可能需要人工核账，日志不是完整可追偿账本。本 PR 不把该限制包装成免费策略，也不扩建新的队列或追偿系统。

验证：`TestResponseUsageCostSnapshotAndRateFailure` 覆盖两条结算主路径的超时不扣款、恢复后用户专属折扣、成功 preview 后缓存失效/组倍率变化时复用快照，以及长上下文缓存输入分项。既有 RecordUsage、Free Fast、response-model、强制缓存及扣费幂等测试必须继续通过。纯服务变更，无 Web surface。
