---
title: Claude 长上下文错误恢复契约
status: approved
approved_by: "user（本会话同意聚焦复审方案）"
approved_at: 2026-10-13
owners: [tk-gateway]
risk: high
---

# Claude 长上下文错误恢复

## 审批范围

用户批准：超限可被 Claude Code 识别、已经开始 SSE 后的错误可见、失败不得误记为 Recovered；用 Claude Code 2.1.283 实测自动恢复及手动 compact。Web impact: none。

不增加容量硬拦截、供应商自动切换、网关历史裁剪或总结；它们分别需要可信容量证据及独立的内容完整性/成本决策。无数据库迁移，不改计费、调度和重试策略，不部署。

## 行为与 owner

`writeAnthropicError` 是 Messages 兼容链路共享错误入口，委托 `gateway_anthropic_error_recovery_tk.go` 实现：

- 仅 HTTP 400 + invalid_request_error + 已证实的完整中文超限句式（允许上游 cch_session_id/request id 后缀）添加 `prompt is too long: ` 前缀。保留原文，不编造 token 数量。英文、其他状态码及近似/引用文案原样返回。
- 原生 Messages 400 的已观测 `error.type: "<nil>"` 仅在上述完整句式命中时修正为 invalid_request_error；其他明确错误类型不改判。普通原生 400 在响应提交前保留原始 body，SSE 内保留有效错误附加字段并补全事件 envelope。
- 响应尚未提交时维持 HTTP 状态及 Anthropic JSON 错误格式。
- 响应已经提交为 SSE 时写一个标准 error 事件并 Flush，通过既有 `MarkOpsStreamFailure` 记录逻辑状态及上游证据；不向 SSE 追加裸 JSON，不发送成功终态。
- Ops 消费既有带内失败 owner：失败落为逻辑错误状态；真正成功的后续尝试仍可记录 recovered telemetry。QA raw 的 HTTP 状态代表实际 wire 状态，不作为业务成功证明。

无新增配置。协议输出与监控标记在同一入口执行，避免调用者遗漏。sentinel 锚住委托及实现。

## 证据与验收

生产 2026-10-12 user16 / tokensea / claude-fable-5-1：截图请求最终 400；前一次请求先 ping 后裸 JSON 错误，HTTP 200 被误记 Recovered。中文错误不匹配 Claude Code 2.1.283 的 prompt-too-long 识别。等待后恢复的供应商内部原因尚未证明。

- 正向：即时中文超限 JSON、心跳后的标准 SSE error，原始供应商诊断仍保留。
- 负向：其他 400、非 400、引用与追加非诊断后缀不误匹配；无未授权重试/裁剪。
- 回归：正常成功及真实失败后恢复不误判；非流式错误保持 JSON。
- 客户端：本地受控上游 + 实际 2.1.283，区分错误识别、恢复尝试、后续有效回复；验证自动恢复和手动 compact，失败必须明确报告。不得用静态字符串检查代替运行。

测试映射见 `.testing/user-stories/stories/US-062-claude-context-error-recovery.md`。
