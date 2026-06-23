#!/usr/bin/env bash
set -euo pipefail

ACCOUNT_NAME="${ACCOUNT_NAME:-}"
ACCOUNT_ID="${ACCOUNT_ID:-}"

if [[ -n "${ACCOUNT_ID}" && ! "${ACCOUNT_ID}" =~ ^[0-9]+$ ]]; then
  echo '{"error":"ACCOUNT_ID must be blank or a numeric account id"}'
  exit 0
fi

if [[ -z "${ACCOUNT_NAME}" ]]; then
  echo '{"error":"ACCOUNT_NAME is required"}'
  exit 0
fi

sql_b64() {
  python3 - "$1" <<'PY'
import base64
import sys

print(base64.b64encode(sys.argv[1].encode("utf-8")).decode("ascii"), end="")
PY
}

ACCOUNT_NAME_B64="$(sql_b64 "${ACCOUNT_NAME}")"
PSQL=(docker exec tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t -v ON_ERROR_STOP=1)

result="$("${PSQL[@]}" -c "
WITH matches AS (
  SELECT
    a.id,
    a.name,
    a.platform,
    a.type,
    a.status,
    a.schedulable,
    a.concurrency,
    a.temp_unschedulable_until AT TIME ZONE 'UTC' AS temp_unschedulable_until_utc,
    left(COALESCE(a.temp_unschedulable_reason,''),240) AS temp_unschedulable_reason,
    left(COALESCE(a.error_message,''),240) AS error_message,
    array_remove(array_agg(DISTINCT ag.group_id), NULL) AS group_ids,
    COALESCE(a.credentials->>'auth_method','') AS auth_method,
    COALESCE(a.credentials->>'region','') AS region,
    COALESCE(a.credentials->>'expires_at','') AS expires_at,
    COALESCE(a.credentials->>'_token_version','') AS token_version,
    (COALESCE(a.credentials->>'access_token','') <> '') AS has_access_token,
    (COALESCE(a.credentials->>'refresh_token','') <> '') AS has_refresh_token,
    (COALESCE(a.credentials->>'client_id','') <> '') AS has_client_id,
    (COALESCE(a.credentials->>'client_secret','') <> '') AS has_client_secret,
    CASE
      WHEN COALESCE(a.credentials->>'access_token','') = '' THEN ''
      ELSE substring(md5(a.credentials->>'access_token') from 1 for 16)
    END AS access_md5_16,
    CASE
      WHEN COALESCE(a.credentials->>'refresh_token','') = '' THEN ''
      ELSE substring(md5(a.credentials->>'refresh_token') from 1 for 16)
    END AS refresh_md5_16,
    CASE
      WHEN COALESCE(a.credentials->>'client_id','') = '' THEN ''
      ELSE substring(md5(a.credentials->>'client_id') from 1 for 16)
    END AS client_id_md5_16,
    CASE
      WHEN COALESCE(a.credentials->>'client_secret','') = '' THEN ''
      ELSE substring(md5(a.credentials->>'client_secret') from 1 for 16)
    END AS client_secret_md5_16
  FROM accounts a
  LEFT JOIN account_groups ag ON ag.account_id = a.id
  WHERE a.name = convert_from(decode('${ACCOUNT_NAME_B64}','base64'),'utf8')
    AND (${ACCOUNT_ID:-0} = 0 OR a.id = ${ACCOUNT_ID:-0})
    AND a.platform = 'kiro'
    AND a.type = 'oauth'
    AND a.deleted_at IS NULL
  GROUP BY a.id
),
match_count AS (
  SELECT count(*) AS n FROM matches
)
SELECT CASE
  WHEN (SELECT n FROM match_count) = 1 THEN (
    SELECT row_to_json(matches)::jsonb FROM matches
  )
  WHEN (SELECT n FROM match_count) = 0 THEN jsonb_build_object(
    'error', 'account_not_found',
    'account_id', NULLIF('${ACCOUNT_ID}', '')::bigint,
    'account_name', convert_from(decode('${ACCOUNT_NAME_B64}','base64'),'utf8')
  )
  ELSE jsonb_build_object(
    'error', 'ambiguous_account_name',
    'account_name', convert_from(decode('${ACCOUNT_NAME_B64}','base64'),'utf8'),
    'matching_ids', (SELECT jsonb_agg(id ORDER BY id) FROM matches)
  )
END;
")"

result="$(printf '%s' "${result}" | tr -d '\n')"

if [[ -z "${result}" ]]; then
  python3 - "${ACCOUNT_ID}" "${ACCOUNT_NAME}" <<'PY'
import json
import sys

print(json.dumps({
    "error": "account_not_found",
    "account_id": int(sys.argv[1]) if sys.argv[1] else None,
    "account_name": sys.argv[2],
}, ensure_ascii=False))
PY
  exit 0
fi

printf '%s\n' "${result}"
