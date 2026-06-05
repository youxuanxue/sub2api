#!/bin/bash
# tokenkey Stage0 pg_dump (every 2h). Installed by stage0-ec2-bootstrap.sh.
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

# Past 24h by mtime, and at most 12 rolling tokenkey-*.sql.gz (2h cadence).
find "${DUMP_DIR}" -maxdepth 1 -type f -name 'tokenkey-*.sql.gz' -mmin +1440 -delete 2>/dev/null || true
while IFS= read -r _oldf; do
  [ -z "${_oldf}" ] && continue
  rm -f "${_oldf}"
done < <(find "${DUMP_DIR}" -maxdepth 1 -type f -name 'tokenkey-*.sql.gz' -printf '%T@\t%p\n' 2>/dev/null \
  | sort -nr | tail -n +13 | cut -f2-)
