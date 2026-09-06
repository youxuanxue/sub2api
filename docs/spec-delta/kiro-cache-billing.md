---
title: Kiro OAuth 本地前缀指纹 cache_read 计费
status: draft
---

# Kiro cache billing（本地前缀指纹）

## Background

Kiro 上游只返回 credits，不返回 Anthropic 式 `cache_read` / `cache_creation` telemetry。TokenKey 将 Kiro 流量映射到 Claude 价表后，整段 prompt 一直按满价 `input` 结算，多轮对话明显偏贵。

## Delta

- **ADDED**：开关 `gateway.kiro_cache_billing.enabled`（默认 `true`；仅字面量 `false` 关闭）。
- **ADDED**：按 `account_id + model + conversationId` 维护 prompt 前缀指纹；同会话次轮起，匹配前缀按 `cache_read` 计价。
- **MODIFIED**：默认开启后 `Usage.InputTokens` 仅为非缓存余量；`CacheReadInputTokens` 为匹配前缀 × 90% haircut。
- **REMOVED**：无。显式关闭时行为与原来一致（cache 字段恒 0）。

## Scenarios

1. **正向（次轮降价）**：同 conversation 首轮写指纹；次轮共享 ≥1024 token 前缀 → `cache_read > 0`，`input + cache_read = 总估 token`。
2. **负向（首轮 / 异会话）**：首轮或不同 conversationId → `cache_read = 0`，全额 input。
3. **负向（过小前缀）**：匹配前缀 < 1024 → 不给 read。
4. **回归**：开关设为 `false` 时 SSE / ForwardResult 不含 cache 字段，计费与旧行为一致。

## Validation

```bash
cd backend
go test -tags=unit ./internal/integration/kiro/ -run 'CacheUsage|ResolveCacheTTL|CacheSessionKey'
go test -tags=unit ./internal/service/ -run 'KiroCacheBilling|KiroPromptUsage|KiroSSEEncoder_Message'
```

上线后默认生效。异常时将 `gateway.kiro_cache_billing.enabled=false` 即可回滚。
