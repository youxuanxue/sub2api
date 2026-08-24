---
title: Prod user-visible failure hotfix — Anthropic SSE and NewAPI chat dispatch
status: approved
approved_by: user (chat confirmation 2026-08-24)
approved_at: 2026-08-24
authors: [agent]
created: 2026-08-24
related_incidents: ["prod user_visible_failure_count 66 at 2026-08-24T07:21:18Z"]
---

# Prod user-visible failure hotfix — 2026-08-24

## Goal

Eliminate the two gateway defects behind the August 24 production alert:

1. A buffered Chat Completions request converted to Anthropic SSE must treat an
   upstream `event:error` frame as an account failover signal instead of
   returning `Upstream stream ended without a response`.
2. Inbound `/v1/chat/completions` selected onto a bridge-eligible `newapi`
   account must enter the existing NewAPI bridge dispatcher. A foreign channel
   credential must never fall through to the official OpenAI host.

## Approved containment

- Keep accounts #94, #72, #57, and #60 unschedulable until their relevant probes
  are green after rollout. Account #60 was added by explicit chat confirmation
  after the same pre-dispatch NewAPI defect recurred on its ch17 pool.
- Account #94 may be restored after rollout only after the observed
  `claude-opus-4-8` CloudWise 402 has been persisted as an exact-model 5h
  cooldown. Restoring the production account remains a separately confirmed
  write operation.
- Do not delete accounts, rewrite credentials, restart production, or deploy as
  part of containment.

## Design

### NewAPI Chat Completions ownership

The production handler calls `ForwardAsChatCompletionsDispatched`, the existing
Tier-1 owner for channel-type-aware relay. The dispatcher remains the sole
owner of bridge eligibility, sticky injection, model rewriting, credential
binding, relay error penalties, and native fallback.

The handler must not copy `platform == newapi` or `channel_type > 0` predicates.

### Anthropic buffered SSE errors

The buffered Chat Completions → Anthropic path recognizes an SSE `event:error`
frame before attempting to assemble a response. It reuses the existing typed
SSE failover path and preserves the upstream error payload for ops evidence and
account policy handling.

An upstream HTTP 402 joins the Gateway failover status SSOT so another eligible
account can serve the current request.

CloudWise is a narrow exception to the generic account-standing policy. Direct
probes of account #94 on 2026-08-24 showed `claude-opus-4-8` returning
`402 Insufficient balance` while the same API key successfully served
`claude-sonnet-4-6`, `claude-sonnet-5`, GLM, and MiniMax. A CloudWise 402 whose
body contains `Insufficient balance` or `insufficient_quota` therefore writes an
exact mapped-model entry in `account.extra.model_rate_limits` with a fixed 5h
reset. It must not write an Anthropic model-class key or disable/block the whole
account. Missing model context, a failed model-cooldown write, non-CloudWise
accounts, and other 402 bodies retain the conservative account-level fallback.

## Required tests

- Handler-selected channel type 17 account enters the NewAPI chat dispatcher.
- Handler-selected channel type 41 account enters the same dispatcher.
- DeepSeek channel type 43 remains bridge-eligible on Chat Completions; live
  account-test evidence showed the account/model itself was healthy while the
  pre-fix production handler sent normal traffic to `/v1/responses`.
- Buffered Anthropic SSE `event:error` returns a typed failover error and does
  not emit the generic empty-stream 502.
- HTTP 402 is failover-eligible and reaches the existing account penalty path.
- CloudWise `Insufficient balance` 402 writes an exact mapped-model 5h cooldown,
  including pool-mode accounts, without setting account `error`.
- The OpenAI-compatible CloudWise path does not create a whole-account runtime
  block after persisting the model cooldown.
- Sibling CloudWise models remain schedulable; missing model context and
  non-CloudWise 402 responses keep the account-level fallback.
- Existing native OpenAI, Anthropic, and Agent Plan exceptions remain green.

## Non-goals

- No deployment without a separate confirmation.
- No schema, API response schema, pricing, billing, or scheduler algorithm
  changes.
- No broad rewrite of the SSE parsers.
- Probe-reserved resources remain in the current alert metric for this hotfix;
  metric separation is a follow-up and must not hide real gateway failures.

## Rollback

- Code rollback: revert the hotfix release.
- Containment rollback: restore `schedulable=true` individually only after the
  account-specific probe passes; #94 also requires upstream balance recovery.
