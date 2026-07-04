---
name: tokenkey-anthropic-oauth-cookie-edge
description: TokenKey Anthropic OAuth account creation from local Claude Desktop/web cookies with edge-only Anthropic egress. Use when a user asks to turn local `~/Library/Application Support/Claude/Cookies` into TokenKey Anthropic OAuth credentials, create or verify an edge account such as `edge-or-2-c`, compare web-cookie auth with TokenKey OAuth credentials, or explicitly says local direct Claude/Anthropic access is forbidden because the IP must be the edge IP.
---

# TokenKey: Anthropic OAuth From Claude Cookies On Edge

Create a TokenKey Anthropic OAuth account by reading local Claude Desktop
cookies only on the operator machine while doing every Claude/Anthropic network
request from the target edge.

Use the deterministic script first:

```bash
python3 ops/anthropic/edge-cookie-oauth-account.py --help
```

## Ground Rules

- Never print `sessionKey`, `cf_clearance`, admin passwords, `access_token`,
  `refresh_token`, or full credentials JSON.
- Do not call `claude.ai` or `platform.claude.com` from the local workstation
  when the user requires edge IP egress. The script enforces this by sending a
  short-lived cookie bundle to the edge and running the OAuth flow there.
- Treat `create` as a live write. Require explicit user approval and the script
  confirm code.
- Keep the local Claude login state intact. The script opens the cookie DB
  read-only and never launches or edits Claude.
- Prefer the normal admin API path on the edge. The script falls back from
  `settings.admin_api_key` to edge admin login when needed; it does not insert
  accounts with bare SQL.

## Fast Path

Check local cookie availability without network or secrets:

```bash
python3 ops/anthropic/edge-cookie-oauth-account.py local-summary
```

Create a new account on an edge:

```bash
python3 ops/anthropic/edge-cookie-oauth-account.py create \
  --edge-id us6 \
  --account-name edge-or-2-c \
  --tier l3 \
  --confirm yes-create-anthropic-oauth-edge-account
```

Useful options:

```bash
# Accept edge-us6 / edge:us6 / us6; default admin file is:
# ~/Codes/keys/tokenkey-<edge-id>-admin-password.txt
--admin-credentials-file ~/Codes/keys/tokenkey-us6-admin-password.txt

# Force a specific Claude organization when cookies see multiple orgs.
--org-uuid <uuid>

# Use the current account if it already exists, without minting new credentials.
--if-exists verify

# Override the short-lived bundle bucket/region when the default is unavailable.
--bundle-bucket "$TOKENKEY_COOKIE_BUNDLE_BUCKET" --bundle-region us-east-1
```

Expected safe success output includes only:

- account id/name/platform/type/status/schedulable
- group names, tier id, stability tier, concurrency, priority
- booleans for access/refresh token presence
- OAuth email domain and org/account UUID presence

## Verification

Run the stability guard after creation:

```bash
python3 ops/anthropic/check-edge-oauth-stability.py \
  --edge-id us6 \
  --account-name edge-or-2-c \
  --json
```

Expected:

- `status=ok`
- `baseline_tier=l3` or the requested tier
- `diff_count=0`

If you need a roster check, use `ops/observability/run-probe.sh` with a
field-named JSON query and redact credential values. Do not print raw
`accounts.credentials`.

## Failure Map

- `claude_orgs_http_error` with `status=403` and `body_kind=html` usually means
  the local Cloudflare clearance is not accepted from the edge IP or UA. Do not
  fall back to local direct access. Establish a Claude web session through that
  edge egress, then rerun.
- `admin_credentials_not_found` means the edge lacks `settings.admin_api_key`
  and the script could not find an admin password. Pass
  `--admin-credentials-file` or recapture/reset the edge admin credentials.
- `admin_login_http_error` with `status=401` usually means the admin credential
  file is stale or was parsed incorrectly. The supported file formats are
  `email=...` / `password=...` or a one-line password plus `--admin-email`.
- `target_account_already_exists` means the name is taken. Use a new account
  name or rerun with `--if-exists verify`.
- `cookie_doc_missing_sessionKey` means the local Claude cookie DB does not
  currently hold a usable web session.

## What The Script Creates

The create path mirrors the successful manual workflow:

1. Decrypt local Claude cookies with `Claude Safe Storage`.
2. Upload a short-lived S3 JSON bundle containing the cookie header and optional
   edge admin login material.
3. Run a temporary remote script through
   `ops/observability/run-probe.sh --target edge:<id>`.
4. On the edge, call:
   - `GET https://claude.ai/api/organizations`
   - `POST https://claude.ai/v1/oauth/<org>/authorize`
   - `POST https://platform.claude.com/v1/oauth/token`
5. Create `platform=anthropic`, `type=oauth` through the edge admin API.
6. Bind `default` group and run `/admin/accounts/:id/apply-tier`.
7. Delete the S3 bundle in a `finally` cleanup.

The script intentionally stores no reusable secret artifact in the repo. If an
operator interrupts it, check the configured S3 prefix and remove stale objects:

```bash
aws s3 ls s3://$TOKENKEY_COOKIE_BUNDLE_BUCKET/tmp/tokenkey-cookie-oauth/ --region us-east-1
```
