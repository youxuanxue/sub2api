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

协议适配器仍负责解析供应商 body、SSE event 和账号配置，因为这些输入具有真实协议差异；适配器只能将结果归一成事实型 failure semantic，不能用 retry / stop / failover 命名 semantic，也不能自行定义 semantic 或 status 对应的 failover 布尔值。handler 的 `FailoverState.HandleFailoverError` 继续独占执行循环，本设计不改变重试次数、账号处罚、sticky 清理、最终错误透传或已输出流的重放边界。

handler 消费的 `UpstreamFailoverError.ShouldRetryNextAccount` 也必须调用同文件的 `classifyGatewayFailoverError`，确保直接构造的 legacy failover error 和显式 shared credential failure 最终仍经过全局 owner。`UpstreamFailoverError` 不暴露 retry / stop action 字段；legacy 空 scope 保持历史 retry 行为，显式 scope 作为事实由全局 policy 映射。新代码用 `applyGatewayFailoverSemantic` 提交 profile + semantic。

## 输入模型

全局 policy 接收：

- `Profile`：Generic、OpenAI、Google、Grok、NewAPI bridge 或 OpenAI passthrough 传输契约。
- `Semantic`：未分类、跨账号共享故障、账号特定故障、瞬时故障；这些值只描述事实，不携带 retry / stop verdict。
- `StatusCode`：上游 HTTP 或从 SSE 推导出的语义状态码。
- passthrough 所需的账号类型和账号自定义 pool retry 命中结果。

判定顺序固定为：未知 profile/semantic fail closed；已分类 semantic 优先于 status；未分类时进入同一文件内的 profile matrix。平台差异因此是一个全局策略的显式输入，不再是散落在各 adapter 中的独立 policy。

## 行为矩阵

| Profile | 未分类时触发 failover |
| --- | --- |
| Generic | `401, 402, 403, 424, 429, 529, >=500` |
| OpenAI / Grok | Generic 加 `405` |
| Google | `401, 403, 429, 529, >=500` |
| NewAPI bridge | `401, 402, 429, 502, 503, 504, 529` |
| OpenAI passthrough | 账号配置命中；所有账号 `429/529`；API key 账号 `500/502/503/504/520..524` |

`shared fault` 由全局 policy 判定为停止换号，即使 transport status 是 5xx；`account fault` 和 `transient fault` 由全局 policy 判定为允许换号，即使 transport status 是 4xx。OpenAI 内容策略、context window、capability-scope 401 和 Grok entitlement/content policy 保持 terminal。OpenAI access-state、request-body-too-large account mismatch、容量/处理瞬时错误，Google project compatibility 400、Grok runtime compatibility，以及 NewAPI arrears 保持 failover。

## 不做什么

- 不合并账号处罚与 failover：是否 cooldown/disable 仍由现有 account health owner 决定。
- 不把所有平台状态码强行统一；例如 NewAPI 普通 `500` 继续 terminal，避免一次请求耗尽 provider pool。
- 不改变 `response.failed` 与通用 SSE `error` 的不同默认值：前者未知失败继续 failover，后者未知 client error 继续 terminal。
- 不增加配置项、数据库字段或 Web surface。

## 守卫

`gateway_failover_policy_test.go` 覆盖全局矩阵、semantic 优先级、未知输入 fail closed、handler runtime choke point、passthrough 账号矩阵和各 adapter 回归。`scripts/sentinels/gateway-tk.json` 锚定 owner、测试和所有 adapter 接线；`check-gateway-tk.py` 会对去除注释和字面量后的整个 backend 生产 Go 树扫描完整函数体，要求任意 `shouldFailover` / `retryNextAccount` facade 以及 `ShouldRetryNextAccount` runtime choke point 委托全局 owner，并禁止重新持有 decision bool、retry / stop action 或 verdict 型 semantic。上游 merge 或新增平台若恢复私有 policy，preflight 必须失败。

## 输出前恢复与缓冲断流修正（2026-10-19）

授权锚点：feng 在本会话要求继续按已提出的优先级修复上游错误，并明确“继续”；本节记录该范围。合并与生产发布仍需独立授权。

- Responses 的 `response.metadata` 与 created/in_progress 一样属于状态信息：先缓冲，成功输出时按原顺序发送；不能凭它阻断已有的输出前故障转移。答案、工具输出或已发送的 reasoning 仍阻断重放。
- 等待响应头时生成的 message_start/ping/comment 使用已有 keepalive 字节 owner。停掉保活写入后，仅保活的响应允许 transport failover；重试只保留一个 Anthropic message_start。客户取消、插件已发送请求保护及现有切换次数上限保持生效。
- 四条 Anthropic SSE 转非流式 JSON 路径在没有完成事件时不得返回正常 stop/completed。缓冲区只有 thinking/redacted_thinking 或空 text 时允许现有故障转移；已有非空 text、工具或未知内容块时返回终止错误、记录 stream_truncated，不重放、不计作成功回答。
- NewAPI HTTP 500 明确携带 `bad_response_body` code/type 时归一为 account fault，由全局 policy 决定换号，并复用短期候选失败惩罚；不新增 disable/cooldown。普通 500 与客户端 400 不扩大重试范围。

验证：`upstream_output_recovery_tk_test.go` 覆盖 metadata、保活、四条缓冲路径和错误分类；`candidate_chat_messages_tk_test.go` 通过 handler 验证缓冲断流的最终 502、thinking-only 恢复与仅成功账号计费；`header_wait_recovery_tk_test.go` 连接真实 executor、保活、transport 分类和 handler 守卫，验证恢复/耗尽时单一消息生命周期。既有 US-047 错误帧、EOF、读失败及 idle timeout 测试同步采用“终止错误而非部分成功”契约；完整终态仍按原路径成功。
