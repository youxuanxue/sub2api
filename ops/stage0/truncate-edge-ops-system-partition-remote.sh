#!/usr/bin/env bash
# Remote payload: TRUNCATE one ops_system_logs monthly partition (write-side, gated by wrapper).
set -euo pipefail

PARTITION_NAME="${PARTITION_NAME:?PARTITION_NAME is required}"

if [[ ! "${PARTITION_NAME}" =~ ^ops_system_logs_20[0-9]{4}$ ]]; then
  echo "TRUNCATE_RESULT {\"ok\":false,\"reason\":\"invalid partition name\"}"
  exit 1
fi

exists="$(docker exec tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t -c \
  "SELECT to_regclass('public.${PARTITION_NAME}') IS NOT NULL;")"
if [ "${exists}" != "t" ]; then
  echo "TRUNCATE_RESULT {\"ok\":false,\"reason\":\"partition not found\",\"partition\":\"${PARTITION_NAME}\"}"
  exit 1
fi

before_bytes="$(docker exec tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t -c \
  "SELECT pg_total_relation_size('public.${PARTITION_NAME}')::bigint;")"
before_rows="$(docker exec tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t -c \
  "SELECT count(*)::bigint FROM ${PARTITION_NAME};")"

docker exec tokenkey-postgres psql -U tokenkey -d tokenkey -X -v ON_ERROR_STOP=1 -c \
  "TRUNCATE TABLE ${PARTITION_NAME};"
docker exec tokenkey-postgres psql -U tokenkey -d tokenkey -X -v ON_ERROR_STOP=1 -c \
  "VACUUM ANALYZE ${PARTITION_NAME};"

after_bytes="$(docker exec tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t -c \
  "SELECT pg_total_relation_size('public.${PARTITION_NAME}')::bigint;")"
after_rows="$(docker exec tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t -c \
  "SELECT count(*)::bigint FROM ${PARTITION_NAME};")"

df_line="$(df -P -h / | awk 'NR==2 {printf "{\"used\":\"%s\",\"avail\":\"%s\",\"pct\":\"%s\"}", $3, $4, $5}')"

printf 'TRUNCATE_RESULT {"ok":true,"partition":"%s","before_rows":%s,"after_rows":%s,"before_bytes":%s,"after_bytes":%s,"root_df":%s}\n' \
  "${PARTITION_NAME}" "${before_rows}" "${after_rows}" "${before_bytes}" "${after_bytes}" "${df_line}"
