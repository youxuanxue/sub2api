# Agent reference (CLAUDE.md overflow)

Long-form operational reference moved out of root `CLAUDE.md` to stay under the Claude Code memory limit. Hard rules remain in `CLAUDE.md`; load this file when you need gateway topology, Studio SSOT, or the full PR checklist.

## Studio SSOT (`/studio` Image / Video / BakeOff)

`/studio` 三页（`ImageStudio` / `VideoStudio` / `BakeOff`）共享的历史、预览、下载、重载行为**禁止**在页面内各写一套。Owner 如下（#1092 视频 SSOT + 图片 SSOT 扩展）：

| 意图 | Owner | 消费方 |
| --- | --- | --- |
| 媒体历史持久化（localStorage 元数据 + IndexedDB 镜像） | `composables/useMediaLibrary.ts` + `utils/studioBlobCache.tk.ts` | Image / Video / BakeOff |
| 图片历史 mount（IDB hydrate → s3Key presign → thumb error 回退） | `composables/useStudioImageLibrary.ts` | ImageStudio, BakeOff |
| 视频历史 mount（IDB hydrate） | `composables/useStudioVideoLibrary.ts` | VideoStudio, BakeOff |
| 图片 lightbox 状态 | `composables/useStudioImagePreview.ts` + `components/StudioImagePreviewLightbox.vue` | ImageStudio |
| 图片 history id / ephemeral src / revised_prompt tooltip | `utils/studioImageHistory.tk.ts` | useMediaLibrary, ImageStudio, BakeOff |
| 视频 lightbox 状态 + 过期播放守卫 | `composables/useStudioVideoPreview.ts` + `components/StudioVideoPreviewLightbox.vue` | VideoStudio, BakeOff |
| 视频卡片 copy-link / 下载 | `composables/useStudioVideoCardActions.ts`（`createStudioVideoActionHandlers` 共享 toast）+ `utils/studioMedia.tk.ts`（`videoCopyLinkAvailable` / `videoTaskCopyLinkAvailable`）+ `utils/studioDownload.tk.ts` | VideoStudio, BakeOff |
| 视频 playback 分类 + IDB 镜像 | `utils/studioPlaybackStorage.tk.ts` (`tagStudioVideoPlayback`) | VideoStudio, BakeOff |
| 视频 tab-local Blob 播放 | `utils/studioMedia.tk.ts` (`videoPlaybackUrl`) | lightbox + BakeOff 面板 |
| Veo 内联 data:video 解析/normalize | `utils/studioInlineVideo.tk.ts` | extractVideoUrl、videoPlaybackUrl、IDB 缓存、copy/download |

新增 Studio 行为时：先查上表能否扩展 owner；若 Image **与** Video **与** BakeOff 任两者都需要，必须进共享 composable/组件并在 `scripts/sentinels/frontend-tk.json` 加锚点。宪法原则见 `dev-rules/global/CLAUDE.md` §5.1。

## Disaster recovery / 数据重建从哪找

prod 数据层要恢复/重建时，**先看 `deploy/aws/RUNBOOK-disaster-recovery.md` 顶部的「恢复资产地图」**——一张表说清每份备份（PG 账本、`.env` 密钥、CFN 模板、S3 离机 dump）存在哪、用哪节恢复。最常见 §3（实例死卷在→换机零丢失）；离机最后一手是 §4.4（S3 `s3://tokenkey-prod-pgdump-<acct>/prod/pgdump/`，hourly，RPO ≤1h）。

## Current Gateway Flow

```
HTTP Request → Auth (JWT/APIKey) → Account Scheduling (sticky/load-aware)
 → Platform-specific forwarding (Claude / OpenAI / Gemini / Antigravity / New API fifth platform `newapi`)
 → Usage recording + quota deduction
```

The fifth platform **`newapi`** is a first-class account/group platform (not an add-on card on the other four): it uses OpenAI-compatible gateway routes and the New API **adaptor** layer in `internal/relay/bridge` when `channel_type > 0`. The `internal/integration/newapi/` package provides the channel-type catalog, affinity helpers, upstream model metadata helpers, and other `newapi`-specific bridge support required by TokenKey's fifth-platform flow.

