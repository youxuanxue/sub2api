#!/usr/bin/env bash
# One-time, opt-in repair. Delivered by run-probe with both SQL companions.
set -euo pipefail
umask 077
[ "$(hostname)" = tokenkey-prod-tokyo-20261017 ]
[ "${APPLY:-}" = yes-tokyo-usage-id-repair ]
ROOT=/var/lib/tokenkey/migration/usage-id-repair-20261017
mkdir -p "$ROOT"
exec 9>"$ROOT/operator.lock"
flock -n 9
run_dir=$(mktemp -d "$ROOT/run-XXXXXXXX")
sql_dir=$(cd "$(dirname "$0")" && pwd)
psql_cmd=(docker exec -i -e PGOPTIONS='-c timezone=UTC -c statement_timeout=10000 -c lock_timeout=250ms' tokenkey-postgres psql -XqAt -v ON_ERROR_STOP=1 -U tokenkey -d tokenkey)
# A failed advance leaves table contents untouched. A sequence gap is harmless.
"${psql_cmd[@]}" < "$sql_dir/tokyo_usage_sequence_20261017.sql" > "$run_dir/advance.out" 2> "$run_dir/advance.err"
# Preserve complete preimages before rekey; no row payloads enter SSM output.
"${psql_cmd[@]}" -c "COPY (SELECT to_jsonb(u)::text FROM usage_logs u WHERE created_at > '2026-10-17T02:54:43.473757Z' AND id BETWEEN 14724938 AND 14727151 ORDER BY id,created_at) TO STDOUT WITH CSV" | gzip > "$run_dir/preimages.csv.gz"
expected_digest=$(python3 - "$run_dir/preimages.csv.gz" <<'PY'
import csv, gzip, hashlib, sys
with gzip.open(sys.argv[1], 'rt', newline='') as f:
    digest = hashlib.md5(''.join(hashlib.md5(row[0].encode()).hexdigest() for row in csv.reader(f)).encode()).hexdigest()
print(digest)
PY
)
sha256sum "$run_dir/preimages.csv.gz" > "$run_dir/SHA256SUMS"
aws s3 cp "$run_dir/preimages.csv.gz" "s3://tokenkey-prod-pgdump-682751977094/prod/pgdump/repair/tokyo-usage-id-20261017/$(basename "$run_dir")/preimages.csv.gz" --region us-east-1 --only-show-errors
"${psql_cmd[@]}" -v expected_content_md5="$expected_digest" < "$sql_dir/tokyo_usage_rekey_20261017.sql" > "$run_dir/id-map.csv" 2> "$run_dir/rekey.err"
touch "$run_dir/COMMITTED"
sha256sum "$run_dir/id-map.csv" >> "$run_dir/SHA256SUMS"
aws s3 cp "$run_dir/id-map.csv" "s3://tokenkey-prod-pgdump-682751977094/prod/pgdump/repair/tokyo-usage-id-20261017/$(basename "$run_dir")/id-map.csv" --region us-east-1 --only-show-errors
aws s3 cp "$run_dir/SHA256SUMS" "s3://tokenkey-prod-pgdump-682751977094/prod/pgdump/repair/tokyo-usage-id-20261017/$(basename "$run_dir")/SHA256SUMS" --region us-east-1 --only-show-errors
printf 'receipt=%s\n' "$run_dir"
printf 'rekeyed_rows=%s\n' "$(($(wc -l < "$run_dir/id-map.csv") - 1))"
"${psql_cmd[@]}" -c "SELECT 'remaining_conflicts',count(*) FROM usage_logs WHERE created_at > '2026-10-17T02:54:43.473757Z' AND id BETWEEN 14724938 AND 14727151; SELECT 'sequence',last_value FROM usage_logs_id_seq;"
