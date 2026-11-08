#!/bin/bash
# Read-only host-side protection probe; verdict logic lives in the Python sibling.
set -u

# Canonical container resolver (ops/lib/resolve-app-container.sh) owns the
# postgres container lookup. run-probe.sh ships it next to
# this probe; the second path covers running straight from the repo.
_tk_resolver=""
for _cand in "${TK_LIB_DIR:-$(dirname "$0")}/resolve-app-container.sh" \
             "$(dirname "$0")/../lib/resolve-app-container.sh"; do
  if [ -f "$_cand" ]; then _tk_resolver="$_cand"; break; fi
done
if [ -z "$_tk_resolver" ]; then
  echo "canonical app-container resolver not found (ops/lib/resolve-app-container.sh)" >&2
  exit 1
fi
# shellcheck source=../lib/resolve-app-container.sh
. "$_tk_resolver"
POSTGRES_CONTAINER="$(tk_resolve_postgres_container "${POSTGRES_CONTAINER:-auto}" || true)"
if [ -z "$POSTGRES_CONTAINER" ]; then
  echo "postgres container unresolved (want tokenkey-postgres or tk-archive-standby)" >&2
  exit 1
fi
PSQL=(docker exec -i -e 'PGOPTIONS=-c default_transaction_read_only=on -c lock_timeout=100ms -c statement_timeout=2s' "$POSTGRES_CONTAINER" psql -U tokenkey -d tokenkey -X -A -t -v ON_ERROR_STOP=1)

partition_sql=""
if [ -n "${PARTITION_COVERAGE_SQL:-}" ]; then
  [ -f "$PARTITION_COVERAGE_SQL" ] && partition_sql="$PARTITION_COVERAGE_SQL"
elif [ -f "$(dirname "$0")/data-layer-partition-coverage.sql" ]; then
  partition_sql="$(dirname "$0")/data-layer-partition-coverage.sql"
fi
if [ -n "$partition_sql" ]; then
  "${PSQL[@]}" < "$partition_sql" 2>/dev/null || printf '%s\n' 'PARTITIONSTATS {"probe_ok":false}'
else
  printf '%s\n' 'PARTITIONSTATS {"probe_ok":false}'
fi

latest_dump="$(find /var/lib/tokenkey/pgdump -maxdepth 1 -type f -name 'tokenkey-*.sql.gz' -printf '%T@\n' 2>/dev/null | sort -nr | head -1)"

# Whether local pgdump is this host's backup path at all.
#
# Reported as EVIDENCE rather than hard-coded per host, and deliberately kept
# that way even though both prod and cn-prod have now retired pgdump
# (2026-10-23: cn-prod moved to Tencent Beijing with pgBackRest and
# CN_PGDUMP_ENABLED is off).
#
# Why not collapse this to a constant now that no host uses pgdump: three
# bootstrap scripts (deploy/hetzner/render-prod-bootstrap.sh,
# deploy/aws/stage0/stage0-ec2-bootstrap.sh, deploy/hetzner/generated-prod-user-data.sh)
# still install tokenkey-pgdump.timer when TOKENKEY_ENABLE_LEGACY_PGDUMP=true
# and no pgdump-retired.json marker exists. Retirement here is achieved by NOT
# installing it, not by a repo-wide flag -- prod carries no retired marker at
# all. So a re-bootstrap with that variable set brings pgdump back as a real
# backup owner, and evidence-based applicability picks that up with no code
# change, while a constant would silently stop watching it.
#
# Applicability is reported as HOST EVIDENCE rather than hard-coded as
# `if target == "prod"`, so the next host migration needs no code edit and
# nobody has to remember this decision. Verified on live prod: no systemd unit,
# no script, no dump dir, and a non-empty archive_check.json receipt.
#
# Fail closed on purpose: "not applicable" requires BOTH that no pgdump
# installation remains AND that a non-empty physical backup receipt exists. A
# host that lost both backup paths still fails, which is the case worth paging
# for. Without this, prod opened a pgdump_freshness issue every day for reading
# "retired under authorization" as "backup stale".
pgdump_installed=false
if systemctl cat tokenkey-pgdump.service >/dev/null 2>&1 \
  || [ -f /usr/local/bin/tokenkey-pgdump.sh ] \
  || [ -d /var/lib/tokenkey/pgdump ]; then
  pgdump_installed=true
fi
physical_receipt=false
for _receipt in health.json archive_check.json; do
  if [ -s "/var/lib/tokenkey/postgres-backup/$_receipt" ]; then physical_receipt=true; break; fi
done
pgdump_applicable=true
if [ "$pgdump_installed" = false ] && [ "$physical_receipt" = true ]; then
  pgdump_applicable=false
fi

if [[ "$latest_dump" =~ ^[0-9]+([.][0-9]+)?$ ]]; then
  latest_dump_iso="$(date -u -d "@${latest_dump%.*}" '+%Y-%m-%dT%H:%M:%SZ' 2>/dev/null || true)" # preflight-allow: swallow — malformed host timestamp becomes explicit null
  printf 'BACKUPSTATS {"latest_pgdump_at":"%s","applicable":%s,"pgdump_installed":%s,"physical_receipt_present":%s}\n' \
    "$latest_dump_iso" "$pgdump_applicable" "$pgdump_installed" "$physical_receipt"
else
  printf 'BACKUPSTATS {"latest_pgdump_at":null,"applicable":%s,"pgdump_installed":%s,"physical_receipt_present":%s}\n' \
    "$pgdump_applicable" "$pgdump_installed" "$physical_receipt"
fi
