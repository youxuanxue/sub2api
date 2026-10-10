#!/usr/bin/env bash
# Upsert a Porkbun A record from local env credentials.
#
# Credentials (never printed, never committed):
#   PORKBUN_API_KEY
#   PORKBUN_SECRET_API_KEY
#
# Default is dry-run (plan only). Pass --apply to create/edit.
# DNS mutation stays a human-gated step; this script only replaces the
# Porkbun console click when env credentials are present.
#
# Usage:
#   bash ops/dns/porkbun-upsert-a.sh <host> <ipv4|-> [--domain tokenkey.dev] [--ttl 300] [--apply]
#
# <host> may be a bare label (api-hz), an FQDN under --domain
# (api-hz.tokenkey.dev), or the apex domain itself.
# Pass ipv4 "-" to keep the existing A content and only change TTL
# (fails if no A record exists — never invents an address).

set -euo pipefail

HOST=""
IPV4=""
DOMAIN="${PORKBUN_DEFAULT_DOMAIN:-tokenkey.dev}"
TTL="${PORKBUN_DEFAULT_TTL:-600}"
APPLY=""
CURL_BIN="${PORKBUN_CURL:-curl}"
API_BASE="${PORKBUN_API_BASE:-https://api.porkbun.com/api/json/v3}"

usage() {
  cat <<EOF >&2
usage: $0 <host> <ipv4|-> [--domain tokenkey.dev] [--ttl 300] [--apply]

Reads PORKBUN_API_KEY and PORKBUN_SECRET_API_KEY from the environment.
Without --apply, only plans (retrieve + intended create/edit/noop).
TTL-only changes (same IPv4, different ttl) are edits, not noops.
ipv4 "-" = preserve existing A content (TTL-only; requires an existing record).
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --domain)
      DOMAIN="${2:-}"
      shift 2
      ;;
    --ttl)
      TTL="${2:-}"
      shift 2
      ;;
    --apply)
      APPLY=--apply
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    --*)
      echo "unknown option: $1" >&2
      usage
      exit 1
      ;;
    *)
      if [[ -z "$HOST" ]]; then
        HOST="$1"
      elif [[ -z "$IPV4" ]]; then
        IPV4="$1"
      else
        echo "unexpected argument: $1" >&2
        usage
        exit 1
      fi
      shift
      ;;
  esac
done

if [[ -z "$HOST" || -z "$IPV4" ]]; then
  usage
  exit 1
fi

PRESERVE_IP=""
if [[ "$IPV4" == "-" ]]; then
  PRESERVE_IP=1
elif [[ ! "$IPV4" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}$ ]]; then
  echo "::error::ipv4 must look like A.B.C.D or '-' to preserve (got: ${IPV4})" >&2
  exit 1
fi

# Porkbun accepts TTLs below the old 600 floor (verified 300 SUCCESS 2026-10-11).
if [[ ! "$TTL" =~ ^[0-9]+$ ]] || [[ "$TTL" -lt 300 ]]; then
  echo "::error::ttl must be an integer >= 300" >&2
  exit 1
fi

if [[ -z "${PORKBUN_API_KEY:-}" || -z "${PORKBUN_SECRET_API_KEY:-}" ]]; then
  echo "::error::missing PORKBUN_API_KEY and/or PORKBUN_SECRET_API_KEY in environment" >&2
  echo "  export them locally (e.g. ~/.zshrc); do not commit credentials" >&2
  exit 2
fi

if [[ "$HOST" == "$DOMAIN" ]]; then
  NAME=""
  FQDN="$DOMAIN"
elif [[ "$HOST" == *."$DOMAIN" ]]; then
  NAME="${HOST%.$DOMAIN}"
  FQDN="$HOST"
elif [[ "$HOST" != *.* ]]; then
  NAME="$HOST"
  FQDN="${NAME}.${DOMAIN}"
else
  echo "::error::host '${HOST}' is not under domain '${DOMAIN}' (pass --domain or use a bare label)" >&2
  exit 1
fi

porkbun_post() {
  local path="$1"
  # Bash parses ${2:-{}} as default "{" plus a literal "}" — use a quoted default.
  local extra_json="${2:-"{}"}"
  local body
  body="$(jq -cn \
    --arg key "$PORKBUN_API_KEY" \
    --arg secret "$PORKBUN_SECRET_API_KEY" \
    --arg extra "$extra_json" \
    '(($extra | fromjson) + {apikey:$key, secretapikey:$secret})')" || {
    echo "::error::failed to build porkbun request body for ${path}" >&2
    exit 1
  }
  # Credentials only travel in the request body; never echo body/keys.
  "$CURL_BIN" -sS -X POST "${API_BASE}${path}" \
    -H 'Content-Type: application/json' \
    -d "$body"
}

