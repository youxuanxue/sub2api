#!/usr/bin/env bash
# Read-only gate: can this Hybrid / layout-exception host accept stock
# deploy-stage0 blue/green without rediscovering host gaps mid-cutover?
#
# Exit 0 + verdict=ready when all hard checks pass.
# Exit 1 + verdict=blocked when a hard prerequisite is missing.
# Never mutates host state.
set -euo pipefail

ROOT="${ROOT:-/var/lib/tokenkey}"
NET="${TK_NETWORK:-tokenkey_tokenkey-network}"
failures=()
warnings=()

note_fail() { failures+=("$1"); }
note_warn() { warnings+=("$1"); }

if ! command -v docker >/dev/null 2>&1; then
  note_fail "docker_missing"
else
  if docker compose version >/dev/null 2>&1; then
    echo "compose=$(docker compose version --short 2>/dev/null || echo ok)"
  else
    note_fail "docker_compose_plugin_missing"
  fi
fi

if [[ ! -f "${ROOT}/.env" ]]; then
  note_fail "missing_env_file"
else
  echo "env_file=ok"
fi

# Live Caddyfile must be the bind-mounted source, not a divergent Stage0 copy.
caddy_src=""
if docker inspect tokenkey-caddy >/dev/null 2>&1; then
  caddy_src="$(docker inspect tokenkey-caddy 2>/dev/null | python3 -c '
import json, sys
try:
    data = json.load(sys.stdin)
except Exception:
    raise SystemExit(0)
for mount in (data[0].get("Mounts") or []) if data else []:
    if mount.get("Destination") == "/etc/caddy/Caddyfile" and mount.get("Source"):
        print(mount["Source"])
        break
' 2>/dev/null || true)"
fi
if [[ -n "${caddy_src}" && -f "${caddy_src}" ]]; then
  echo "caddyfile=${caddy_src}"
  if [[ "${caddy_src}" == "${ROOT}/caddy/Caddyfile" ]]; then
    echo "caddyfile_layout=stage0"
  else
    echo "caddyfile_layout=exception"
  fi
  # Must be rewriteable by render_caddy_with_upstream (exactly one upstream).
  rp_count="$(awk '
    /^[[:space:]]*reverse_proxy[[:space:]]+/ && ($0 ~ /\{[[:space:]]*$/ || (NF == 2 && $2 ~ /^[^[:space:]]+:[0-9]+$/)) {
      count += 1
    }
    END { print count+0 }
  ' "${caddy_src}")"
  echo "caddyfile_reverse_proxy_count=${rp_count}"
  if [[ "${rp_count}" != "1" ]]; then
    note_fail "caddyfile_reverse_proxy_not_unique:${rp_count}"
  fi
else
  note_fail "caddyfile_unresolved"
fi

if [[ -f "${ROOT}/formal/app/config.yaml" ]]; then
  echo "app_data=${ROOT}/formal/app"
elif [[ -f "${ROOT}/app/config.yaml" ]]; then
  echo "app_data=${ROOT}/app"
else
  note_fail "app_data_config_missing"
fi

if docker network inspect "${NET}" >/dev/null 2>&1; then
  echo "network=${NET}=ok"
else
  note_fail "network_missing:${NET}"
fi

# DNS aliases required by blue/green compose DATABASE_HOST/REDIS_HOST.
if docker network inspect "${NET}" >/dev/null 2>&1; then
  if ! docker run --rm --network "${NET}" busybox:1.36 nslookup tokenkey-postgres >/dev/null 2>&1; then
    note_fail "dns_missing:tokenkey-postgres"
  else
    echo "dns_tokenkey-postgres=ok"
  fi
  if ! docker run --rm --network "${NET}" busybox:1.36 nslookup tokenkey-redis >/dev/null 2>&1; then
    note_fail "dns_missing:tokenkey-redis"
  else
    echo "dns_tokenkey-redis=ok"
  fi
fi

if docker inspect tokenkey >/dev/null 2>&1; then
  img="$(docker inspect tokenkey --format '{{.Config.Image}}' 2>/dev/null || true)"
  echo "legacy_image=${img}"
  if [[ "${img}" == *@sha256:* ]]; then
    echo "legacy_image_form=digest"
  fi
else
  note_warn "legacy_tokenkey_absent"
fi

if [[ ! -f /root/.docker/config.json ]] && [[ ! -f "${HOME:-/root}/.docker/config.json" ]]; then
  note_warn "ghcr_auth_absent_local_images_only"
fi

if [[ -d "${ROOT}/aws" ]]; then
  echo "hybrid_aws_dir=present"
else
  note_warn "hybrid_aws_dir_absent_skip_mount"
fi

active="$(sed -n '1p' "${ROOT}/active-color" 2>/dev/null | tr -d '[:space:]' || true)"
echo "active_color=${active:-none}"

join_csv() {
  local IFS=,
  printf '%s' "$*"
}

if [[ "${#failures[@]}" -gt 0 ]]; then
  printf 'verdict=blocked\n'
  printf 'failures=%s\n' "$(join_csv "${failures[@]}")"
  [[ "${#warnings[@]}" -eq 0 ]] || printf 'warnings=%s\n' "$(join_csv "${warnings[@]}")"
  exit 1
fi

printf 'verdict=ready\n'
[[ "${#warnings[@]}" -eq 0 ]] || printf 'warnings=%s\n' "$(join_csv "${warnings[@]}")"
exit 0