**Image and video generation surfaces** ride on the same `newapi` (and `openai`) compat-pool routing:

- **Sync image** — `POST /v1/images/generations` (and `POST /images/generations` alias) via `bridge.RunImageRelay` and `bridge.DispatchImageGenerations`. Volcengine `channel_type=45` (Doubao Seedream) is supported through the upstream `volcengine` adapter.
- **Async video** — `POST /v1/video/generations` + `GET /v1/video/generations/:task_id` (and the OpenAI-compat aliases `POST /v1/videos` + `GET /v1/videos/:task_id`, plus their no-prefix variants). Submit returns a TokenKey-issued `task_id` (prefix `vt_`); subsequent polls hit the upstream task adapter pinned at submit time. Supported channel types are auto-derived from `relay.GetTaskAdaptor` — i.e. whatever new-api's task-adaptor registry maps (as of 2026-06 that includes VolcEngine `45` / DoubaoVideo `54` → Doubao Seedance, Vertex AI `41` → Veo, plus Ali, Kling, Jimeng, Vidu, Sora/OpenAI, Gemini, MiniMax); never hard-code a channel list in TK code or docs — the predicate below is the single source of truth. Routing metadata lives in `service.VideoTaskCache` (Redis primary, in-memory fallback for single-replica dev). Default record TTL is 24h; terminal status (`succeeded`/`failed`) deletes the record. Adding a new task adapter upstream requires no TK code changes — the `IsVideoSupportedChannelType` predicate sees the new channel type as soon as the upstream adapter map registers it.

**Scheduling-pool semantics (per `docs/approved/newapi-as-fifth-platform.md`, shipped):** the OpenAI-compatible pool now partitions strictly by `group.platform`. `openai` groups schedule only `openai` accounts; `newapi` groups schedule only `newapi` accounts (with `channel_type > 0`). The canonical predicate is `account.IsOpenAICompatPoolMember(groupPlatform)` in `backend/internal/service/account_tk_compat_pool.go`, used by load-balance, sticky-session, and recheck paths in `openai_account_scheduler.go` / `openai_gateway_service.go`. Cross-platform fallback is forbidden — an empty pool surfaces an error. Sticky-session bindings whose bound account drifted to the wrong platform (or whose `channel_type` was reset to 0) are invalidated and the request fails over to load-balance. `messages_dispatch_model_config` is preserved for `openai` and `newapi` groups, cleared for `anthropic` / `gemini` / `antigravity` (predicate: `isOpenAICompatPlatformGroup`). Sticky routing (per `docs/approved/sticky-routing.md`, shipped) layers above this pool to optimize prompt-cache hit rates within each platform's bucket.

## prod ↔ Edge mirror relay topology

Production (`api.tokenkey.dev`) is **not** where the upstream Anthropic OAuth capacity lives. prod holds `cc-<edge>` mirror accounts (`platform=anthropic, type=apikey`) whose `credentials.base_url = https://api-<edge>.tokenkey.dev`; they relay prod traffic to the **Edge Stage0 stacks** (Lightsail-only since 2026-06-07; the EC2/CFN edge matrix was retired — fleet source of truth: `deploy/aws/lightsail/edge-targets-lightsail.json`), which hold the real OAuth/setup-token accounts and forward to Anthropic. (prod itself remains EC2/CFN; only the edge fleet is Lightsail.) So the prod anthropic pool failing over across `cc-us3 / cc-us6 / …` is failing over across *edges*, not local accounts.

**Attribution discipline:** a prod `cc-<edge>` cooldown (`temp_unschedulable_reason.matched_keyword='anthropic_upstream_error'`) is almost never prod-local — the real cause is on that edge. prod's `upstream-429 by account` / `recovered-200` are polluted by client-cancel tagging + failover smear and **cannot tell a dead edge from a healthy one** (a dead single-account edge and a healthy multi-account edge can both read ~1300 upstream-429). The reliable signal is each edge's OWN access-log `served_200 : no_available_429` ratio + schedulable-account count — run `ops/observability/scan-edge-health.sh`. See skills `tokenkey-online-log-troubleshooting` (§0 trap 9, §6.1 D) and `tokenkey-online-traffic-profile` (§4.1).

