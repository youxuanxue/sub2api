#!/usr/bin/env bash
# Render deploy/stage0/Caddyfile template → final prod Caddyfile.
#
# Env:
#   API_DOMAIN   (required) machine/API host, e.g. api.tokenkey.dev
#   ACME_EMAIL   Let's Encrypt contact
#   SITE_DOMAIN  optional human apex host, e.g. tokenkey.dev
#                When unset and API_DOMAIN is api.*, derives apex by stripping api.
#   GLOBAL_SITE_DOMAIN  optional CallModel (overseas) human facade, e.g. callmodel.io
#   GLOBAL_SITE_PHASE   disabled (default), candidate (302 admin kick), or live (301)
#   API_ALIAS_DOMAIN    optional second machine host, e.g. api.callmodel.io
#                       Reuses @machine allowlist; non-machine → GLOBAL_SITE_DOMAIN.
#                       Requires SITE_DOMAIN and GLOBAL_SITE_DOMAIN.
#   API_CN_DOMAIN       optional CN machine host, e.g. api-cn.tokenkey.dev
#                       Reuses @machine allowlist; non-machine → SITE_DOMAIN.
#                       Requires SITE_DOMAIN.
#   PROD_CADDY_ROLE     app (default) or entry. App hosts keep tokenkey:8080;
#                       entry renders the private apps[] pool from the fleet matrix.
#   PROD_FLEET_MATRIX   prod fleet matrix for entry mode
#                       (default: deploy/tencent/candidate-usw-20261022.json).
#
# Public status.tokenkey.dev is Better Stack Free (not a Caddy vhost).
# Public /privacy and /terms are static files under the apex vhost (/data/legal).
#
# Usage:
#   API_DOMAIN=api.tokenkey.dev ACME_EMAIL=ops@example.com \
#     bash deploy/stage0/render-prod-caddyfile.sh template out
#   API_DOMAIN=api.tokenkey.dev ACME_EMAIL=ops@example.com \
#     PROD_CADDY_ROLE=entry \
#     bash deploy/stage0/render-prod-caddyfile.sh template out

set -euo pipefail

template="${1:?template path}"
output="${2:?output path}"

: "${API_DOMAIN:?API_DOMAIN required}"

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "${script_dir}/../.." && pwd)"
caddy_role="${PROD_CADDY_ROLE:-app}"
case "${caddy_role}" in
  app)
    caddy_upstream_target="tokenkey:8080"
    caddy_upstream_weights=""
    caddy_dial_timeout="5s"
    ;;
  entry)
    matrix="${PROD_FLEET_MATRIX:-${repo_root}/deploy/tencent/candidate-usw-20261022.json}"
    [[ -f "${matrix}" ]] || {
      echo "PROD_FLEET_MATRIX is required for entry mode: ${matrix}" >&2
      exit 1
    }
    entry_pool="$(
      python3 - "${matrix}" <<'PY'
import ipaddress
import json
import sys

path = sys.argv[1]
try:
    data = json.load(open(path, encoding="utf-8"))
except (OSError, ValueError) as error:
    raise SystemExit(f"invalid PROD_FLEET_MATRIX: {error}")
apps = data.get("apps")
if not isinstance(apps, list) or len(apps) < 2:
    raise SystemExit("PROD_FLEET_MATRIX apps[] must contain at least two private backends")

pool = []
seen = set()
for app in apps:
    if not isinstance(app, dict):
        raise SystemExit("PROD_FLEET_MATRIX apps[] entries must be objects")
    raw_ip = app.get("private_ip")
    if not isinstance(raw_ip, str):
        raise SystemExit(f"PROD_FLEET_MATRIX app {app.get('name', '<unnamed>')} lacks private_ip")
    try:
        address = ipaddress.ip_address(raw_ip)
    except ValueError:
        raise SystemExit(f"PROD_FLEET_MATRIX invalid private_ip: {raw_ip}")
    if not address.is_private or address.is_loopback:
        raise SystemExit(f"PROD_FLEET_MATRIX requires private app IPs, got {raw_ip}")
    if address in seen:
        raise SystemExit(f"PROD_FLEET_MATRIX duplicate private_ip: {raw_ip}")
    weight = app.get("weight")
    if type(weight) is not int or not 1 <= weight <= 100:
        raise SystemExit(
            f"PROD_FLEET_MATRIX app {app.get('name', raw_ip)} weight must be an integer 1-100"
        )
    seen.add(address)
    pool.append((weight, address))

