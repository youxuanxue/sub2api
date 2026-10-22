---
title: Kiro Payload Overflow Contract
status: approved
approved_by: "user (explicitly requested the overflow-policy fix after direct Kiro probing; 2026-10-22 requested the measured kiro-lb#60 and Kiro-Go#119 reduction)"
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
| `/v1/messages` | 413 | `request_too_large` |
| `/v1/chat/completions` | 413 | `request_too_large` |

The error message may report the serialized size and limit, but must not include
request content. The typed rejection is caller-owned: it is not wrapped as an
`UpstreamFailoverError`, does not select another account, and does not penalize
account health.

## Scope Boundary

This decision does not raise the Kiro wire limit, summarize history, shorten
current user prose, or add provider fallback. It does not change authentication,
billing, scheduling, persistence, database schema, or deployment state. A future
limit change requires new direct evidence because Kiro does not publish this
threshold as a stable contract.

## Verification

The linked Story covers history truncation, current-message and UTF-8 integrity,
fixed-payload overflow, zero upstream calls, non-failover typing, and both public
error envelopes: `.testing/user-stories/stories/US-065-kiro-payload-overflow.md`.
