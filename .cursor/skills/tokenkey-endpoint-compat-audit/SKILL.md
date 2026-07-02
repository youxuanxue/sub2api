---
name: tokenkey-endpoint-compat-audit
description: Audit TokenKey endpoint compatibility across direct platform keys, universal keys, and newapi channels. Use for prod endpoint matrix probes, universal-key full matrix checks, direct-vs-universal routing parity, count_tokens fallback status, or reports about chat/responses/messages/images/video support. Separates route-gate openness from true upstream servability and forbids unsupported claims without code/probe evidence.
---

# TokenKey Endpoint Compatibility Audit

## Scope

Use this skill when the task asks for TokenKey endpoint support across platforms or key types, especially:

- direct platform key support for `/v1/messages`, `/v1/messages/count_tokens`, `/v1/chat/completions`, `/v1/responses`, Gemini `/v1beta`, image, video, or embedding endpoints;
- universal key support or direct-vs-universal parity for the same model name;
- newapi channel endpoint behavior;
- count_tokens upstream support and estimate fallback behavior;
- a post-release endpoint compatibility report.

Do not infer support from route registration alone. Classify evidence as route-gate, gateway handler support, scheduler/model support, or live upstream servability.

## Decision Tree

1. Need direct platform/key route-gate matrix:
   run `bash ops/observability/endpoint-compat-audit.sh --direct-route-gate`.

2. Need universal key end-to-end servability:
   set `TK_FULLTEST_KEY`, optionally `TK_FULLTEST_KIRO_KEY`, then run
   `bash ops/observability/endpoint-compat-audit.sh --universal-matrix --with-extras`.
   Add `--skip-paid` unless the user explicitly accepts image/video cost.

3. Need both in one release audit:
   run `bash ops/observability/endpoint-compat-audit.sh --all --with-extras`.
   Use `--skip-paid` when paid media probes are not approved.

4. Need a single account/model isolation:
   use `tokenkey-account-model-probe` after this skill identifies a suspect platform, group, account, or model.

5. Need model catalog/mapping drift instead of endpoint behavior:
   use `tokenkey-modelops-planner`; return here after the catalog source is fixed.

## Script Entrypoints

Unified wrapper:

```bash
bash ops/observability/endpoint-compat-audit.sh --print
```

Direct route-gate matrix:

```bash
bash ops/observability/endpoint-compat-audit.sh --direct-route-gate
```

This wraps:

```bash
bash ops/observability/run-probe.sh \
  --target prod \
  --script ops/observability/probe-endpoint-matrix.sh \
  --with ops/pricing/probe_reserved_resources.sh
```

Universal full matrix:

```bash
export TK_FULLTEST_KEY='sk-...'
export TK_FULLTEST_KIRO_KEY='sk-...' # optional, only for direct Kiro row
bash ops/observability/endpoint-compat-audit.sh --universal-matrix --with-extras --skip-paid
```

This wraps `ops/test/gateway_full_matrix_test.sh`.

## Interpretation Rules

- `route_verdict=open` means the local route/platform gate did not reject the request. It does not prove the selected upstream can serve the model.
- `PASS` in `gateway_full_matrix_test.sh` means live end-to-end response shape matched the expected protocol.
- `SKIP` is not a gateway regression by itself; read the reason. Common SKIPs: unauthorized group, empty pool, upstream transient, model not provisioned, paid media skipped.
- `FAIL` is actionable unless the body proves a client/auth setup mistake.
- For count_tokens, distinguish:
  - native upstream count support;
  - OpenAI-compatible `/v1/responses/input_tokens` bridge;
  - local estimate fallback for upstreams that do not expose token counting, currently expected for Gemini/Kiro/Antigravity and some upstream errors.
- For universal parity, evaluate the exact tuple `(endpoint shape, requested model name, key owner entitled groups)`. A universal key may choose among entitled groups, but it must not select a group that the same endpoint would reject for a direct key bound to that group.

## Reporting Format

Report a compact table with these columns:

- `platform/group`
- `endpoint`
- `direct route-gate`
- `direct live servability`
- `universal live servability`
- `evidence`
- `fallback / next action`

Use these verdict labels:

- `supported`: live response shape passed.
- `supported_with_estimate`: count_tokens succeeded via local estimate fallback.
- `route_open_unservable`: route passes but scheduler/upstream/model cannot serve.
- `closed_by_gateway`: local route/platform policy rejects.
- `not_authorized`: key owner lacks the group/platform.
- `unknown`: not probed; include the exact missing command or secret.

## Parity Fix Checklist

When a universal key differs from a direct key for the same model:

1. Confirm the direct key behavior with `probe-endpoint-matrix.sh` or a single direct curl.
2. Identify the endpoint shape in `backend/internal/service/universal_routing_tk_endpoint_map.go`.
3. Check candidate platform coverage against the direct route handler in `backend/internal/server/routes/gateway_tk_openai_compat_handlers.go` and downstream handler support.
4. Check per-group policy filters in `backend/internal/service/universal_routing_tk_resolver.go`, especially `allow_messages_dispatch`.
5. Check model support truth in `backend/internal/service/universal_routing_tk_serving.go`.
6. Add/adjust unit tests before changing routing.

Never claim "all platforms support endpoint X" unless both code path and live probe evidence support that statement.