# Keep output stable and aligned with the live entry Caddyfile: higher weight
# first, then private IP. apps[] remains the rolling order, not the pool order.
pool.sort(key=lambda item: (-item[0], item[1]))
print(" ".join(f"{address}:8080" for _, address in pool) + "|" +
      " ".join(str(weight) for weight, _ in pool))
PY
    )" || exit 1
    caddy_upstream_target="${entry_pool%%|*}"
    caddy_upstream_weights="${entry_pool#*|}"
    [[ -n "${caddy_upstream_target}" && -n "${caddy_upstream_weights}" ]] || {
      echo "entry mode produced an empty upstream pool" >&2
      exit 1
    }
    caddy_dial_timeout="3s"
    ;;
  *)
    echo "PROD_CADDY_ROLE must be app or entry" >&2
    exit 1
    ;;
esac

site_domain="${SITE_DOMAIN:-}"
global_site_domain="${GLOBAL_SITE_DOMAIN:-}"
global_site_phase="${GLOBAL_SITE_PHASE:-disabled}"
api_alias_domain="${API_ALIAS_DOMAIN:-}"
api_cn_domain="${API_CN_DOMAIN:-}"
if [[ -z "${site_domain}" && "${API_DOMAIN}" == api.* ]]; then
  site_domain="${API_DOMAIN#api.}"
fi
if [[ "${site_domain}" == "${API_DOMAIN}" ]]; then
  site_domain=""
fi

case "${global_site_phase}" in
  disabled)
    global_site_domain=""
    global_redirect_status="302"
    ;;
  candidate)
    [[ -n "${global_site_domain}" ]] || {
      echo "GLOBAL_SITE_DOMAIN is required when GLOBAL_SITE_PHASE=candidate" >&2
      exit 1
    }
    global_redirect_status="302"
    ;;
  live)
    [[ -n "${global_site_domain}" ]] || {
      echo "GLOBAL_SITE_DOMAIN is required when GLOBAL_SITE_PHASE=live" >&2
      exit 1
    }
    global_redirect_status="301"
    ;;
  *)
    echo "GLOBAL_SITE_PHASE must be disabled, candidate, or live" >&2
    exit 1
    ;;
esac

if [[ "${global_site_phase}" != "disabled" && -z "${site_domain}" ]]; then
  echo "SITE_DOMAIN must resolve when GLOBAL_SITE_PHASE=${global_site_phase}" >&2
  exit 1
fi

if [[ -n "${api_alias_domain}" && -z "${site_domain}" ]]; then
  echo "SITE_DOMAIN must resolve when API_ALIAS_DOMAIN is set" >&2
  exit 1
fi

if [[ -n "${api_alias_domain}" && -z "${global_site_domain}" ]]; then
  echo "GLOBAL_SITE_DOMAIN must resolve when API_ALIAS_DOMAIN is set (CallModel human face)" >&2
  exit 1
fi

if [[ -n "${api_cn_domain}" && -z "${site_domain}" ]]; then
  echo "SITE_DOMAIN must resolve when API_CN_DOMAIN is set" >&2
  exit 1
fi

tmp="$(mktemp)"
policy_tmp="$(mktemp)"
trap 'rm -f "${tmp}" "${policy_tmp}"' EXIT

export API_DOMAIN ACME_EMAIL
export SITE_DOMAIN="${site_domain}"
export GLOBAL_SITE_DOMAIN="${global_site_domain}"
export GLOBAL_REDIRECT_STATUS="${global_redirect_status}"
export API_ALIAS_DOMAIN="${api_alias_domain}"
export API_CN_DOMAIN="${api_cn_domain}"
export CADDY_UPSTREAM_TARGET="${caddy_upstream_target}"
export CADDY_UPSTREAM_WEIGHTS="${caddy_upstream_weights}"
export CADDY_DIAL_TIMEOUT="${caddy_dial_timeout}"
envsubst '$API_DOMAIN $ACME_EMAIL $SITE_DOMAIN $GLOBAL_SITE_DOMAIN $GLOBAL_REDIRECT_STATUS $API_ALIAS_DOMAIN $API_CN_DOMAIN $CADDY_UPSTREAM_TARGET $CADDY_UPSTREAM_WEIGHTS $CADDY_DIAL_TIMEOUT' < "${template}" > "${tmp}"

