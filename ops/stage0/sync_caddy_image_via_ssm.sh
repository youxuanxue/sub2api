#!/usr/bin/env bash
#
# Stage0 Caddy IMAGE swap + Caddyfile sync primitive (prod).
#
# Why this exists (and why sync_caddyfile_via_ssm.sh can't do it):
#   The per-IP `rate_limit` directive lives in github.com/mholt/caddy-ratelimit,
#   which is NOT in stock caddy:2-alpine. Shipping it requires BOTH:
#     (a) swapping the caddy container image to the custom multi-arch build
#         (ghcr.io/<owner>/tokenkey-caddy:*-ratelimit), AND
#     (b) the Caddyfile carrying the `rate_limit` block.
#   These are coupled: the new Caddyfile is unparseable on the old image
#   ("unknown directive: rate_limit"), and the new image is pointless without
#   the directive. sync_caddyfile_via_ssm.sh hot-reloads in place and validates
#   with caddy:2-alpine — both of which BREAK on the rate_limit Caddyfile. An
#   image change also fundamentally cannot hot-reload; the container must be
#   recreated. Hence this companion.
#
# What it does (mirrors boot UserData render + deploy_via_ssm.sh pull-then-swap):
#   1. base64 the canonical repo docker-compose.yml + Caddyfile and ship both in
#      the SSM command (does not depend on CFN having propagated new SSM params;
#      same philosophy as sync_caddyfile_via_ssm.sh).
#   2. On host: back up live docker-compose.yml + Caddyfile.
#   3. Write new compose; render new Caddyfile (template + envsubst, same vars as
#      boot — API_DOMAIN / ACME_EMAIL from /var/lib/tokenkey/.env).
#   4. `docker compose pull caddy` — pull the new image BEFORE recreate so the
#      443 blackout window is just the container restart, not the pull.
#   5. Validate the rendered Caddyfile with the NEW image (it understands
#      rate_limit; caddy:2-alpine would not).
#   6. `docker compose up -d --no-deps --force-recreate caddy` — recreate with
#      new image + new config. ~2-5s of 443 unavailability; run at low traffic.
#   7. Verify caddy running + the rate_limit directive is live + serving.
#   8. Rollback on any ERR: restore both files and recreate caddy with the OLD
#      compose (old image), so a failed swap returns to the last-good state.
#
# Reboot durability is OUT OF SCOPE (same as sync_caddyfile_via_ssm.sh): the SSM
# Parameter UserData reads at next launch is CFN-owned. build-cfn.sh has already
# refreshed the embedded blob; a CFN stack UpdateStack propagates it to Parameter
# Store for the next reboot. This script fixes the LIVE host now.
#
# PRECONDITION: the custom image tag referenced by docker-compose.yml must already
# be published AND public on GHCR (run .github/workflows/caddy-image.yml first,
# then set package visibility public). Otherwise `docker compose pull caddy`
# fails and the script rolls back without changing anything.
#
# Usage:
#   ops/stage0/sync_caddy_image_via_ssm.sh <instance_id> [comment]
#
# Env:
#   AWS_REGION / AWS_DEFAULT_REGION   region for SSM (optional)
#   STAGE0_SSM_TIMEOUT_SECONDS        SSM poll timeout (default 360 — covers image pull)
#   STAGE0_SSM_OUTPUT_DIR             where to drop ssm-params/stdout/stderr

set -euo pipefail

INSTANCE_ID="${1:-${INSTANCE_ID:-}}"
COMMENT="${2:-${SSM_COMMENT:-sync-caddy-image}}"
TIMEOUT_SECONDS="${STAGE0_SSM_TIMEOUT_SECONDS:-360}"
OUTPUT_DIR="${STAGE0_SSM_OUTPUT_DIR:-.}"

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${HERE}/../.." && pwd)"

COMPOSE_SRC="${REPO_ROOT}/deploy/aws/stage0/docker-compose.yml"
CADDY_SRC="${REPO_ROOT}/deploy/aws/stage0/Caddyfile"

