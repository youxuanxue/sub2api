#!/usr/bin/env bash
# Render deploy/aws/stage0/Caddyfile template → final prod Caddyfile.
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
#
# Public status.tokenkey.dev is Better Stack Free (not a Caddy vhost).
# Public /privacy and /terms are static files under the apex vhost (/data/legal).
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
api_alias_domain="${API_ALIAS_DOMAIN:-}"
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

tmp="$(mktemp)"
trap 'rm -f "${tmp}"' EXIT

export API_DOMAIN ACME_EMAIL
export SITE_DOMAIN="${site_domain}"
export GLOBAL_SITE_DOMAIN="${global_site_domain}"
export GLOBAL_REDIRECT_STATUS="${global_redirect_status}"
export API_ALIAS_DOMAIN="${api_alias_domain}"
envsubst '$API_DOMAIN $ACME_EMAIL $SITE_DOMAIN $GLOBAL_SITE_DOMAIN $GLOBAL_REDIRECT_STATUS $API_ALIAS_DOMAIN' < "${template}" > "${tmp}"

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
    -e '/^# END_API_ALIAS_VHOST$/d'
}

strip_block() {
  local src="$1"
  local dest="$2"
  local begin="$3"
  local end="$4"
  sed "/^# ${begin}\$/,/^# ${end}\$/d" "${src}" > "${dest}"
}

if [[ -z "${site_domain}" ]]; then
  no_apex="$(mktemp)"
  no_global="$(mktemp)"
  no_alias="$(mktemp)"
  no_snippet="$(mktemp)"
  no_split="$(mktemp)"
  trap 'rm -f "${tmp}" "${no_apex}" "${no_global}" "${no_alias}" "${no_snippet}" "${no_split}"' EXIT
  strip_block "${tmp}" "${no_apex}" "BEGIN_APEX_VHOST" "END_APEX_VHOST"
  strip_block "${no_apex}" "${no_global}" "BEGIN_GLOBAL_VHOST" "END_GLOBAL_VHOST"
  strip_block "${no_global}" "${no_alias}" "BEGIN_API_ALIAS_VHOST" "END_API_ALIAS_VHOST"
  strip_block "${no_alias}" "${no_snippet}" "BEGIN_API_MACHINE_SNIPPET" "END_API_MACHINE_SNIPPET"
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
  strip_render_markers < "${cur}" > "${output}"
fi
