#!/usr/bin/env bash
# Adopt an already formatted, verified disk. Never format or overwrite a runtime.
set -euo pipefail
bundle=${1:?protected bundle directory required}
cd "$bundle"
python3 verify-host.py --target-file target.json
[[ $EUID = 0 ]] || { echo 'root is required' >&2; exit 1; }
if [[ -e /opt/tokenkey/target.json ]]; then
  cmp -s target.json /opt/tokenkey/target.json || { echo 'Target ownership changed' >&2; exit 1; }
else
  [[ ! -e /var/lib/tokenkey/.env && ! -e /data/tokenkey/.env ]] || {
    echo 'Refusing an unowned existing runtime' >&2; exit 1;
  }
  for name in tokenkey tokenkey-caddy tokenkey-postgres tokenkey-redis; do
    if docker inspect "$name" >/dev/null 2>&1; then
      echo "Refusing existing unowned container $name" >&2; exit 1
    fi
  done
fi
install -d -m 0700 /opt/tokenkey
install -m 0600 target.json /opt/tokenkey/target.json
install -d -m 0755 /usr/local/lib/tokenkey /data/tokenkey /var/lib/tokenkey
install -m 0644 verify-host.py /usr/local/lib/tokenkey/verify-host.py
if ! mountpoint -q /var/lib/tokenkey; then
  mount --bind /data/tokenkey /var/lib/tokenkey
fi
python3 verify-host.py --target-file target.json --require-runtime
cat > /etc/systemd/system/var-lib-tokenkey.mount <<'UNIT'
[Unit]
Description=TokenKey data on the verified NAS data disk
RequiresMountsFor=/data
Before=docker.service tokenkey.service
[Mount]
What=/data/tokenkey
Where=/var/lib/tokenkey
Type=none
Options=bind
[Install]
WantedBy=multi-user.target
UNIT
for unit in docker tokenkey; do
  install -d -m 0755 "/etc/systemd/system/$unit.service.d"
  cat > "/etc/systemd/system/$unit.service.d/zhupeng-volume.conf" <<'UNIT'
[Unit]
RequiresMountsFor=/var/lib/tokenkey
[Service]
ExecStartPre=/usr/bin/python3 /usr/local/lib/tokenkey/verify-host.py --target-file /opt/tokenkey/target.json --require-runtime
UNIT
done
systemctl daemon-reload
systemctl enable var-lib-tokenkey.mount >/dev/null
install -d -m 0755 -o 1000 -g 1000 /var/lib/tokenkey/app
install -d -m 0700 /var/lib/tokenkey/pgdump
install -d -m 0755 /var/lib/tokenkey/postgres /var/lib/tokenkey/redis /var/lib/tokenkey/caddy/data /var/lib/tokenkey/caddy/config
# Initial bootstrap is never an upgrade/restart path.
if [[ -e /var/lib/tokenkey/active-color ]]; then
  echo 'Blue/green already initialized; use deploy for upgrades'; exit 0
fi
if [[ -e /var/lib/tokenkey/.env ]]; then
  cmp -s .env /var/lib/tokenkey/.env || { echo 'Existing secrets/config differ; explicit update required' >&2; exit 1; }
else
  install -m 0600 .env /var/lib/tokenkey/.env
fi
docker compose --project-name tokenkey --env-file .env -f compose.yml -f compose.override.yml -f compose.local.yml -f compose.tunnel.yml config --no-interpolate > compose.merged.yml
install -m 0600 compose.merged.yml /var/lib/tokenkey/docker-compose.yml
install -m 0644 Caddyfile /var/lib/tokenkey/caddy/Caddyfile
cd /var/lib/tokenkey
docker compose --project-name tokenkey --env-file .env -f docker-compose.yml config --quiet
docker compose --project-name tokenkey --env-file .env -f docker-compose.yml up -d
# A fresh database on NAS storage applies the full migration history. Allow the
# initial install to finish without weakening the blue/green readiness gates.
for attempt in $(seq 1 300); do
  if [[ -s caddy/data/caddy/pki/authorities/local/root.crt ]] && curl -fsS --max-time 3 http://127.0.0.1:18080/health >/dev/null; then
    install -m 0644 caddy/data/caddy/pki/authorities/local/root.crt /usr/local/share/ca-certificates/tokenkey-zhupeng-test.crt
    update-ca-certificates >/dev/null
    domain=$(sed -n 's/^API_DOMAIN=//p' .env)
    curl -fsS --max-time 5 --resolve "$domain:443:127.0.0.1" "https://$domain/health"
    echo 'tk_ssh_bootstrap: OK (tunnel-only, private test CA)'
    exit 0
  fi
  sleep 2
done
echo 'Bootstrap health deadline exceeded; existing upstream sub2api is untouched' >&2
exit 1
