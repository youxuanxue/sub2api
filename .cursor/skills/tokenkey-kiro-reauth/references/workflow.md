## Deterministic Workflow

Default to the orchestrator unless you are debugging a specific phase. The
preferred shorthand is:

```bash
python3 .cursor/skills/tokenkey-kiro-reauth/scripts/run_kiro_reauth_flow.py \
  <account-name> <edge-id> \
  --apply --ensure-schedulable --verify-real-request
```

The explicit form still works:

```bash
python3 .cursor/skills/tokenkey-kiro-reauth/scripts/run_kiro_reauth_flow.py \
  --edge-id <edge> \
  --account-name <name> \
  --apply \
  --ensure-schedulable \
  --verify-real-request
```

Useful variants:

```bash
# read-only plan / target resolution only
python3 .cursor/skills/tokenkey-kiro-reauth/scripts/run_kiro_reauth_flow.py \
  <account-name> <edge-id> --plan-only

# local reauth already fresh, compare only, no writes
python3 .cursor/skills/tokenkey-kiro-reauth/scripts/run_kiro_reauth_flow.py \
  <account-name> <edge-id>

# mint a fresh local access token first, then apply and verify
python3 .cursor/skills/tokenkey-kiro-reauth/scripts/run_kiro_reauth_flow.py \
  <account-name> <edge-id> \
  --local-refresh --apply --ensure-schedulable --verify-real-request
```

### 1. Resolve the Edge and Identify the Exact Account

The orchestrator handles this. If you are debugging manually, resolve target
metadata first:

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

### 3. Local Credential Handling

Use this only after the user confirms Kiro CLI was re-authorized locally. Raw
credentials must never be printed or written to a temporary payload file.

The standalone helper emits only a hash-based metadata summary:

```bash
python3 .cursor/skills/tokenkey-kiro-reauth/scripts/local_kiro_credentials.py
python3 .cursor/skills/tokenkey-kiro-reauth/scripts/local_kiro_credentials.py --refresh
```

For live repair, use the orchestrator. It imports the local helper and passes the
admin payload directly in memory to the apply helper's stdin; only safe summaries
reach stdout:

```bash
python3 .cursor/skills/tokenkey-kiro-reauth/scripts/run_kiro_reauth_flow.py \
  <account-name> <edge-id> \
  --apply --ensure-schedulable --verify-real-request
```

Rules:

- Prefer applying the **exact local current cache** when the user says local
  Kiro works right now.
- Use `--local-refresh` only when intentionally minting fresh token material.
- Never add output modes that serialize full credentials or admin payloads.
- `auth_method=idc` requires `client_id` and `client_secret`.
- `auth_method=social` uses only `refresh_token` for refresh.

### 4. Edge Auth Summary Probe

After you know the exact `ACCOUNT_NAME`, read the edge summary. `ACCOUNT_ID` is
optional and should be passed when already known:

```bash
bash ops/observability/run-probe.sh \
  --target edge:<edge> \
  --script .cursor/skills/tokenkey-kiro-reauth/scripts/probe_edge_auth_summary.sh \
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

1. Run the orchestrator with `--apply`; it reads the local cache, constructs the
   request in memory, and pipes it directly to the apply helper without emitting
   or materializing secrets.
2. The orchestrator performs the read-before-write account identity check.
3. It re-reads the edge auth summary immediately afterward and compares safe
   fingerprints before running the optional real request.

Do not use the apply helper directly for normal runs. The orchestrator already
wraps payload construction, identity check, apply, post-summary, compare, and
real request verification.

Important:

- Do not paste secrets into shell history, SSM command text, or chat.
- `apply_edge_kiro_oauth.py` now does a read-before-write identity check via
  `GET /api/v1/admin/accounts/:id`; keep `--expected-account-name` accurate and
  treat a mismatch as a stop-the-line signal, not a warning.
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

The orchestrator performs this comparison in memory. For offline debugging, the
compare helper accepts files containing **safe summaries only**; never write raw
credentials or an admin payload to those files.

If you used `--local-refresh`, compare against the **refreshed** local summary,
not against an older cached access token.

### 7. Real Kiro Request Verification

The authoritative success check is a real Kiro-group request through the edge,
not admin usage and not speculative refresh output.

```bash
bash ops/observability/run-probe.sh \
  --target edge:<edge> \
  --script .cursor/skills/tokenkey-kiro-reauth/scripts/probe_real_kiro_request.sh \
  --env ACCOUNT_ID=<id> \
  --env GROUP_NAME=kiro \
  --env MODEL=claude-opus-4-8
```

This probe:

- resolves the exact target account inside the named group, then fetches that
  group's direct API key from the edge DB
- sends a real `POST /v1/messages` **from inside the `tokenkey` container**
- correlates the resulting request by exact `X-Client-Request-ID` /
  `usage_logs.request_id=client:<id>` and reports whether the target account
  actually served it
- returns only safe response shape data, exact request correlation, and recent
  Kiro access-log rows

Success criterion:

- `verification.verdict == "exact_target_verified"`
- `request.http_status == 200`
- response `type=message`
- `role=assistant`

The returned text does **not** need to match an exact marker string. A real 200
that routed to a **different** Kiro sibling account is **not** success for the
reauth target; the probe reports that as `wrong_account_served`, not as a pass.

### 8. Verification Summary

Your final summary must include:

- edge id, region, instance id, domain
- account id and account name
- whether local and edge auth fingerprints match
- account state after apply: `active`, `schedulable`, no temp-unsched, no error
- real `/v1/messages` verification result
- exact Kiro error rows, if any
- explicit distinction between Kiro failures and unrelated Anthropic failures
