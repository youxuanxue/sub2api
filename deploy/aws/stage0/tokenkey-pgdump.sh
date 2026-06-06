#!/bin/bash
# tokenkey Stage0 pg_dump (hourly). Installed by stage0-ec2-bootstrap.sh.
# Off-box: if TOKENKEY_PGDUMP_S3_URI is set (via /var/lib/tokenkey/.env, injected
# by stage0-backups.yaml deploy), each fresh dump is also copied to S3 so the
# ledger survives instance/volume loss (off-box RPO = dump cadence). Best-effort:
# an S3 failure never fails the local dump (the local rolling copy is still made).
set -euo pipefail
DUMP_DIR=/var/lib/tokenkey/pgdump
TS=$(date -u +%Y%m%dT%H%M%SZ)
OUT="${DUMP_DIR}/tokenkey-${TS}.sql.gz"
PART="${OUT}.part"
rm -f "${PART}"

# Remove bogus sub-kib dumps from failed runs (e.g. disk full).
find "${DUMP_DIR}" -maxdepth 1 -type f -name 'tokenkey-*.sql.gz' -size -2k -delete 2>/dev/null || true
# Remove legacy pre-*.dump files left by older manual pre-migration snapshots.
find "${DUMP_DIR}" -maxdepth 1 -type f -name 'pre-*.dump' -delete 2>/dev/null || true

set -o pipefail
if ! docker exec tokenkey-postgres pg_dump -U tokenkey -d tokenkey --format=plain --no-owner \
    | gzip -9 > "${PART}"; then
  rm -f "${PART}"
  exit 1
fi

SZ=$(wc -c < "${PART}")
if [ "${SZ}" -lt 2048 ]; then
  rm -f "${PART}"
  exit 1
fi

mv -f "${PART}" "${OUT}"

# Off-box copy to S3 (best-effort; never fails the local dump). Source the .env to
# pick up TOKENKEY_PGDUMP_S3_URI (e.g. s3://tokenkey-stage0-backups/prod/pgdump).
# Absent var => no-op (back-compat; edges with no bucket configured just skip).
S3_URI=""
if [ -r /var/lib/tokenkey/.env ]; then
  S3_URI="$(sed -n 's/^TOKENKEY_PGDUMP_S3_URI=//p' /var/lib/tokenkey/.env | head -1)"
fi
if [ -n "${S3_URI}" ]; then
  if command -v aws >/dev/null 2>&1; then
    if aws s3 cp --only-show-errors "${OUT}" "${S3_URI%/}/$(basename "${OUT}")"; then
      echo "pgdump: off-boxed $(basename "${OUT}") -> ${S3_URI%/}/"
    else
      echo "::warning::pgdump: S3 off-box failed for $(basename "${OUT}") (local copy kept)" >&2
    fi
  else
    echo "::warning::pgdump: aws CLI absent; skipping S3 off-box" >&2
  fi
fi

# Past 24h by mtime, and at most 24 rolling tokenkey-*.sql.gz (hourly cadence).
find "${DUMP_DIR}" -maxdepth 1 -type f -name 'tokenkey-*.sql.gz' -mmin +1440 -delete 2>/dev/null || true
while IFS= read -r _oldf; do
  [ -z "${_oldf}" ] && continue
  rm -f "${_oldf}"
done < <(find "${DUMP_DIR}" -maxdepth 1 -type f -name 'tokenkey-*.sql.gz' -printf '%T@\t%p\n' 2>/dev/null \
  | sort -nr | tail -n +25 | cut -f2-)
