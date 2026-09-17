# Agent reference (CLAUDE.md overflow)

Long-form operational reference moved out of root `CLAUDE.md` so Claude Code does
not auto-load essays every turn. Session-level hard-rule **bullets** stay in
`CLAUDE.md`; hard-rule **detail** (§4 / §5.x / §9 / account usage) is in
[`claude-hard-rules.md`](claude-hard-rules.md). Load this file for gateway
topology, Studio SSOT, model-delivery nav, or PR gate navigation.

## Public signup offer SSOT

注册承诺、公开与登录态接入指南的唯一 owner 清单与共享行为契约见
[Public Quickstart 与注册承诺](../approved/public-quickstart-registration-offer.md)。
本导航不复制组件名、金额来源或页面行为规则。

## Studio SSOT (`/studio` Image / Video / BakeOff)

`/studio` 三页（`ImageStudio` / `VideoStudio` / `BakeOff`）共享的历史、预览、下载、重载行为**禁止**在页面内各写一套。Owner 如下（#1092 视频 SSOT + 图片 SSOT 扩展）：

| 意图 | Owner | 消费方 |
| --- | --- | --- |
| 媒体历史持久化（localStorage 元数据 + IndexedDB 镜像） | `composables/useMediaLibrary.ts` + `utils/studioBlobCache.tk.ts` | Image / Video / BakeOff |
| 图片历史 mount（IDB hydrate → s3Key presign → thumb error 回退） | `composables/useStudioImageLibrary.ts` | ImageStudio, BakeOff |
| 视频历史 mount（IDB hydrate） | `composables/useStudioVideoLibrary.ts` | VideoStudio, BakeOff |
| 图片 lightbox 状态 | `composables/useStudioImagePreview.ts` + `components/StudioImagePreviewLightbox.vue` | ImageStudio |
| 图片卡片／预览下载与批量下载 | `composables/useStudioImageCardActions.ts` | ImageStudio, BakeOff |
| 视频提交选项 | `composables/useStudioVideoSubmitOptions.ts` | VideoStudio, BakeOff |
| 图片 history id / ephemeral src / revised_prompt tooltip | `utils/studioImageHistory.tk.ts` | useMediaLibrary, ImageStudio, BakeOff |
| 视频 lightbox 状态 + 过期播放守卫 | `composables/useStudioVideoPreview.ts` + `components/StudioVideoPreviewLightbox.vue` | VideoStudio, BakeOff |
| 视频卡片 copy-link / 下载 | `composables/useStudioVideoCardActions.ts`（`createStudioVideoActionHandlers` 共享 toast）+ `utils/studioMedia.tk.ts`（`videoCopyLinkAvailable` / `videoTaskCopyLinkAvailable`）+ `utils/studioDownload.tk.ts` | VideoStudio, BakeOff |
| 视频 playback 分类 + IDB 镜像 | `utils/studioPlaybackStorage.tk.ts` (`tagStudioVideoPlayback`) | VideoStudio, BakeOff |
| 视频 tab-local Blob 播放 | `utils/studioMedia.tk.ts` (`videoPlaybackUrl`) | lightbox + BakeOff 面板 |
| Veo 内联 data:video 解析/normalize | `utils/studioInlineVideo.tk.ts` | extractVideoUrl、videoPlaybackUrl、IDB 缓存、copy/download |

新增 Studio 行为时：先查上表能否扩展 owner；若 Image **与** Video **与** BakeOff 任两者都需要，必须进共享 composable/组件并在 `scripts/sentinels/frontend-tk.json` 加锚点。宪法原则见 `dev-rules/global/CLAUDE.md` §5.1。

## Client identity / fingerprint evidence SSOT

客户端身份只走一条 daily/manual 路径：`.github/workflows/client-fidelity-watch.yml`。它串联 release metadata、registry/fixture gate、生产观察与一个汇总报告；artifacts 和 tracking issues 保留，但不创建 cache branch 或自动 draft PR。

静态身份清单唯一 owner 是 `scripts/fingerprint/client_identity_registry.json`：只登记 release source、compile/runtime owner、证据模式、capture tool、production observer 与 companion identity，不保存当前版本、漂移状态或采集结果。真实证据仍由各 capture/observer owner 产生；release 新版本只表示 `stale`，生产配置 parity（例如 Kiro DB row）必须标 `production_configured`，不能冒充 `wire_observed`。

人工对齐从 `tokenkey-fingerprint-alignment-all` 或报告路由到单平台 skill；四个 capture engines 保持独立机制与 `0/1/2/3` exit contract。

## Disaster recovery / 数据重建从哪找

prod 数据层要恢复/重建时，**先看 `deploy/aws/RUNBOOK-disaster-recovery.md` 顶部的「恢复资产地图」**——一张表说清每份备份（PG 账本、`.env` 密钥、CFN 模板、S3 离机 dump）存在哪、用哪节恢复。最常见 §3（实例死卷在→换机零丢失）；离机最后一手是 §4.4（S3 `s3://tokenkey-prod-pgdump-<acct>/prod/pgdump/`，hourly，RPO ≤1h）。

## Current Gateway Flow

