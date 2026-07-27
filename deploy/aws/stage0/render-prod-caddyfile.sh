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

tmp="$(mktemp)"
trap 'rm -f "${tmp}"' EXIT

export API_DOMAIN ACME_EMAIL
export SITE_DOMAIN="${site_domain}"
envsubst '$API_DOMAIN $ACME_EMAIL $SITE_DOMAIN' < "${template}" > "${tmp}"

if [[ -z "${site_domain}" ]]; then
  sed '/^# BEGIN_APEX_VHOST$/,/^# END_APEX_VHOST$/d' "${tmp}" > "${output}"
else
  sed '/^# BEGIN_APEX_VHOST$/d; /^# END_APEX_VHOST$/d' "${tmp}" > "${output}"
fi