## Model serving SSOT (`model_mapping`, catalog, prod vs edge)

Customer-visible serving is gated by three layers that must stay aligned on **prod**:

1. **可展示** — `supported*CatalogModels` / `ServableClientFacingIDs` (public `/models`, `/pricing`, menu).
2. **已定价** — channel pricing + `tk_pricing_overlay.json` (zero-price leak is an ops alert, not a customer block).
3. **可服务 + prod 账号放行** — prod `accounts.credentials.model_mapping` (plus optional runtime replacement in `settings.tk_account_model_mapping_runtime`) must match the compiled Go floor for each managed platform.

**Official upstream aliases are displayable when priced and servable.** For every
TokenKey-managed native platform and newapi `channel_type`, if the provider's
official model page (or curated `tk_served_models.json` row for newapi long-tail)
declares a model id or alias, and TokenKey has verified it is **priced + servable**
on the target account/path, it belongs in the public catalog/menu — not only the
stable bare id. Legacy retirement redirects and third-party slugs without an
official declaration stay **priced-only** (explicit requests must not bill `$0`).

**prod is the only post-release config check target.** After deploy + smoke, run:

```bash
python3 ops/pricing/manage-account-model-mapping-runtime.py check-accounts --json
```

Default scope is prod only. A violation is a yellow configuration-drift finding:
review the Go-SSOT-derived diff and converge prod via:

```bash
python3 ops/pricing/manage-account-model-mapping-runtime.py apply-accounts --target prod --dry-run
python3 ops/pricing/manage-account-model-mapping-runtime.py apply-accounts --target prod --confirm yes-apply-account-model-mapping
```

For an explicit new-model activation, modelops may separately run a release-floor
precheck from the checkout that owns the intended Go SSOT:

```bash
python3 ops/pricing/manage-account-model-mapping-runtime.py release-gate
```

That command gates the modelops activation only. Generic `deploy-stage0.yml`
deploys and rollbacks never call it and must not depend on live mapping
convergence or on the target tag containing the Go helper. Their acceptance
path is image deployment plus real post-deploy smoke/display canaries.

**Edge accounts keep empty `model_mapping`.** User traffic is `client → prod gateway → edge relay → upstream`; prod already selects the model and routes to the edge mirror/OAuth account. Edge-side mapping is therefore platform-level passthrough (empty = unrestricted). Do **not** treat edge empty mappings as drift, do **not** bulk-apply prod floors to edges, and do **not** fail release checks because `--include-edges` shows violations. Use `--include-edges` only for explicit edge-specific troubleshooting.

**New-model probes must split gateway vs upstream.** `400 Unsupported model: <id>` from a prod/edge TokenKey probe means the model is absent from catalog/floor/mapping — not proof that the upstream provider cannot serve it. For capability truth on a specific OAuth account, use direct upstream probes (e.g. `ops/stage0/probe_grok_upstream_model.sh` on edge) or account-scoped gateway probes after prod mapping is updated.

Operator entry: skill `tokenkey-modelops-planner` (branch D). Details: `ops/pricing/README.md` § Account model_mapping runtime hot update.

## PR Checklist

