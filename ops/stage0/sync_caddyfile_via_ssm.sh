#!/usr/bin/env bash
#
# Stage0 Caddyfile hot-sync primitive.
#
# Why this exists:
#   deploy_via_ssm.sh INTENTIONALLY does NOT refresh /var/lib/tokenkey/caddy/
#   Caddyfile (see its header). The Caddyfile is rendered once at instance
#   launch by CFN UserData (SSM Parameter → base64 -d | gunzip → envsubst). So a
#   Caddyfile directive change in this repo (e.g. lb_try_duration 30s → 120s)
#   lands on a RUNNING host only via instance replacement OR this script.
#
# What this script does (mirrors the CFN UserData render EXACTLY):
#   1. base64 the canonical repo Caddyfile (deploy/aws/stage0/Caddyfile for
#      prod, Caddyfile.edge for edge) — the same file build-cfn.sh embeds into
#      the CFN SSM Parameter, kept bit-identical by `build-cfn.sh --check`
#      (preflight). Shipping the repo file (not re-reading the SSM Parameter)
#      means the sync does not depend on a CFN stack update having already
#      propagated the new blob to Parameter Store.
#   2. On the host: write it to caddy/Caddyfile.template, derive the render vars
#      (same set as boot UserData), then `envsubst` — matching the UserData
#      "envsubst < caddy/Caddyfile.template > caddy/Caddyfile" line:
#        - API_DOMAIN / ACME_EMAIL: sourced from /var/lib/tokenkey/.env (the
#          boot UserData persists both there).
#        - MAIN_GATEWAY_ALLOWED_CIDR (edge only): NOT in .env — boot UserData
#          holds it only transiently. Recovered from the live Caddyfile's
#          `remote_ip` line so the relay allowlist is preserved verbatim. For
#          prod the template has no such token, so the var stays empty/no-op.
#      For prod hosts already migrated to blue/green, rewrite the rendered
#      canonical prod upstream from tokenkey:8080 to tokenkey-${active}:8080 so
#      directive hot-sync never disables the active color.
#   3. Validate the rendered config in a throwaway caddy:2-alpine container.
#   4. Apply IN PLACE (`cat new > Caddyfile`, NOT mv): the compose mount binds
#      the single FILE, so replacing the inode via mv would leave the running
#      tokenkey-caddy container reading the OLD inode. cat-truncate keeps the
#      inode the container already has mapped.
#   5. `docker exec tokenkey-caddy caddy reload` — hot reload, zero connection
#      drop. Verify the new directive is present and Caddy is still serving.
#   6. Rollback (restore backup + reload) on any ERR.
#
# Reboot durability is OUT OF SCOPE for this script: the SSM Parameter that
# UserData reads at the NEXT launch is CFN-owned. build-cfn.sh has already
# refreshed the embedded blob in the template; a normal CFN stack update (or
# re-provision) propagates it to Parameter Store. This script handles the LIVE
# host NOW; the stack update handles the next reboot. Running only this script
# leaves the host live-correct but the SSM Parameter stale until that update —
# acceptable because instance replacement is rare and re-runs UserData against
# whatever the Parameter holds at that time.
#
# Usage:
#   ops/stage0/sync_caddyfile_via_ssm.sh <prod|edge> <instance_id> [comment]
#   EDGE_ID=<edge> ops/stage0/sync_caddyfile_via_ssm.sh edge <mi-id> [comment]
#
# Env:
#   AWS_REGION / AWS_DEFAULT_REGION   region for SSM (optional)
#   EDGE_ID                           Lightsail Hybrid edge id; when set and
#                                     instance_id is mi-*, targets by tag like
#                                     deploy_via_ssm.sh.
#   STAGE0_SSM_TIMEOUT_SECONDS        SSM poll timeout (default 240)
#   STAGE0_SSM_OUTPUT_DIR             where to drop ssm-params/stdout/stderr

set -euo pipefail

KIND="${1:-}"
INSTANCE_ID="${2:-${INSTANCE_ID:-}}"
COMMENT="${3:-${SSM_COMMENT:-sync-caddyfile}}"
TIMEOUT_SECONDS="${STAGE0_SSM_TIMEOUT_SECONDS:-240}"
OUTPUT_DIR="${STAGE0_SSM_OUTPUT_DIR:-.}"

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${HERE}/../.." && pwd)"

# Shared SSM "resolve managed-instance after tag-targeted send" helper.
# shellcheck source=ssm_resolve_invocation_mi.inc.sh
source "${HERE}/ssm_resolve_invocation_mi.inc.sh"

case "${KIND}" in
  prod) CADDY_SRC="${REPO_ROOT}/deploy/aws/stage0/Caddyfile" ;;
  edge) CADDY_SRC="${REPO_ROOT}/deploy/aws/stage0/Caddyfile.edge" ;;
  *)
    echo "sync_caddyfile_via_ssm: first arg must be 'prod' or 'edge' (got '${KIND}')" >&2
    exit 1
    ;;
esac

