#!/usr/bin/env bash
# Write-capable: ensure api-cn.tokenkey.dev has a Caddy machine vhost on the
# live cutover Caddyfile (Tencent USW layout-exception). TLSV1_ALERT_INTERNAL_ERROR
# happens when DNS points here but Caddy has no matching site block / SNI.
# Delivered via run-probe.sh. Requires APPLY=yes-api-cn-caddy-vhost.
set -euo pipefail

APPLY="${APPLY:-}"
CN_HOST="${CN_HOST:-api-cn.tokenkey.dev}"
SITE_REDIR="${SITE_REDIR:-tokenkey.dev}"

live_caddyfile() {
  docker inspect tokenkey-caddy --format '{{range .Mounts}}{{if eq .Destination "/etc/caddy/Caddyfile"}}{{.Source}}{{end}}{{end}}'
}

print_sites() {
  local f="$1"
  sudo grep -nE '^[a-zA-Z0-9.*-]+ \{' "$f" || true
}

ensure_vhost() {
  local live="$1"
  local ts backup staged
  ts="$(date -u +%Y%m%dT%H%M%SZ)"
  backup="${live}.before-api-cn-${ts}"
  staged="/tmp/Caddyfile.api-cn.${ts}"

  sudo cp -a "$live" "$backup"
  echo "backup=${backup}"

  if sudo grep -qE "^${CN_HOST//./\\.} \\{" "$live"; then
    echo "vhost already present: ${CN_HOST}"
    return 0
  fi

  sudo cp -a "$live" "$staged"
  sudo tee -a "$staged" >/dev/null <<EOF

# BEGIN_API_CN_VHOST (layout remediator ${ts})
${CN_HOST} {
	import tokenkey_tls
	import tokenkey_encode_static
	import tokenkey_api_machine
	handle {
		redir https://${SITE_REDIR}{uri} permanent
	}
	import tokenkey_limits_log
}
# END_API_CN_VHOST
EOF

  echo "=== validate staged ==="
  sudo docker run --rm -v "${staged}:/tmp/Caddyfile:ro" caddy:2-alpine \
    caddy validate --config /tmp/Caddyfile --adapter caddyfile

  echo "=== apply in-place (preserve bind-mount inode) ==="
  sudo sh -c "cat '${staged}' > '${live}'"
  sudo rm -f "$staged"

  echo "=== reload ==="
  sudo docker exec tokenkey-caddy caddy reload --config /etc/caddy/Caddyfile --adapter caddyfile
}

echo "=== before sites ==="
LIVE="$(live_caddyfile)"
[ -n "$LIVE" ] && [ -f "$LIVE" ] || { echo "::error::no live Caddyfile from tokenkey-caddy mount"; exit 1; }
echo "live=${LIVE}"
print_sites "$LIVE"

echo "=== before probe ${CN_HOST}/health ==="
curl -sS -o /dev/null -w 'http=%{http_code}\n' --max-time 10 "https://${CN_HOST}/health" || true

if [ "$APPLY" != "yes-api-cn-caddy-vhost" ]; then
  echo "dry-run only; set APPLY=yes-api-cn-caddy-vhost to write"
  exit 0
fi

ensure_vhost "$LIVE"

echo "=== after sites ==="
print_sites "$LIVE"
sudo grep -nE "^${CN_HOST//./\\.} \\{" "$LIVE"

echo "=== after probe ==="
# Give ACME/SNI a moment after reload
sleep 2
code="$(curl -sS -o /dev/null -w '%{http_code}' --max-time 15 "https://${CN_HOST}/health" || echo 000)"
echo "http=${code}"
if [ "$code" != "200" ]; then
  echo "::error::${CN_HOST}/health expected 200 got ${code}"
  exit 1
fi
echo "=== remediate api-cn caddy vhost done ==="
