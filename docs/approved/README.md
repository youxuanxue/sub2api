---
title: Approved docs index
status: approved
approved_by: "docs cleanup 2026-07-09"
revised_at: 2026-09-03
---

# Approved docs index

- [Kiro 文件工具分块兼容方案](kiro-file-tool-chunking.md)：已确认的按账号分块提示；实测缓解长静默断流，明确轮数、成本和非保证边界。

- [cn-prod 私有镜像分发](cn-prod-image-distribution.md)：CI S3 中转、SSM 预下载和不中断网关的边界。

- [OAuth 搜索历史与工具权限](openai-search-history-tool-permissions.md)：历史声明补全、原工具权限及保护错误诊断。

- [Antigravity 模型能力修复](antigravity-model-capability-repair.md)：保留 Argon、统一发现入口、模型级冷却与已确认无效映射清理。

- [OAuth 集中后的网关安全](oauth-consolidation-gateway-safety.md)：固定代理、请求错误语义、流式巡检和数据处置边界。

- 已完成：[AWS 迁移收尾](aws-migration-closeout-20261016.md)：备份、QA timer、独立健康监控与旧主机资源收尾边界（执行记录）。

- archived：[东京切流复审修复](tokyo-cutover-review-repair-20261017.md)（Tokyo 已删除）。

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

2026-09-30 增量：[Gemini Web 直接 Pro 与共享画布契约](gemini-web-channel.md)（已获本次会话实施授权，待发布验证）。

## TokenKey SSOT 导航

国内 prod（现腾讯北京；UCloud 段为首发历史）的授权范围、首次初始化和发布边界：[国内部署契约](design-domestic-cluster.md)。

本节只提供契约入口；规则、实现 owner 和验收状态在链接目标维护。

