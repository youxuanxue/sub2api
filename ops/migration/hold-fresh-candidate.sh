#!/usr/bin/env bash
# Persist the existing no-writer boundary on an explicitly named fresh candidate.
set -euo pipefail
umask 077
EXPECTED=${1:?expected fresh candidate hostname required}
case "$EXPECTED" in
  tokenkey-prod-tokyo-20261017|tokenkey-prod-aws-20261015|tokenkey-edge-uk1-aws-20261015|tokenkey-edge-uk2-aws-20261015) ;;
  *) exit 20 ;;
esac
[[ $(hostname) == "$EXPECTED" ]]
[[ $(docker network inspect tokenkey_tokenkey-network --format '{{.Internal}}') == true ]]
[[ $(systemctl show tokenkey-pgdump.service -p ActiveState --value) == inactive ]]
ROOT=/var/lib/tokenkey/migration
mkdir -p "$ROOT" /etc/systemd/system/tokenkey.service.d
if [[ ! -f "$ROOT/precutover-hold-before.json" ]]; then
  docker inspect $(docker ps -aq) --format '{{json .Name}} {{json .HostConfig.RestartPolicy}}' > "$ROOT/precutover-hold-before.json"
  systemctl is-enabled tokenkey.service tokenkey-pgdump.timer > "$ROOT/precutover-hold-units.txt" || true
fi
touch "$ROOT/DATA_SYNC_HOLD"
cat > /etc/systemd/system/tokenkey.service.d/migration-hold.conf <<'UNIT'
[Unit]
ConditionPathExists=!/var/lib/tokenkey/migration/DATA_SYNC_HOLD
UNIT
# Do not let ExecStop stop PostgreSQL just to mark this oneshot inactive.
# The hold explicitly owns container stops; remove this drop-in at activation.
cat > /etc/systemd/system/tokenkey.service.d/migration-hold-stop.conf <<'UNIT'
[Service]
ExecStop=
ExecStop=/usr/bin/true
UNIT
systemctl daemon-reload
systemctl disable --now tokenkey-pgdump.timer
systemctl disable tokenkey.service
systemctl stop tokenkey.service
while read -r name; do
  [[ "$name" == tokenkey-postgres ]] && continue
  docker update --restart=no "$name" >/dev/null
  if [[ $(docker inspect "$name" --format '{{.State.Running}}') == true ]]; then
    docker stop --time 60 "$name" >/dev/null
  fi
done < <(docker ps -a --format '{{.Names}}')
systemctl start tokenkey.service
[[ $(systemctl show tokenkey.service -p ConditionResult --value) == no ]]
[[ $(docker ps --format '{{.Names}}') == tokenkey-postgres ]]
printf 'HOLD_OK hostname=%s systemd_start_skipped=true only_postgres=true\n' "$EXPECTED"
