---
title: Kiro Payload Overflow Contract
status: approved
approved_by: "user (explicitly requested the overflow-policy fix after direct Kiro probing; 2026-10-22 requested the measured kiro-lb#60 and Kiro-Go#119 reduction)"
approved_at: 2026-10-17
owners: [tk-gateway]
risk: high
related_prs: [2688, 2941]
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

Those 2026-10-17 probes are coarse: they took no sample between 950,000 and
999,999, so 950,000 is the largest value tried, not a measured boundary. A
2026-10-26 direct re-probe narrowed the real boundary and found it varies by
account. See Evidence Update (2026-10-26) below. The 900 KiB limit and the
approved behavior in this contract are unchanged.

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

## Evidence Update (2026-10-26)

New direct evidence, recorded here because the Evidence And Decision section
above is the first thing a reader consults and its boundary range is now known
to be coarse. This section adds evidence only. It changes no approved behavior:
the 900 KiB limit, the reduction order, the no-rewrite rules for user prose and
tool definitions, and the local-rejection semantics all stand as written above
(the rejection's status/type was revised on 2026-10-27 as noted there; the
no-upstream-call, non-failover and no-penalty properties are unchanged).

Method: 39 upstream requests direct to
`runtime.us-east-1.kiro.dev/generateAssistantResponse` with synthetic `x`
padding, on accounts 305 / 325 / 326.

Verified:

- **The threshold does depend on the model.** An earlier reading of these probes
  said it did not, because `claude-sonnet-5` and `claude-opus-5.5` accepted and
  rejected the same ladder byte for byte on one account. A same-baseline re-test
  has since refuted that as a general claim: on the same account, on real
  translator output, at the same 920,000 bytes, varying only `modelId`,
  `claude-sonnet-5` returned 200 while `claude-sonnet-4.5` returned 400
  `CONTENT_LENGTH_EXCEEDS_THRESHOLD`, putting `sonnet-4.5`'s threshold near
  300,000–350,000 rather than just under 1,000,000. The original observation holds
  only between those two models, which happen to share a high threshold.
  Evidence grades that still differ: the `sonnet-4.5` rejection reproduced on both
  real translator output and a hand-built body, while the opus-series acceptances
  at 962,560 are hand-built only (a real-translator re-test for `opus-5.5` is in
  flight). `sonnet-4.5` and `haiku-4.5` were rejected at 962,560 and must stay at
  921,600. Any limit change has to be made per model, not globally.
- **The size the guard validates is 81 bytes short of what ships.**
  `ValidatePayloadSize` runs before `setPayloadProfileArnForAccount`, and
  `ProfileArn` carries `omitempty`, so the injected
  `"profileArn":"<65-char ARN>"` (65 + 16 bytes of wrapper) falls entirely outside
  what the guard measured. Confirmed on real wire: `wire_bytes=920081` against
  `validated_bytes_equiv=920000`. Every threshold figure in this document is
  therefore 81 bytes optimistic about what reaches Kiro. A separate PR fixes the
  gap; this contract's 900 KiB limit is unchanged.
- The threshold does depend on the account, which was not previously known. At
  least two tiers exist: two accounts' boundaries fall in the same 16-byte
  interval, (993,815, 993,831], and a third sits about 4.5 KB higher, at
  (998,387, 998,400]. Three samples cannot establish which tier is normal, so
  neither tier is called an outlier. This is also not evidence of continuous
  per-account variation.
- This is deterministic, not jitter: one size was rejected 4/4 on each of the
  two lower accounts, and the higher account's boundary reproduced byte for byte
  on later calls.
- The measured surface is the whole serialized body, not just the current
  message. Moving the padding into history turns reproduced the same boundary.
- The existing 921,600-byte limit therefore has roughly 72,215 bytes of real
  margin below the lowest observed boundary (993,815) — a figure that rests only
  on that observation, not on which tier is normal — the 28,400 figure above
  is margin below a probe value, not below a boundary.
- `ops_error_logs` has never recorded an upstream `CONTENT_LENGTH_EXCEEDS_THRESHOLD`
  (0 occurrences over the retained window since 2026-10-11). Every 413 with a
  recorded byte size was the local guard. This confirms this contract's finding
  that the batch made no upstream call, and it is why the real upstream boundary
  can only be obtained by direct probing.

Still unknown, and material to any future limit change:

- Only 3 of 24 accounts were probed; the rest were quota-exhausted or invalid.
  An unprobed account could sit below any raised limit.
- The roughly 4.5 KB gap between the two tiers is unexplained. Credential field
  lengths, region, creation date, plan, channel type, tier and quota dimension
  were all checked and are identical across the accounts; name prefix does not
  explain it either. The difference is upstream-side and not visible in TK's
  database.
- All measurements are from a single day. This contract's warning that the
  boundary may move with upstream protocol changes still holds.
- The gateway path is unverified. These probes bypassed TK translation, and
  measured translated bodies were materially smaller than their ingress bodies
  (ingress 966,247–984,081 vs translated 928,064–958,922 for the same shape).
  Any `maxPayloadBytes` change must be validated through `/v1/messages` first.

Raising the limit is a separate decision and needs its own review. The
candidate value under discussion, its yield attribution, and the remaining
unknowns are tracked in
[`../task-breakdown-kiro-auto-mode-transcript-trim.md`](../task-breakdown-kiro-auto-mode-transcript-trim.md)
(status `proposed`). The kiro-lb capacity experiment is still not a basis for
relaxing this limit.

## Scope Boundary

This decision does not raise the Kiro wire limit, summarize history, shorten
current user prose, or add provider fallback. It does not change authentication,
billing, scheduling, persistence, database schema, or deployment state. A future
limit change requires new direct evidence because Kiro does not publish this
threshold as a stable contract. The 2026-10-26 probes are such evidence, but
recording them here does not itself authorize a new limit: that requires
gateway-path validation and a separate approval.

## Pending exception (not in force)

A **gated** exception for Claude Code auto-mode classifier transcripts — allow
structured whole-record deletion of old tool-process lines inside current user
prose when an account Extra flag is on — is proposed in
[`kiro-auto-mode-transcript-trim.md`](kiro-auto-mode-transcript-trim.md)
(`status: pending`). Until that document is human-approved and flipped out of
`pending`, this overflow contract’s “must not shorten current user prose” rule
and default off behavior remain authoritative. Head/tail truncation, summarization,
and cross-platform overflow failover stay out of scope here.

## Verification

The linked Story covers history truncation, current-message and UTF-8 integrity,
fixed-payload overflow, zero upstream calls, non-failover typing, and both public
error envelopes: `.testing/user-stories/stories/US-065-kiro-payload-overflow.md`.
