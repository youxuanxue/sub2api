---
title: Responses continuation and Gemini Chat tools repair
status: approved
approved_by: "feng (conversation, 2026-09-09: fix all three issues and submit/push a PR)"
created: 2026-09-09
---

# Responses continuation and Gemini Chat tools repair

The user authorized repairing unavailable continuation error classification,
Grok account 65 reconnect behavior, and Gemini Chat function-tool routing, then
submitting and pushing one PR. This supplements candidate-eligibility-ssot.md and
protocol-routing-ssot.md. Merge, production cutover and edge rollout remain
separate; production traffic must not switch without explicit user approval.

Unavailable or unauthorized previous_response_id returns 400, while storage
failures remain server errors. Never strip a missing identifier and silently
forward only the current turn. Ownership and current account authorization
remain required before continuation execution and billing.

The Grok WS HTTP bridge retains complete input/output history for reconnects
through a bounded Redis record (version 1, user-scoped response hash, actual
account ID, at most 1 MiB, existing response-sticky TTL). Publish state before
delivering response.completed. Reconnecting gateway instances consume the same
format and revalidate authorization. store:false suppresses persistence for the
whole dependent chain, including a later store:true turn. Unknown, expired,
oversize, foreign-account or other-user history cannot become a fresh request.
Old binaries never persisted bridge history or sufficient owner records, so
pre-existing old-version sessions cannot be reconstructed from an ID alone;
clients must replay complete history to start a new chain. Old binaries also
cannot consume this new replay format: rollback compatibility does not mean
inventing support in 1.8.207. This limitation must remain in cutover review.

Gemini Chat conversion accepts standard function tools with absent/auto choice,
non-strict schemas, and validated text/function-call history. Unsupported forced
choices, disabled parallel calls, strict schemas, structured output, built-ins,
reasoning, cache, continuation and multimodal constraints remain rejected by
Plan. Existing converters own execution, stream termination, tool results and
usage; account mappings and advertised endpoint capabilities are not rewritten.

Validation covers HTTP error classification, real WS connections across
independent gateway services, tenant/account isolation, missing/expired cache,
storage failures, store:false chains, Gemini Plan/converter reachability and
tool histories. These backend tests are not UI e2e or live cutover evidence.
No web UI impact, database schema change, pricing change or live mapping change.
