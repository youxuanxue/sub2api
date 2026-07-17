---
title: Approved docs index
status: approved
approved_by: "docs cleanup 2026-07-09"
---

# Approved docs index

`docs/approved/` is the approval baseline for high-risk work. These files are
load-bearing: code comments, sentinels, migrations, and preflight checks refer
to their paths. Prefer status changes and short index notes over moving files.

Status vocabulary is enforced by `dev-rules/scripts/check_approved_docs.py`:
`draft`, `pending`, `approved`, `shipped`, `archived`.

## Shipped baselines

| File | Topic |
| --- | --- |
| [`admin-ui-newapi-platform-end-to-end.md`](admin-ui-newapi-platform-end-to-end.md) | Admin UI newapi lifecycle |
| [`deploy-stage0-workflow.md`](deploy-stage0-workflow.md) | Cloud-agent tag/deploy workflow |
| [`messages-compaction-policy.md`](messages-compaction-policy.md) | Messages auto-compaction |
| [`newapi-allow-image-generation-ops.md`](newapi-allow-image-generation-ops.md) | newapi image-generation ops switch |
| [`newapi-as-fifth-platform.md`](newapi-as-fifth-platform.md) | NewAPI as fifth platform |
| [`newapi-followup-bugs-and-forwarding-fields.md`](newapi-followup-bugs-and-forwarding-fields.md) | NewAPI follow-up fixes |
| [`openai-codex-as-claude-thinking-continuity.md`](openai-codex-as-claude-thinking-continuity.md) | Codex-as-Claude thinking continuity |
| [`sticky-routing.md`](sticky-routing.md) | Sticky routing and prompt cache |

## Active approved baselines

| File | Topic |
| --- | --- |
| [`admin-dashboard-rollup-performance.md`](admin-dashboard-rollup-performance.md) | Admin dashboard rollups |
| [`admin-ui-performance-rollups.md`](admin-ui-performance-rollups.md) | Admin UI rollup performance |
| [`anthropic-window-util-sched.md`](anthropic-window-util-sched.md) | Upstream window-util scheduling |
| [`cc-only-disable-prep-decisions.md`](cc-only-disable-prep-decisions.md) | cc-only disable prep |
| [`channel-pricing-refund-gate-and-runtime-pricing.md`](channel-pricing-refund-gate-and-runtime-pricing.md) | Runtime pricing and refund gate |
| [`disable-cancel-storm-detector.md`](disable-cancel-storm-detector.md) | Cancel-storm detector retirement |
| [`glm-direct-zhipuv4-onboarding.md`](glm-direct-zhipuv4-onboarding.md) | GLM direct onboarding |
| [`grok-relay-first-class-platform.md`](grok-relay-first-class-platform.md) | Grok relay platform |
| [`newapi-served-models-reconciler.md`](newapi-served-models-reconciler.md) | No unattended newapi auto-sync |
| [`ops-sla-error-owner-scope.md`](ops-sla-error-owner-scope.md) | Ops SLA owner scope |
| [`ops-unified-contract.md`](ops-unified-contract.md) | Ops unified contract |
| [`priced-or-it-doesnt-ship.md`](priced-or-it-doesnt-ship.md) | Runtime priced-serving gate |
| [`pricing-availability-source-of-truth.md`](pricing-availability-source-of-truth.md) | Pricing availability SSOT |
| [`pricing-serving-single-source-of-truth.md`](pricing-serving-single-source-of-truth.md) | Pricing/serving ownership |
| [`rpm-override-deferred-removal.md`](rpm-override-deferred-removal.md) | RPM override layer |
| [`served-model-reconcile-planner.md`](served-model-reconcile-planner.md) | Modelops planner |
| [`tk041-migration-checksum-remediation.md`](tk041-migration-checksum-remediation.md) | Migration checksum remediation |
| [`tk052-reenable-anthropic-request-normalize.md`](tk052-reenable-anthropic-request-normalize.md) | Anthropic request normalize |
| [`universal-key-routing.md`](universal-key-routing.md) | Universal key routing |
| [`upstream-merge-2026-07-02.md`](upstream-merge-2026-07-02.md) | Upstream merge anchor |
| [`user-cold-start.md`](user-cold-start.md) | New-user cold start |

## Pending decisions

| File | Topic |
| --- | --- |
| [`design-prod-data-archive-rds.md`](design-prod-data-archive-rds.md) | Prod history archive and PostgreSQL RDS migration |
