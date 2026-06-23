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

## Ground Rules

- Treat all Kiro credentials as secrets. Never print `access_token`,
  `refresh_token`, `client_secret`, or full credentials JSON in chat or logs.
- Prefer read-only diagnosis first. Writing refreshed credentials to an edge is
  a live operation and requires explicit user authorization.
- Use exact account identity before writing: edge id, account id, account name,
  `platform=kiro`, `type=oauth`, group binding, status, schedulable.
- Use `ops/observability/run-probe.sh --target edge:<id>` for remote probes.
  If a custom temporary probe is required, create it via `apply_patch`, run it,
  then delete it before the final response.
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
- `GET /api/v1/admin/accounts/:id/usage?source=active&force=true` is not Kiro
  `/v1/messages` truth. `backend/internal/service/account_usage_service.go`
  drives the Anthropic OAuth usage probe path there. A 401 on that endpoint does
  **not** prove Kiro message routing is broken.
- `POST /api/v1/admin/accounts/:id/apply-oauth-credentials` clears account error
  and invalidates token cache, but does **not** guarantee `schedulable=true`.
  Always verify `schedulable` after apply, and flip it explicitly if still
  false.
- On Stage0 edges, host `localhost:8080` is often not bound. Real Kiro probes
  should run **inside** the `tokenkey` container against
  `http://localhost:8080`, or hit the public domain via Caddy if container exec
  is unavailable.

## Deterministic Workflow

Default to the orchestrator unless you are debugging a specific phase:

```bash
python3 .cursor/skills/tokenkey-kiro-reauth/scripts/run_kiro_reauth_flow.py \
  --edge-id <edge> \
  --account-id <id> \
  --account-name <name> \
  --apply \
  --ensure-schedulable \
  --verify-real-request \
  --admin-password-file ~/Codes/keys/tokenkey-<edge>-admin-password.txt
```

Useful variants:

```bash
# read-only plan / target resolution only
python3 .cursor/skills/tokenkey-kiro-reauth/scripts/run_kiro_reauth_flow.py \
  --edge-id <edge> --account-id <id> --account-name <name> --plan-only

# local reauth already fresh, compare only, no writes
python3 .cursor/skills/tokenkey-kiro-reauth/scripts/run_kiro_reauth_flow.py \
  --edge-id <edge> --account-id <id> --account-name <name>

# mint a fresh local access token first, then apply and verify
python3 .cursor/skills/tokenkey-kiro-reauth/scripts/run_kiro_reauth_flow.py \
  --edge-id <edge> --account-id <id> --account-name <name> \
  --local-refresh --apply --ensure-schedulable --verify-real-request \
  --admin-password-file ~/Codes/keys/tokenkey-<edge>-admin-password.txt
```

### 1. Resolve the Edge and Identify the Exact Account

If you are not using the orchestrator, resolve target metadata first:

```bash
python3 ops/stage0/edge_ssm_execution.py --repo-root . --edge-id <edge> --format json
```

Then find the exact Kiro OAuth account. Minimal roster SQL:

```sql
SELECT row_to_json(t) FROM (
  SELECT a.id, a.name, a.platform, a.type, a.status, a.schedulable,
         a.concurrency, a.temp_unschedulable_until,
         a.temp_unschedulable_reason,
         left(COALESCE(a.error_message,''),240) AS error_message,
         array_remove(array_agg(DISTINCT ag.group_id), NULL) AS group_ids
  FROM accounts a
  LEFT JOIN account_groups ag ON ag.account_id = a.id
  WHERE a.platform = 'kiro' AND a.deleted_at IS NULL
  GROUP BY a.id
  ORDER BY a.id
) t;
```

Scope every later query by both `id` and `name`.

### 2. Read-Only Diagnosis

Use the normal Kiro windows:

- `usage_logs WHERE account_id=<kiro_id> AND created_at >= now()-interval '<N> minutes'`
- `ops_error_logs WHERE account_id=<kiro_id> OR platform='kiro'`
- `docker logs tokenkey --since <N>m`, filtered to
  `path=/v1/messages` and `platform/billing_platform=kiro`

Always distinguish exact Kiro rows from unrelated Anthropic `claude-*` rows on
the same edge.

### 3. Local Credential Extraction

Use this only after the user confirms Kiro was opened and re-authorized locally.
Run locally on the operator machine. Do not save the emitted JSON under a repo
path.

Commands:

```bash
python3 .cursor/skills/tokenkey-kiro-reauth/scripts/local_kiro_credentials.py --mode full
python3 .cursor/skills/tokenkey-kiro-reauth/scripts/local_kiro_credentials.py --mode admin-payload
python3 .cursor/skills/tokenkey-kiro-reauth/scripts/local_kiro_credentials.py --mode summary
```

