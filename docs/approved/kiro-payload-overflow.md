---
title: Kiro Payload Overflow Contract
status: approved
approved_by: "user (explicitly requested the overflow-policy fix after direct Kiro probing; 2026-10-22 requested the measured kiro-lb#60 and Kiro-Go#119 reduction)"
approved_at: 2026-10-17
owners: [tk-gateway]
risk: high
related_prs: [2688, 2941]
revised: 2026-11-20（删直探阶梯/账号分层 Evidence；保留 900 KiB 行为契约）
---

# Kiro Payload Overflow Contract

## Limit decision

保留译后 wire **900 KiB（921,600 字节）** 本地上限：低于直探观测到的最低上游
边界，且不依赖未公布、按账号/模型漂移的阈值。上限量的是 Claude→Kiro 翻译后的
最终 JSON，不是客户端原始 HTTP body。一次性直探阶梯与账号分层表已删。

## Approved Behavior

The translator reduces a payload that exceeds 900 KiB in this order:

1. Remove oldest history screenshots one at a time, keep the surrounding turn
   text, and append an omission note. System priming is not stripped.
2. Remove the oldest conversation history turns. Preserve system priming, the
   recent and active tool turn, and the current user prose.
3. If the payload is still over, clip the largest tool-result text to an 8 KiB
   head and a 4 KiB tail, including tool output already folded into message
   content after the `Tool results:` marker. Leave user prose outside that
   marker unchanged.
4. If the payload is still over, drop oldest screenshots on the current message.
5. When steps 3–4 bring the payload under the limit, restore history turns that
   were removed only to make room for that current-message bulk. Drop turns
   again only if the restored history still does not fit.

Requests that are already under 900 KiB stay byte-identical. The translator
must not shorten current user prose and must not alter tool definitions to
force the request under the limit. JPEG recompression and always-on RTK output
filters are not part of this policy: the former changes bytes Kiro validates,
and the latter rewrites fitting tool output.

This reduction was requested on 2026-10-22 after measuring
minpeter/kiro-lb#60 and Quorinex/Kiro-Go#119 against the observed
1,130,062-byte 413. History-image removal is the part of kiro-lb#60 that
kept text turns. Kiro-Go's RTK filters were not ported. The gated tool-result
clip is the measured replacement for their smart truncate, which does not
shrink a single oversized line.

If the payload remains above 900 KiB after the reductions above, the gateway
rejects it locally before any upstream call:

| Client API | HTTP status | Error type |
| --- | --- | --- |
| `/v1/messages` | 400 | `invalid_request_error` |
| `/v1/chat/completions` | 400 | `invalid_request_error` |

Revised 2026-10-27 (was 413 `request_too_large` on both rows). In Anthropic's
own contract 413 `request_too_large` is the per-endpoint HTTP body byte ceiling
— 32 MB on Messages, rejected at the edge before reaching the API — while a
prompt that outgrows the model's input limit is 400 `invalid_request_error`
whose message leads with `prompt is too long`. This local guard is the second
case, so it now reports the second shape. Returning 413 gave Claude Code a
status it only ever sees for a 32 MB body: it rendered as the model being
unavailable, and callers switched models instead of shortening the
conversation. An upstream 413 passed through from a supplier keeps 413
`request_too_large` unchanged; only the local input-limit guard moved.

The client-facing message must not include request content, the serialized
size, the limit, the fulfilling supplier, or the fact that history truncation
was attempted — none of which a caller can act on. It states that the prompt is
too long and names the one available action (compact the conversation or start a
new session). The full oversize diagnostics stay internal via
`TkAnnotateRequestTooLargeOps` and the ops error log. The typed rejection is
caller-owned: it is not wrapped as an `UpstreamFailoverError`, does not select
another account, and does not penalize account health.

## Scope Boundary

不抬限、不摘要历史、不加跨供应 failover。缩短 current user prose 仅允许下一节
默认关闭的账号开关。改 `maxPayloadBytes` 须：按模型（非全局）、经 `/v1/messages`
网关路径复测、单独审批。kiro-lb 容量实验不是抬限依据。一次性账号分层直探表已删。

## Gated exception (in force when the account flag is on)

A **gated** exception for Claude Code auto-mode classifier transcripts allows
structured whole-record deletion of old tool-process lines inside current user
prose when account Extra `kiro_auto_mode_transcript_trim=true`. The contract is
[`kiro-auto-mode-transcript-trim.md`](kiro-auto-mode-transcript-trim.md)
(`status: shipped`). It has been in the released gateway since 1.8.329
(`#2933` and follow-ups; public overflow copy is the 400 envelope from
`#2941`). Missing or non-boolean Extra stays off, and that default-off path
still obeys this document’s “must not shorten current user prose” rule.
Head/tail truncation, summarization, and cross-platform overflow failover stay
out of scope here.

## Verification

The linked Story covers history truncation, current-message and UTF-8 integrity,
fixed-payload overflow, zero upstream calls, non-failover typing, and both public
error envelopes: `.testing/user-stories/stories/US-065-kiro-payload-overflow.md`.
