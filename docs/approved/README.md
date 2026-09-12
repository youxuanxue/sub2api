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

## TokenKey SSOT 导航

本节只提供契约入口；规则、实现 owner 和验收状态在链接目标维护。

| 要判断的事实 | 契约入口 |
| --- | --- |
| 展示、请求计划与运行时可用性的组合边界 | [模型交付承诺](pricing-serving-single-source-of-truth.md) |
| endpoint 原生协议与 generation 路由合法性 | [协议路由](protocol-routing-ssot.md) |
| 授权范围内的候选资格、账号调度与实现 owner | [候选资格 Owners 表](candidate-eligibility-ssot.md#owners) |
| Key 授权与计费归属 | [Universal key routing](universal-key-routing.md) |
| 官方价格注册表与热发布 | [Pricing registry](pricing-registry-hot-reload.md) |
| 分组／渠道价目优先级与 alias | [价格与 alias](pricing-serving-single-source-of-truth.md#4-价格与-alias) |
| 可用性观测证据与目录裁剪 | [Availability evidence](pricing-availability-source-of-truth.md#1-本文唯一拥有的事实) |
| 模型映射与显式激活 | [Model surface activation](model-surface-activation-contract.md) |
| 供应源发现、校验与投影 | [Supplier source](model-supplier-source-probe-sync-split.md) |
| Gateway 是否换号 | [Failover policy](gateway-failover-policy-ssot.md) |

候选实现的发布证据与待验收项目只在
[US-050](../../.testing/user-stories/stories/US-050-candidate-eligibility-ssot.md#coverage-boundaries)
维护；契约索引不另存“已上线／已验收”状态。

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
| [`candidate-eligibility-ssot.md`](candidate-eligibility-ssot.md) | Candidate scheduling policy and implementation boundary |
| [`candidate-request-policy-convergence.md`](candidate-request-policy-convergence.md) | Direct/Universal model mapping boundary and continuation migration impact |
| [`universal-key-capability-discovery.md`](universal-key-capability-discovery.md) | Per-key protocol/operation discovery |
| [`grok-relay-first-class-platform.md`](grok-relay-first-class-platform.md) | Grok relay platform |
| [`cursor-oauth-service.md`](cursor-oauth-service.md) | Cursor native OAuth, credential renewal, billing and the shared unsupported-output-limit owner |
| [`continuation-gemini-repair.md`](continuation-gemini-repair.md) | Responses continuation and Gemini Chat tools repair |
| [`gemini-chat-conversion.md`](gemini-chat-conversion.md) | Gemini generateContent → Chat conversion edge and its capability contract |
| [`kiro-claude-code-completion-continuity.md`](kiro-claude-code-completion-continuity.md) | Kiro Claude Code completion |
| [`kiro-content-filter-outcome.md`](kiro-content-filter-outcome.md) | Kiro content-filter outcome |
| [`kiro-turn-boundary.md`](kiro-turn-boundary.md) | Kiro client-owned turn boundary |
| [`volcengine-plan-asr.md`](volcengine-plan-asr.md) | VolcEngine Agent Plan speech recognition |
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
| [`qa-bundle-session-export.md`](qa-bundle-session-export.md) | QA Bundle 保真会话导出与 traj SSOT |
| [`design-prod-qa-24h-s3-lifecycle.md`](design-prod-qa-24h-s3-lifecycle.md) | QA 24h S3 lifecycle |
| [`security-capture-and-ingress.md`](security-capture-and-ingress.md) | QA capture protection, trusted ingress and public image download boundary |
| [`design-fleet-pgdump-restore-canary.md`](design-fleet-pgdump-restore-canary.md) | Fleet pgdump restore canary |
| [`design-edge-env-secrets-recovery.md`](design-edge-env-secrets-recovery.md) | Edge env secrets recovery |
| [`design-edge-model-family-alert.md`](design-edge-model-family-alert.md) | Edge model-family alert |
| [`edge-bluegreen-release-safety.md`](edge-bluegreen-release-safety.md) | Edge blue/green safety |
| [`design-apex-domain-phase2.md`](design-apex-domain-phase2.md) | Apex domain phase2 |
| [`prod-component-release.md`](prod-component-release.md) | Independent prod component releases |

### Ops / admin / misc

| File | Topic |
| --- | --- |
| [`ops-unified-contract.md`](ops-unified-contract.md) | Ops unified contract |
| [`edge-admin-handoff-v2.md`](edge-admin-handoff-v2.md) | Pending: proof-bound Edge admin handoff |
| [`ops-sla-error-owner-scope.md`](ops-sla-error-owner-scope.md) | Ops SLA owner scope |
| [`admin-dashboard-rollup-performance.md`](admin-dashboard-rollup-performance.md) | Admin dashboard rollups |
| [`admin-ui-performance-rollups.md`](admin-ui-performance-rollups.md) | Admin UI rollup performance |
| [`user-cold-start.md`](user-cold-start.md) | New-user cold start |
| [`public-quickstart-registration-offer.md`](public-quickstart-registration-offer.md) | Public Quickstart and shared registration offer |
| [`usage-balance-fallback.md`](usage-balance-fallback.md) | Usage balance fallback |
| [`design-dual-market-homepage.md`](design-dual-market-homepage.md) | 双市场首页与统一产品矩阵 |

### Upstream merge anchors

| File | Topic |
| --- | --- |
| [`upstream-merge-2026-08-15-migrations.md`](upstream-merge-2026-08-15-migrations.md) | Upstream merge 2026-08-15 migrations |

## Pending baselines

（当前无 pending 项。）