if [[ -z "${INSTANCE_ID}" ]]; then
  echo "sync_caddyfile_via_ssm: instance id is required" >&2
  exit 1
fi
if [[ ! -f "${CADDY_SRC}" ]]; then
  echo "sync_caddyfile_via_ssm: missing ${CADDY_SRC}" >&2
  exit 1
fi

# base64 the canonical repo Caddyfile; tr -d '\n' keeps it a single token so it
# embeds cleanly in the SSM command array.
CADDY_B64="$(base64 < "${CADDY_SRC}" | tr -d '\n')"

ssm_region_args=()
if [[ -n "${AWS_REGION:-${AWS_DEFAULT_REGION:-}}" ]]; then
  ssm_region_args=(--region "${AWS_REGION:-${AWS_DEFAULT_REGION}}")
fi

mkdir -p "${OUTPUT_DIR}"
params_file="${OUTPUT_DIR}/ssm-params.json"
stdout_file="${OUTPUT_DIR}/stdout.txt"
stderr_file="${OUTPUT_DIR}/stderr.txt"

jq -n --arg b64 "${CADDY_B64}" --arg kind "${KIND}" '{
  commands: [
    "set -euo pipefail",
    ("KIND=" + $kind),
    "CADDY_DIR=/var/lib/tokenkey/caddy",
    "LIVE=$CADDY_DIR/Caddyfile",
    "TS=$(date +%Y%m%d-%H%M%S)",
    "BACKUP=$CADDY_DIR/Caddyfile.before-$TS",
    "echo \"=== sync Caddyfile (kind=$KIND backup=$BACKUP) ===\"",
    "[ -f \"$LIVE\" ] || { echo \"::error::no live Caddyfile at $LIVE — is this a Stage0 host?\"; exit 1; }",
    "sudo cp -a \"$LIVE\" \"$BACKUP\"",
    "rollback() { rc=$?; echo \"::warning::sync failed; restoring previous Caddyfile\"; if [ -f \"$BACKUP\" ]; then sudo sh -c \"cat '\''$BACKUP'\'' > '\''$LIVE'\''\"; sudo docker exec tokenkey-caddy caddy reload --config /etc/caddy/Caddyfile --adapter caddyfile 2>&1 | tail -5 || true; fi; exit $rc; }",
    "trap rollback ERR",
    "echo \"=== derive render vars (same set as boot UserData) ===\"",
    "# API_DOMAIN / ACME_EMAIL are persisted in the host .env at boot.",
    "set -a; . /var/lib/tokenkey/.env; set +a",
    "[ -n \"${API_DOMAIN:-}\" ] || { echo \"::error::API_DOMAIN empty in /var/lib/tokenkey/.env\"; exit 1; }",
    "# MAIN_GATEWAY_ALLOWED_CIDR (edge only) is NOT in .env — it lives only in",
    "# boot UserData. Recover the live value from the remote_ip line in the",
    "# current rendered Caddyfile so the allowlist survives the re-render verbatim.",
    "MAIN_GATEWAY_ALLOWED_CIDR=\"$(sed -n '\''s/^[[:space:]]*remote_ip[[:space:]][[:space:]]*\\(.*\\)$/\\1/p'\'' \"$LIVE\" | head -1)\"",
    "if [ \"$KIND\" = edge ] && [ -z \"$MAIN_GATEWAY_ALLOWED_CIDR\" ]; then echo \"::error::could not read remote_ip allowlist from live edge Caddyfile $LIVE\"; exit 1; fi",
    "echo \"render vars: API_DOMAIN=$API_DOMAIN ACME_EMAIL=${ACME_EMAIL:-} MAIN_GATEWAY_ALLOWED_CIDR=${MAIN_GATEWAY_ALLOWED_CIDR:-<none>}\"",
    ("printf '\''%s'\'' \"" + $b64 + "\" | base64 -d | sudo tee \"$CADDY_DIR/Caddyfile.template\" >/dev/null"),
    "sudo sh -c \"API_DOMAIN='\''$API_DOMAIN'\'' ACME_EMAIL='\''${ACME_EMAIL:-}'\'' MAIN_GATEWAY_ALLOWED_CIDR='\''${MAIN_GATEWAY_ALLOWED_CIDR:-}'\'' envsubst '\''\\$API_DOMAIN \\$ACME_EMAIL \\$MAIN_GATEWAY_ALLOWED_CIDR'\'' < '\''$CADDY_DIR/Caddyfile.template'\'' > '\''$CADDY_DIR/Caddyfile.new'\''\"",
    "if [ \"$KIND\" = prod ] && [ -r /var/lib/tokenkey/active-color ]; then ACTIVE_COLOR=\"$(sed -n '\''1p'\'' /var/lib/tokenkey/active-color | tr -d '\''[:space:]'\'')\"; case \"$ACTIVE_COLOR\" in blue|green) UPSTREAM=\"tokenkey-$ACTIVE_COLOR:8080\"; sudo awk -v upstream=\"$UPSTREAM\" '\''/^[[:space:]]*reverse_proxy[[:space:]]+/ && $0 ~ /\\{[[:space:]]*$/ { count += 1; if (count == 1) { match($0, /[^[:space:]]/); indent = RSTART > 1 ? substr($0, 1, RSTART - 1) : \"\"; print indent \"reverse_proxy \" upstream \" {\" } else { print }; next } { print } END { if (count != 1) exit 7 }'\'' \"$CADDY_DIR/Caddyfile.new\" | sudo tee \"$CADDY_DIR/Caddyfile.rewritten\" >/dev/null; sudo mv \"$CADDY_DIR/Caddyfile.rewritten\" \"$CADDY_DIR/Caddyfile.new\"; echo \"prod blue/green active upstream preserved: $UPSTREAM\" ;; *) echo \"::error::invalid active-color for prod blue/green Caddy sync: ${ACTIVE_COLOR:-<empty>}\"; exit 1 ;; esac; fi",
    "echo === validate rendered config in throwaway caddy container ===",
    "sudo docker run --rm -v \"$CADDY_DIR/Caddyfile.new\":/tmp/Caddyfile:ro caddy:2-alpine caddy validate --config /tmp/Caddyfile --adapter caddyfile",
    "echo \"=== apply IN PLACE (cat-truncate keeps inode the bind-mount maps) ===\"",
    "sudo sh -c \"cat '\''$CADDY_DIR/Caddyfile.new'\'' > '\''$LIVE'\''\"",
    "sudo rm -f \"$CADDY_DIR/Caddyfile.new\"",
    "echo === hot reload caddy ===",
    "sudo docker exec tokenkey-caddy caddy reload --config /etc/caddy/Caddyfile --adapter caddyfile",
    "echo === verify ===",
    "sudo docker inspect tokenkey-caddy --format '\''caddy state={{.State.Status}} running={{.State.Running}}'\''",
    "grep -nE '\''lb_try_duration'\'' \"$LIVE\" || true",
    "trap - ERR",
    "echo === sync done ==="
  ]
}' > "${params_file}"

