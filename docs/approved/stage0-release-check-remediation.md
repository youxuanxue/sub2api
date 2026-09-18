---
title: Stage0 release-check remediation
status: approved
approved_by: "user (conversation: 同意。请修复所有的问题。提交并推送pr)"
approved_at: "2026-09-18"
authors: [codex]
created_at: "2026-09-18"
---

# Stage0 release-check remediation

## Scope

Repair the review findings for v1.8.234 through 31c4d1cc5e. Approval covers
implementation, tests, commit, push and PR creation; merge and production
rollout remain separate actions.

## Invariants and acceptance

- PostgreSQL atomic updates and transactions own balance concurrency. Remove
  the process mutex that can hold a user lock while waiting for a connection
  owned by a refund. The exhausted-pool integration regression must finish
  both operations and reconcile the final balance; settlement stays idempotent.
- Existing media classification owns versioned Grok video and Gemini image
  aliases. Both account-test dialogs share test-model preference and eligibility;
  the key guide consumes that same media classification. Verify submitted
  prompts, image previews, generated snippets and Studio selection in a browser.
- Gemini traffic aliases resolve complete price owners through registry
  `_aliases`, per [pricing SSOT](pricing-serving-single-source-of-truth.md).
  Preserve the approved target standard tariffs and inherit all owner tiers.
- Prod PG defaults live only in the compose overlay. Generate bootstrap bytes
  from it, load it on the first systemd start, preserve explicit env overrides,
  and make failed or unfinished SSM invocations exit nonzero. Tests use local
  command fixtures and do not change a production host.
- New saturation ZSET history uses versioned keys, per
  [rolling saturation](rolling-capacity-saturation.md), so old STRING readers
  and writers remain compatible through blue/green overlap and rollback.

## Production impact

Removing the mutex restores database row contention rather than risking pool
inversion; no schema or ledger migration is needed. Redis history initially
starts empty for the new key version, temporarily reducing soft deprioritization.
PG tuning apply recreates PostgreSQL and can interrupt requests; application
release alone does not execute that maintenance operation. Existing runtime
pricing registry snapshots remain authoritative until explicitly refreshed by
modelops; this PR does not publish runtime prices or account mappings.
