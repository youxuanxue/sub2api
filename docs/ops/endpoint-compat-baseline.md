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

## Latest Baseline

| Field | Value |
|---|---|
| Baseline date | 2026-07-03 |
| Target | prod (`https://api.tokenkey.dev`) |
| Code anchor | `2dd4f3364c3c` on `main`, squash merge of PR #1178 |
| Paid media probes | approved and run |
| Direct route-gate command | `bash ops/observability/endpoint-compat-audit.sh --direct-route-gate` |
| Universal matrix command | `bash ops/observability/endpoint-compat-audit.sh --universal-matrix --with-extras` |
| Cleanup command | `bash ops/observability/run-probe.sh --target prod --script ops/observability/cleanup-probe-resources.sh --env TK_PROBE_CLEANUP_APPLY=1` |

### Evidence Pointers

| Evidence | Result |
|---|---|
| `/tmp/tokenkey-universal-paid-media-20260703-112145.log` | `PASS=13 SKIP=5 FAIL=0` |
| `/tmp/tokenkey-direct-route-gate-canonical3-20260703-112947.log` | no `config_error`; all probed route gates open or WS prelude returns `426` |
| `/tmp/tokenkey-probe-resource-cleanup-apply-20260703-112447.log` | active probe groups/keys `51/50 -> 0/0` |
| `/tmp/tokenkey-account-model-probe-cleanup-20260703-113537.log` | account 63 served `gpt-5.1`; cleanup left active probe resources at `0/0` |

## Compatibility Matrix

| platform/group | endpoint | direct route-gate | direct live servability | universal live servability | evidence | fallback / next action |
|---|---|---|---|---|---|---|
| anthropic | `/v1/messages`, `/v1/messages/count_tokens`, `/v1/chat/completions`, `/v1/responses` | open | route_open_unservable: empty direct probe pool returned `429` | partial: text SKIP timeout; count_tokens supported | direct route-gate log; universal paid-media log | Reprobe Anthropic text only when capacity/account pool changes; count_tokens is already covered. |
| openai | `/v1/messages`, `/v1/messages/count_tokens`, `/v1/chat/completions`, `/v1/responses` | open | supported | supported for text and responses | direct route-gate log; universal paid-media log | No full-matrix rerun needed unless OpenAI gateway routing changes. |
| openai | image `gpt-image-1` | unknown | unknown | unknown: SKIP timeout/connection interruption | universal paid-media log | Retry paid OpenAI image only when investigating media capacity or after upstream/account change. |
| openai | embeddings `text-embedding-3-small` | unknown | unknown | unknown: SKIP `429` upstream throttle | universal paid-media log | Focused retry when embedding support matters; not a gateway regression. |
| gemini | `/v1/messages`, `/v1/messages/count_tokens`, `/v1/chat/completions`, `/v1/responses` | open | route_open_unservable: empty direct probe pool returned `429` | unknown: text SKIP `429` upstream throttle | direct route-gate log; universal paid-media log | Reprobe after Gemini pool/capacity changes. |
| gemini | image `imagen-4.0-fast-generate-001` | unknown | unknown | supported | universal paid-media log | Keep as supported; retry only after media routing/provisioning changes. |
| gemini | video `veo-3.1-generate-001` | unknown | unknown | unknown: SKIP `400` not provisioned/model unservable | universal paid-media log | Reprobe only after Gemini video account/model provisioning changes. |
| antigravity | `/v1/messages`, `/v1/messages/count_tokens`, `/v1/chat/completions`, `/v1/responses` | open | supported | supported for text | direct route-gate log; universal paid-media log | Keep direct-vs-universal parity watch when Gemini `/v1beta` routing changes. |
| newapi | `/v1/messages`, `/v1/messages/count_tokens`, `/v1/chat/completions`, `/v1/responses` | open | supported | supported for text | direct route-gate log; universal paid-media log | Reprobe after newapi channel mapping or compatibility-pool changes. |
| newapi | image/video | unknown | unknown | supported for `doubao-seedream-4-0-250828` and `doubao-seedance-2-0-260128` | universal paid-media log | Keep as paid-media baseline; focused paid reprobe after media channel changes. |
| kiro | `/v1/messages`, `/v1/messages/count_tokens`, `/v1/chat/completions`, `/v1/responses` | open | route_open_unservable: empty direct probe pool returned `429` | supported for text | direct route-gate log; universal paid-media log | If direct Kiro serving is claimed, run account-model probe against the target account/model. |
| grok | `/v1/messages`, `/v1/messages/count_tokens`, `/v1/chat/completions`, `/v1/responses` | open | supported | supported for text | direct route-gate log; universal paid-media log | No full-matrix rerun needed unless Grok model default or relay changes. |
| all platforms with `/v1/responses` GET prelude | WebSocket prelude | open: `426` upgrade required | supported as route-gate prelude | unknown | direct route-gate log | Treat `426` as expected route-open prelude, not a failure. |

## Next Probe Focus

1. Retry OpenAI image only after paid media approval and an OpenAI image account
   or upstream-capacity change.
2. Retry Gemini video only after `veo-3.1-generate-001` account/model
   provisioning changes.
3. Reprobe Anthropic/Gemini/Kiro direct live rows only when a real schedulable
   direct probe pool exists; current `429` rows prove route openness, not
   servability.
4. Reprobe OpenAI embeddings only when embeddings become part of an endpoint
   compatibility claim or a routing change touches embeddings.
5. After every direct/account probe, run probe-resource cleanup dry-run. Apply
   cleanup if active `__tk_probe_*` groups or keys are nonzero.

