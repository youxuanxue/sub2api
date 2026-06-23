---
name: tokenkey-kiro-reauth
description: TokenKey Kiro OAuth re-authorization and refresh troubleshooting workflow. Use when a Kiro edge account shows OAuth 401, Invalid bearer token, grant revoked upstream, repeated manual re-oauth, Kiro refresh failures, or when copying freshly logged-in local Kiro credentials onto an edge and verifying Kiro Claude traffic.
---

# TokenKey: Kiro Re-OAuth Runbook

This skill handles the Kiro OAuth lifecycle on TokenKey edges: diagnose whether
Kiro is failing, extract fresh credentials from a locally re-authorized Kiro IDE,
apply them to the correct edge account, clear runtime state, and verify traffic.

Use `tokenkey-online-log-troubleshooting` and `tokenkey-online-traffic-profile`
alongside this skill for live SSM probes. This skill adds the Kiro-specific
decision tree and reauth steps.

## Ground Rules

- Treat all Kiro credentials as secrets. Never print access_token,
  refresh_token, client_secret, or full credentials JSON in chat or logs.
- Prefer a read-only diagnosis first. Writing refreshed credentials to an edge is
  a live operation and requires an explicit user request such as "update us6".
- Use `ops/observability/run-probe.sh --target edge:<id>` for remote read-only
  probes when possible. If a custom probe is needed, create a temporary file via
  `apply_patch`, run it, then delete it before final response.
- Every remote SELECT must output field-named JSON (`row_to_json` or
  `jsonb_build_object`). Do not rely on column positions.
- Use exact account identity before writing: edge id, account id, account name,
  platform=`kiro`, type=`oauth`, group, status, schedulable.
- Do not use `probe-caps.sh PLATFORM=kiro` alone to attribute errors. Its error
  filter also includes global "no available/rate_limit" keywords; those can be
  Anthropic account errors on the same edge.

## Decision Tree

1. **Current traffic check**: If the user asks whether Kiro is failing now, run a
   read-only edge probe for account roster, `usage_logs`, `ops_error_logs`, and
   gateway completed logs scoped to Kiro.
2. **Refresh capability check**: If the user asks why reauth repeats, inspect
   Kiro token metadata and token_refresh logs before assuming refresh is broken.
3. **Reauth apply**: If the user says local Kiro has been re-authorized and asks
   to update an edge, extract fresh local credentials, apply them to the target
   edge account, clear error/temp-unschedulable/token cache, then verify.
4. **Postmortem**: Explain whether the event was:
   - benign expiry/refresh race, which background refresh should recover;
   - failed refresh due to invalid_grant/invalid_client, which needs reauth;
   - 401 on still-valid access token, which indicates upstream grant revocation
     and cannot be fixed by refreshing the same grant.

## Local Credential Extraction

Use this only after the user confirms Kiro was opened and re-authorized locally.
Run locally on the operator machine; do not persist the output to a repo file.

```bash
python3 - <<'PY'
import glob, json, os
cache = os.path.expanduser("~/.aws/sso/cache")
tok_path = os.path.join(cache, "kiro-auth-token.json")
t = json.load(open(tok_path, encoding="utf-8"))
auth = "social" if str(t.get("authMethod", "")).lower() == "social" else "idc"
out = {
    "access_token": t["accessToken"],
    "refresh_token": t["refreshToken"],
    "region": t.get("region", "us-east-1"),
    "auth_method": auth,
}
if auth == "idc":
    regs = []
    for path in glob.glob(os.path.join(cache, "*.json")):
        if path.endswith("kiro-auth-token.json"):
            continue
        try:
            j = json.load(open(path, encoding="utf-8"))
        except Exception:
            continue
        if "clientSecret" in j and "clientId" in j:
            regs.append(j)
    if not regs:
        raise SystemExit("missing Kiro IdC client registration in ~/.aws/sso/cache")
    reg = regs[-1]
    out["client_id"] = reg["clientId"]
    out["client_secret"] = reg["clientSecret"]
print(json.dumps(out, ensure_ascii=False, indent=2))
PY
```

Important shape:

- `auth_method=idc` requires `client_id` and `client_secret`.
- `auth_method=social` uses only `refresh_token` for refresh.
- If `expires_at` is absent in local cache, derive it by refreshing once through
  the existing TokenKey Kiro refresh path or let the edge refresh after apply.
  Prefer applying an access token plus refresh token from the real cache.

## Edge Diagnosis Probe

Resolve and identify the target:

```bash
python3 ops/stage0/edge_ssm_execution.py --repo-root . --edge-id <edge> --format json
```

Then run a read-only probe. Scope by both platform and account id/name once
known. Minimal SQL sections:

```sql
SELECT row_to_json(t) FROM (
  SELECT a.id, a.name, a.platform, a.type, a.status, a.schedulable,
         a.concurrency, a.temp_unschedulable_until,
         a.temp_unschedulable_reason,
         left(COALESCE(a.error_message,''),240) AS error_message,
         ag.group_id, ag.priority AS group_priority
  FROM accounts a
  LEFT JOIN account_groups ag ON ag.account_id=a.id
  WHERE a.platform='kiro' AND a.deleted_at IS NULL
  ORDER BY a.id, ag.group_id NULLS LAST
) t;
```

