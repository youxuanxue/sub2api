# TokenKey Endpoint Compatibility Baseline

This file is the curated endpoint-compatibility memory for TokenKey probes. It
is not a raw log archive and it is not a public product promise. Keep only
stable probe conclusions, evidence pointers, and the next probe focus.

## Update Rules

- Update this file after a release audit, endpoint-routing fix, media probe, or
  direct-vs-universal parity investigation.
- Do not paste full response bodies or secrets. Keep raw logs in `/tmp`, CI
  artifacts, or an incident bundle, and record only their paths or URLs.
- Record `unknown`, `SKIP`, and `FAIL` rows because they drive the next focused
  probe. Do not re-run the whole matrix when a focused row is enough.
- Treat `route_open_unservable` as a route-gate result, not live upstream
  support. Confirm live support with a universal matrix or account-model probe.
- Accepted parity target: for the same model name and protocol, a universal key
  should match a direct key bound to the same entitled group when that group has
  a schedulable account pool. Empty direct pools are recorded as
  `route_open_unservable` / `SKIP`, not as product defects.

## Latest Baseline

| Field | Value |
|---|---|
| Baseline date | 2026-07-24 |
| Target | prod (`https://api.tokenkey.dev`) |
| Runtime code anchor | `v1.8.120` release (`backend/cmd/server/VERSION`); last live deploy `v1.8.119`. The 2026-07-05 focused Anthropic closeout also includes the live config remediation that set edge default Anthropic group `id=1` to `claude_code_only=false` on `us3/us4/us5/us6`. |
| Paid media probes | approved and rerun post-`v1.8.80` / #1207 for Imagen, Veo, and Grok media SSOT display gate plus direct-vs-universal parity; latest full displayed+priced paid media gate on 2026-07-05 returned `DISPLAY_KEEP=19 DISPLAY_BLOCK=0 REPROBE_REQUIRED=0 FAIL=0`. |
| Direct route-gate command | `bash ops/observability/endpoint-compat-audit.sh --direct-route-gate` |
| Universal matrix command | `bash ops/observability/endpoint-compat-audit.sh --universal-matrix --with-extras --skip-paid` |
| SSOT model matrix command | `bash ops/observability/endpoint-compat-audit.sh --ssot-model-matrix --list --include-paid --show-excluded` |
| SSOT display gate command | `bash ops/observability/endpoint-compat-audit.sh --ssot-model-matrix --gate --show-excluded` |
| SSOT delta gate (catalog PR/push) | `python3 scripts/checks/ssot-delta-gate.py check --base origin/main --skip-live` (CI job `ssot-delta-gate`; builds the checkout-local display projection and lists diff-scoped pending-live model ids without probing prod) |
| SSOT deploy canary (prod post-deploy) | `bash ops/observability/endpoint-compat-audit.sh --ssot-model-matrix --gate --deploy-canary --deploy-closeout` |
| SSOT recent-success skip probe (ad hoc) | `bash ops/observability/run-probe.sh --target prod --script ops/observability/probe-ssot-recent-success.sh --env WINDOW_HOURS=24` |
| Baseline freshness gate | `python3 scripts/check_endpoint_compat_baseline_freshness.py` (preflight + release.yml; baseline must mention `backend/cmd/server/VERSION`) |
| Focused paid SSOT gate command | `python3 ops/test/gateway_model_ssot_matrix.py gate --include-paid --show-excluded --model imagen-4.0-generate-001 --model veo-3.1-generate-001 --model grok-imagine-image --model grok-imagine-image-quality --model grok-imagine-video` |
| Focused postrelease media parity command | `bash ops/observability/run-probe.sh --target prod --script ops/observability/probe-media-parity-postrelease.sh --with ops/pricing/probe_reserved_resources.sh` |
| Studio Imagen no-platform triage command | `bash ops/observability/run-probe.sh --target prod --script ops/observability/probe-studio-imagen-no-platform.sh` |
| Focused parity fix anchors | `backend/internal/service/universal_routing_tk_serving.go`; `backend/internal/service/gateway_service_tk_kiro_mirror_scheduling.go`; `backend/internal/service/openai_gateway_bridge_responses_fallback.go`; `backend/internal/service/openai_gateway_bridge_dispatch.go`; `backend/internal/service/openai_gateway_service.go`; `backend/internal/service/grok_media.go`; `backend/internal/service/openai_gateway_grok.go`; `backend/internal/service/openai_gateway_grok_video_tk.go`; `backend/internal/web/embed_on.go` |
| Display remediation state | All 19 current displayed+priced paid media rows are live `keep_displayed`: Antigravity image chat rows, Vertex Imagen/Veo, Grok Imagine image/video, and Volcengine Seedream/Seedance rows. Native Gemini Google One pool (`Google-Gemini` group 8; accounts `gemini-eng-g2`/`gemini-am-g2`) was retired on 2026-07-04 after direct account probes returned upstream `429`; do not claim native Gemini text support until a new pool live-probes `200`. |
| Non-paid SSOT cleanup state | Latest 2026-07-05 `v1.8.84` live `--gate-sharded` reprobe after opening edge default Anthropic group `id=1`: Anthropic `40/40`, OpenAI `60/60`, Gemini `12/12`, Antigravity `40/40`, newapi `184/184`, and Grok `20/20` all returned `200 PASS keep_displayed`; aggregate effective displayed rows `DISPLAY_KEEP=356 DISPLAY_BLOCK=0 REPROBE_REQUIRED=0 FAIL=0 EXCLUDED_BLOCK=0`. Kiro reported `NO_ROWS=1` because it currently has no public `/pricing` rows. Previous Anthropic failures were caused by edge group `claude_code_only=true` rejecting generic/universal traffic, not by model retirement or empty native pools. |
| Cleanup command | `bash ops/observability/run-probe.sh --target prod --script ops/observability/cleanup-probe-resources.sh` |
| Probe prune command | `bash ops/observability/run-probe.sh --target prod --script ops/observability/prune-probe-resources.sh` (keeps canonical `*_srcgrp_*` scopes only) |