eff_instance_id="${INSTANCE_ID}"
if [[ "${INSTANCE_ID}" == mi-* && -n "${EDGE_ID:-}" ]]; then
  cmd_id="$(aws "${ssm_region_args[@]}" ssm send-command \
    --targets "Key=tag:EdgeId,Values=${EDGE_ID}" "Key=tag:Platform,Values=lightsail" \
    --document-name AWS-RunShellScript \
    --comment "${COMMENT} kind=${KIND}" \
    --parameters "file://${params_file}" \
    --query 'Command.CommandId' --output text)"
  eff_instance_id="$(ssm_resolve_invocation_mi "${AWS_REGION:-${AWS_DEFAULT_REGION:-}}" "${cmd_id}")"
  if [[ "${eff_instance_id}" != "${INSTANCE_ID}" ]]; then
    echo "::warning::SSM send resolved instance ${eff_instance_id}; caller passed ${INSTANCE_ID}"
  fi
else
  cmd_id="$(aws "${ssm_region_args[@]}" ssm send-command \
    --instance-ids "${INSTANCE_ID}" \
    --document-name AWS-RunShellScript \
    --comment "${COMMENT} kind=${KIND}" \
    --parameters "file://${params_file}" \
    --query 'Command.CommandId' --output text)"
fi

echo "ssm command-id=${cmd_id}"
if [[ -n "${GITHUB_OUTPUT:-}" ]]; then
  echo "command_id=${cmd_id}" >> "${GITHUB_OUTPUT}"
fi

deadline=$(( $(date +%s) + TIMEOUT_SECONDS ))
status="InProgress"
while true; do
  status="$(aws "${ssm_region_args[@]}" ssm get-command-invocation \
    --command-id "${cmd_id}" --instance-id "${eff_instance_id}" \
    --query 'Status' --output text 2>/dev/null || echo InProgress)"
  case "${status}" in
    Success|Failed|TimedOut|Cancelled) break ;;
  esac
  if [[ $(date +%s) -ge ${deadline} ]]; then
    echo "::error::ssm timeout" >&2
    status="TimedOut"
    break
  fi
  sleep 5
done

aws "${ssm_region_args[@]}" ssm get-command-invocation \
  --command-id "${cmd_id}" --instance-id "${eff_instance_id}" \
  --query 'StandardOutputContent' --output text > "${stdout_file}"
aws "${ssm_region_args[@]}" ssm get-command-invocation \
  --command-id "${cmd_id}" --instance-id "${eff_instance_id}" \
  --query 'StandardErrorContent' --output text > "${stderr_file}"

echo '--- ssm stdout (last 8KB) ---'
tail -c 8192 "${stdout_file}"
echo
echo '--- ssm stderr (last 8KB) ---'
tail -c 8192 "${stderr_file}"
echo

if [[ "${status}" != "Success" ]]; then
  echo "::error::ssm command status=${status}" >&2
  exit 1
fi
