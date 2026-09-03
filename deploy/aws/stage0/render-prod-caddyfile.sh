#!/usr/bin/env bash
# Render deploy/aws/stage0/Caddyfile template → final prod Caddyfile.
#
# Env:
#   API_DOMAIN   (required) machine/API host, e.g. api.tokenkey.dev
#   ACME_EMAIL   Let's Encrypt contact
#   SITE_DOMAIN  optional human apex host, e.g. tokenkey.dev
#                When unset and API_DOMAIN is api.*, derives apex by stripping api.
#   GLOBAL_SITE_DOMAIN  optional overseas homepage host, e.g. global.tokenkey.dev
#   GLOBAL_SITE_PHASE   disabled (default), candidate (302 + noindex), or live (301)
#
# Usage:
#   API_DOMAIN=api.tokenkey.dev ACME_EMAIL=ops@example.com \
#     bash deploy/aws/stage0/render-prod-caddyfile.sh template out

set -euo pipefail

template="${1:?template path}"
output="${2:?output path}"

: "${API_DOMAIN:?API_DOMAIN required}"

site_domain="${SITE_DOMAIN:-}"
global_site_domain="${GLOBAL_SITE_DOMAIN:-}"
global_site_phase="${GLOBAL_SITE_PHASE:-disabled}"
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

tmp="$(mktemp)"
trap 'rm -f "${tmp}"' EXIT

export API_DOMAIN ACME_EMAIL
export SITE_DOMAIN="${site_domain}"
export GLOBAL_SITE_DOMAIN="${global_site_domain}"
export GLOBAL_REDIRECT_STATUS="${global_redirect_status}"
envsubst '$API_DOMAIN $ACME_EMAIL $SITE_DOMAIN $GLOBAL_SITE_DOMAIN $GLOBAL_REDIRECT_STATUS' < "${template}" > "${tmp}"

strip_render_markers() {
  sed \
    -e '/^# BEGIN_APEX_VHOST$/d' \
    -e '/^# END_APEX_VHOST$/d' \
    -e '/^# BEGIN_API_FULL_PROXY$/d' \
    -e '/^# END_API_FULL_PROXY$/d' \
    -e '/^# BEGIN_API_MACHINE_SPLIT$/d' \
    -e '/^# END_API_MACHINE_SPLIT$/d' \
    -e '/^# BEGIN_GLOBAL_VHOST$/d' \
    -e '/^# END_GLOBAL_VHOST$/d' \
    -e '/^# BEGIN_GLOBAL_NOINDEX$/d' \
    -e '/^# END_GLOBAL_NOINDEX$/d'
}

if [[ -z "${site_domain}" ]]; then
  sed '/^# BEGIN_APEX_VHOST$/,/^# END_APEX_VHOST$/d' "${tmp}" \
    | sed '/^# BEGIN_GLOBAL_VHOST$/,/^# END_GLOBAL_VHOST$/d' \
    | sed '/^# BEGIN_API_MACHINE_SPLIT$/,/^# END_API_MACHINE_SPLIT$/d' \
    | strip_render_markers > "${output}"
else
  rendered="${tmp}"
  phase_tmp="$(mktemp)"
  trap 'rm -f "${tmp}" "${phase_tmp}"' EXIT
  sed '/^# BEGIN_API_FULL_PROXY$/,/^# END_API_FULL_PROXY$/d' "${rendered}" > "${phase_tmp}"
  if [[ -z "${global_site_domain}" ]]; then
    sed '/^# BEGIN_GLOBAL_VHOST$/,/^# END_GLOBAL_VHOST$/d' "${phase_tmp}" \
      | strip_render_markers > "${output}"
  elif [[ "${global_site_phase}" == "live" ]]; then
    sed '/^# BEGIN_GLOBAL_NOINDEX$/,/^# END_GLOBAL_NOINDEX$/d' "${phase_tmp}" \
      | strip_render_markers > "${output}"
  else
    strip_render_markers < "${phase_tmp}" > "${output}"
  fi
fi
