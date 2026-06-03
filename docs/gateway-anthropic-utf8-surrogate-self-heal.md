---
title: Anthropic 请求体 UTF-8 / 孤立代理（lone surrogate）预过滤自愈
date: 2026-06-03
scope: backend (gateway Anthropic forward / passthrough / count_tokens 路径)
status: shipped
upstream_issues:
  - anthropics/claude-code#60168  # Please finally fix the JSON low surrogate issue
  - anthropics/claude-code#63885  # surrogates not allowed when dragging macOS screenshots
  - anthropics/claude-code#64777  # str is not valid UTF-8: surrogate
ledger:
  - .cache/anthropic/cc-fixes.json
  - .cache/anthropic/cc-fact-checks.json
related:
  - docs/global/tokenkey-opc-transformation-plan.md  # rule §5 上游隔离边界
---

# 概述

TokenKey 转发的 Claude Code 会话会因为**请求体里出现孤立 UTF-16 代理（unpaired
surrogate）或非法原始 UTF-8 字节**，被上游 Anthropic API 以硬 400 拒绝：

```
The request body is not valid JSON: str is not valid UTF-8:
surrogate code point ... is not allowed
```

最常见的触发场景：

- macOS 上把截图直接拖进 Claude Code（#63885）；
- 粘贴的二进制 / 多语言混排 blob 里带了截断的多字节 rune（#64777）；
- 历史复发到用户在 issue 标题里直接写「**Please finally** fix the JSON low
  surrogate issue」（#60168）。

这类 400 会**直接 brick 用户当前会话**——客户端自己修不掉（它就是产生方），而网关
是唯一能在请求到达 Anthropic 之前看到并修复字节流的位置。

# 设计

与既有的 `thinking.type=adaptive`（#514）、空文本块剥离、签名块重试等自愈一脉相承，
但本修复是 **pre-flight（预过滤）** 而非 retry-on-400：孤立代理在发往 Anthropic 的
JSON 里**永远非法**，不存在需要保留的合法形态，所以在转发前清洗即可，省掉那一次注定
失败的往返。

## 核心：`TkSanitizeRequestBodyUTF8`

位于 `backend/internal/service/gateway_request_tk_utf8.go`（`*_tk_*.go` 伴生文件，
按规则 §5 把 fork-only 的请求改写逻辑挡在上游 `gateway_request.go` 之外，保持
`git merge upstream/main` 零冲突）。

两段式、纯函数、零副作用：

1. **Stage 1 — JSON 转义层**：扫描 `\uHHHH` 转义，把**孤立**的高/低代理（以及没有
   配对低代理的高代理）改写为 `�`（U+FFFD）。合法的高+低代理对、转义反斜杠
   `\\uXXXX`、其它 BMP 转义**逐字节保留**。
2. **Stage 2 — 原始字节层**：当 `utf8.Valid` 为假时，用 `bytes.ToValidUTF8` 把每段
   极大非法字节串替换为 U+FFFD。

**安全契约**：

- 没有孤立代理、也没有非法 UTF-8 的请求 → 原切片**逐字节原样返回**，`changed=false`，
  常规流量零分配、零改写。99.99% 的请求完全不受影响。
- 一个**本来不会 400** 的请求，永远不会被本清洗器改动。
- `sanitize(sanitize(x)) == sanitize(x)`（幂等，有测试固化）。

## 接入点

`TkSanitizeRequestBody`（带日志的包装）作为预过滤步骤注入到三条 forward 路径，**紧贴
现有 `StripEmptyTextBlocks` 之前**（清洗在内层先跑，因为 `StripEmptyTextBlocks` 会把
body 当 JSON 解析）：

| 路径 | 位置 |
| --- | --- |
| `Forward` | `gateway_service.go` `StripEmptyTextBlocks(TkSanitizeRequestBody(body, account))` |
| `forwardAnthropicAPIKeyPassthroughWithInput` | 同形包装 `input.Body` |
| `ForwardCountTokens` | 同形包装（兼护 per-account upstream-error 熔断器） |

每处仅一行包裹，上游 `gateway_service.go` 只在既有的三个 body 预过滤注入点被触及。

## 可观测性

发生改写时输出一行结构化日志（`account` + 改写前后字节数），便于线上统计该问题的真实
频率——且**不再产生它本要触发的上游 400**。

# 验证

- `backend/internal/service/gateway_request_tk_utf8_test.go`：23 个表驱动子测试覆盖
  孤立高/低代理、body 末尾截断、代理对保留、转义反斜杠非转义、原始非法字节、
  组合失败、幂等性、修复后 JSON 可解析。
- `go build ./...` + `go test -tags=unit ./internal/service/` 通过。

# 台账

修复记录登记在 watchdog 读取的人工台账：`.cache/anthropic/cc-fixes.json`（标记
`fixed_in_tokenkey`）与 `.cache/anthropic/cc-fact-checks.json`（`fixed_if_all_present`
needle，命中即 `fixed_verified`）。后续同类 issue 复现前应先查这两个文件。
