#!/usr/bin/env bash
# Re-encrypt model_supplier_sources.encrypted_credential from managed-account
# api_key plaintext when TOTP_ENCRYPTION_KEY can no longer decrypt stored
# ciphertext (e.g. after a host migration that changed the key without
# re-protecting supplier sources).
#
# Default is dry-run. Apply only with APPLY=1.
# Delivered via: bash ops/observability/run-probe.sh --target prod --script ...
set -euo pipefail

APPLY="${APPLY:-0}"
SOURCE_IDS="${SOURCE_IDS:-}" # empty = all sources that have managed accounts

. /tmp/resolve-app-container.sh
if ! APP_CONTAINER="$(tk_resolve_app_container auto)"; then
  echo '{"verdict":"setup_error","error":"app_container_unresolved"}'
  exit 2
fi

export TOTP_ENCRYPTION_KEY
TOTP_ENCRYPTION_KEY="$(docker exec "$APP_CONTAINER" sh -c 'printf %s "$TOTP_ENCRYPTION_KEY"')"
if [ -z "$TOTP_ENCRYPTION_KEY" ] || [ "${#TOTP_ENCRYPTION_KEY}" -ne 64 ]; then
  echo '{"verdict":"setup_error","error":"totp_encryption_key_unavailable"}'
  exit 2
fi

SNAPSHOT_SQL=$(cat <<'SQL'
SELECT jsonb_build_object(
  'sources', COALESCE((
    SELECT jsonb_agg(jsonb_build_object(
      'id', id,
      'supplier_name', supplier_name,
      'supplier_lane', supplier_lane,
      'encrypted_credential', encrypted_credential,
      'credential_fingerprint', credential_fingerprint
    ) ORDER BY id)
    FROM model_supplier_sources
  ), '[]'::jsonb),
  'accounts', COALESCE((
    SELECT jsonb_agg(jsonb_build_object(
      'id', a.id,
      'source_id', (a.extra->>'supplier_source_id')::bigint,
      'api_key', a.credentials::jsonb->>'api_key'
    ) ORDER BY a.id)
    FROM accounts a
    WHERE a.deleted_at IS NULL
      AND COALESCE(a.extra, '{}'::jsonb) ? 'supplier_source_id'
  ), '[]'::jsonb)
)::text;
SQL
)

SNAPSHOT_FILE="$(mktemp)"
trap 'rm -f "$SNAPSHOT_FILE"' EXIT
docker exec -i \
  -e 'PGOPTIONS=-c default_transaction_read_only=on -c lock_timeout=100ms -c statement_timeout=10s' \
  tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t -v ON_ERROR_STOP=1 \
  -c "$SNAPSHOT_SQL" >"$SNAPSHOT_FILE"

export APPLY SOURCE_IDS
export SUPPLIER_REENCRYPT_SNAPSHOT_FILE="$SNAPSHOT_FILE"
python3 <<'PY'
from __future__ import annotations

import base64
import binascii
import hashlib
import hmac
import json
import os
import subprocess
import sys

from cryptography.hazmat.primitives.ciphers.aead import AESGCM

apply = os.environ.get("APPLY", "0") == "1"
source_filter = {
    int(part) for part in os.environ.get("SOURCE_IDS", "").split(",") if part.strip()
}
with open(os.environ["SUPPLIER_REENCRYPT_SNAPSHOT_FILE"], encoding="utf-8") as fh:
    snapshot = json.loads(fh.read())
key_hex = os.environ["TOTP_ENCRYPTION_KEY"].strip()
aes_key = binascii.unhexlify(key_hex)
aesgcm = AESGCM(aes_key)
hmac_key = key_hex.encode("utf-8")

accounts_by_source: dict[int, set[str]] = {}
for account in snapshot.get("accounts") or []:
    source_id = account.get("source_id")
    api_key = (account.get("api_key") or "").strip()
    if not isinstance(source_id, int) or not api_key:
        continue
    accounts_by_source.setdefault(source_id, set()).add(api_key)