```
HTTP Request → Auth (JWT/APIKey) → Account Scheduling (sticky/load-aware)
 → Platform-specific forwarding (Claude / OpenAI / Gemini / Antigravity / New API fifth platform `newapi`)
 → Usage recording + quota deduction
```

转发、图片/视频协议与 adapter 选择以 [协议路由契约](../approved/protocol-routing-ssot.md)
和 `backend/internal/relay/bridge/` 为准；endpoint/schema 查生成的
[接入契约](../agent_integration.md)。本导航不维护第二份平台/channel-type 列表、任务状态或缓存 TTL。

**Scheduling and authorized support:** the current policy is owned by
[`candidate-eligibility-ssot.md`](../approved/candidate-eligibility-ssot.md).
Candidate execution and discovery use complete authorization paths and the actual
account's Plan. Legacy platform-pool helpers serve explicitly non-candidate callers;
their group-platform partition is not the current candidate policy. User pricing
menus enrich the same candidate support projection with catalog prices and metadata.

## prod ↔ Edge mirror relay topology

Production (`api.tokenkey.dev`) is **not** where the upstream Anthropic OAuth capacity lives. prod holds `cc-<edge>` mirror accounts (`platform=anthropic, type=apikey`) whose `credentials.base_url = https://api-<edge>.tokenkey.dev`; they relay prod traffic to the **Edge Stage0 stacks** (Lightsail-only since 2026-06-07; the EC2/CFN edge matrix was retired — fleet source of truth: `deploy/aws/lightsail/edge-targets-lightsail.json`), which hold the real OAuth/setup-token accounts and forward to Anthropic. (prod itself remains EC2/CFN; only the edge fleet is Lightsail.) So the prod anthropic pool failing over across `cc-us3 / cc-us6 / …` is failing over across *edges*, not local accounts.

**Attribution discipline:** a prod `cc-<edge>` cooldown (`temp_unschedulable_reason.matched_keyword='anthropic_upstream_error'`) is almost never prod-local — the real cause is on that edge. prod's `upstream-429 by account` / `recovered-200` are polluted by client-cancel tagging + failover smear and **cannot tell a dead edge from a healthy one** (a dead single-account edge and a healthy multi-account edge can both read ~1300 upstream-429). The reliable signal is each edge's OWN access-log `served_200 : no_available_429` ratio + schedulable-account count — run `ops/observability/scan-edge-health.sh`. See skills `tokenkey-online-log-troubleshooting` (§0 trap 9, §6.1 D) and `tokenkey-online-traffic-profile` (§4.1).

## Model serving SSOT / 模型交付 SSOT

架构与判定只在 `docs/approved/pricing-serving-single-source-of-truth.md`；generation
协议只在 `docs/approved/protocol-routing-ssot.md`。操作入口是 skill
`tokenkey-modelops-planner`，命令索引见 `ops/pricing/README.md`，CLI 参数以 argparse
及生成的 `docs/agent_integration.md` 为准。本文件不复制判定表、账号信息或命令手册。

账号 mapping 的写入范围见
[模型激活契约](../approved/model-surface-activation-contract.md) 与
[供应源受管账号所有权](../approved/model-supplier-source-management.md)：
受管账号由 Supplier Sync 拥有，不能按普通账号灌入 compiled floor。
证据、Edge relay 与发布边界沿用上述契约。

## PR Checklist

按触达范围运行测试/build/lint，提交前运行 `scripts/preflight.sh`。接口 mocks、Ent 生成物、pnpm lock、release/ARM/tag 等不变式见 [Hard Rules](claude-hard-rules.md)；流程/PR freshness 由 `.cursor/rules/product-dev.mdc` 拥有。

- 非 trivial PR 出口先跑 `bash scripts/upstream/check-drift.sh`；若落后，先合 upstream 或明确记录本 PR 先发原因。
- 上游删除、merge audit、merge button 与 override marker 由 [上游纪律](upstream-merge-discipline.md) 和 preflight 守卫；失败修复后重跑，不复制脚本判定表。
- 根 README/通用 compose 保持 upstream 对齐；TokenKey Stage0/AWS/smoke/image/domain/operator 操作归 `deploy/*` 或对应 skill。仅真实 build/release 契约需要时改根文件，优先短指针。
- 改 dev-rules 时先 push 子模块，再提交父仓库指针与生成产物。
- 修 upstream/claude-code issue 时，更新 `ops/issue-watchdog/upstream.json` 或 `anthropic.json` 的判断/证据和行为回归测试，不编辑生成 triage/fixes。commit 声明 `Upstream-Fixes: Wei-Shaw/sub2api#NNNN` 或 `Anthropic-Fixes: anthropics/claude-code#NNNN`；preflight 校验 ledger 与 `fixed_if_all_present` anchors。

## Issue watchdog

入口 `.github/workflows/upstream-issue-watchdog.yml`；采集/checkpoint/Issue 更新由
`scripts/upstream/watchdog-run.py` 拥有。候选未经验证不能直接判高风险，字符串锚点不能代替行为验证关闭 Issue；修复仍须显式 `mode=fix`。
维护账本或排查游标/产物时读 [ledger contract](../../ops/issue-watchdog/README.md)，不在此复制工作流细则。
