#!/usr/bin/env bash
# install-data-machine-identity.sh — COS key rotation for a data machine.
#
# Design: docs/approved/design-data-plane-split.md §两站统一布局. After the
# cutover the database and its pgBackRest jobs run on the data machine, which
# so far kept a static config written by hand. This installs the site's identity
# refresher and a 15-minute timer, so the mounted config is re-rendered from the
# data machine's own root-only /etc/tokenkey/cos-backup.env. Rotating the key is
# then one file on one host.
#
# Run ON the data machine as root (TAT RunCommand). Default MODE=plan prints
# what would change and writes nothing. MODE=apply needs
#   TK_DATA_IDENTITY_CONFIRM=install-data-identity-<SITE>-<hostname>
# and refuses before any write when the credential file is missing or unsafe,
# when this is not the site's data machine, or when the database container is
# not running here.
#
# Bundle layout: the files below sit next to this script (BUNDLE_DIR), plus
# ops/lib/resolve-app-container.sh for the database-server lookup.
#   prod:    tencent_backup_identity.py postgres_backup.py candidate-usw-20261022.json
#   cn-prod: cn_backup_identity.py prod-target.json
#
# Exit: 0 ok; 2 precondition failed (nothing changed); 3 failed after a write
# started; 64 refused (confirm missing or wrong).
set -euo pipefail

SITE="${SITE:?SITE must be prod or cn-prod}"
MODE="${MODE:-plan}"
BUNDLE_DIR="${BUNDLE_DIR:-$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)}"
# Overridable for tests only; the refreshers themselves hard-code these paths.
CREDENTIALS="${CREDENTIALS:-/etc/tokenkey/cos-backup.env}"
DATA_CONFIG="${DATA_CONFIG:-/etc/tokenkey/pgbackrest-cos/pgbackrest.conf}"
UNIT_DIR="${UNIT_DIR:-/etc/systemd/system}"
LIB_ROOT="${LIB_ROOT:-/usr/local/lib/tokenkey}"

case "$SITE" in
  prod)
    LIB="$LIB_ROOT/backup-identity"
    FILES="tencent_backup_identity.py postgres_backup.py candidate-usw-20261022.json"
    ENTRY="$LIB/tencent_backup_identity.py refresh"
    ENV_LINE="Environment=TK_TENCENT_PROD_MATRIX=$LIB/candidate-usw-20261022.json"
    UNIT=tokenkey-backup-identity
    BACKUP_PREFIX=tokenkey-postgres ;;
  cn-prod)
    LIB="$LIB_ROOT/backup-identity"
    FILES="cn_backup_identity.py prod-target.json"
    ENTRY="$LIB/cn_backup_identity.py"
    ENV_LINE="Environment=TK_CN_PROD_TARGET=$LIB/prod-target.json"
    UNIT=tokenkey-cn-backup-identity
    BACKUP_PREFIX=tokenkey-cn-postgres ;;
  *) echo "FAIL SITE must be prod or cn-prod" >&2; exit 2 ;;
esac

die() { echo "FAIL $*" >&2; exit "${2:-2}"; }
hostname="$(hostname)"
for _tk_lib in "${TK_LIB_DIR:-$BUNDLE_DIR}" "$(dirname "${BASH_SOURCE[0]}")/../lib"; do
  [ -f "$_tk_lib/resolve-app-container.sh" ] && break
done
# shellcheck source=../lib/resolve-app-container.sh
. "$_tk_lib/resolve-app-container.sh" || die "container resolver not found (ops/lib/resolve-app-container.sh)"

data_host() {
  case "$SITE" in
    prod) python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["host_watch"]["data_hostname"])' "$BUNDLE_DIR/candidate-usw-20261022.json" ;;
    cn-prod) python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["data_instance_name"])' "$BUNDLE_DIR/prod-target.json" ;;
  esac
}

service_body() {
  cat <<UNIT
[Unit]
Description=TokenKey ${SITE}-data pgBackRest COS identity refresh
After=network-online.target

[Service]
Type=oneshot
${ENV_LINE}
ExecStart=/usr/bin/python3 ${ENTRY}
TimeoutStartSec=60
UMask=0077
UNIT
}