### Evidence Pointers

| Evidence | Result |
|---|---|
| `/tmp/tokenkey-direct-route-gate-1.8.76-20260703-134217.log` | no `config_error`; all probed route gates open or WS prelude returns `426` |
| `/tmp/tokenkey-universal-skip-paid-1.8.76-20260703-134335.log` | `PASS=11 SKIP=7 FAIL=0`; Gemini text and OpenAI embeddings hit transient `429` |
| `/tmp/tokenkey-universal-skip-paid-retry-1.8.76-20260703-134536.log` | `PASS=12 SKIP=6 FAIL=0`; Gemini text recovered, OpenAI embeddings still transient `429` |
| `/tmp/tokenkey-universal-paid-media-embedding-1.8.76-20260703-141410.log` | `PASS=14 SKIP=4 FAIL=0`; paid media rerun: Gemini image and newapi image/video passed; OpenAI image unauthorized for this universal key; Gemini video not provisioned; OpenAI embeddings still `429` |
| `/tmp/tokenkey-universal-embedding-retry-1.8.76-20260703-141551.log` | `PASS=12 SKIP=6 FAIL=0`; non-paid retry recovered Gemini text; OpenAI embeddings still `429` |
| `/tmp/tokenkey-ssot-model-matrix-list-1.8.76-20260703-142404.tsv` | SSOT-derived live `/pricing` model/protocol matrix generated; excluded rows are explicit vendor/platform mapping gaps |
| `/tmp/tokenkey-ssot-model-matrix-smoke-1.8.76-20260703-142516.log` | SSOT matrix smoke run completed with no `FAIL`; surfaced current non-provisioned Anthropic catalog rows as `SKIP` |
| `/tmp/tokenkey-ssot-model-matrix-nonpaid-1.8.76-20260703-143955.log` | SSOT-derived non-paid matrix run: most rows passed or were classified as model/protocol not provisioned; initial `FAIL` rows were isolated for focused rerun |
| `/tmp/tokenkey-ssot-focused-grok-messages-1.8.76-20260703-150238.log` | Focused rerun: Grok non-default `/v1/messages` rows are model/protocol-not-provisioned `SKIP`, not gateway schema `FAIL` |
| `/tmp/tokenkey-ssot-focused-newapi-responses-1.8.76-20260703-150240.log` | Focused rerun: selected newapi `/v1/responses` rows are model/protocol-not-provisioned `SKIP`, not gateway schema `FAIL` |
| `/tmp/tokenkey-ssot-focused-newapi-chat-1.8.76-20260703-150241.log` | Focused rerun: GLM and Qwen preview chat rows pass when probed with required stream / thinking request shape |
| `/tmp/tokenkey-ssot-display-gate-nonpaid-1.8.76-20260703.log` | SSOT display gate for non-paid rows: `DISPLAY_KEEP=308 DISPLAY_BLOCK=97 REPROBE_REQUIRED=3 FAIL=0 EXCLUDED_BLOCK=9`; gate intentionally fails until non-`keep_displayed` rows are hidden, provisioned, mapped, or reprobed |
| `/tmp/tokenkey-ssot-display-gate-nonpaid-post1210-20260704T070908Z.log` | Post-native-Gemini-retirement non-paid live gate: `DISPLAY_KEEP=321 DISPLAY_BLOCK=86 REPROBE_REQUIRED=1 FAIL=0 EXCLUDED_BLOCK=9`; native Gemini rows are gone, remaining blockers are display/protocol/provisioning actions. |
| `/tmp/tokenkey-ssot-display-gate-nonpaid-localprojection-20260704T075010Z.log` | In-flight local projection for the non-paid cleanup: `DISPLAY_KEEP=318 DISPLAY_BLOCK=49 REPROBE_REQUIRED=1 FAIL=0 EXCLUDED_BLOCK=9`; this is not a deployed live result yet. |
| `/tmp/tokenkey-ssot-gate-paid-media-1.8.76-20260703.log` | Focused paid-media gate: Gemini image and newapi image/video are `keep_displayed`; Gemini video is `hide_or_provision` |
| `/tmp/tokenkey-ssot-gate-paid-image-full-1.8.76-20260703.log` | Full SSOT paid image gate: `DISPLAY_KEEP=6 DISPLAY_BLOCK=0 REPROBE_REQUIRED=2 FAIL=0`; Gemini image and newapi Seedream rows are display-safe; Grok image rows need retry/non-transient proof |
| `/tmp/tokenkey-ssot-gate-paid-video-full-1.8.76-20260703.log` | Full SSOT paid video gate: `DISPLAY_KEEP=5 DISPLAY_BLOCK=1 REPROBE_REQUIRED=1 FAIL=0`; newapi Seedance rows are display-safe; Gemini video is `hide_or_provision`; Grok video needs retry/non-transient proof |
| `/tmp/tokenkey-ssot-gate-paid-grok-media-retry-1.8.76-20260703.log` | Focused Grok paid media retry: all three Grok media rows still returned `502` and remain `reprobe_required`, not display-safe |
| `/tmp/tokenkey-focused-media-deepctx-20260703.log` | Focused read-only media context: Grok image errors in 24h concentrate on prod account `65`; 7d video usage includes successful billed `veo-3.1-generate-001` rows |
| `/tmp/tokenkey-focused-media-billing-20260703.log` | Focused 24h media billing/error context: billed `veo-3.1-generate-001` video rows exist; Grok media errors are 502 on image and both video endpoints |
| `/tmp/tokenkey-grok-relay-log-grep-20260703.log` | Prod container log grep: both universal key and direct probe key selected account `65` and failed before edge with `invalid base url: host is not allowed: api-us4.tokenkey.dev` |
| `/tmp/tokenkey-grok-image-error-triage-20260703.log` | Grok image `/v1/images/generations` focused error triage: 13/13 recent rows are 502 with no upstream event, consistent with local relay failure |
| `/tmp/tokenkey-grok-video-error-triage-20260703.log` | Grok native `/v1/video/generations` focused error triage: 9/9 recent rows are 502 with no upstream event, consistent with local relay failure |
| `/tmp/tokenkey-grok-videos-error-triage-20260703.log` | Grok OpenAI-compatible `/v1/videos/generations` focused error triage: 3/3 recent rows are 502 with no upstream event, consistent with local relay failure |
| `/tmp/tokenkey-ssot-gate-embeddings-protocol-1.8.76-20260703.log` | SSOT embedding gate: Vertex AI embedding rows are `hide_or_map_vendor`; no live universal embedding row is display-safe yet |
| `/tmp/tokenkey-ssot-gate-embedding-1.8.76-20260703.log` | Focused `text-embedding-3-small` gate: `NO_ROWS=1`; that hardcoded probe model is not currently in the displayed+priced SSOT matrix |
| `/tmp/tokenkey-probe-cleanup-dryrun-1.8.76-20260703-134455.log` | active probe groups/keys remain `0/0`; no apply needed |
| `/tmp/tokenkey-universal-paid-media-20260703-112145.log` | previous paid-media baseline retained for trend comparison |
| `/tmp/tokenkey-account-model-probe-cleanup-20260703-113537.log` | previous account-model sanity: account 63 served `gpt-5.1`; cleanup left active probe resources at `0/0` |
| `/tmp/tokenkey-postrelease-media-parity-20260703.log` | post-#1194 paid parity probe: Veo direct/universal both returned queued video `200`; Grok image direct/universal both returned xAI upstream `422`; Grok video direct/universal both returned `502 Video submit failed` |
| `/tmp/tokenkey-postrelease-media-parity-attribution-20260703.log` | attribution for the same run: universal Veo used group 16/account 57 and direct probe used Vertex account 74; Grok direct/universal both used account 65, image owner=`provider` upstream `422`, video owner=`platform` internal `502` |
| `/tmp/tokenkey-grok-media-variants-20260704.log` | focused paid Grok account-65 shape canary: image quality/min, image quality with lower `1k`, and image fast all returned upstream `200`; root `/videos/generations` returned embedded frontend HTML `200`, proving the observed video 502 was a gateway/edge path-shape issue, not xAI video unavailability |
| `/tmp/tokenkey-grok-media-v1-20260704.log` | focused paid Grok account-65 shape canary with `/v1/videos/generations`: xAI-shaped video through edge returned `200` with `request_id`; #1198 fixes gateway URL construction to use this `/v1` path and adds frontend bypass for root `/video*` / `/videos*` API paths |
| `/tmp/tokenkey-studio-imagen-no-platform-20260703.log` | Studio/BakeOff user-reported `imagen-4.0-generate-001` failure found one universal key id 5 local `403 No platform...` at `2026-07-03T15:24:40Z`, plus an older successful route to group 16/account 59 that reached upstream quota `429` |
| `/tmp/tokenkey-studio-imagen-no-platform-types-20260703.log` | live entitlement/config context: user 1 is entitled to Google-Vertex group 16; group 16 is active, `allow_image_generation=true`, and five schedulable `service_account` Vertex accounts exactly map `imagen-4.0-generate-001` |
| `/tmp/tokenkey-media-parity-post1198-imagen-20260704T031944Z.log` | post-`v1.8.79` paid direct-vs-universal parity: Imagen image, Veo video, Grok image, Grok `/v1/video/generations`, and Grok xAI-native `/v1/videos/generations` all returned `200`; direct and universal selected the matching source group/account class. |
| `/tmp/tokenkey-studio-imagen-no-platform-post1198-20260704T032114Z.log` | post-release 2h Studio Imagen no-platform triage: `rows=0`, `no_platform_rows=0`; no new local `403 No platform...` for `imagen-4.0-generate-001`. |
| `/tmp/tokenkey-ssot-paid-gate-post1198-focused-20260704T032154Z.log` | focused paid SSOT gate over current public `/pricing`: only `imagen-4.0-generate-001` is currently displayed+priced among the requested Imagen/Veo/Grok media set; it returned `PASS keep_displayed`. |
| `/tmp/tokenkey-ssot-paid-list-post1198-focused-20260704T032223Z.log` | focused SSOT list confirms current public `/pricing` contains `vertex_ai / imagen-4.0-generate-001` image row only; Veo and Grok media are not current displayed+priced rows. |
| `/tmp/tokenkey-probe-cleanup-dryrun-post1198-20260704T032318Z.log` | probe-resource cleanup dry-run after post-#1198 paid probes: active probe groups/keys remain `0/0`; no apply needed. |
| `/tmp/tokenkey-ssot-paid-list-post1207-20260704T060045Z.json` | post-`v1.8.80` focused live `/api/v1/public/pricing` projection contains five displayed+priced paid media rows: Imagen standard image, Veo 3.1 video, Grok Imagine image, Grok Imagine image quality, and Grok Imagine video; excluded rows for this scope = `0`. |
| `/tmp/tokenkey-ssot-paid-gate-post1207-20260704T0603Z.log` | post-`v1.8.80` focused paid SSOT gate: all five focused paid media rows returned `200 PASS keep_displayed`; summary `DISPLAY_KEEP=5 DISPLAY_BLOCK=0 REPROBE_REQUIRED=0 FAIL=0 EXCLUDED_BLOCK=0 NO_ROWS=0`. |
| `/tmp/tokenkey-media-parity-post1207-20260704T060045Z.log` | post-`v1.8.80` focused paid direct-vs-universal parity: Imagen image, Veo video, Grok image, Grok quality image, Grok `/v1/video/generations`, and Grok xAI-native `/v1/videos/generations` all returned `200` with expected shapes. Universal key id 5 resolved the matching source groups 16/25; direct probe keys reused source-group mirrors. |
| `/tmp/tokenkey-probe-cleanup-dryrun-post1207-20260704T0602Z.log` | probe-resource cleanup dry-run after post-#1207 paid probes: active probe groups/keys remain `0/0`; no apply needed. |
| 2026-07-04 prod SSM retirement verification | `Google-Gemini` group 8 and accounts 22/24 are soft-deleted; `account_groups` and `user_allowed_groups` rows for group 8 are `0`; `probe-caps PLATFORM=gemini` shows no active Gemini accounts and Redis `active_ids` is empty; probe cleanup shows active `__tk_probe_*` resources `0/0`. |
| `/tmp/tokenkey-anthropic-caps-fable-check-20260704T075317Z.log` | Anthropic group 1 has active native API-key accounts `cc-us5`/`cc-us6` plus Kiro mirror stubs. Recent prod errors show Kiro mirror stubs reject `claude-fable-5` and `claude-opus-4-1`; those stubs must not claim the full Anthropic catalog. |
| `/tmp/tokenkey-account50-cc-us5-fable-messages-20260704T075400Z.log` | Account-model probe: native Anthropic account 50 `cc-us5` served `claude-fable-5` on `/v1/messages` with HTTP `200`; usage attribution matched account 50. |
| `/tmp/tokenkey-account50-cc-us5-opus41-messages-20260704T080501Z.log` | Account-model probe: native Anthropic account 50 `cc-us5` served `claude-opus-4-1` on `/v1/messages` with HTTP `200`; usage attribution matched account 50. |
| `/tmp/tokenkey-account55-cc-us6-fable-messages-after-reset-20260704T080253Z.log` | Account-model probe: native Anthropic account 55 `cc-us6` returned gateway `429 No available accounts` while carrying Fable class cooldown. Operator confirmed this was quota/cooldown, not evidence that the model is unsupported. |
| `/tmp/tokenkey-ssot-post1181-antigravity-20260704T112727Z.log` | Post-`v1.8.81` / #1215 focused universal probe: antigravity text `gemini-3-flash` and `gemini-3.5-flash` returned `200 PASS` for `/v1/chat/completions` and `/v1/responses`; summary `PASS=10 SKIP=0 FAIL=0`. |
| `/tmp/tokenkey-ssot-gate-post1181-antigravity-chat-20260704T112727Z.log` | Post-`v1.8.81` antigravity chat display gate: `DISPLAY_KEEP=8 DISPLAY_BLOCK=0 REPROBE_REQUIRED=0 FAIL=0`. |
| `/tmp/tokenkey-ssot-gate-post1181-antigravity-responses-20260704T112727Z.log` | Post-`v1.8.81` antigravity responses display gate: `DISPLAY_KEEP=8 DISPLAY_BLOCK=0 REPROBE_REQUIRED=0 FAIL=0`. |
| `/tmp/tokenkey-ssot-post1181-grok-messages-20260704T112727Z.log` | Post-`v1.8.81` focused Grok `/v1/messages` probe for four non-default SKUs: all `400 SKIP model/protocol not provisioned` on the current universal key; no gateway schema `FAIL`. |
| `/tmp/tokenkey-ssot-post1181-newapi-responses-20260704T112727Z.log` | Post-`v1.8.81` focused newapi `/v1/responses` probe: `deepseek-chat` and `qwen3.7-max-preview` returned `200 PASS`; `glm-4.5`, `glm-4.5-air`, and `qwen3-8b` returned `400 SKIP model/protocol not provisioned` on the current universal key. |
| `/tmp/tokenkey-ssot-display-gate-nonpaid-post1215-20260704T113601Z.log` | Post-`v1.8.81` full non-paid SSOT display gate: `DISPLAY_KEEP=329 DISPLAY_BLOCK=27 REPROBE_REQUIRED=0 FAIL=0 EXCLUDED_BLOCK=0`; no gateway schema `FAIL`. Blockers = Anthropic Fable/Opus 4.1 chat/messages/responses, Grok four `/v1/messages` SKUs, newapi Doubao legacy + GLM/Qwen `/v1/responses` provisioning gaps. |
| `/tmp/tokenkey-ssot-display-gate-nonpaid-post1217-v1.8.82-20260704T140823Z.log` | Post-#1217 / `v1.8.82` full non-paid SSOT display gate: `DISPLAY_KEEP=315 DISPLAY_BLOCK=41 REPROBE_REQUIRED=0 FAIL=1`. Grok four `/v1/messages` SKUs all `keep_displayed`; newapi `/v1/responses` GLM/Qwen/Doubao rows unchanged; Anthropic Fable/Opus 4.1 mixed 429 empty pool + 400 provision + one `claude-fable-5` count_tokens `FAIL`. |
| `/tmp/tokenkey-ssot-gate-sharded-deploy-closeout-latest-online-20260705.log` | Latest `v1.8.83` full non-paid sharded closeout: OpenAI `60/60`, Gemini `12/12`, Grok `20/20`, newapi `184/184`, and Antigravity `40/40` all `keep_displayed`; Anthropic only `32 keep / 6 block / 2 reprobe / 2 fail`. The two `FAIL` rows are `claude-fable-5` and `claude-opus-4-1` `/v1/messages/count_tokens`, both returning `400 {"error":{"message":"Upstream request failed","type":"upstream_error"}}`. |
| `/tmp/tokenkey-edge-anthropic-fable-opus-account-probes-20260705.log` | Edge OAuth focused account probes: edge us5 account 2 returned `200` for `claude-fable-5` and `claude-opus-4-1` on `/v1/messages` and `/v1/messages/count_tokens`; edge us6 account 14 returned `200` for `claude-opus-4-1` on both paths, while `claude-fable-5` was a local model-level cooldown/empty-pool `429` (`model_rate_limited=1` on count_tokens). This confirms the current prod count_tokens `FAIL` should be fixed in gateway account selection/failover, not by declaring edge OAuth count_tokens unsupported. |
| `/tmp/tokenkey-edge-group1-cc-only-off-20260705T0903Z.log` | Live edge config verification after the 2026-07-05 remediation: deployable Anthropic edges `us3/us4/us5/us6` all have group `id=1`, `platform=anthropic`, `claude_code_only=false`, `fallback_group_id=null`. |
| `/tmp/tokenkey-ssot-gate-focused-anthropic-fable-opus-after-edge-group-open-20260705T0903Z.log` | Focused live SSOT gate after opening edge group 1: `claude-fable-5` and `claude-opus-4-1` returned `200 PASS keep_displayed` on `chat`, `count_tokens`, `messages`, and `responses`; summary `DISPLAY_KEEP=8 DISPLAY_BLOCK=0 REPROBE_REQUIRED=0 FAIL=0 EXCLUDED_BLOCK=0 NO_ROWS=0`. |
| `/tmp/tokenkey-ssot-gate-sharded-nonpaid-after-edge-group-open-20260705T0905Z.log` | Full live non-paid SSOT sharded gate after opening edge group 1: Anthropic `40`, OpenAI `60`, Gemini `12`, Antigravity `40`, newapi `184`, and Grok `20` effective displayed rows all returned `keep_displayed`; all shards had `DISPLAY_BLOCK=0 REPROBE_REQUIRED=0 FAIL=0 EXCLUDED_BLOCK=0`. Kiro returned `NO_ROWS=1` because no public `/pricing` rows matched. |
| `/tmp/tokenkey-ssot-gate-paid-media-full-latest-online-20260705.log` | Latest full displayed+priced paid media gate: 19 rows returned `200 PASS keep_displayed`; summary `DISPLAY_KEEP=19 DISPLAY_BLOCK=0 REPROBE_REQUIRED=0 FAIL=0 EXCLUDED_BLOCK=0 NO_ROWS=0`. |
| PR #1265 / 2026-07-07 Antigravity Claude catalog probe | Live `cloudcode-pa` `fetchAvailableModels` for the tested Antigravity account exposed only `claude-opus-4-6-thinking` and `claude-sonnet-4-6`. Newer/other Claude ids such as `claude-fable-5`, `claude-opus-4-8`, `claude-sonnet-5`, and `claude-haiku-4-5` reached upstream but returned `404 NOT_FOUND`; the old gateway surfaced that as generic `502 Upstream request failed`. |

