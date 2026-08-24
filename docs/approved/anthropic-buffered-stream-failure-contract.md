---
title: Anthropic Buffered Stream Failure Contract
status: approved
approved_by: "feng (conversation approval, 2026-08-24)"
approved_at: 2026-08-24
authors: [agent]
created: 2026-08-24
related_stories: [US-047]
---

# Anthropic Buffered Stream Failure Contract

## 目标

修复 OpenAI Chat Completions / Responses 转 Anthropic 上游时，非流式客户端由 SSE 缓冲组装响应的失败语义。上游虽然返回 HTTP 200，但可能在 SSE 内发送 `error`、中途 EOF、读错误或数据间隔超时；这些都必须进入既有账号健康、failover 和 Ops 归因链路，不能伪装成网关内部故障或干净成功。

## 行为矩阵

| 上游结果 | 客户端行为 | 调度与健康 | Ops |
| --- | --- | --- | --- |
| 完整终止事件 | 返回正常 HTTP 200 | 不 failover | 不记录失败 |
| 尚无可用内容时出现 `error` | 当前账号尝试失败；failover 耗尽后返回映射后的真实错误 | 复用既有账号健康与 failover | 记录 provider、账号和事件原因 |
| 尚无可用内容时超时、读错误或未完整 EOF | 当前账号尝试失败；failover 耗尽后返回 502 | 复用既有账号健康与 failover | 记录 provider、账号和中断类型 |
| 已有可用内容后出现 `error`、超时、读错误或未完整 EOF | 保留部分 HTTP 200，不重放 | 不 failover，避免重复计费和回答漂移 | 标记 `stream_truncated`，计入 SLA |
| 只有 `message_start`、没有内容，随后失败 | 视为“尚无可用内容” | failover | 不得返回空 HTTP 200 |

“可用内容”指已组装出至少一个 Anthropic content block；只有 `message_start` 不算可用内容。完整终止以 `message_delta.stop_reason` 或 `message_stop` 为准。

## 错误映射

failover 耗尽后保留上游消息，并按现有 OpenAI-compatible 错误表面返回：

| Anthropic error type | 对客 HTTP | 对客错误类型 |
| --- | --- | --- |
| `invalid_request_error` | 400 | `invalid_request_error` |
| `authentication_error` / `permission_error` | 502 | `upstream_error` |
| `not_found_error` | 404 | `not_found_error` |
| `request_too_large` | 413 | `request_too_large` |
| `rate_limit_error` | 429 | `rate_limit_error` |
| `overloaded_error` | 503 | `overloaded_error` |
| 未知服务端错误 | 502 | `server_error` |

错误事件、超时和未完整 EOF 均不得在 service 层提前写响应；service 返回 `UpstreamFailoverError`，由现有 handler 在账号切换耗尽后统一写最终响应。部分内容路径除外：它已经产生可计费内容，仍返回单次尝试的部分结果。

## Ops 与账号身份

每个新增 upstream event 必须填写：

- `Platform`
- `AccountID`
- `AccountName`
- `UpstreamStatusCode`
- `UpstreamRequestID`
- `Kind`、`Reason`、`Message`

这样平台级 `skip_monitoring` 规则、账号排障、provider SLA 和 failover attempt 链才使用同一份事实。原始错误 payload 仍只在 `log_upstream_error_body` 开启时持久化到 detail；failover 内存对象可携带经过上游错误读取上限约束的错误体，用于最终映射与透传规则。

## 范围

本次覆盖四条缓冲组装路径：Anthropic 平台的 Chat Completions / Responses，以及国产供应商 native-Anthropic 的 Chat Completions / Responses。

不修改流式客户端已经提交响应后的重放策略，不新增配置开关，不修改数据库或前端字段。现有 sentinel 继续锚住单一 owner 与四个调用点。

## 验证

- `message_start -> error` 必须进入 failover，不能返回空 200。
- error-only、空流、未完整 EOF、读错误和 native idle timeout 在无内容时返回 `UpstreamFailoverError`。
- 同类故障发生在已有 content block 之后时保留 200，并带 `CountTowardsSLA=true` 的 stream failure。
- 404 / 413 的错误类型不再落成 `server_error`。
- upstream event 带完整平台和账号身份。
- 完整流不产生 upstream event 或 stream failure。