For traffic and errors, query:

- `usage_logs WHERE account_id=<kiro_id> AND created_at >= now()-interval '<N> minutes'`
- `ops_error_logs WHERE account_id=<kiro_id> OR platform='kiro' OR upstream_errors contains platform/account_id`
- `docker logs tokenkey --since <N>m`, parse JSON lines containing
  `http request completed`, then filter `account_id=<kiro_id>` or
  `platform/billing_platform=kiro`.

Report windows in UTC and local time. Mention when unrelated Anthropic
`claude-*` errors on the same edge are not Kiro.

## Applying Re-Authorization

Only proceed after explicit user authorization to update the live edge.

Preferred path:

1. Read target account and assert exactly one Kiro OAuth account matches the
   intended account id/name.
2. Merge new Kiro credentials into existing credentials. Preserve unrelated
   fields such as model_mapping, profile_arn if the new value is blank, and any
   runtime metadata not being intentionally changed.
3. Set:
   - `credentials.access_token`
   - `credentials.refresh_token`
   - `credentials.region`
   - `credentials.auth_method`
   - `credentials.client_id` and `credentials.client_secret` for IdC
   - `credentials._token_version` to a fresh millisecond timestamp
4. Clear account error and temporary unschedulable state in the same remediation
   flow:
   - `status='active'`
   - `schedulable=true`
   - `error_message=''`
   - clear `temp_unschedulable_until/reason` if present
5. Invalidate runtime caches for the account if the service exposes them on that
   edge. If using direct SQL, also restart only when cache invalidation is not
   available and stale credentials persist.

Safer application surfaces:

- Admin UI / API `POST /api/v1/admin/accounts/:id/apply-oauth-credentials` when
  you have an admin session for the edge. This endpoint clears error and
  invalidates token cache server-side.
- Direct SSM + psql only when the admin UI/session path is unavailable. Use a
  one-off script with JSON input supplied out-of-band; never echo the secret.

Avoid these mistakes:

- Do not paste secrets into command text that will be stored in shell history,
  SSM command details, or chat. If unavoidable for an emergency, rotate again
  immediately afterward and note the exposure.
- Do not update a broad `WHERE platform='kiro'` set. Use `id + name + platform +
  type + deleted_at IS NULL`.
- Do not overwrite full `extra` JSON with a credentials-only payload.

## Verification

After apply, run the checks in this order:

1. Account row: active, schedulable, no error, has access_token/refresh_token,
   and `expires_at` is parseable if present.
2. Refresh state: look for `token_refresh.service_started`,
   `token_refresh.account_refreshed`, `token_refresh.cycle_completed`, and
   absence of `token_refresh.account_refresh_failed` for the Kiro account.
3. Traffic: recent gateway completed logs for Kiro show `/v1/messages` status
   200; `ops_error_logs` exact Kiro filter is empty or clearly old.
4. If possible, run a small real Kiro-group request through the edge using the
   direct Kiro key. Do not use a default Anthropic key; `claude-*` normally
   routes to Anthropic unless the key/group is Kiro-scoped.

Verification summary must include:

- edge id, region/instance/domain
- account id/name
- time window
- request counts by status/model
- exact Kiro error rows, if any
- explicit distinction between Kiro errors and unrelated Anthropic errors

## Refresh Semantics

TokenKey has Kiro refresh:

- `backend/internal/service/kiro_token_refresher.go` implements
  `KiroTokenRefresher`.
- `backend/internal/integration/kiro/refresh.go` calls:
  - social: `prod.us-east-1.auth.desktop.kiro.dev/refreshToken`
  - IdC: `oidc.<region>.amazonaws.com/token`
- `backend/internal/service/token_refresh_service.go` registers the refresher.
- `backend/internal/repository/account_repo.go` includes Kiro in
  `ListOAuthRefreshCandidates` via `engine.OAuthRefreshPlatforms()`.

Refresh can fix expired or near-expired access tokens. It cannot fix upstream
grant revocation. In TokenKey, a 401 on a still-valid OAuth access token is
treated as revoked grant and escalates to manual re-authorization:

- `backend/internal/service/ratelimit_service_tk_oauth401.go`
- marker: `oauth_401_valid_token_revoked`
- user-facing text includes:
  `OAuth 401 on a still-valid access token — grant revoked upstream`

For repeated reauth, check likely upstream causes:

- same Kiro/AWS account re-logged on another host, invalidating the old grant
- old edge/container still using an older refresh_token
- IdC `client_id/client_secret` changed or mismatched with refresh_token
- user/session revoked in AWS/Kiro account management
- upstream risk/security policy revoking desktop grants

## Related References

- `docs/operator/kiro-account-onboarding.md` for the operator-facing local Kiro
  credential extraction flow.
- `backend/internal/service/kiro_token_refresher.go` for refresh behavior.
- `backend/internal/integration/kiro/refresh.go` for Kiro refresh endpoints.
- `backend/internal/repository/account_repo.go` for refresh candidate selection.
- `ops/observability/run-probe.sh` for SSM read-only probe transport.
