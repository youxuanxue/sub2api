---
title: Gateway Failover Policy Single Source of Truth
status: approved
approved_by: "feng (conversation directive, 2026-09-03)"
approved_at: 2026-09-03
authors: [codex]
created: 2026-09-03
related_stories: [US-049]
---

# Gateway Failover Policy Single Source of Truth

## 目标与边界

所有 gateway、平台和 channel adapter 共用一个“是否切换到下一账号”的 owner：

```text
backend/internal/service/gateway_failover_policy.go
  classifyGatewayFailover(observation) -> decision.RetryNextAccount
```

协议适配器仍负责解析供应商 body、SSE event 和账号配置，因为这些输入具有真实协议差异；适配器只能将结果归一成 failure semantic，不能自行定义 semantic 或 status 对应的 failover 布尔值。handler 的 `FailoverState.HandleFailoverError` 继续独占执行循环，本设计不改变重试次数、账号处罚、sticky 清理、最终错误透传或已输出流的重放边界。

## 输入模型

全局 policy 接收：

- `Profile`：Generic、OpenAI、Google、Grok、NewAPI bridge 或 OpenAI passthrough 传输契约。
- `Semantic`：未分类、确定性请求失败、账号特定失败、供应商瞬时失败。
- `StatusCode`：上游 HTTP 或从 SSE 推导出的语义状态码。
- passthrough 所需的账号类型和账号自定义 pool retry 命中结果。

判定顺序固定为：未知 profile/semantic fail closed；已分类 semantic 优先于 status；未分类时进入同一文件内的 profile matrix。平台差异因此是一个全局策略的显式输入，不再是散落在各 adapter 中的独立 policy。

## 行为矩阵

| Profile | 未分类时触发 failover |
| --- | --- |
| Generic | `401, 402, 403, 424, 429, 529, >=500` |
| OpenAI / Grok | Generic 加 `405` |
| Google | `401, 403, 429, 529, >=500` |
| NewAPI bridge | `401, 402, 429, 502, 503, 504` |
| OpenAI passthrough | 账号配置命中；所有账号 `429/529`；API key 账号 `500/502/503/504/520..524` |

`terminal request` 无条件停止换号，即使 transport status 是 5xx；`retryable account` 和 `retryable transient` 无条件允许换号，即使 transport status 是 4xx。OpenAI 内容策略、context window、capability-scope 401 和 Grok entitlement/content policy 保持 terminal。OpenAI access-state、request-body-too-large account mismatch、容量/处理瞬时错误，Google project compatibility 400、Grok runtime compatibility，以及 NewAPI arrears 保持 failover。

## 不做什么

- 不合并账号处罚与 failover：是否 cooldown/disable 仍由现有 account health owner 决定。
- 不把所有平台状态码强行统一；例如 NewAPI 普通 `500` 继续 terminal，避免一次请求耗尽 provider pool。
- 不改变 `response.failed` 与通用 SSE `error` 的不同默认值：前者未知失败继续 failover，后者未知 client error 继续 terminal。
- 不增加配置项、数据库字段或 Web surface。

## 守卫

`gateway_failover_policy_test.go` 覆盖全局矩阵、semantic 优先级、未知输入 fail closed、passthrough 账号矩阵和各 adapter 回归。`scripts/sentinels/gateway-tk.json` 锚定 owner、测试和所有 adapter 接线；`check-gateway-tk.py` 还会扫描整个 service package，要求今后新增的任意 `shouldFailover` facade 直接调用全局 owner，并禁止 classification structure 重新持有 `ShouldFailover bool` 字段。上游 merge 或新增平台若恢复私有 policy，preflight 必须失败。
