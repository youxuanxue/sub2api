#!/usr/bin/env bash
# Render deploy/aws/stage0/Caddyfile template → final prod Caddyfile.
#
# Env:
#   API_DOMAIN   (required) machine/API host, e.g. api.tokenkey.dev
#   ACME_EMAIL   Let's Encrypt contact
#   SITE_DOMAIN  optional human apex host, e.g. tokenkey.dev
#                When unset and API_DOMAIN is api.*, derives apex by stripping api.
#
# Usage:
#   API_DOMAIN=api.tokenkey.dev ACME_EMAIL=ops@example.com \
#     bash deploy/aws/stage0/render-prod-caddyfile.sh template out

set -euo pipefail

template="${1:?template path}"
output="${2:?output path}"

: "${API_DOMAIN:?API_DOMAIN required}"

site_domain="${SITE_DOMAIN:-}"
if [[ -z "${site_domain}" && "${API_DOMAIN}" == api.* ]]; then
  site_domain="${API_DOMAIN#api.}"
fi
if [[ "${site_domain}" == "${API_DOMAIN}" ]]; then
  site_domain=""
fi

tmp="$(mktemp)"
trap 'rm -f "${tmp}"' EXIT

export API_DOMAIN ACME_EMAIL
export SITE_DOMAIN="${site_domain}"
envsubst '$API_DOMAIN $ACME_EMAIL $SITE_DOMAIN' < "${template}" > "${tmp}"

strip_render_markers() {
  sed \
    -e '/^# BEGIN_APEX_VHOST$/d' \
    -e '/^# END_APEX_VHOST$/d' \
    -e '/^# BEGIN_API_FULL_PROXY$/d' \
    -e '/^# END_API_FULL_PROXY$/d' \
    -e '/^# BEGIN_API_MACHINE_SPLIT$/d' \
    -e '/^# END_API_MACHINE_SPLIT$/d'
}

if [[ -z "${site_domain}" ]]; then
  sed '/^# BEGIN_APEX_VHOST$/,/^# END_APEX_VHOST$/d' "${tmp}" \
    | sed '/^# BEGIN_API_MACHINE_SPLIT$/,/^# END_API_MACHINE_SPLIT$/d' \
    | strip_render_markers > "${output}"
else
  sed '/^# BEGIN_API_FULL_PROXY$/,/^# END_API_FULL_PROXY$/d' "${tmp}" \
    | strip_render_markers > "${output}"
fi
