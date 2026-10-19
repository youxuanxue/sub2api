---
title: Gateway Failover Policy Single Source of Truth
status: approved
approved_by: "feng (conversation directive, 2026-09-03; 500-default amendment 2026-10-19)"
approved_at: 2026-10-19
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

「换了也没用 → 停换号」的事实目录（SharedFault catalog）单一 owner：

```text
docs/approved/gateway-failover-policy-ssot.md  §SharedFault
backend/internal/service/gateway_failover_shared_fault_ssot_tk.go
  gatewayCompatSharedFault(message, body) bool
```

适配器只能调用该 catalog helper（或提交已归一的 `gatewayFailureSemanticSharedFault`），不得在各 channel 私自维护第二份「停换号」短语表。

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
| NewAPI bridge | `401, 402, 429, 529, >=500` |
| OpenAI passthrough | 账号配置命中；所有账号 `429/529`；API key 账号 `500/502/503/504/520..524` |

原则：用户可见恢复优先。未分类的账号/中继 5xx 默认换号；只有证明「下一台也必然同样死」才停。

`shared fault` 由全局 policy 判定为停止换号，即使 transport status 是 5xx；`account fault` 和 `transient fault` 由全局 policy 判定为允许换号，即使 transport status 是 4xx。OpenAI access-state、request-body-too-large account mismatch、容量/处理瞬时错误，Google project compatibility 400、Grok runtime compatibility，以及 NewAPI arrears 保持 failover。

防池子被刷穿靠执行层既有机制（`failedAccountIDs`、hop 上限、饱和/失败反馈降权、request-scoped 5xx 不轻易 `SetError`），不靠把 NewAPI 普通 `500` 设为 terminal。

## SharedFault（换了也没用 → 停换号）

下列事实必须归一为 `gatewayFailureSemanticSharedFault`，由全局 policy 停换号。新增形态只能追加本表与 `gateway_failover_shared_fault_ssot_tk.go`，禁止在 adapter 内另起短语表。

| 类别 | 判定入口（代码） | 说明 |
| --- | --- | --- |
| 内容安全 / usage policy / cyber policy | `isOpenAISafetySessionBlockFault`（经 `gatewayCompatSharedFault`） | 同 prompt 换号也会再炸 |
| Context window 超限 | `isOpenAIContextWindowError`（经 `gatewayCompatSharedFault`） | 请求本身超窗，换号无益 |
| OpenAI-compat 策略/安全/非法请求标记 | `isOpenAINonRetryableClientError`（经 `gatewayCompatSharedFault`） | 含 content_policy / safety / invalid_request 等；中继误标成 5xx 时仍停 |
| Grok entitlement / content policy | Grok adapter → SharedFault | 保持既有 Grok body 分类，最终仍交全局 owner |
| 通用 400「普通 invalid request」 | `gateway400FailureSemantic` → SharedFault | 与账号能力/模型不匹配（AccountFault）区分 |
| 显式 scope Provider/Request | `UpstreamFailoverError` scope 映射 | legacy 空 scope 仍 retry |

OpenAI-compat / NewAPI bridge 在提交 observation 前必须调用 `gatewayCompatSharedFault`；命中则 semantic=`SharedFault`，不得再依赖 status 矩阵误换号。

## 不做什么

- 不合并账号处罚与 failover：是否 cooldown/disable 仍由现有 account health owner 决定。NewAPI bridge 对普通 5xx 保持 request-scoped 换号，不因模糊 500 永久禁用账号。
- 不为「防刷池」把 NewAPI 普通 `500` 设回 terminal；停换号只走 SharedFault catalog。
- 不改变 `response.failed` 与通用 SSE `error` 的不同默认值：前者未知失败继续 failover，后者未知 client error 继续 terminal。
- 不增加配置项、数据库字段或 Web surface。
- 不把 NVIDIA「万物 failover」推广到全部 NewAPI 通道。

## 守卫

`gateway_failover_policy_test.go` 覆盖全局矩阵、semantic 优先级、未知输入 fail closed、handler runtime choke point、passthrough 账号矩阵和各 adapter 回归。`gateway_failover_shared_fault_ssot_tk_test.go` 锚定 SharedFault catalog。`scripts/sentinels/gateway-tk.json` 锚定 owner、测试和所有 adapter 接线；`check-gateway-tk.py` 会对去除注释和字面量后的整个 backend 生产 Go 树扫描完整函数体，要求任意 `shouldFailover` / `retryNextAccount` facade 以及 `ShouldRetryNextAccount` runtime choke point 委托全局 owner，并禁止重新持有 decision bool、retry / stop action 或 verdict 型 semantic。上游 merge 或新增平台若恢复私有 policy，preflight 必须失败。

## 输出前恢复与缓冲断流修正（2026-10-19）

授权锚点：feng 在本会话要求继续按已提出的优先级修复上游错误，并明确“继续”；本节记录该范围。合并与生产发布仍需独立授权。

- Responses 的 `response.metadata` 与 created/in_progress 一样属于状态信息：先缓冲，成功输出时按原顺序发送；不能凭它阻断已有的输出前故障转移。答案、工具输出或已发送的 reasoning 仍阻断重放。
- 等待响应头时生成的 message_start/ping/comment 使用已有 keepalive 字节 owner。停掉保活写入后，仅保活的响应允许 transport failover；重试只保留一个 Anthropic message_start。客户取消、插件已发送请求保护及现有切换次数上限保持生效。
- 四条 Anthropic SSE 转非流式 JSON 路径在没有完成事件时不得返回正常 stop/completed。缓冲区只有 thinking/redacted_thinking 或空 text 时允许现有故障转移；已有非空 text、工具或未知内容块时返回终止错误、记录 stream_truncated，不重放、不计作成功回答。
- NewAPI bridge 未分类 `>=500`（含 `bad_response_body` HTML/畸形响应）默认换号，与 OpenAI/Google 对齐；`bad_response_body` 另归一为 account fault 以复用短期候选失败惩罚，不新增 disable/cooldown。SharedFault catalog（内容/安全/超窗等）仍停换号；客户端 400 不扩大重试范围。

验证：`upstream_output_recovery_tk_test.go` 覆盖 metadata、保活、四条缓冲路径和错误分类；`candidate_chat_messages_tk_test.go` 通过 handler 验证缓冲断流的最终 502、thinking-only 恢复与仅成功账号计费；`header_wait_recovery_tk_test.go` 连接真实 executor、保活、transport 分类和 handler 守卫，验证恢复/耗尽时单一消息生命周期。既有 US-047 错误帧、EOF、读失败及 idle timeout 测试同步采用“终止错误而非部分成功”契约；完整终态仍按原路径成功。`gateway_failover_shared_fault_ssot_tk_test.go` / `TestTkBridgeUpstreamShouldFailoverAfterPenalty_Unclassified5xxFailovers` 锚定 500-default 与 SharedFault catalog。
