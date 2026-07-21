---
title: Kiro Content Filter Outcome Contract
status: approved
approved_by: "user (implementation directive after root-cause review, 2026-07-21)"
approved_at: 2026-07-21
authors: [agent]
created: 2026-07-21
related_prs: [1398]
related_commits: []
---

# Kiro Content Filter Outcome Contract

## Root Cause

Kiro can return HTTP 200 AWS EventStream with
`metadataEvent.stopReason=CONTENT_FILTERED`, followed by usage events, but no
`assistantResponseEvent`. The gateway ignored `metadataEvent`, classified the
result as an opaque empty response, returned 502, and triggered account/edge
failover. PR #1398 only adds a fallback for a metered empty response; it does
not consume Kiro's authoritative terminal outcome.

## Approved Behavior

When Kiro reports `CONTENT_FILTERED` and no assistant text, reasoning, or tool
output was produced, the request is a non-retryable upstream policy rejection:

| Client API | HTTP status | Error type/code |
| --- | --- | --- |
| `/v1/messages` | 400 | `invalid_request_error` |
| `/v1/chat/completions` | 400 | `content_filter_error` |
| `/v1/responses` | 400 | `content_filter` |

All three surfaces use the client message `Request was blocked by upstream
content filtering`. The outcome must not trigger account failover, account
cooldown, token refresh, or other account-health penalties.

If Kiro emits assistant text, reasoning, or tool output before the same stop
reason, the gateway preserves that output as a normal successful response. An
ordinary empty response without `CONTENT_FILTERED` retains the existing 502
failover behavior.

## Mirror Contract

The edge `/v1/messages` response carries the internal header
`X-TokenKey-Kiro-Outcome: content_filtered`. Prod trusts this header only for a
configured Kiro mirror stub, then maps it to the client-facing Chat Completions
or Responses error shape before failover and rate-limit handling. Relay logic
must not infer this outcome from mutable error text.

## Risk Boundaries

- No route, request schema, authentication, billing, quota, or persistent data
  shape changes.
- The change intentionally alters public error status and body only for the
  previously misclassified Kiro content-filter terminal outcome.
- Ops evidence records `policy_error/content_filtered` with upstream status
  200, matching the actual Kiro transport response.

## Verification

- Protocol parser callback receives `metadataEvent.stopReason`.
- Native non-streaming and streaming Kiro paths return the typed policy error
  without response bytes or failover wrapping.
- A filtered outcome with assistant output remains successful.
- Native Chat Completions maps the typed error to 400.
- Kiro mirror Chat Completions and Responses map the trusted header to 400
  without retrying or penalizing the account.
- Ordinary empty EventStreams retain the existing 502 failover behavior.
