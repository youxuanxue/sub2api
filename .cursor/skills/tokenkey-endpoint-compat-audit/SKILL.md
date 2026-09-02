---
name: tokenkey-endpoint-compat-audit
description: Audit TokenKey endpoint compatibility across direct platform keys, universal keys, and newapi channels. Use for endpoint matrices, direct-vs-universal parity, count_tokens fallback, or chat/responses/messages/media support. Treats probes as evidence, never catalog policy.
---

# TokenKey endpoint compatibility audit

Use this skill for endpoint behavior. Catalog/menu policy and delivery eligibility
remain owned by `docs/approved/pricing-serving-single-source-of-truth.md`; model or
mapping drift routes to `tokenkey-modelops-planner`.

## Choose the probe

| Question | Command |
| --- | --- |
| Is a direct route accepted by the gateway? | `bash ops/observability/endpoint-compat-audit.sh --direct-route-gate` |
| Does a universal key work end to end? | `bash ops/observability/endpoint-compat-audit.sh --universal-matrix --with-extras --skip-paid` |
| What rows derive from public pricing? | `bash ops/observability/endpoint-compat-audit.sh --ssot-model-matrix --list --include-paid --show-excluded` |
| Probe the derived non-paid rows | `bash ops/observability/endpoint-compat-audit.sh --ssot-model-matrix --run` |
| Classify derived rows for release evidence | `bash ops/observability/endpoint-compat-audit.sh --ssot-model-matrix --gate --show-excluded` |

Set `TK_FULLTEST_KEY` for universal probes and `TK_FULLTEST_KIRO_KEY` only when a
direct Kiro row is required. Paid image/video calls require explicit authorization
and `--include-paid`; `--skip-paid` proves nothing about media.

For one account/model, use `tokenkey-account-model-probe`. Probe commands must run
through `ops/observability/run-probe.sh` when reserved resources are needed.

## Interpret evidence

- `route_verdict=open` proves only that the local route gate accepted the shape.
- `PASS` proves the tested tuple `(model, protocol, entitled group, schedulable
  account pool)` returned the expected live shape.
- `SKIP` needs its reason. Auth, entitlement, empty pool, cooldown, `429`, and `5xx`
  do not prove that a model is absent.
- `FAIL` is actionable only after excluding probe setup and auth mistakes.
- `426` on a Responses WebSocket prelude is an open route, not an endpoint failure.
- Count-token results must distinguish native support, the Responses bridge, and a
  local estimate fallback.

Probe actions such as `keep_displayed`, `hide_or_provision`, `hide_or_add_pool`,
`hide_or_fix_entitlement`, and `reprobe_required` are operational evidence labels.
They do not edit or overrule CatalogPolicy. Only reviewed `structurally_gone`
evidence (`model_not_found` or retired) may authorize removing a catalog row;
capacity, auth, throttling, or transient failures must be remediated or reprobed.

Excluded public-pricing rows are explicit routing evidence and must not be silently
counted as PASS or SKIP.

## Report

Return a compact table containing platform/group and endpoint, direct route/live
results, universal live result, a durable artifact or PR/run link, and the next
focused action. Use `supported`, `supported_with_estimate`,
`route_open_unservable`, `closed_by_gateway`, `not_authorized`, or `unknown`.

Do not persist local `/tmp` paths or point-in-time probe history in the repository;
CI artifacts, incident bundles, and PR/run links own raw evidence.

## Parity diagnosis

When direct and universal behavior differs for the same tuple, inspect the direct
request, then these owners:

- `backend/internal/service/universal_routing_tk_endpoint_map.go`
- `backend/internal/server/routes/gateway_tk_openai_compat_handlers.go`
- `backend/internal/service/universal_routing_tk_resolver.go`
- `backend/internal/service/universal_routing_tk_serving.go`

Add or update unit tests before changing routing. Never claim broad platform
support from route registration or one unrelated successful model.

After probes, run the cleanup dry-run:

```bash
bash ops/observability/run-probe.sh \
  --target prod \
  --script ops/observability/cleanup-probe-resources.sh
```

Actual cleanup requires explicit `TK_PROBE_CLEANUP_APPLY=1`; pruning old rows uses
`prune-probe-resources.sh` and separately requires `TK_PROBE_PRUNE_APPLY=1`.
