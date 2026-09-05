---
title: Approved docs index
status: approved
approved_by: "docs cleanup 2026-07-09"
revised_at: 2026-09-03
---

# Approved docs index

`docs/approved/` is the approval baseline for high-risk work. These files are
load-bearing: code comments, sentinels, migrations, and preflight checks refer
to their paths. Prefer status changes and short index notes over moving files.

Published migration files are immutable. A historical migration may retain a
reference to a retired design document; treat that path as migration history
and do not restore the retired document or edit the migration to rewrite it.
This applies to the legacy references in `tk_006_add_qa_records_synth_fields.sql`
and `tk_054_qwen_glm_dashscope_model_mapping.sql`.

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
- [`pricing-availability-source-of-truth.md`](pricing-availability-source-of-truth.md) — availability Evidence / structurally-gone（`superseded_by` 只移交交付公式，Evidence owner 仍有效）
- [`pricing-registry-hot-reload.md`](pricing-registry-hot-reload.md) — complete registry 热发布
- [`model-surface-activation-contract.md`](model-surface-activation-contract.md) — `modelops activate` 证据契约

Watchlist 机器源：`ops/pricing/servable-reprobe-ledger.json`。

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

## Active by theme

### Routing / keys / platforms

| File | Topic |
| --- | --- |
| [`gateway-failover-policy-ssot.md`](gateway-failover-policy-ssot.md) | Gateway failover decision owner |
| [`openai-compat-first-selection-failure.md`](openai-compat-first-selection-failure.md) | OpenAI-compatible first-selection failure contract |
| [`universal-key-routing.md`](universal-key-routing.md) | Universal key routing |
| [`universal-key-capability-discovery.md`](universal-key-capability-discovery.md) | Per-key protocol/operation discovery |
| [`grok-relay-first-class-platform.md`](grok-relay-first-class-platform.md) | Grok relay platform |
| [`kiro-claude-code-completion-continuity.md`](kiro-claude-code-completion-continuity.md) | Kiro Claude Code completion |
| [`kiro-content-filter-outcome.md`](kiro-content-filter-outcome.md) | Kiro content-filter outcome |
| [`anthropic-window-util-sched.md`](anthropic-window-util-sched.md) | Upstream window-util scheduling |
| [`anthropic-buffered-stream-failure-contract.md`](anthropic-buffered-stream-failure-contract.md) | Anthropic buffered stream failure |
| [`cc-only-disable-prep-decisions.md`](cc-only-disable-prep-decisions.md) | Relaxing cc-only OAuth identity gates |
| [`rpm-override-deferred-removal.md`](rpm-override-deferred-removal.md) | RPM override layer |
| [`model-supplier-source-management.md`](model-supplier-source-management.md) | 供应源管理 |
| [`model-supplier-source-probe-sync-split.md`](model-supplier-source-probe-sync-split.md) | 供应源探测/同步拆分 |
| [`model-supplier-source-fmgo-seedance-account-rewrite.md`](model-supplier-source-fmgo-seedance-account-rewrite.md) | FMGo Seedance 账号改写 |

### Data layer / QA / deploy safety

| File | Topic |
| --- | --- |
| [`design-capacity-first-data-layer-safety.md`](design-capacity-first-data-layer-safety.md) | Capacity-first 阈值 |
| [`design-data-layer-prod-export-canary.md`](design-data-layer-prod-export-canary.md) | 生产只读 export canary |
| [`design-data-layer-archive-rehearsal.md`](design-data-layer-archive-rehearsal.md) | Archive rehearsal |
| [`design-data-layer-phase1-closeout.md`](design-data-layer-phase1-closeout.md) | Phase1 closeout |
| [`design-phase1-prod-activation-gates.md`](design-phase1-prod-activation-gates.md) | Phase1 activation gates |
| [`design-prod-archive-bucket.md`](design-prod-archive-bucket.md) | 长期 archive 桶 |
| [`design-prod-qa-24h-s3-lifecycle.md`](design-prod-qa-24h-s3-lifecycle.md) | QA 24h S3 lifecycle |
| [`design-fleet-pgdump-restore-canary.md`](design-fleet-pgdump-restore-canary.md) | Fleet pgdump restore canary |
| [`design-edge-env-secrets-recovery.md`](design-edge-env-secrets-recovery.md) | Edge env secrets recovery |
| [`design-edge-model-family-alert.md`](design-edge-model-family-alert.md) | Edge model-family alert |
| [`edge-bluegreen-release-safety.md`](edge-bluegreen-release-safety.md) | Edge blue/green safety |
| [`design-apex-domain-phase2.md`](design-apex-domain-phase2.md) | Apex domain phase2 |

### Ops / admin / misc

| File | Topic |
| --- | --- |
| [`ops-unified-contract.md`](ops-unified-contract.md) | Ops unified contract |
| [`ops-sla-error-owner-scope.md`](ops-sla-error-owner-scope.md) | Ops SLA owner scope |
| [`admin-dashboard-rollup-performance.md`](admin-dashboard-rollup-performance.md) | Admin dashboard rollups |
| [`admin-ui-performance-rollups.md`](admin-ui-performance-rollups.md) | Admin UI rollup performance |
| [`user-cold-start.md`](user-cold-start.md) | New-user cold start |
| [`usage-balance-fallback.md`](usage-balance-fallback.md) | Usage balance fallback |

### Upstream merge anchors

| File | Topic |
| --- | --- |
| [`upstream-merge-2026-08-15-migrations.md`](upstream-merge-2026-08-15-migrations.md) | Upstream merge 2026-08-15 migrations |

## Pending baselines

（当前无 pending 项。）
