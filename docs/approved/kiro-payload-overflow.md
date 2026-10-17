---
title: Kiro Payload Overflow Contract
status: approved
approved_by: "user (explicitly requested the overflow-policy fix after direct Kiro probing)"
approved_at: 2026-10-17
owners: [tk-gateway]
risk: high
related_prs: [2688]
---

# Kiro Payload Overflow Contract

## Evidence And Decision

The same Kiro account and `claude-opus-5.5` model accepted serialized request
bodies of 921,600 and 950,000 bytes, then rejected 999,999, 1,000,000,
1,048,576, and 2,435,861 bytes with HTTP 400
`CONTENT_LENGTH_EXCEEDS_THRESHOLD`. These probes establish an observed boundary
between 950,000 and 999,999 bytes, not a stable provider-published limit.

TokenKey therefore retains the existing 900 KiB (921,600-byte) translated-wire
limit. It leaves 28,400 bytes below the largest accepted probe and avoids
depending on an undocumented boundary that may move with upstream protocol
changes. The limit applies to the final JSON body after Claude-to-Kiro
translation; it is not a limit on the client's original HTTP body.

## Approved Behavior

The translator may remove the oldest conversation history until the final Kiro
payload fits. It preserves system priming, the recent and active tool turn, and
the current message. It must never shorten or replace the current user message,
and it must not silently alter fixed current-turn tool definitions to force the
request under the limit.

If the payload remains above 900 KiB after removable history is exhausted, the
gateway rejects it locally before any upstream call:

| Client API | HTTP status | Error type |
| --- | --- | --- |
| `/v1/messages` | 413 | `request_too_large` |
| `/v1/chat/completions` | 413 | `request_too_large` |

The error message may report the serialized size and limit, but must not include
request content. The typed rejection is caller-owned: it is not wrapped as an
`UpstreamFailoverError`, does not select another account, and does not penalize
account health.

## Scope Boundary

This decision does not raise the Kiro wire limit, summarize history, truncate
the latest message, or add provider fallback. It does not change authentication,
billing, scheduling, persistence, database schema, or deployment state. A future
limit change requires new direct evidence because Kiro does not publish this
threshold as a stable contract.

## Verification

The linked Story covers history truncation, current-message and UTF-8 integrity,
fixed-payload overflow, zero upstream calls, non-failover typing, and both public
error envelopes: `.testing/user-stories/stories/US-065-kiro-payload-overflow.md`.
