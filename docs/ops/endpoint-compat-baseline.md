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
| Baseline date | 2026-07-03 |
| Target | prod (`https://api.tokenkey.dev`) |
| Runtime code anchor | `v1.8.76` / `8454a255ad3b` on `origin/main` |
| Paid media probes | approved and rerun post-1.8.76 |
| Direct route-gate command | `bash ops/observability/endpoint-compat-audit.sh --direct-route-gate` |
| Universal matrix command | `bash ops/observability/endpoint-compat-audit.sh --universal-matrix --with-extras --skip-paid` |
| SSOT model matrix command | `bash ops/observability/endpoint-compat-audit.sh --ssot-model-matrix --list --include-paid --show-excluded` |
| SSOT display gate command | `bash ops/observability/endpoint-compat-audit.sh --ssot-model-matrix --gate --show-excluded` |
| Cleanup command | `bash ops/observability/run-probe.sh --target prod --script ops/observability/cleanup-probe-resources.sh` |

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
| `/tmp/tokenkey-ssot-gate-paid-media-1.8.76-20260703.log` | Focused paid-media gate: Gemini image and newapi image/video are `keep_displayed`; Gemini video is `hide_or_provision` |
| `/tmp/tokenkey-ssot-gate-embeddings-protocol-1.8.76-20260703.log` | SSOT embedding gate: Vertex AI embedding rows are `hide_or_map_vendor`; no live universal embedding row is display-safe yet |
| `/tmp/tokenkey-ssot-gate-embedding-1.8.76-20260703.log` | Focused `text-embedding-3-small` gate: `NO_ROWS=1`; that hardcoded probe model is not currently in the displayed+priced SSOT matrix |
| `/tmp/tokenkey-probe-cleanup-dryrun-1.8.76-20260703-134455.log` | active probe groups/keys remain `0/0`; no apply needed |
| `/tmp/tokenkey-universal-paid-media-20260703-112145.log` | previous paid-media baseline retained for trend comparison |
| `/tmp/tokenkey-account-model-probe-cleanup-20260703-113537.log` | previous account-model sanity: account 63 served `gpt-5.1`; cleanup left active probe resources at `0/0` |

## Compatibility Matrix