| 要判断的事实 | 契约入口 |
| --- | --- |
| 展示、请求计划与运行时可用性的组合边界 | [模型交付承诺](pricing-serving-single-source-of-truth.md) |
| endpoint 原生协议与 generation 路由合法性 | [协议路由](protocol-routing-ssot.md) |
| Responses bridge 参数错误、流式终止及运营错误边界 | [Responses bridge 参数错误分类](responses-bridge-client-error.md) |
| Ali / Volc Plan Chat 的工具选择、developer 与显式 JSON 模式 | [Plan Chat 参数兼容](chat-plan-parameter-compatibility.md) |
| 授权范围内的候选资格、账号调度与实现 owner | [候选资格 Owners 表](candidate-eligibility-ssot.md#owners) |
| 请求输入模态 × 供应侧已知能力（ContentKinds ∩ known-negative）；kimi-k3 客户视频请求形态 | [多模态供应能力门](multimodal-supply-capability-ssot.md) |
| Studio / Quickstart 生图参数与请求示例 | [生图能力与接入示例](image-generation-quickstart-studio.md)（GPT 精确画布见 [strict canvas](gpt-image-strict-canvas-postprocess.md)，已合并；OAuth 观感脚手架见 [perceived quality](gpt-image-oauth-perceived-quality.md)） |
| Key 授权与计费归属 | [Universal key routing](universal-key-routing.md) |
| Kiro OAuth 混入 Claude（anthropic）组 | [Kiro mixed_scheduling](kiro-mixed-scheduling-claude-groups.md) |
| 推理响应 `usage.cost` 标准官方价出口 | [Response usage.cost](response-usage-cost.md) |
| 官方价格注册表与热发布 | [Pricing registry](pricing-registry-hot-reload.md) |
| 分组／渠道价目优先级与 alias | [价格与 alias](pricing-serving-single-source-of-truth.md#4-价格与-alias) |
| 可用性观测证据与目录裁剪 | [Availability evidence](pricing-availability-source-of-truth.md#1-本文唯一拥有的事实) |
| 模型映射与显式激活 | [Model surface activation](model-surface-activation-contract.md) |
| 供应源发现、校验与投影 | [Supplier source](model-supplier-source-probe-sync-split.md) |
| Gateway 是否换号 | [Failover policy](gateway-failover-policy-ssot.md) |

候选实现的发布证据与待验收项目只在
[US-050](../../.testing/user-stories/stories/US-050-candidate-eligibility-ssot.md#coverage-boundaries)
维护；契约索引不另存“已上线／已验收”状态。
上游合并前使用 `scripts/checks/upstream-ssot-impact.py` 按 owner 聚合协议、候选、定价、目录和网关影响面。
目录声明、推荐撤下及展示投影的职责见[目录 owner 与写入边界](pricing-serving-single-source-of-truth.md#目录-owner-与写入边界)。`model-owner-manifest.py` 复用 canonical NewAPI manifest parser 检查声明；`catalog-serving-drift.py` 校验价格 owner 解析，协议与映射继续由既有 owner 裁决。
逐 AC 的机器状态见 [`ops/observability/candidate-eligibility-acceptance-ledger.json`](../../ops/observability/candidate-eligibility-acceptance-ledger.json)，由 `scripts/checks/candidate-acceptance-ledger.py` 校验；它不替代 US-050 的解释性证据。

`CatalogPolicy` 由上述 owner 的投影组成；原生目录刷新、NewAPI 声明、价格发布和账号 mapping 激活各走自己的写入边界。availability 只表示证据，前端 presentation helper 消费后端目录与授权能力。长期 advisory 统一登记在 [`ops/observability/advisory-ledger.json`](../../ops/observability/advisory-ledger.json)，每项必须有 owner、到期日和 remediation。

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
| [`client-closed-499-ssot.md`](client-closed-499-ssot.md) | Client-closed 499 classification owners (ingress predicate, constant, ops phase) |
| [`upstream-model-retirement.md`](upstream-model-retirement.md) | Upstream model retirement evidence, failover and SLA attribution |
| [`openai-compat-first-selection-failure.md`](openai-compat-first-selection-failure.md) | OpenAI-compatible first-selection failure contract |
| [`gpt-image-aspect-ratio-soft-control.md`](gpt-image-aspect-ratio-soft-control.md) | GPT Image `aspect_ratio` soft marker, floor identity |
| [`gpt-image-strict-canvas-postprocess.md`](gpt-image-strict-canvas-postprocess.md) | GPT Image local exact canvas + format post-process（已合并 #2385） |
| [`gpt-image-native-cutover.md`](gpt-image-native-cutover.md) | GPT Image OAuth 默认原生 Images，404/405 回退 Responses |
| [`universal-key-routing.md`](universal-key-routing.md) | Universal key routing |
| [`candidate-eligibility-ssot.md`](candidate-eligibility-ssot.md) | Candidate scheduling policy and implementation boundary |
| [`multimodal-supply-capability-ssot.md`](multimodal-supply-capability-ssot.md) | Multimodal supply gate: ContentKinds ∩ evidence-backed known-negatives (not a Plan rewrite) |
| [`design-candidate-routing-runtime-snapshot.md`](design-candidate-routing-runtime-snapshot.md) | Candidate read snapshot, canonical request and QA redaction performance plan |
| [`rolling-capacity-saturation.md`](rolling-capacity-saturation.md) | Rolling account-capacity saturation scheduling |
| [`candidate-request-policy-convergence.md`](candidate-request-policy-convergence.md) | Direct/Universal model mapping boundary and continuation migration impact |
| [`universal-key-capability-discovery.md`](universal-key-capability-discovery.md) | Per-key protocol/operation discovery |
| [`discovery-require-live-account.md`](discovery-require-live-account.md) | 无活账号则不展示（精简 B；瞬时限流不进菜单） |
| [`grok-accounts.md`](grok-accounts.md) | Grok accounts |
| [`continuation-gemini-repair.md`](continuation-gemini-repair.md) | Responses continuation and Gemini Chat tools repair |
| [`gemini-chat-conversion.md`](gemini-chat-conversion.md) | Gemini generateContent → Chat conversion edge and its capability contract |
| [`gemini-messages-conversion.md`](gemini-messages-conversion.md) | Gemini → Messages conversion and capability contract |
| [`kiro-claude-code-completion-continuity.md`](kiro-claude-code-completion-continuity.md) | Kiro Claude Code completion |
| [`kiro-content-filter-outcome.md`](kiro-content-filter-outcome.md) | Kiro content-filter outcome |
| [`kiro-payload-overflow.md`](kiro-payload-overflow.md) | Kiro translated-payload overflow and current-turn preservation |
| [`kiro-turn-boundary.md`](kiro-turn-boundary.md) | Kiro client-owned turn boundary |
| [`volcengine-plan-asr.md`](volcengine-plan-asr.md) | VolcEngine Agent Plan speech recognition |
| [`anthropic-window-util-sched.md`](anthropic-window-util-sched.md) | Upstream window-util scheduling |
| [`anthropic-buffered-stream-failure-contract.md`](anthropic-buffered-stream-failure-contract.md) | Anthropic buffered stream failure |
| [`claude-context-error-recovery.md`](claude-context-error-recovery.md) | Claude context rejection recognition and committed SSE failure reporting |
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
| [`ops-error-logs-column-contract.md`](ops-error-logs-column-contract.md) | `ops_error_logs` 列契约：声明 == 写入 ∪ DB 托管（无 Ent schema 表的机械替代） |
| [`design-prod-qa-24h-s3-lifecycle.md`](design-prod-qa-24h-s3-lifecycle.md) | QA 24h S3 lifecycle |
| [`security-capture-and-ingress.md`](security-capture-and-ingress.md) | QA capture protection, trusted ingress and public image download boundary |
| [`qa-redaction-identified-formats-only.md`](qa-redaction-identified-formats-only.md) | QA 脱敏已识别格式边界与性能优化 |
| [`design-prod-physical-backup.md`](design-prod-physical-backup.md) | 每周全量、两小时块增量、WAL 连续归档及隔离验收 |
| [`cn-prod-logical-backup.md`](cn-prod-logical-backup.md) | 国内节点逻辑 dump（退役中，cn-prod 已改 pgBackRest） |
| [`design-fleet-pgdump-restore-canary.md`](design-fleet-pgdump-restore-canary.md) | Fleet pgdump restore canary |
| [`hcloud-pgdump-storage.md`](hcloud-pgdump-storage.md) | archived：Hetzner 期 pgdump 存储（已被物理备份取代） |
| [`hetzner-cloud-full-migration.md`](hetzner-cloud-full-migration.md) | 舰队不变量锁（Hetzner 期选址为历史；执行 SSOT：`deploy/hetzner/README.md`） |
| [`design-apex-domain-phase2.md`](design-apex-domain-phase2.md) | Apex domain phase2 |
| [`prod-component-release.md`](prod-component-release.md) | Independent prod component releases |

### Ops / admin / misc

| File | Topic |
| --- | --- |
| [`ops-unified-contract.md`](ops-unified-contract.md) | Ops unified contract |
| [`ops-sla-error-owner-scope.md`](ops-sla-error-owner-scope.md) | Ops SLA owner scope |
| [`proxy-unreachable-p0-alert.md`](proxy-unreachable-p0-alert.md) | 代理出口 IP:port 不可达 P0 告警 |
| [`admin-dashboard-rollup-performance.md`](admin-dashboard-rollup-performance.md) | Admin dashboard rollups |
| [`admin-ui-performance-rollups.md`](admin-ui-performance-rollups.md) | Admin UI rollup performance |
| [`user-cold-start.md`](user-cold-start.md) | New-user cold start |
| [`trial-media-recharge-ux-and-total-recharged.md`](trial-media-recharge-ux-and-total-recharged.md) | Trial media 402 Studio UX + `users.total_recharged` / 总充值 SSOT (A1/B1) |
| [`public-quickstart-registration-offer.md`](public-quickstart-registration-offer.md) | Public Quickstart and shared registration offer |
| [`usage-balance-fallback.md`](usage-balance-fallback.md) | Usage balance fallback |
| [`response-usage-cost.md`](response-usage-cost.md) | Inference response `usage.cost` (TotalCost / list price) |
| [`design-dual-market-homepage.md`](design-dual-market-homepage.md) | 双市场首页与统一产品矩阵（海外 host 全门面条款见 facade 文档） |
| [`design-callmodel-product-facade.md`](design-callmodel-product-facade.md) | CallModel 产品门面（同壳反代 + 品牌铬；局部 supersede dual-market） |

### Upstream merge anchors

| File | Topic |
| --- | --- |
| [`upstream-merge-2026-08-15-migrations.md`](upstream-merge-2026-08-15-migrations.md) | Upstream merge 2026-08-15 migrations |
| [`upstream-merge-20260923.md`](upstream-merge-20260923.md) | TokenKey upstream merge 2026-09-23 |
| [`upstream-merge-20260930.md`](upstream-merge-20260930.md) | 隔离上游合并与 TokenKey 不变量裁决；不授权主线合并或部署 |
| [`upstream-merge-2026-10-06.md`](upstream-merge-2026-10-06.md) | 隔离上游合并、241 迁移与 TokenKey CHECK/404 裁决；不授权主线合并或部署 |
| [`upstream-merge-2026-10-22-migrations.md`](upstream-merge-2026-10-22-migrations.md) | 2026-10-22 上游迁移已随 #2796 合入 |

## Pending baselines

- [Gemini Web 新通路](gemini-web-channel.md) — 数据库会话、worker 鉴权、官方响应与单 owner 契约。

- [Gateway capability verification](prod-replay-capability-matrix.md) — offline matrix planning and explicit isolated checks, independent of deployment.

- [Stage0 release-check remediation](stage0-release-check-remediation.md) — approved fixes and production impact boundaries.

- [Antigravity OpenAI Images 协议适配](antigravity-images-generations-adapter.md)：复用原生 Gemini Plan、单张 b64_json、参数边界与计费前响应校验。

## QA raw-only 与历史设计

| File | Topic |
| --- | --- |
| [qa-raw-only.md](qa-raw-only.md) | QA raw-only 契约（Hetzner 目标已由 AWS S3 取代；上传暂停） |
| [qa-bundle-session-export.md](qa-bundle-session-export.md) | archived：退役用户会话导出 |
| [design-split-deploy-qa-bundle.md](design-split-deploy-qa-bundle.md) | archived：退役 Bundle worker 发布 |
| [prod-component-release.md](prod-component-release.md) | archived：原组件发布设计，由 raw-only 替代 |

网关生产蓝绿发布以 [deploy-stage0-workflow.md](deploy-stage0-workflow.md) 为基线，QA 部分以 raw-only 覆盖。

- [Volc Agent Plan Kimi Chat compatibility](volc-kimi-chat-compatibility.md) — supplier-scoped thinking and tool selectors.

- [Kimi 请求错误修复与请求体边界](kimi-input-error-remediation.md) — Direct/Universal 共享路由上限、解压完整性与客户端参数改法。

- [Kiro 流、规则与刷新可靠性](kiro-stream-auth-reliability.md) — 已批准的协议校验、规则保留、工具身份及认证防护。