require_success() {
  local resp="$1"
  local context="$2"
  local status
  status="$(jq -r '.status // empty' <<<"$resp")"
  if [[ "$status" != "SUCCESS" ]]; then
    local message
    message="$(jq -r '.message // .error // "unknown error"' <<<"$resp")"
    echo "::error::porkbun ${context} failed: ${message}" >&2
    exit 1
  fi
}

ping_resp="$(porkbun_post /ping)"
require_success "$ping_resp" ping

retrieve_resp="$(porkbun_post "/dns/retrieve/${DOMAIN}")"
require_success "$retrieve_resp" retrieve

match_json="$(jq -c --arg fqdn "$FQDN" --arg name "$NAME" '
  [.records[]?
    | select(.type == "A")
    | select(.name == $fqdn or (.name == $name and $name != ""))
  ]
' <<<"$retrieve_resp")"
match_count="$(jq 'length' <<<"$match_json")"

action="create"
record_id=""
current_ip=""
current_ttl=""
if [[ "$match_count" -gt 1 ]]; then
  echo "::error::found ${match_count} A records for ${FQDN}; resolve duplicates in Porkbun first" >&2
  exit 1
elif [[ "$match_count" -eq 1 ]]; then
  record_id="$(jq -r '.[0].id' <<<"$match_json")"
  current_ip="$(jq -r '.[0].content' <<<"$match_json")"
  current_ttl="$(jq -r '.[0].ttl // empty' <<<"$match_json")"
  if [[ -n "$PRESERVE_IP" ]]; then
    IPV4="$current_ip"
  fi
  if [[ "$current_ip" == "$IPV4" && "$current_ttl" == "$TTL" ]]; then
    action="noop"
  else
    action="edit"
  fi
elif [[ -n "$PRESERVE_IP" ]]; then
  echo "::error::ipv4 '-' requires an existing A record for ${FQDN}" >&2
  exit 1
fi

cat <<PLAN
=== Porkbun A upsert plan ===
fqdn     : ${FQDN}
domain   : ${DOMAIN}
name     : ${NAME:-"(apex)"}
ipv4     : ${IPV4}${PRESERVE_IP:+ (preserve)}
ttl      : ${TTL}
action   : ${action}
record_id: ${record_id:-"(none)"}
current  : ${current_ip:-"(none)"}
cur_ttl  : ${current_ttl:-"(none)"}
PLAN

if [[ "$APPLY" != "--apply" ]]; then
  echo "(dry run — pass --apply to execute)"
  exit 0
fi

case "$action" in
  noop)
    echo "noop: ${FQDN} already A ${IPV4} ttl=${TTL}"
    ;;
  create)
    create_payload="$(jq -cn \
      --arg name "$NAME" \
      --arg content "$IPV4" \
      --arg ttl "$TTL" \
      '{type:"A", content:$content, ttl:$ttl} + (if $name == "" then {} else {name:$name} end)')"
    create_resp="$(porkbun_post "/dns/create/${DOMAIN}" "$create_payload")"
    require_success "$create_resp" create
    new_id="$(jq -r '.id // empty' <<<"$create_resp")"
    echo "created: ${FQDN} A ${IPV4} ttl=${TTL} id=${new_id:-unknown}"
    ;;
  edit)
    edit_payload="$(jq -cn \
      --arg name "$NAME" \
      --arg content "$IPV4" \
      --arg ttl "$TTL" \
      '{type:"A", content:$content, ttl:$ttl} + (if $name == "" then {} else {name:$name} end)')"
    edit_resp="$(porkbun_post "/dns/edit/${DOMAIN}/${record_id}" "$edit_payload")"
    require_success "$edit_resp" edit
    echo "edited: ${FQDN} A ${current_ip} -> ${IPV4} ttl ${current_ttl:-(none)} -> ${TTL} id=${record_id}"
    ;;
  *)
    echo "::error::internal: unknown action ${action}" >&2
    exit 1
    ;;
esac

echo "NEXT: dig +short @1.1.1.1 ${FQDN}  # expect ${IPV4}"
echo "      update matrix porkbun_a_ipv4 if this host is a managed edge"
