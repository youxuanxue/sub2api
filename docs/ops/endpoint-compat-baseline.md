# TokenKey Endpoint Compatibility Baseline

This file is the curated endpoint-compatibility memory for TokenKey probes. It
is not a raw log archive and it is not a public product promise. Keep only
stable probe conclusions, reproducible commands, and the next probe focus.

## Update Rules

- Update this file after a release audit, endpoint-routing fix, media probe, or
  direct-vs-universal parity investigation.
- Do not paste full response bodies, secrets, or temporary log paths. Keep raw
  logs outside Git in CI artifacts or an incident bundle; record only durable
  release/PR references and the curated conclusion.
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
| Baseline date | 2026-08-19 |
| Target | prod (`https://tokenkey.dev`; `https://api.tokenkey.dev` redirects control-plane paths with HTTP 301) |
| Runtime code anchor | `v1.8.162` release (`backend/cmd/server/VERSION`); last live deploy `v1.8.161`. v1.8.142 image built but canary deploy failed (duplicate route panic, #1611); v1.8.143 pending prod deploy. The 2026-07-05 focused Anthropic closeout also includes the live config remediation that set edge default Anthropic group `id=1` to `claude_code_only=false` on `us3/us4/us5/us6`. |
| Latest universal smoke | `v1.8.161`, canonical host: control `models`/`usage` 200; Anthropic messages + count_tokens, OpenAI chat + responses, newapi chat, and Grok chat passed; Gemini native and Antigravity were `429` empty-pool skips; OpenAI embeddings was an entitlement `403` skip; Kiro direct row was not run because no direct Kiro key; paid media was intentionally skipped. Summary: `PASS=9 SKIP=9 FAIL=0` (recorded in PR #1719). |
| Paid media probes | approved and rerun post-`v1.8.80` / #1207 for Imagen, Veo, and Grok media SSOT display gate plus direct-vs-universal parity; latest full displayed+priced paid media gate on 2026-07-05 returned `DISPLAY_KEEP=19 DISPLAY_BLOCK=0 REPROBE_REQUIRED=0 FAIL=0`. |
| Direct route-gate command | `bash ops/observability/endpoint-compat-audit.sh --direct-route-gate` |
| Universal matrix command | `TK_FULLTEST_BASE_URL=https://tokenkey.dev bash ops/observability/endpoint-compat-audit.sh --universal-matrix --with-extras --skip-paid` (the probe's old default `api.tokenkey.dev` now returns canonical HTTP 301 on control-plane paths) |
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