if [[ -z "${INSTANCE_ID}" ]]; then
  echo "sync_caddy_image_via_ssm: instance id is required" >&2
  exit 1
fi
for f in "${COMPOSE_SRC}" "${CADDY_SRC}"; do
  [[ -f "$f" ]] || { echo "sync_caddy_image_via_ssm: missing $f" >&2; exit 1; }
done

COMPOSE_B64="$(base64 < "${COMPOSE_SRC}" | tr -d '\n')"
CADDY_B64="$(base64 < "${CADDY_SRC}" | tr -d '\n')"

ssm_region_args=()
if [[ -n "${AWS_REGION:-${AWS_DEFAULT_REGION:-}}" ]]; then
  ssm_region_args=(--region "${AWS_REGION:-${AWS_DEFAULT_REGION}}")
fi

mkdir -p "${OUTPUT_DIR}"
params_file="${OUTPUT_DIR}/ssm-params.json"
stdout_file="${OUTPUT_DIR}/stdout.txt"
stderr_file="${OUTPUT_DIR}/stderr.txt"

jq -n --arg compose_b64 "${COMPOSE_B64}" --arg caddy_b64 "${CADDY_B64}" '{
  commands: [
    "set -euo pipefail",
    "ROOT=/var/lib/tokenkey",
    "CADDY_DIR=$ROOT/caddy",
    "LIVE_COMPOSE=$ROOT/docker-compose.yml",
    "LIVE_CADDY=$CADDY_DIR/Caddyfile",
    "TS=$(date +%Y%m%d-%H%M%S)",
    "BK_COMPOSE=$ROOT/docker-compose.yml.before-$TS",
    "BK_CADDY=$CADDY_DIR/Caddyfile.before-$TS",
    "echo \"=== sync caddy image+config (backup compose=$BK_COMPOSE caddy=$BK_CADDY) ===\"",
    "[ -f \"$LIVE_COMPOSE\" ] || { echo \"::error::no live compose at $LIVE_COMPOSE — is this a Stage0 host?\"; exit 1; }",
    "[ -f \"$LIVE_CADDY\" ] || { echo \"::error::no live Caddyfile at $LIVE_CADDY\"; exit 1; }",
    "sudo cp -a \"$LIVE_COMPOSE\" \"$BK_COMPOSE\"",
    "sudo cp -a \"$LIVE_CADDY\" \"$BK_CADDY\"",
    "rollback() { rc=$?; echo \"::warning::sync failed; restoring previous compose+Caddyfile and recreating caddy on OLD image\"; sudo sh -c \"cat '\''$BK_COMPOSE'\'' > '\''$LIVE_COMPOSE'\''\" || true; sudo sh -c \"cat '\''$BK_CADDY'\'' > '\''$LIVE_CADDY'\''\" || true; cd \"$ROOT\" && sudo docker compose --env-file .env up -d --no-deps --force-recreate caddy 2>&1 | tail -8 || true; exit $rc; }",
    "trap rollback ERR",
    "echo \"=== derive render vars from host .env (same set as boot UserData) ===\"",
    "set -a; . $ROOT/.env; set +a",
    "[ -n \"${API_DOMAIN:-}\" ] || { echo \"::error::API_DOMAIN empty in $ROOT/.env\"; exit 1; }",
    "echo \"render vars: API_DOMAIN=$API_DOMAIN ACME_EMAIL=${ACME_EMAIL:-}\"",
    "echo \"=== write new compose ===\"",
    ("printf '\''%s'\'' \"" + $compose_b64 + "\" | base64 -d | sudo tee \"$LIVE_COMPOSE\" >/dev/null"),
    "echo \"=== render new Caddyfile (template + envsubst, IDENTICAL to boot) ===\"",
    ("printf '\''%s'\'' \"" + $caddy_b64 + "\" | base64 -d | sudo tee \"$CADDY_DIR/Caddyfile.template\" >/dev/null"),
    "sudo sh -c \"API_DOMAIN='\''$API_DOMAIN'\'' ACME_EMAIL='\''${ACME_EMAIL:-}'\'' envsubst '\''\\$API_DOMAIN \\$ACME_EMAIL'\'' < '\''$CADDY_DIR/Caddyfile.template'\'' > '\''$CADDY_DIR/Caddyfile.new'\''\"",
    "echo \"=== pull new caddy image BEFORE recreate (keeps 443 blackout to just the restart) ===\"",
    "cd \"$ROOT\" && sudo docker compose --env-file .env pull caddy",
    "NEW_IMG=$(cd \"$ROOT\" && sudo docker compose --env-file .env config --images caddy 2>/dev/null | head -1)",
    "echo \"new caddy image: ${NEW_IMG:-<unknown>}\"",
    "echo \"=== validate rendered Caddyfile with the NEW image (understands rate_limit) ===\"",
    "sudo docker run --rm -v \"$CADDY_DIR/Caddyfile.new\":/tmp/Caddyfile:ro \"$NEW_IMG\" caddy validate --config /tmp/Caddyfile --adapter caddyfile",
    "echo \"=== apply Caddyfile IN PLACE (cat-truncate keeps the bind-mounted inode) ===\"",
    "sudo sh -c \"cat '\''$CADDY_DIR/Caddyfile.new'\'' > '\''$LIVE_CADDY'\''\"",
    "sudo rm -f \"$CADDY_DIR/Caddyfile.new\"",
    "echo \"=== recreate caddy with new image + new config (~2-5s 443 blip) ===\"",
    "cd \"$ROOT\" && sudo docker compose --env-file .env up -d --no-deps --force-recreate caddy",
    "echo \"=== verify ===\"",
    "for i in 1 2 3 4 5 6 7 8 9 10; do s=$(sudo docker inspect tokenkey-caddy --format '\''{{.State.Status}}'\'' 2>/dev/null || echo missing); echo \"caddy status try $i: $s\"; [ \"$s\" = running ] && break; sleep 2; done",
    "[ \"$s\" = running ] || { echo \"::error::caddy not running after recreate (status=$s)\"; exit 1; }",
    "sudo docker inspect tokenkey-caddy --format '\''caddy image={{.Config.Image}} running={{.State.Running}}'\''",
    "grep -nE '\''rate_limit'\'' \"$LIVE_CADDY\" || { echo \"::error::rate_limit directive missing from live Caddyfile after sync\"; exit 1; }",
    "echo \"=== smoke: caddy answers 443 locally ===\"",
    "curl -ksS -o /dev/null -w '\''local https status=%{http_code}\\n'\'' https://localhost/ || echo '\''::warning::local https probe failed (cert/SNI may need the public hostname) — check externally'\''",
    "trap - ERR",
    "echo === sync done ==="
  ]
}' > "${params_file}"

cmd_id="$(aws "${ssm_region_args[@]}" ssm send-command \
  --instance-ids "${INSTANCE_ID}" \
  --document-name AWS-RunShellScript \
  --comment "${COMMENT}" \
  --parameters "file://${params_file}" \
  --query 'Command.CommandId' --output text)"

echo "ssm command-id=${cmd_id}"
if [[ -n "${GITHUB_OUTPUT:-}" ]]; then
  echo "command_id=${cmd_id}" >> "${GITHUB_OUTPUT}"
fi

deadline=$(( $(date +%s) + TIMEOUT_SECONDS ))
status="InProgress"
while true; do
  status="$(aws "${ssm_region_args[@]}" ssm get-command-invocation \
    --command-id "${cmd_id}" --instance-id "${INSTANCE_ID}" \
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
  --command-id "${cmd_id}" --instance-id "${INSTANCE_ID}" \
  --query 'StandardOutputContent' --output text > "${stdout_file}"
aws "${ssm_region_args[@]}" ssm get-command-invocation \
  --command-id "${cmd_id}" --instance-id "${INSTANCE_ID}" \
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
