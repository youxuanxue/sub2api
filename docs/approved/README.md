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

## 模型交付阅读顺序（最高优先）

一次请求能不能交付，只按这个顺序读；不要从历史清单或 skill 散文另建真相：

1. [`pricing-serving-single-source-of-truth.md`](pricing-serving-single-source-of-truth.md) — `CatalogPolicy + RequestPlan + RuntimeReadiness`
2. [`protocol-routing-ssot.md`](protocol-routing-ssot.md) — generation RequestPlan（协议路由）
3. [`ops/pricing/README.md`](../../ops/pricing/README.md) — probe / refresh / mapping 工具表（非判定公式）
4. 运营入口 skill：`tokenkey-modelops-planner`（再按分支加载子 skill）

卫星（只回答自己那一块，不是第二套交付公式）：

- [`priced-or-it-doesnt-ship.md`](priced-or-it-doesnt-ship.md) — 运行期价格闸
- [`pricing-availability-source-of-truth.md`](pricing-availability-source-of-truth.md) — availability Evidence / structurally-gone
- [`served-model-reconcile-planner.md`](served-model-reconcile-planner.md) — modelops 只读对账边界

Watchlist / skiplist / deadlist 机器源：`ops/pricing/servable-reprobe-ledger.json`（不要另建手写模型清单）。

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
| [`cc-only-disable-prep-decisions.md`](cc-only-disable-prep-decisions.md) | canonical Anthropic OAuth identity gates when relaxing cc-only |
| [`channel-pricing-refund-gate-and-runtime-pricing.md`](channel-pricing-refund-gate-and-runtime-pricing.md) | Runtime pricing and refund gate |
| [`design-data-layer-prod-export-canary.md`](design-data-layer-prod-export-canary.md) | 生产只读、export-only、无删除归档 canary |
| [`design-capacity-first-data-layer-safety.md`](design-capacity-first-data-layer-safety.md) | Capacity-first 阈值契约；正式 probe 已晋升进 daily diagnostics |
| [`design-prod-archive-bucket.md`](design-prod-archive-bucket.md) | 长期 ops archive 桶 + promote |
| [`design-prod-qa-24h-s3-lifecycle.md`](design-prod-qa-24h-s3-lifecycle.md) | Prod-only QA 24h 在线层与 7d raw S3 生命周期 SSOT |
| [`disable-cancel-storm-detector.md`](disable-cancel-storm-detector.md) | Cancel-storm detector retirement |
| [`glm-direct-zhipuv4-onboarding.md`](glm-direct-zhipuv4-onboarding.md) | GLM direct onboarding |
| [`grok-relay-first-class-platform.md`](grok-relay-first-class-platform.md) | Grok relay platform |
| [`kiro-claude-code-completion-continuity.md`](kiro-claude-code-completion-continuity.md) | Kiro Claude Code completion continuity |
| [`model-supplier-source-management.md`](model-supplier-source-management.md) | 模型供应源管理、探测门禁与账号 ownership |
| [`newapi-served-models-reconciler.md`](newapi-served-models-reconciler.md) | No unattended newapi auto-sync |
| [`ops-sla-error-owner-scope.md`](ops-sla-error-owner-scope.md) | Ops SLA owner scope |
| [`ops-unified-contract.md`](ops-unified-contract.md) | Ops unified contract |
| [`priced-or-it-doesnt-ship.md`](priced-or-it-doesnt-ship.md) | Runtime priced-serving gate |
| [`pricing-availability-source-of-truth.md`](pricing-availability-source-of-truth.md) | Availability evidence and structural-gone catalog pruning |
| [`pricing-registry-hot-reload.md`](pricing-registry-hot-reload.md) | Global pricing registry owner and protected hot reload |
| [`pricing-serving-single-source-of-truth.md`](pricing-serving-single-source-of-truth.md) | One delivery promise, three decision boundaries |
| [`protocol-routing-ssot.md`](protocol-routing-ssot.md) | Generation endpoint capability and protocol route planning |
| [`rpm-override-deferred-removal.md`](rpm-override-deferred-removal.md) | RPM override layer |
| [`served-model-reconcile-planner.md`](served-model-reconcile-planner.md) | Modelops planner |
| [`tk041-migration-checksum-remediation.md`](tk041-migration-checksum-remediation.md) | Migration checksum remediation |
| [`tk052-reenable-anthropic-request-normalize.md`](tk052-reenable-anthropic-request-normalize.md) | Anthropic request normalize |
| [`universal-key-routing.md`](universal-key-routing.md) | Universal key routing |
| [`universal-key-capability-discovery.md`](universal-key-capability-discovery.md) | Per-key/protocol/operation discovery projection |
| [`upstream-merge-2026-07-02.md`](upstream-merge-2026-07-02.md) | Upstream merge anchor |
| [`upstream-merge-2026-08-15-migrations.md`](upstream-merge-2026-08-15-migrations.md) | Upstream merge 2026-08-15 migration anchor |
| [`user-cold-start.md`](user-cold-start.md) | New-user cold start |

## Pending baselines

（当前无 pending 项。）