plans: list[dict] = []
errors: list[dict] = []
for source in snapshot.get("sources") or []:
    source_id = source.get("id")
    if not isinstance(source_id, int):
        continue
    if source_filter and source_id not in source_filter:
        continue
    keys = sorted(accounts_by_source.get(source_id, set()))
    if not keys:
        errors.append({"source_id": source_id, "error": "no_managed_account_api_key"})
        continue
    if len(keys) != 1:
        errors.append(
            {
                "source_id": source_id,
                "error": "ambiguous_managed_account_api_keys",
                "distinct_keys": len(keys),
            }
        )
        continue
    api_key = keys[0]
    ciphertext = source.get("encrypted_credential") or ""
    try:
        raw = base64.b64decode(ciphertext)
        aesgcm.decrypt(raw[:12], raw[12:], None)
        decrypt_ok = True
    except Exception:
        decrypt_ok = False
    fingerprint = "hmac-sha256:" + hmac.new(
        hmac_key, api_key.encode("utf-8"), hashlib.sha256
    ).hexdigest()
    nonce = os.urandom(12)
    new_ct = base64.b64encode(
        nonce + aesgcm.encrypt(nonce, api_key.encode("utf-8"), None)
    ).decode("ascii")
    needs_update = (not decrypt_ok) or fingerprint != (source.get("credential_fingerprint") or "")
    plans.append(
        {
            "source_id": source_id,
            "supplier_name": source.get("supplier_name"),
            "supplier_lane": source.get("supplier_lane"),
            "decrypt_ok": decrypt_ok,
            "fingerprint_match": fingerprint == (source.get("credential_fingerprint") or ""),
            "needs_update": needs_update,
            "api_key_len": len(api_key),
            "api_key_sha16": hashlib.sha256(api_key.encode("utf-8")).hexdigest()[:16],
            "new_fingerprint": fingerprint,
            "new_encrypted_credential": new_ct if needs_update else None,
        }
    )

print(
    json.dumps(
        {
            "verdict": "blocked" if errors else ("dry_run" if not apply else "applying"),
            "apply": apply,
            "errors": errors,
            "planned": [
                {k: v for k, v in row.items() if k != "new_encrypted_credential"}
                for row in plans
            ],
            "update_count": sum(1 for row in plans if row["needs_update"]),
        },
        ensure_ascii=False,
    )
)

if errors:
    sys.exit(2)
if not apply:
    sys.exit(0)

updated = 0
for row in plans:
    if not row["needs_update"]:
        continue
    update_sql = (
        "UPDATE model_supplier_sources SET "
        f"encrypted_credential = $cred${row['new_encrypted_credential']}$cred$, "
        f"credential_fingerprint = '{row['new_fingerprint']}', "
        f"updated_at = NOW() WHERE id = {row['source_id']} RETURNING id;"
    )
    out = subprocess.check_output(
        [
            "docker",
            "exec",
            "-i",
            "tokenkey-postgres",
            "psql",
            "-U",
            "tokenkey",
            "-d",
            "tokenkey",
            "-X",
            "-A",
            "-t",
            "-v",
            "ON_ERROR_STOP=1",
            "-c",
            update_sql,
        ],
        text=True,
    ).strip()
    # psql -c may append a command tag after the RETURNING row.
    returned_id = next((line.strip() for line in out.splitlines() if line.strip()), "")
    if returned_id != str(row["source_id"]):
        print(
            json.dumps(
                {
                    "verdict": "apply_error",
                    "source_id": row["source_id"],
                    "returning": out,
                }
            )
        )
        sys.exit(3)
    updated += 1

verify_rows = subprocess.check_output(
    [
        "docker",
        "exec",
        "tokenkey-postgres",
        "psql",
        "-U",
        "tokenkey",
        "-d",
        "tokenkey",
        "-X",
        "-A",
        "-t",
        "-c",
        "SELECT id || '|' || encrypted_credential FROM model_supplier_sources ORDER BY id;",
    ],
    text=True,
).strip().splitlines()
ok = fail = 0
for line in verify_rows:
    if not line.strip():
        continue
    sid, ct = line.split("|", 1)
    if source_filter and int(sid) not in source_filter:
        continue
    try:
        raw = base64.b64decode(ct)
        aesgcm.decrypt(raw[:12], raw[12:], None)
        ok += 1
    except Exception:
        fail += 1
        print(json.dumps({"verdict": "verify_fail", "source_id": int(sid)}))
print(json.dumps({"verdict": "applied", "updated": updated, "verify_ok": ok, "verify_fail": fail}))
if fail:
    sys.exit(4)
PY