timer_body() {
  cat <<'UNIT'
[Unit]
Description=TokenKey pgBackRest COS identity refresh schedule (data machine)

[Timer]
OnBootSec=1min
OnUnitActiveSec=15min
AccuracySec=30s

[Install]
WantedBy=timers.target
UNIT
}

# --- preconditions (shared by plan and apply; no writes) ----------------------
problems=""
for f in $FILES; do [ -s "$BUNDLE_DIR/$f" ] || problems="$problems; missing bundle file $f"; done
want_host="$(data_host 2>/dev/null || true)"
[ -n "$want_host" ] && [ "$hostname" = "$want_host" ] \
  || problems="$problems; this host is $hostname, the $SITE data machine is ${want_host:-unknown}"
# The live database server, not a dataplane client: the refresher only makes
# sense where pgBackRest archives from.
tk_resolve_postgres_server_container auto >/dev/null 2>&1 \
  || problems="$problems; no running Postgres server here"
[ -d "$(dirname "$DATA_CONFIG")" ] || problems="$problems; $(dirname "$DATA_CONFIG") missing"
if [ ! -f "$CREDENTIALS" ]; then
  problems="$problems; $CREDENTIALS missing (copy it from the custody bucket first)"
else
  case "$(stat -c '%U %a' "$CREDENTIALS")" in
    "root 600"|"root 400") ;;
    *) problems="$problems; $CREDENTIALS must be root-owned 0600 or 0400" ;;
  esac
fi

echo "== plan site=$SITE host=$hostname"
echo "  install $FILES -> $LIB"
echo "  unit    $UNIT.service + $UNIT.timer (every 15 min), then one refresh"
echo "  config  $DATA_CONFIG (root:postgres 0640) re-rendered from $CREDENTIALS"
for name in "$UNIT.service" "$UNIT.timer"; do
  body="$( [ "${name##*.}" = service ] && service_body || timer_body )"
  if [ -f "$UNIT_DIR/$name" ]; then
    [ "$(cat "$UNIT_DIR/$name")" = "$body" ] && echo "  $name unchanged" || echo "  $name update"
  else
    echo "  $name create"
  fi
done
[ -z "$problems" ] || die "precondition:${problems#;}"
if [ "$MODE" != apply ]; then
  echo "RESULT plan ok; to apply: MODE=apply TK_DATA_IDENTITY_CONFIRM=install-data-identity-$SITE-$hostname"
  exit 0
fi

want="install-data-identity-${SITE}-${hostname}"
[ "${TK_DATA_IDENTITY_CONFIRM:-}" = "$want" ] || { echo "REFUSED: TK_DATA_IDENTITY_CONFIRM must be $want" >&2; exit 64; }

# --- apply ---------------------------------------------------------------------
before="$(sha256sum "$DATA_CONFIG" | cut -c1-16)"
install -d -m 0700 "$LIB"
for f in $FILES; do install -m 0600 "$BUNDLE_DIR/$f" "$LIB/$f"; done
service_body > "$UNIT_DIR/$UNIT.service"
timer_body > "$UNIT_DIR/$UNIT.timer"
systemd-analyze verify "$UNIT_DIR/$UNIT.service" "$UNIT_DIR/$UNIT.timer" >/dev/null 2>&1 \
  || die "systemd-analyze rejected the units" 3
systemctl daemon-reload
# A refresh that cannot read the credential leaves the config untouched; the
# unit failing here stops the install before the timer is enabled.
systemctl start "$UNIT.service" || die "first refresh failed; config unchanged, timer not enabled" 3
systemctl enable --now "$UNIT.timer" >/dev/null
after="$(sha256sum "$DATA_CONFIG" | cut -c1-16)"
[ "$(stat -c '%U %g %a' "$DATA_CONFIG")" = "root 70 640" ] || die "$DATA_CONFIG lost root:70 0640" 3
# archive-check runs `pgbackrest check` against COS with the new config and
# writes the receipt the watchers read; a bad key fails here, not hours later.
systemctl start "$BACKUP_PREFIX-archive-check.service" || die "$BACKUP_PREFIX-archive-check failed after refresh" 3
echo "  config sha $before -> $after"
echo "RESULT installed $UNIT.timer=$(systemctl is-active "$UNIT.timer")"
