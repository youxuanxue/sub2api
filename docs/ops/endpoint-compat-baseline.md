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
| Runtime code anchor | `v1.8.76` / `8454a255ad3b` on `origin/main` |
| Paid media probes | skipped in the post-1.8.76 run; previous paid evidence retained below |
| Direct route-gate command | `bash ops/observability/endpoint-compat-audit.sh --direct-route-gate` |
| Universal matrix command | `bash ops/observability/endpoint-compat-audit.sh --universal-matrix --with-extras --skip-paid` |
| Cleanup command | `bash ops/observability/run-probe.sh --target prod --script ops/observability/cleanup-probe-resources.sh` |

### Evidence Pointers

| Evidence | Result |
|---|---|
| `/tmp/tokenkey-direct-route-gate-1.8.76-20260703-134217.log` | no `config_error`; all probed route gates open or WS prelude returns `426` |
| `/tmp/tokenkey-universal-skip-paid-1.8.76-20260703-134335.log` | `PASS=11 SKIP=7 FAIL=0`; Gemini text and OpenAI embeddings hit transient `429` |
| `/tmp/tokenkey-universal-skip-paid-retry-1.8.76-20260703-134536.log` | `PASS=12 SKIP=6 FAIL=0`; Gemini text recovered, OpenAI embeddings still transient `429` |
| `/tmp/tokenkey-probe-cleanup-dryrun-1.8.76-20260703-134455.log` | active probe groups/keys remain `0/0`; no apply needed |
| `/tmp/tokenkey-universal-paid-media-20260703-112145.log` | previous paid-media baseline: `PASS=13 SKIP=5 FAIL=0`; not rerun for 1.8.76 |
| `/tmp/tokenkey-account-model-probe-cleanup-20260703-113537.log` | previous account-model sanity: account 63 served `gpt-5.1`; cleanup left active probe resources at `0/0` |

## Compatibility Matrix

| platform/group | endpoint | direct route-gate | direct live servability | universal live servability | evidence | fallback / next action |
|---|---|---|---|---|---|---|
| anthropic | `/v1/messages`, `/v1/chat/completions`, `/v1/responses` | open | route_open_unservable: empty direct probe pool returned `429` | supported | direct route-gate log; universal retry log | No text rerun needed unless Anthropic capacity/account pool changes. |
| anthropic | `/v1/messages/count_tokens` | open | route_open_unservable: empty direct probe pool returned `429` | supported | direct route-gate log; universal retry log | Count-tokens universal path is covered; direct live support needs a schedulable direct pool. |
| openai | `/v1/messages`, `/v1/messages/count_tokens`, `/v1/chat/completions`, `/v1/responses` | open | supported | supported for text and responses | direct route-gate log; universal retry log | No full-matrix rerun needed unless OpenAI gateway routing changes. |
| openai | image `gpt-image-1` | unknown | unknown | unknown: not reprobed post-1.8.76 (`--skip-paid`) | universal retry log; previous paid-media log | Retry paid OpenAI image only with paid-media approval or after upstream/account change. |
| openai | embeddings `text-embedding-3-small` | unknown | unknown | unknown: repeated SKIP `429` upstream throttle | universal logs | Focused retry when embedding support matters; not a gateway regression. |
| gemini | `/v1/messages`, `/v1/messages/count_tokens`, `/v1/chat/completions`, `/v1/responses` | open | route_open_unservable: empty direct probe pool returned `429` | supported | direct route-gate log; universal retry log | First universal run hit transient `429`; retry passed. |
| gemini | image `imagen-4.0-fast-generate-001` | unknown | unknown | unknown: not reprobed post-1.8.76 (`--skip-paid`); previous paid run supported | universal retry log; previous paid-media log | Focused paid reprobe after media routing/provisioning changes. |
| gemini | video `veo-3.1-generate-001` | unknown | unknown | unknown: not reprobed post-1.8.76 (`--skip-paid`); previous paid run was not provisioned/model unservable | universal retry log; previous paid-media log | Reprobe only after Gemini video account/model provisioning changes. |
| antigravity | `/v1/messages`, `/v1/messages/count_tokens`, `/v1/chat/completions`, `/v1/responses` | open | supported | supported for text | direct route-gate log; universal retry log | Keep direct-vs-universal parity watch when Gemini `/v1beta` routing changes. |
| newapi | `/v1/messages`, `/v1/messages/count_tokens`, `/v1/chat/completions`, `/v1/responses` | open | supported | supported for text | direct route-gate log; universal retry log | Reprobe after newapi channel mapping or compatibility-pool changes. |
| newapi | image/video | unknown | unknown | unknown: not reprobed post-1.8.76 (`--skip-paid`); previous paid run supported for `doubao-seedream-4-0-250828` and `doubao-seedance-2-0-260128` | universal retry log; previous paid-media log | Focused paid reprobe after media channel changes. |
| kiro | `/v1/messages`, `/v1/messages/count_tokens`, `/v1/chat/completions`, `/v1/responses` | open | route_open_unservable: empty direct probe pool returned `429` | supported for text | direct route-gate log; universal retry log | If direct Kiro serving is claimed, run account-model probe against the target account/model. |
| grok | `/v1/messages`, `/v1/messages/count_tokens`, `/v1/chat/completions`, `/v1/responses` | open | supported | supported for text | direct route-gate log; universal retry log | No full-matrix rerun needed unless Grok model default or relay changes. |
| all platforms with `/v1/responses` GET prelude | WebSocket prelude | open: `426` upgrade required | unknown | unknown | direct route-gate log | Treat `426` as expected route-open prelude, not a failure. |

## Next Probe Focus

1. OpenAI embeddings returned `429` in both post-1.8.76 universal runs. Retry
   only when embeddings become part of an endpoint compatibility claim or a
   routing/account change touches embeddings.
2. Paid media was not rerun post-1.8.76. With explicit paid approval, the
   highest-value focused paid checks are OpenAI image and Gemini video.
3. Reprobe Anthropic/Gemini/Kiro direct live rows only when a real schedulable
   direct probe pool exists; current `429` rows prove route openness, not
   servability.
4. After every direct/account probe, run probe-resource cleanup dry-run. Apply
   cleanup if active `__tk_probe_*` groups or keys are nonzero. The 1.8.76
   dry-run showed active probe groups/keys remain `0/0`.
