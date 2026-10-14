#!/bin/bash
# Shared host pg_dump (every 2 hours): Hetzner renderers; EC2 bootstrap is standby.
# PRECIOUS-CLASS dump: schema for all tables + data for everything EXCEPT the
# bulky reconstructible log tables (see EXCLUDE_DATA_GLOBS below). A restore is
# structurally complete; the excluded log tables come back empty and refill from
# live traffic. Recovery owner: deploy/hetzner/README.md.
# Off-box: private /etc/tokenkey/pgdump-storage.json on Hetzner; standby uses
# TOKENKEY_PGDUMP_S3_URI from /var/lib/tokenkey/.env. Each fresh
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
GZIP_LEVEL="${TOKENKEY_PGDUMP_GZIP_LEVEL:-1}" # fast compression to bound CPU time
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
# Remove stale temp files left if the dump process was killed before cleanup.
find "${DUMP_DIR}" -maxdepth 1 -type f -name 'tokenkey-*.sql.gz.part' -mmin +360 -delete 2>/dev/null || true # preflight-allow: swallow
# Remove legacy pre-*.dump files left by older manual pre-migration snapshots.
find "${DUMP_DIR}" -maxdepth 1 -type f -name 'pre-*.dump' -delete 2>/dev/null || true
# Prune BEFORE dumping so a near-full volume frees space first (dead-lock fix).
prune_rolling

# Precious-class dump. This periodic dump exists to protect the IRREPLACEABLE data
# (the billing ledger usage_billing_dedup + all OLTP/config) — NOT the bulky,
# reconstructible time-series LOG tables. Pre-tiering the full-DB dump dragged
# ~8GB of logs (ops_system_logs 4.3G + usage_logs 2.3G + ops_error_logs 1.4G +
# qa_records ~0.8G) into each periodic copy, and the dump's own footprint was the
# disk-full fuse (2026-06-15 prod P0). We use --exclude-table-data (NOT
# --exclude-table) so the table SCHEMA is still dumped and a restore stays
# structurally complete — only the rows are dropped; these tables refill from
# live traffic and carry their own DELETE/partition retention. New tables are
# dumped by default (precious-safe — a new ledger table is never silently
# missed); if a new BULKY table appears it just needs adding here, and disk
# growth is already covered by the >=85% data-volume alert.
EXCLUDE_DATA_GLOBS=(
  'ops_system_logs*'   # ~4.3G operational system logs (own retention)
  'usage_logs*'        # ~2.3G per-request usage detail (NOT billing SoT post-071; 90d retention)
  'ops_error_logs*'    # ~1.4G operational error logs (own retention)
  'qa_records*'        # QA row data is excluded; only the dedicated raw-QA archive is its history backup
)
exclude_args=()
for _g in "${EXCLUDE_DATA_GLOBS[@]}"; do
  exclude_args+=( "--exclude-table-data=${_g}" )
done

set -o pipefail
priority_prefix=()
if command -v ionice >/dev/null 2>&1; then
  priority_prefix+=(ionice -c2 -n7)
fi
if command -v nice >/dev/null 2>&1; then
  priority_prefix+=(nice -n 19)
fi
if ! docker exec tokenkey-postgres sh -c '
    if command -v ionice >/dev/null 2>&1 && command -v nice >/dev/null 2>&1; then
      exec ionice -c2 -n7 nice -n 19 pg_dump "$@"
    elif command -v nice >/dev/null 2>&1; then
      exec nice -n 19 pg_dump "$@"
    else
      exec pg_dump "$@"
    fi
  ' sh -U tokenkey -d tokenkey --format=plain --no-owner "${exclude_args[@]}" \
    | "${priority_prefix[@]}" gzip -"${GZIP_LEVEL}" > "${PART}"; then
  rm -f "${PART}"
  exit 1
fi

SZ=$(wc -c < "${PART}")
if [ "${SZ}" -lt 2048 ]; then
  rm -f "${PART}"
  exit 1
fi

mv -f "${PART}" "${OUT}"

# Off-box copy to S3 (best-effort; never fails the local dump). Read the .env to
# pick up TOKENKEY_PGDUMP_S3_URI (e.g. s3://tokenkey-stage0-backups/prod/pgdump).
# Absent var => no-op (back-compat; edges with no bucket configured just skip).
S3_URI=""
if [ -r /var/lib/tokenkey/.env ]; then
  S3_URI="$(sed -n 's/^TOKENKEY_PGDUMP_S3_URI=//p' /var/lib/tokenkey/.env | head -1)"
fi
if [ -e /etc/tokenkey/pgdump-storage.json ] || [ -L /etc/tokenkey/pgdump-storage.json ]; then
  # Explicit host-only Hetzner identity takes precedence. A malformed config,
  # missing helper/key or upload failure must NEVER use the AWS role instead.
  if ! python3 /usr/local/lib/tokenkey/pgdump_storage.py --upload "${OUT}"; then
    echo "::warning::pgdump: configured off-box upload failed (local copy kept; no AWS fallback)" >&2
  fi
elif [ -n "${S3_URI}" ]; then
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