If local cache lacks a parseable `expires_at`, or you intentionally want a newly
minted token without reopening Kiro:

```bash
python3 .cursor/skills/tokenkey-kiro-reauth/scripts/local_kiro_credentials.py --refresh --mode admin-payload
python3 .cursor/skills/tokenkey-kiro-reauth/scripts/local_kiro_credentials.py --refresh --mode summary
```

Rules:

- Prefer applying the **exact local current cache** when the user says local
  Kiro works right now.
- Use `--refresh` only when you intentionally want fresh token material or need
  a derived `expires_at`.
- `auth_method=idc` requires `client_id` and `client_secret`.
- `auth_method=social` uses only `refresh_token` for refresh.

### 4. Edge Auth Summary Probe

After you know the exact `ACCOUNT_ID` and `ACCOUNT_NAME`, read the edge summary:

```bash
bash ops/observability/run-probe.sh \
  --target edge:<edge> \
  --script .cursor/skills/tokenkey-kiro-reauth/scripts/probe_edge_auth_summary.sh \
  --env ACCOUNT_ID=<id> \
  --env ACCOUNT_NAME=<name>
```

This emits:

- account state: `status`, `schedulable`, temp-unsched fields, error text
- auth metadata: `auth_method`, `region`, `expires_at`, `_token_version`
- credential fingerprints only: `access_md5_16`, `refresh_md5_16`,
  `client_id_md5_16`, `client_secret_md5_16`

Use it both before and after apply.

### 5. Apply Re-Authorization

Only proceed after explicit user approval.

Preferred path:

1. Generate the request body locally with
   `local_kiro_credentials.py --mode admin-payload`.
2. Apply it with the bundled helper:

```bash
python3 .cursor/skills/tokenkey-kiro-reauth/scripts/apply_edge_kiro_oauth.py \
  --base-url https://api-<edge>.tokenkey.dev \
  --account-id <id> \
  --expected-account-name <name> \
  --payload-file /tmp/kiro-admin-payload.json \
  --admin-password-file ~/Codes/keys/tokenkey-<edge>-admin-password.txt \
  --ensure-schedulable
```

3. Re-read the edge auth summary immediately afterward.

Important:

- Do not paste secrets into shell history, SSM command text, or chat.
- Do not update a broad `WHERE platform='kiro'` set.
- Do not overwrite full `extra` JSON with a credentials-only payload.
- `apply-oauth-credentials` already invalidates token cache; do not add a blind
  restart unless cache invalidation is unavailable and you have evidence of
  stale credentials persisting.

### 6. Post-Apply Auth Match Check

Do not stop at “apply returned 200”. Compare the local summary with the edge
summary:

- `refresh_md5_16` must match
- `client_id_md5_16` and `client_secret_md5_16` must match for `auth_method=idc`
- `auth_method` and `region` must match
- `access_md5_16` must match when you applied the exact local current cache

Mechanical compare:

```bash
python3 .cursor/skills/tokenkey-kiro-reauth/scripts/compare_auth_summaries.py \
  --local /tmp/kiro-local-summary.json \
  --edge /tmp/kiro-edge-summary.json
```

If you used `--refresh`, compare against the **refreshed** local summary, not
against an older cached access token.

### 7. Real Kiro Request Verification

The authoritative success check is a real Kiro-group request through the edge,
not admin usage and not speculative refresh output.

```bash
bash ops/observability/run-probe.sh \
  --target edge:<edge> \
  --script .cursor/skills/tokenkey-kiro-reauth/scripts/probe_real_kiro_request.sh \
  --env GROUP_NAME=kiro \
  --env MODEL=claude-opus-4-8
```

This probe:

- fetches the direct Kiro group API key from the edge DB
- sends a real `POST /v1/messages` **from inside the `tokenkey` container**
- returns only safe response shape data and recent Kiro access-log rows

Success criterion:

- `request.http_status == 200`
- response `type=message`
- `role=assistant`

The returned text does **not** need to match an exact marker string. A real 200
message response is the truth signal. Recent Kiro access-log rows are secondary
evidence; an empty recent window does not override a successful real 200
response.

### 8. Verification Summary

Your final summary must include:

- edge id, region, instance id, domain
- account id and account name
- whether local and edge auth fingerprints match
- account state after apply: `active`, `schedulable`, no temp-unsched, no error
- real `/v1/messages` verification result
- exact Kiro error rows, if any
- explicit distinction between Kiro failures and unrelated Anthropic failures

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
