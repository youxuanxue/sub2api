---
name: tokenkey-kiro-reauth
description: TokenKey Kiro OAuth re-authorization and refresh troubleshooting workflow. Use when a Kiro edge account shows OAuth 401, Invalid bearer token, grant revoked upstream, repeated manual re-oauth, Kiro refresh failures, or when copying freshly logged-in local Kiro credentials onto an edge and verifying Kiro Claude traffic.
---

# TokenKey: Kiro Re-OAuth Runbook

This skill repairs a Kiro OAuth account on a TokenKey edge and proves the edge
can still serve real Kiro traffic.

Use `tokenkey-online-log-troubleshooting` and
`tokenkey-online-traffic-profile` for generic logs/traffic windows. This skill
adds the Kiro-specific auth/apply/verify workflow plus bundled scripts for the
mechanical steps:

- `.cursor/skills/tokenkey-kiro-reauth/scripts/run_kiro_reauth_flow.py`
- `.cursor/skills/tokenkey-kiro-reauth/scripts/local_kiro_credentials.py`
- `.cursor/skills/tokenkey-kiro-reauth/scripts/apply_edge_kiro_oauth.py`
- `.cursor/skills/tokenkey-kiro-reauth/scripts/compare_auth_summaries.py`
- `.cursor/skills/tokenkey-kiro-reauth/scripts/probe_edge_auth_summary.sh`
- `.cursor/skills/tokenkey-kiro-reauth/scripts/probe_real_kiro_request.sh`

## Fast Path

Use the orchestrator first. It normalizes `edge-<id>` / `edge:<id>` to the
deployable edge id, resolves the exact Kiro OAuth account by name when
`--account-id` is omitted, defaults the admin password file from the normalized
edge id, and fails closed on zero or multiple matches.

```bash
# read-only: resolve edge + exact account, no local credential read, no writes
python3 .cursor/skills/tokenkey-kiro-reauth/scripts/run_kiro_reauth_flow.py \
  <account-name> <edge-id> --plan-only

# read-only: compare local safe fingerprints with edge safe fingerprints
python3 .cursor/skills/tokenkey-kiro-reauth/scripts/run_kiro_reauth_flow.py \
  <account-name> <edge-id>

# repair + verify: apply current local Kiro cache, restore schedulable, prove traffic
python3 .cursor/skills/tokenkey-kiro-reauth/scripts/run_kiro_reauth_flow.py \
  <account-name> <edge-id> \
  --apply --ensure-schedulable --verify-real-request
```

Example: `kiro-us6-real edge-us6` resolves to edge `us6`, account id `2`, and
`~/Codes/keys/tokenkey-us6-admin-password.txt` when present.

## Ground Rules

- Treat all Kiro credentials as secrets. Never print `access_token`,
  `refresh_token`, `client_secret`, or full credentials JSON in chat or logs.
- Prefer read-only diagnosis first. Writing refreshed credentials to an edge is
  a live operation and requires explicit user authorization.
- Use exact account identity before writing: normalized edge id, account id,
  account name,
  `platform=kiro`, `type=oauth`, group binding, status, schedulable.
- Use `ops/observability/run-probe.sh --target edge:<id>` for remote probes.
  Prefer the bundled probes; create a custom temporary probe only when a bundled
  probe cannot answer the question, then delete it before the final response.
- Every remote SQL query must emit field-named JSON (`row_to_json` or
  `jsonb_build_object`). Never rely on column positions.

## False Signals: Do Not Treat These As Kiro Truth

- `probe-caps.sh PLATFORM=kiro` alone is not enough to attribute an incident. It
  also catches global `no available` / rate-limit rows that may belong to
  Anthropic on the same edge.
- `POST /api/v1/admin/accounts/:id/refresh` is not Kiro-safe today. In
  `backend/internal/handler/admin/account_handler.go`, Kiro falls through to the
  Anthropic OAuth refresh branch. Use it only as code diagnosis, not as Kiro
  remediation or proof.
- `GET /api/v1/admin/accounts/:id/usage?source=active&force=true` queries Kiro's
  `GetUsageLimits` control plane. An explicit invalid-token 401/403 may trigger
  one lock-protected Kiro OAuth refresh and one usage retry, but usage success
  never clears an account error or restores scheduling. It is still not Kiro
  `/v1/messages` truth; only a real model request proves data-plane recovery.
- `POST /api/v1/admin/accounts/:id/apply-oauth-credentials` clears account error
  and invalidates token cache, but does **not** guarantee `schedulable=true`.
  Always verify `schedulable` after apply, and flip it explicitly if still
  false.
- On Stage0 edges, host `localhost:8080` is often not bound. Real Kiro probes
  should run **inside** the `tokenkey` container against
  `http://localhost:8080`, or hit the public domain via Caddy if container exec
  is unavailable.

## Deterministic Workflow

Use Fast Path for normal plan/compare/apply+verify. Read this reference only for phase-level debugging, local token refresh, manual credential acquisition, or alternate probe methods. Never print secrets; an explicit invalid grant requires reauthorization, and only a real model request proves recovery. 见 [操作细则](references/workflow.md)。

## Refresh Semantics

TokenKey has a real Kiro refresher:

- `backend/internal/service/kiro_token_refresher.go`
- `backend/internal/integration/kiro/refresh.go`
- `backend/internal/service/token_refresh_service.go`
- `backend/internal/repository/account_repo.go` via
  `engine.OAuthRefreshPlatforms()`

Kiro refresh endpoints:

- social: `https://prod.us-east-1.auth.desktop.kiro.dev/refreshToken`
- IdC: `https://oidc.<region>.amazonaws.com/token`

Refresh can fix expired or near-expired access tokens. It cannot fix upstream
grant revocation. In TokenKey, a 401 on a still-valid OAuth access token is
treated as revoked grant and escalates to manual re-authorization:

- code: `backend/internal/service/ratelimit_service_tk_oauth401.go`
- marker: `oauth_401_valid_token_revoked`
- user-facing text includes:
  `OAuth 401 on a still-valid access token — grant revoked upstream`

Repeated reauth usually points to one of:

- same Kiro/AWS account re-logged on another host, invalidating the old grant
- old edge/container still using an older `refresh_token`
- `client_id/client_secret` changed or no longer matches the `refresh_token`
- user/session revoked upstream
- upstream risk/security policy revoking desktop grants

## Related References

- `docs/operator/kiro-account-onboarding.md`
- `backend/internal/handler/admin/account_handler.go`
- `backend/internal/service/account_usage_service.go`
- `backend/internal/service/kiro_token_refresher.go`
- `backend/internal/integration/kiro/refresh.go`
- `backend/internal/repository/account_repo.go`
- `ops/observability/run-probe.sh`
