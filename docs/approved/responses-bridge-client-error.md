---
title: Responses bridge 参数错误分类
status: approved
approved_by: "user (本会话：同意。继续。批准此前提出的 Responses 参数错误修复方案)"
risk: high
---

Ali Token Plan 的 DeepSeek Responses 请求在上游实际收到 max_output_tokens=8 时返回 400，要求该值至少为 16。此前两个 Responses handler 未识别 NewAPIRelayError，最终回写通用 502，隐藏了参数诊断。

本次批准的范围：两个 handler 复用同一桥接客户端错误 owner，尚未提交的 Responses 请求保留上游 400 和脱敏后的 message/type/code/param；已发送心跳的 SSE 使用现有 response.failed 终止事件，保留 code/message。取消继续使用 499，已提交的完整响应不追加内容。

仅处理桥接的上游错误类型和 HTTP 400；本地配置、凭据、欠费、429 及 5xx 继续使用既有分类、处罚和 failover。不得自动增大或删除调用方 token 上限，不调整全局最小 token 数。调用方需自行将该 Ali Responses 请求的上限设为至少 16，并停止重试确定性参数错误。

Owners：服务端错误提取在 backend/internal/service/newapi_bridge_usage.go；共享 Responses 写出在 backend/internal/handler/tk_newapi_relay_error.go；两个 handler 的已有 fallback 保留取消、keepalive 和提交守卫。

验收由 TestResponsesRelayClientError 覆盖真实 NewAPI 错误解析、两个入口、JSON、心跳 SSE、已提交响应、取消以及上游凭据和本地配置错误。服务侧回归覆盖脱敏和既有 failover/处罚边界。本次无 Web UI 变化，不执行 UI e2e；不修改生产配置或部署。