- `go test -tags=unit ./...` passes
- `go test -tags=integration ./...` passes
- `golangci-lint run ./...` — no new issues
- `pnpm-lock.yaml` in sync (if `package.json` changed)
- Test stubs complete (if interfaces changed)
- Ent generated code committed (if schema changed)
- `go build ./...` succeeds (cross-repo dependency compiles)
- If bumping `backend/cmd/server/VERSION` for a release: commit message contains **no** literal `[skip ci]` / `[ci skip]` anywhere (rule 9.2 — discussion of the marker counts as carrying it). Use `bash scripts/release-tag.sh vX.Y.Z` to push the tag — it enforces this mechanically.
- If touching `.github/workflows/release.yml`: `simple_release` default stays `false`; warning banner step is intact (rule 9.1)
- If the PR deletes any upstream-owned file/method/route: PR description contains the (a)/(b)/(c) justification block from rule §5.x; otherwise change to "override default" or "disable via setting" instead
- After upstream merge: PR body includes `git log --oneline upstream/main..HEAD | wc -l` and the top-5 lines of `git diff --stat upstream/main..HEAD -- backend/` (rule §5.y audit cadence). The `Upstream Merge PR Shape` workflow (§5.y.1) enforces this automatically — fix any failures it reports rather than ignoring them.
- Drift check: before opening any non-trivial PR, run `bash scripts/upstream/check-drift.sh`. If TK is behind upstream/main, pause and either land the upstream merge first or document why this PR ships out of order.
- Upstream override marker (rule §5.y.1): if the PR diff touches any upstream-shaped path (handlers / services / repositories / middleware / relay / server / views / components / api / migrations / ent schema, excluding `*_tk_*.go` / `*.tk.ts` / `*_test.go` / TK-only subpackages), the gate is **coverage-first** — a pure-insertion diff or **verified sentinel coverage** of every deletion-bearing upstream file (its `path` pinned in some `scripts/sentinels/*.json`, pre-existing or added this PR) auto-passes with no marker. Only an *uncovered* revert-risk edit needs a marker. `upstream-touch-guarded` is **mechanically verified**: it claims the touched files are already pinned, so a false claim (no covering sentinel `path`) **fails** the gate — prefer adding the real anchor. The other three (`upstream-touch-trivial` / `upstream-merge` / `no-upstream-touch`) are honest opt-outs asserting protection is not needed. `scripts/preflight.sh` enforces this mechanically via `scripts/checks/upstream-override-marker.py`.
- Reviewer picks the GitHub merge button per rule §5.y: **Squash and merge** for TK-originated PRs (feature / fix / chore / docs), **Create a merge commit** for `merge/upstream-*` PRs. Never use **Rebase and merge** on `main`.
- **Root docs / deploy boundary:** keep root user-facing files (`README*.md`, root compose examples, generic upstream deployment snippets) aligned with upstream by default. TokenKey-specific local Stage0 validation, AWS prod deployment, smoke-test, image/tag, domain, and operator runbook changes belong under `deploy/*` (or the matching skill text), not in root README files. Only change root files when the build/release contract truly requires it, and prefer a short pointer to `deploy/*` over duplicating deployment steps.
- If the PR touches `dev-rules/` (submodule pointer bump or `.cursor/rules/` resync): per rule §10, the dev-rules submodule MUST be pushed first; this PR's CI `preflight` job will fail otherwise (the dev-rules SHA in `.gitmodules` won't be reachable on `origin/main`).
- **If the PR fixes an upstream / claude-code issue, record it in the fix ledger from inside the PR** (so the issue-watchdog triage doesn't re-select an already-fixed issue, and you never need a separate固化 PR):
  1. Add a fact-check entry to `.cache/upstream/fact-checks.json` (for Wei-Shaw/sub2api) or `.cache/anthropic/cc-fact-checks.json` (for anthropics/claude-code). The `fixed_if_all_present` anchors (`path:needle`) are the irreducible human judgment — pick stable lines that prove the fix is present on `main`.
  2. Run `python3 scripts/upstream/apply-fix-ledger.py --ledger <upstream|anthropic> --apply` to propagate it into `fixes.json` + `triage.json` (byte-identical to the daily `*-issue-watchdog.yml`, so no dual-writer churn).
  3. Declare the issue in a commit message trailer: `Upstream-Fixes: Wei-Shaw/sub2api#NNNN` or `Anthropic-Fixes: anthropics/claude-code#NNNN` (comma-separated for multi-issue fixes).
  `scripts/preflight.sh` gates this mechanically (`sub2api: fix-ledger consistency`): a commit that declares a fix via the trailer must carry a matching fact-check whose anchors resolve and whose propagation is already applied — otherwise preflight fails with the exact `--apply` command to run. The gate is trailer-scoped, so PRs that don't declare a fix are unaffected.
