#!/bin/bash
# tokenkey Stage0 pg_dump (hourly). Installed by stage0-ec2-bootstrap.sh.
# Off-box: if TOKENKEY_PGDUMP_S3_URI is set (via /var/lib/tokenkey/.env), each fresh
# dump is also copied to S3 (archive of record; off-box RPO = dump cadence). S3
# failure never fails the local dump.
#
# Local retention: keep only ${KEEP} newest rolling dumps — S3 is the archive, local
# copies only serve fast on-box restore, so a small count keeps the data volume from
# filling as the DB grows. Pruning runs BEFORE the dump (not only after): the old
# code pruned only post-dump, so once the volume hit 100% the dump failed writing
# .part and exited before pruning — dumps piled up forever and Postgres crashed on
# the full volume (2026-06-15 prod P0). Pruning first self-heals a near-full volume.
set -euo pipefail
DUMP_DIR=/var/lib/tokenkey/pgdump
KEEP="${TOKENKEY_PGDUMP_KEEP:-6}"   # newest local rolling copies to retain
TS=$(date -u +%Y%m%dT%H%M%SZ)
OUT="${DUMP_DIR}/tokenkey-${TS}.sql.gz"
PART="${OUT}.part"
rm -f "${PART}"

# Keep newest ${KEEP} tokenkey-*.sql.gz by mtime; delete the rest.
prune_rolling() {
  while IFS= read -r _oldf; do
    [ -z "${_oldf}" ] && continue
    rm -f "${_oldf}"
  done < <(find "${DUMP_DIR}" -maxdepth 1 -type f -name 'tokenkey-*.sql.gz' -printf '%T@\t%p\n' 2>/dev/null \
    | sort -nr | tail -n +"$((KEEP + 1))" | cut -f2-)
}

# Remove bogus sub-kib dumps from failed runs (e.g. disk full).
find "${DUMP_DIR}" -maxdepth 1 -type f -name 'tokenkey-*.sql.gz' -size -2k -delete 2>/dev/null || true
# Remove legacy pre-*.dump files left by older manual pre-migration snapshots.
find "${DUMP_DIR}" -maxdepth 1 -type f -name 'pre-*.dump' -delete 2>/dev/null || true
# Prune BEFORE dumping so a near-full volume frees space first (dead-lock fix).
prune_rolling

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

# Trim the freshly written dump back to ${KEEP} newest.
prune_rolling
