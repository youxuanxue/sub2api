#!/usr/bin/env bash
# clear-canonical-fingerprint-cache.sh — wipe Redis fingerprint:{accountID}
# entries for every Anthropic OAuth account bound to the canonical TLS profile,
# so the next request re-seeds the HTTP fingerprint from
# applyCanonicalHTTPObserved (deploy/aws/stage0/claude_cli_2_1_150_node24_20260525.json)
# instead of carrying the old client_id / observed block written under the
# pre-rename profile name.
#
# Determinism contract (matches dev-rules-convention.mdc §"skill / command 确定性基线"):
#   - Same DB state + Redis state → same one-line JSON report
#   - All SQL reads are bound to a NUMERIC-validated profile id (no template injection)
#   - Redis DELs are scoped strictly to accounts whose extra.tls_fingerprint_profile_id
#     matches the canonical profile row; non-canonical accounts are untouched
#
# Invocation (read DB live, write Redis live — operator must explicitly target):
#   bash ops/observability/run-probe.sh --target prod \
#     --script ops/observability/clear-canonical-fingerprint-cache.sh
#   bash ops/observability/run-probe.sh --target edge:uk1 \
#     --script ops/observability/clear-canonical-fingerprint-cache.sh
#
# Env overrides:
#   CANONICAL_NAME (default: current canonical name)
#   POSTGRES_USER  (default: tokenkey)
#   POSTGRES_DB    (default: tokenkey)
#
# Output (single line on stdout, JSON):
#   {"status":"ok","canonical_name":"...","profile_id":N,
#    "cleared":[id1,id2,...],"skipped_already_empty":[id3,...]}
#
# Exit codes:
#   0 success or "no work to do" (profile not in DB yet — apply manage-anthropic-config first)
#   1 schema / sanity failure (e.g. non-numeric profile id from DB)

set -euo pipefail

CANONICAL_NAME="${CANONICAL_NAME:-claude_cli_2_1_150_node24_20260525}"
DB_USER="${POSTGRES_USER:-tokenkey}"
DB_NAME="${POSTGRES_DB:-tokenkey}"

# Reject any CANONICAL_NAME that is not snake_case + digits. Profile names in
# this repository follow that shape by convention; tightening this here
# protects the SQL below from any env-override injection attempt, even though
# the env source is operator-controlled.
if ! [[ "$CANONICAL_NAME" =~ ^[A-Za-z0-9_]+$ ]]; then
  printf '{"status":"error","reason":"CANONICAL_NAME must match [A-Za-z0-9_]+ (got: %s)"}\n' \
    "$CANONICAL_NAME"
  exit 1
fi

# Step 1 — resolve canonical profile id from DB.
PROFILE_ID=$(docker exec tokenkey-postgres psql -U "$DB_USER" -d "$DB_NAME" -X -A -t -c \
  "SELECT id FROM tls_fingerprint_profiles WHERE name = '$CANONICAL_NAME' LIMIT 1;" \
  | tr -d '[:space:]' || true)

if [ -z "$PROFILE_ID" ]; then
  printf '{"status":"skipped","canonical_name":"%s","reason":"canonical profile not in DB; run manage-anthropic-config.py apply first"}\n' \
    "$CANONICAL_NAME"
  exit 0
fi

# Reject non-numeric profile id — protects the next SQL from any unexpected DB
# output shape.
if ! [[ "$PROFILE_ID" =~ ^[0-9]+$ ]]; then
  printf '{"status":"error","canonical_name":"%s","reason":"unexpected profile_id shape from DB: %s"}\n' \
    "$CANONICAL_NAME" "$PROFILE_ID"
  exit 1
fi

# Step 2 — enumerate Anthropic OAuth accounts bound to that profile id.
ACCOUNT_IDS=$(docker exec tokenkey-postgres psql -U "$DB_USER" -d "$DB_NAME" -X -A -t -c \
  "SELECT id FROM accounts
   WHERE platform='anthropic'
     AND type='oauth'
     AND deleted_at IS NULL
     AND COALESCE(extra->>'enable_tls_fingerprint','false')='true'
     AND COALESCE(extra->>'tls_fingerprint_profile_id','0')::bigint = $PROFILE_ID
   ORDER BY id;")

# Step 3 — DEL fingerprint:{id} per account; record which keys actually existed.
cleared_arr=()
skipped_arr=()
for id in $ACCOUNT_IDS; do
  [ -z "$id" ] && continue
  if ! [[ "$id" =~ ^[0-9]+$ ]]; then
    continue
  fi
  res=$(docker exec tokenkey-redis redis-cli DEL "fingerprint:$id" 2>&1 | tr -d '[:space:]')
  case "$res" in
    1) cleared_arr+=("$id") ;;
    0) skipped_arr+=("$id") ;;
    *) skipped_arr+=("$id") ;;  # unknown redis result: treat as non-fatal
  esac
done

# Step 4 — emit single-line JSON report.
join_csv() {
  local IFS=,
  if [ "$#" -eq 0 ]; then
    echo "[]"
  else
    echo "[$*]"
  fi
}

cleared_json=$(join_csv "${cleared_arr[@]:-}")
skipped_json=$(join_csv "${skipped_arr[@]:-}")

printf '{"status":"ok","canonical_name":"%s","profile_id":%s,"cleared":%s,"skipped_already_empty":%s}\n' \
  "$CANONICAL_NAME" "$PROFILE_ID" "$cleared_json" "$skipped_json"