## Compatibility Matrix

| platform/group | endpoint | direct route-gate | direct live servability | universal live servability | evidence | fallback / next action |
|---|---|---|---|---|---|---|
| anthropic | `/v1/messages`, `/v1/chat/completions`, `/v1/responses` | open | route_open_unservable: empty direct probe pool returned `429` | supported | direct route-gate log; universal retry log | No text rerun needed unless Anthropic capacity/account pool changes. |
| anthropic | `/v1/messages/count_tokens` | open | route_open_unservable: empty direct probe pool returned `429` | supported | direct route-gate log; universal retry log | Count-tokens universal path is covered; direct live support needs a schedulable direct pool. |
| anthropic group 1 native API-key accounts | `claude-fable-5` and `claude-opus-4-1` across chat/messages/count_tokens/responses | open | supported: prod direct account probes and operator direct tests showed `cc-us5`/`cc-us6` can serve `claude-fable-5`; `cc-us5` served `claude-opus-4-1`; focused account probes also proved native count_tokens support. | supported: after setting edge default Anthropic group `id=1` to `claude_code_only=false`, focused SSOT gate returned `200 PASS keep_displayed` for both models on `chat`, `count_tokens`, `messages`, and `responses`. | account-model probe logs; edge group verification log; focused post-remediation SSOT gate log | Resolved. Keep edge default Anthropic group 1 open for generic/universal traffic; use separate exclusive groups if a future Claude-Code-only pool is needed. |
| openai | `/v1/messages`, `/v1/messages/count_tokens`, `/v1/chat/completions`, `/v1/responses` | open | supported | supported for text and responses | direct route-gate log; universal retry log | No full-matrix rerun needed unless OpenAI gateway routing changes. |
| openai | image `gpt-image-1` | unknown | unknown | not_authorized for the current universal key in the hardcoded matrix; not present in the current displayed+priced SSOT paid-media rows | paid-media post-1.8.76 log; focused paid-media gate | If OpenAI image should be a visible product surface, first add the catalog/entitlement path, then require a `keep_displayed` gate result. |
| openai | embeddings `text-embedding-3-small` | unknown | unknown | unknown: repeated hardcoded-matrix SKIP `429`; not present in the current displayed+priced SSOT matrix | universal logs; embedding retry log; focused embedding gate | Do not treat this as display-safe. If OpenAI embeddings should be displayed, add the SSOT pricing/catalog row and rerun the embedding gate with a non-throttled pool. |
| gemini native / retired Google-Gemini group 8 | `/v1/messages`, `/v1/messages/count_tokens`, `/v1/chat/completions`, `/v1/responses`, `/v1beta/models/*` | open | route_open_unservable: group 8 and accounts 22/24 soft-deleted on 2026-07-04; no active native Gemini accounts remain | unknown post-retirement; do not claim native Gemini universal support from pre-retirement logs | 2026-07-04 account probes and retirement verification | Native Gemini smoke is disabled by default. Re-enable only after a new native pool returns live `200` through account-model probe and SSOT gate. |
| gemini native / image | unknown | unknown | unknown post-retirement; Vertex Imagen rows are tracked under newapi / Google-Vertex | full paid image gate; Studio Imagen triage | Keep Gemini-native image separate from Vertex Imagen: same model family string, different serving platform/protocol. |
| newapi / Google-Vertex group 16 | image `imagen-4.0-generate-001` | open | supported: post-`v1.8.80` direct probe returned image `200` through the source-group mirror | supported: post-`v1.8.80` universal key id 5 returned image `200` using entitled group 16; paid SSOT gate returned `keep_displayed`; post-release no-platform triage found zero new rows after #1198 | post-#1207 paid parity log; post-#1207 focused paid SSOT gate/list; Studio Imagen no-platform postrelease triage; #1198 anchors `account.go`, `universal_routing_tk_serving.go`, `universal_routing_tk_resolver.go` | Resolved for the displayed+priced Imagen standard row. Keep watching user Studio errors, but no follow-up routing fix is pending. |
| newapi / Google-Vertex group 16 | video `veo-3.1-generate-001` | open | supported: post-`v1.8.80` direct probe returned queued video `200` through the source-group mirror | supported: post-`v1.8.80` universal key id 5 returned queued video `200` using entitled group 16; paid SSOT gate returned `keep_displayed` | post-#1207 paid parity log; post-#1207 focused paid SSOT gate/list | Resolved for the displayed+priced Veo 3.1 row. Future Veo additions must enter the same pricing/list/gate path before display. |
| antigravity | `/v1/messages`, `/v1/messages/count_tokens`, `/v1/chat/completions`, `/v1/responses` | open | supported for Gemini text/image rows and the live Antigravity Claude subset only | supported for text including chat/responses via Gemini native OpenAI compat (#1215 / `v1.8.81`); Antigravity Claude is catalog-gated to `claude-sonnet-4-6` and `claude-opus-4-6-thinking` on the tested account; image models stay on Anthropic/`chat_image` | post-#1215 antigravity focused probe + chat/responses display gate logs; PR #1265 Antigravity Claude catalog probe | Antigravity text chat/responses rows are display-safe on the current universal key (`keep_displayed` for eight priced text models). Do not expose or passthrough non-catalog Antigravity Claude ids such as `claude-fable-5`, `claude-opus-4-8`, or `claude-sonnet-5`; map legacy aliases only to live ids or reject them as unsupported. Keep image SKUs on the Anthropic/`chat_image` path. |
| newapi | `/v1/messages`, `/v1/messages/count_tokens`, `/v1/chat/completions`, `/v1/responses` | open | supported for text | supported post-`v1.8.83`: sharded closeout returned `184/184 keep_displayed`, including legacy Doubao, GLM, and Qwen `/v1/responses` rows | latest sharded closeout log | #1223 proactive responses→chat fallback resolved the previous GLM/Qwen/Doubao `/v1/responses` blockers. |
| newapi | image/video | unknown | unknown | supported for current SSOT Seedream 4.0/4.5/5.0 and Seedance 1.0/1.5/2.0 rows; latest full paid media gate returned all Volcengine media rows `200 PASS keep_displayed` | latest full paid media gate; prior focused Vertex/Grok logs | Keep `--include-paid` probes explicit; default non-paid gates do not prove media servability. |
| grok group 25 / account 65 | image/video | open | supported: post-`v1.8.80` direct probes returned `200` for `grok-imagine-image`, `grok-imagine-image-quality`, `/v1/video/generations`, and xAI-native `/v1/videos/generations` through the source-group mirror | supported: post-`v1.8.80` universal key id 5 resolved group 25/account 65 and returned matching `200` for the same image/video rows; paid SSOT gate returned `keep_displayed` for all three displayed+priced Grok media rows; `/v1/videos/generations` correctly returns native `request_id` shape | post-#1207 paid parity log; post-#1207 focused paid SSOT gate/list; 2026-07-04 Grok paid shape canary logs; #1198 / #1207 anchors | Routing, upstream servability, public pricing display, Group Catalog, `/v1/models`, universal routing, and account scheduling are resolved for the tested Grok media rows. Future Grok media additions must enter the same pricing/list/gate path before display. |
| kiro | `/v1/messages`, `/v1/messages/count_tokens`, `/v1/chat/completions`, `/v1/responses` | open | route_open_unservable: empty direct probe pool returned `429` | supported for text | direct route-gate log; universal retry log | If direct Kiro serving is claimed, run account-model probe against the target account/model. |
| grok | `/v1/messages`, `/v1/messages/count_tokens`, `/v1/chat/completions`, `/v1/responses` | open | supported | supported for default text; four non-default Grok text SKUs on `/v1/messages` are display-safe post-#1217 (`keep_displayed` for all 16 rows) | post-#1217 SSOT display gate log | #1217 Grok messages gateway provision resolved the four SKU blockers from post-#1215. |
| all platforms with `/v1/responses` GET prelude | WebSocket prelude | open: `426` upgrade required | unknown | unknown | direct route-gate log | Treat `426` as expected route-open prelude, not a failure. |
| public `/pricing` SSOT projection | all derived model/protocol rows | n/a | n/a | latest full non-paid sharded gate returned all effective displayed rows `keep_displayed` after edge group 1 remediation: Anthropic 40, OpenAI 60, Gemini 12, Antigravity 40, newapi 184, Grok 20; no display blocks, reprobes, failures, or excluded blocks. Paid media focused full gate remains 19 keep, 0 block/fail. | latest SSOT list; latest sharded closeout; focused Anthropic post-remediation gate; latest paid media gate | Current non-paid and paid displayed+priced SSOT rows are display-safe. Future rows must enter the same live `/pricing` projection and gate path before display. |

## Display Gate Rule

Do not add a fourth manually maintained catalog status. The product rule is
derived at release/probe time:

```text
public /pricing row + SSOT matrix probe verdict -> display gate action
```

- `keep_displayed`: the row can remain visible for that model/protocol surface.
- `hide_or_provision`, `hide_or_add_pool`, `hide_or_fix_entitlement`, and
  `hide_or_map_vendor`: the row should be hidden/disabled for that surface, or
  the underlying pool/entitlement/vendor mapping must be fixed before display.
- `reprobe_required`: the row is not proven display-safe; retry with a
  non-throttled/non-transient pool before making a product claim.

This keeps `/pricing` as the single derived matrix source while turning every
`SKIP` or excluded displayed+priced row into a concrete product action.
For curated newapi rows, `tk_served_models.json` uses the existing `display`
boolean as the display projection: `display=true` means priced+mapped+allowed on
public catalog/menu surfaces; `display=false` keeps runtime pricing/mapping
intent but hides the row until provisioning or a later SSOT gate proves it.

## Next Probe Focus

1. Non-paid SSOT closeout is clean on live `v1.8.84`: all effective displayed rows across Anthropic, OpenAI, Gemini, Antigravity, newapi, and Grok are `keep_displayed` with `DISPLAY_BLOCK=0 REPROBE_REQUIRED=0 FAIL=0 EXCLUDED_BLOCK=0`. Anthropic Fable/Opus 4.1 blockers are resolved by keeping edge default Anthropic group `id=1` open to generic traffic (`claude_code_only=false`).
2. Embeddings are not display-safe. `text-embedding-3-small` is not currently
   in the displayed+priced SSOT matrix, and the displayed Vertex AI embedding
   rows are `hide_or_map_vendor`. Decide whether to map/provision universal
   embeddings or remove/hide those rows from the relevant surface.
3. Paid media status after the latest 2026-07-05 full gate: all 19 displayed+priced
   image/video rows are `keep_displayed`, including Antigravity image chat,
   Vertex Imagen/Veo, Grok Imagine, and Volcengine Seedream/Seedance. Keep this
   as the release rule for future paid media additions: no public display until
   the live `/pricing` projection contains the priced row and an explicit
   `--include-paid` SSOT gate returns `keep_displayed`. `gpt-image-1` is still
   only a hardcoded probe row for the current key, not a displayed+priced SSOT row.
4. Do not reintroduce a fleet ops/reconcile path that sets Anthropic group
   `claude_code_only=true` globally. If a Claude-Code-only pool is needed, use a
   separate exclusive group instead of the public/default group.
5. Current live SSOT projection has `EXCLUDED_BLOCK=0`. Future excluded
   public-pricing rows whose vendors do not map to a universal platform/endpoint
   candidate should remain hidden by default. Re-display only after a real
   platform mapping/provisioning path exists and the gate returns `keep_displayed`.
6. Antigravity Claude is not a broad Anthropic catalog surface. Reprobe
   `fetchAvailableModels` and a focused `streamGenerateContent?alt=sse` matrix
   before adding any Antigravity Claude id beyond `claude-sonnet-4-6` and
   `claude-opus-4-6-thinking`; a bare upstream `404` is a structural
   catalog/account gate, not a transient 5xx.
7. Run SSOT gates before release close-out:
   **Structural** (every commit via preflight): `catalog-serving-drift.py`,
   `display-coverage-gate.py check --base origin/main`.
   **PR delta** (CI when catalog paths change): `python3 scripts/checks/ssot-delta-gate.py check --base origin/main --skip-live` — derives the candidate display rows from the checkout-local pricing projection and lists pending-live model ids without probing prod, avoiding a deploy-order deadlock for new mapping/catalog changes.
   **Deploy canary** (prod post-deploy, owns live proof): `bash ops/observability/endpoint-compat-audit.sh --ssot-model-matrix --gate --deploy-canary --deploy-closeout`.
   Add `--include-paid` when paid media is intentionally in scope. Do **not** schedule daily full `--gate-sharded` scans (account-ban risk); use focused `--model` reprobes from this baseline when a row regresses.
   Update this baseline to mention `v{VERSION}` on every deploy (`python3 scripts/check_endpoint_compat_baseline_freshness.py`). Release bump scripts run `scripts/sync_endpoint_compat_baseline_anchor.py` automatically.
8. Reprobe Anthropic/Gemini/Kiro direct live rows only when a real schedulable
   direct probe pool exists; current `429` rows prove route openness, not
   servability.
9. After every direct/account probe, run probe-resource cleanup dry-run. Apply
   cleanup if active `__tk_probe_*` groups or keys are nonzero. The post-#1198
   dry-run showed active probe groups/keys remain `0/0`.
