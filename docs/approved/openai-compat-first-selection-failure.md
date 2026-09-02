---
title: OpenAI-compatible first-selection failure contract
status: pending
approved_by: pending
created: 2026-09-02
owners: [tk-platform]
related_prs: []
scope: "OpenAI-compatible gateway first account-selection HTTP failures"
---

# OpenAI-compatible first-selection failure contract

## Decision

OpenAI, NewAPI, Grok and other OpenAI-compatible gateway routes use one owner for
the first account-selection failure. The owner receives the model actually used
for selection separately from the client-facing model name.

The error priority is deterministic:

| Condition | HTTP | Error type | Retry-After |
| --- | ---: | --- | --- |
| Explicit unsupported or retired model error | 400 | `invalid_request_error` or the existing retired-model type | absent |
| Empty selection with a persistent group mapping gap | 404 | `model_not_found` | absent |
| Genuine empty or temporarily exhausted pool | 429 | existing `api_error` / `rate_limit_error` | present for the empty-pool contract |
| Database, context or other scheduler fault | 503 | `api_error` | absent |

Persistent mapping diagnosis is allowed only for a nil selection or an error in
the typed no-available family. It must not relabel a concrete scheduler failure
as a client-owned 404.

## Scope

The contract applies to the OpenAI-compatible first-selection branches for chat
completions, embeddings, responses, OpenAI-shaped messages, token counting,
images, video submission and alpha search. Later failover exhaustion keeps its
existing upstream-error contract.

The native Anthropic `GatewayHandler`, successful routing, request validation,
billing, schema and Web behavior are unchanged.

## Upstream Protection

The helper implementation, focused behavior tests and every route call site are
registered in `scripts/sentinels/gateway-tk.json`. Removing the helper or any
load-bearing call site must fail preflight.

## Validation

- Focused unit tests cover 400, 404, 429 and 503 precedence, including the
  production NewAPI embeddings regression.
- Gateway sentinel validation covers the helper, regression tests and every
  modified first-selection call site.
- Full project preflight must pass before push.