strip_render_markers() {
  sed \
    -e '/^# BEGIN_APEX_VHOST$/d' \
    -e '/^# END_APEX_VHOST$/d' \
    -e '/^# BEGIN_API_FULL_PROXY$/d' \
    -e '/^# END_API_FULL_PROXY$/d' \
    -e '/^# BEGIN_API_MACHINE_SPLIT$/d' \
    -e '/^# END_API_MACHINE_SPLIT$/d' \
    -e '/^# BEGIN_API_MACHINE_SNIPPET$/d' \
    -e '/^# END_API_MACHINE_SNIPPET$/d' \
    -e '/^# BEGIN_GLOBAL_VHOST$/d' \
    -e '/^# END_GLOBAL_VHOST$/d' \
    -e '/^# BEGIN_API_ALIAS_VHOST$/d' \
    -e '/^# END_API_ALIAS_VHOST$/d' \
    -e '/^# BEGIN_API_CN_VHOST$/d' \
    -e '/^# END_API_CN_VHOST$/d' \
    -e '/^# BEGIN_UPSTREAM_APP_POLICY$/d' \
    -e '/^# END_UPSTREAM_APP_POLICY$/d' \
    -e '/^# BEGIN_UPSTREAM_ENTRY_POLICY$/d' \
    -e '/^# END_UPSTREAM_ENTRY_POLICY$/d'
}

strip_block() {
  local src="$1"
  local dest="$2"
  local begin="$3"
  local end="$4"
  sed "/^# ${begin}\$/,/^# ${end}\$/d" "${src}" > "${dest}"
}

if [[ "${caddy_role}" == app ]]; then
  strip_block "${tmp}" "${policy_tmp}" "BEGIN_UPSTREAM_ENTRY_POLICY" "END_UPSTREAM_ENTRY_POLICY"
else
  strip_block "${tmp}" "${policy_tmp}" "BEGIN_UPSTREAM_APP_POLICY" "END_UPSTREAM_APP_POLICY"
fi
mv "${policy_tmp}" "${tmp}"
policy_tmp=""

if [[ -z "${site_domain}" ]]; then
  no_apex="$(mktemp)"
  no_global="$(mktemp)"
  no_alias="$(mktemp)"
  no_cn="$(mktemp)"
  no_snippet="$(mktemp)"
  no_split="$(mktemp)"
  trap 'rm -f "${tmp}" "${no_apex}" "${no_global}" "${no_alias}" "${no_cn}" "${no_snippet}" "${no_split}"' EXIT
  strip_block "${tmp}" "${no_apex}" "BEGIN_APEX_VHOST" "END_APEX_VHOST"
  strip_block "${no_apex}" "${no_global}" "BEGIN_GLOBAL_VHOST" "END_GLOBAL_VHOST"
  strip_block "${no_global}" "${no_alias}" "BEGIN_API_ALIAS_VHOST" "END_API_ALIAS_VHOST"
  strip_block "${no_alias}" "${no_cn}" "BEGIN_API_CN_VHOST" "END_API_CN_VHOST"
  strip_block "${no_cn}" "${no_snippet}" "BEGIN_API_MACHINE_SNIPPET" "END_API_MACHINE_SNIPPET"
  strip_block "${no_snippet}" "${no_split}" "BEGIN_API_MACHINE_SPLIT" "END_API_MACHINE_SPLIT"
  strip_render_markers < "${no_split}" > "${output}"
else
  rendered="${tmp}"
  cur="$(mktemp)"
  next="$(mktemp)"
  trap 'rm -f "${tmp}" "${cur}" "${next}"' EXIT
  strip_block "${rendered}" "${cur}" "BEGIN_API_FULL_PROXY" "END_API_FULL_PROXY"
  if [[ -z "${global_site_domain}" ]]; then
    strip_block "${cur}" "${next}" "BEGIN_GLOBAL_VHOST" "END_GLOBAL_VHOST"
    mv "${next}" "${cur}"
  fi
  if [[ -z "${api_alias_domain}" ]]; then
    strip_block "${cur}" "${next}" "BEGIN_API_ALIAS_VHOST" "END_API_ALIAS_VHOST"
    mv "${next}" "${cur}"
  fi
  if [[ -z "${api_cn_domain}" ]]; then
    strip_block "${cur}" "${next}" "BEGIN_API_CN_VHOST" "END_API_CN_VHOST"
    mv "${next}" "${cur}"
  fi
  strip_render_markers < "${cur}" > "${output}"
fi