| platform/group | endpoint | direct route-gate | direct live servability | universal live servability | evidence | fallback / next action |
|---|---|---|---|---|---|---|
| anthropic | `/v1/messages`, `/v1/chat/completions`, `/v1/responses` | open | route_open_unservable: empty direct probe pool returned `429` | supported | direct route-gate log; universal retry log | No text rerun needed unless Anthropic capacity/account pool changes. |
| anthropic | `/v1/messages/count_tokens` | open | route_open_unservable: empty direct probe pool returned `429` | supported | direct route-gate log; universal retry log | Count-tokens universal path is covered; direct live support needs a schedulable direct pool. |
| openai | `/v1/messages`, `/v1/messages/count_tokens`, `/v1/chat/completions`, `/v1/responses` | open | supported | supported for text and responses | direct route-gate log; universal retry log | No full-matrix rerun needed unless OpenAI gateway routing changes. |
| openai | image `gpt-image-1` | unknown | unknown | not_authorized for the current universal key in the hardcoded matrix; not present in the current displayed+priced SSOT paid-media rows | paid-media post-1.8.76 log; focused paid-media gate | If OpenAI image should be a visible product surface, first add the catalog/entitlement path, then require a `keep_displayed` gate result. |
| openai | embeddings `text-embedding-3-small` | unknown | unknown | unknown: repeated hardcoded-matrix SKIP `429`; not present in the current displayed+priced SSOT matrix | universal logs; embedding retry log; focused embedding gate | Do not treat this as display-safe. If OpenAI embeddings should be displayed, add the SSOT pricing/catalog row and rerun the embedding gate with a non-throttled pool. |
| gemini | `/v1/messages`, `/v1/messages/count_tokens`, `/v1/chat/completions`, `/v1/responses` | open | route_open_unservable: empty direct probe pool returned `429` | supported | direct route-gate log; universal retry log | First universal run hit transient `429`; retry passed. |
| gemini | image `imagen-4.0-fast-generate-001` | unknown | unknown | supported | paid-media post-1.8.76 log | Direct live parity still needs a schedulable direct pool. |
| gemini | video `veo-3.1-generate-001` | unknown | unknown | route_open_unservable: model/account not provisioned on current pool; display gate says `hide_or_provision` | paid-media post-1.8.76 log; focused paid-media gate | Hide/disable this paid surface until Gemini video account/model provisioning changes and gate returns `keep_displayed`. |
| antigravity | `/v1/messages`, `/v1/messages/count_tokens`, `/v1/chat/completions`, `/v1/responses` | open | supported | supported for text | direct route-gate log; universal retry log | Keep direct-vs-universal parity watch when Gemini `/v1beta` routing changes. |
| newapi | `/v1/messages`, `/v1/messages/count_tokens`, `/v1/chat/completions`, `/v1/responses` | open | supported | supported for text | direct route-gate log; universal retry log | Reprobe after newapi channel mapping or compatibility-pool changes. |
| newapi | image/video | unknown | unknown | supported for `doubao-seedream-4-0-250828` and `doubao-seedance-2-0-260128` | paid-media post-1.8.76 log | Expand with the SSOT matrix when intentionally probing all paid media rows. |
| kiro | `/v1/messages`, `/v1/messages/count_tokens`, `/v1/chat/completions`, `/v1/responses` | open | route_open_unservable: empty direct probe pool returned `429` | supported for text | direct route-gate log; universal retry log | If direct Kiro serving is claimed, run account-model probe against the target account/model. |
| grok | `/v1/messages`, `/v1/messages/count_tokens`, `/v1/chat/completions`, `/v1/responses` | open | supported | supported for text | direct route-gate log; universal retry log | No full-matrix rerun needed unless Grok model default or relay changes. |
| all platforms with `/v1/responses` GET prelude | WebSocket prelude | open: `426` upgrade required | unknown | unknown | direct route-gate log | Treat `426` as expected route-open prelude, not a failure. |
| public `/pricing` SSOT projection | all derived non-paid model/protocol rows | n/a | n/a | no gateway schema `FAIL`, but the display gate is not clean: 308 rows can stay displayed, 97 rows should hide/provision, 3 rows need reprobe, and 9 excluded rows need mapping or hiding | SSOT matrix list, full non-paid run, focused rerun logs, display gate log | Use this as the full-matrix source and release gate. Do not hand-maintain a second all-model list. Paid full-matrix execution still requires explicit `--include-paid`. |

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

## Next Probe Focus

1. Treat the non-paid display gate as the current product action list. The
   dominant `hide_or_provision` blockers are selected newapi `/v1/responses`,
   Antigravity/Gemini OpenAI-compatible text protocols, Grok non-default
   `/v1/messages`, future OpenAI catalog rows, and a small Anthropic catalog
   prep set. Either hide/disable those model+protocol surfaces or provision
   real support; do not leave them as visible "maybe works" entries.
2. Embeddings are not display-safe. `text-embedding-3-small` is not currently
   in the displayed+priced SSOT matrix, and the displayed Vertex AI embedding
   rows are `hide_or_map_vendor`. Decide whether to map/provision universal
   embeddings or remove/hide those rows from the relevant surface.
3. Paid media gate outcome: Gemini image and newapi image/video can stay
   displayed; Gemini video is `hide_or_provision`. `gpt-image-1` is still only a
   hardcoded probe row for the current key, not a displayed+priced SSOT row.
4. Reprobe the three `reprobe_required` rows from the non-paid gate with a
   longer timeout or a cleaner pool before deciding whether they are displayable
   or should join the hide/provision list.
5. The SSOT matrix currently excludes public-pricing rows whose vendors do not
   map to a universal platform/endpoint candidate. Decide whether those rows
   should become real universal surfaces or be removed/hidden from the relevant
   catalog surface.
6. Run the SSOT display gate before release close-out:
   `bash ops/observability/endpoint-compat-audit.sh --ssot-model-matrix --gate --show-excluded`
   for non-paid rows, and add `--include-paid` when paid media is intentionally
   in scope. A clean gate means every displayed+priced row in scope is live
   supported; any non-`keep_displayed` row becomes either a hide/disable task or
   a provisioning/mapping task.
7. Reprobe Anthropic/Gemini/Kiro direct live rows only when a real schedulable
   direct probe pool exists; current `429` rows prove route openness, not
   servability.
8. After every direct/account probe, run probe-resource cleanup dry-run. Apply
   cleanup if active `__tk_probe_*` groups or keys are nonzero. The 1.8.76
   dry-run showed active probe groups/keys remain `0/0`.
