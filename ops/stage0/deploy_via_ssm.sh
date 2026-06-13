#!/usr/bin/env bash
#
# Stage0 SSM deploy primitive.
#
# Scope of this script:
#   1. Patch /var/lib/tokenkey/.env to point at the new image tag.
#   2. `docker compose pull tokenkey` — pull the new image BEFORE draining,
#      while the old container is still healthy and serving 100% of traffic.
#      Pull is the single largest variable in the swap (large multi-arch Go
#      image + slow disk/link, can be tens of seconds). Doing it here keeps it
#      OUT of the new-request blackout window: Caddy's active health only flips
#      the old container out once /health=503 (step 3), so any work done before
#      that costs zero client-facing downtime. Previously pull ran AFTER drain,
#      adding its full duration to the window that clients spend queued in
#      Caddy's lb_try_duration (→ 502 + client retries once it overran 30s).
#   3. Send SIGUSR1 to tokenkey → wait for /health/inflight to report
#      draining=true && in_flight=0 (pre-drain so live SSE finishes). Only now
#      does Caddy active health (health_uri /health) remove the old upstream.
#      Bounded by TWO early-exits so a node carrying long-lived SSE never burns
#      the full budget for a drain that will not complete: (a) a hard cap of 15
#      tries × 2s = 30s, and (b) a plateau break — if in_flight stops decreasing
#      for 3 consecutive VALID readings, stop waiting (the remaining streams are
#      long-lived and the swap will sever them regardless). A failed/empty poll
#      is treated as no-information: it does not advance the plateau counter (and
#      the in_flight=0 break already reads an empty poll as not-drained), so a
#      transient wget blip cannot end the drain early. Measured on the prod
#      1.7.100 redeploy (2026-06-13): under sustained streaming the old 38-try
#      (76s) loop NEVER reached in_flight=0 — it drifted 18→8 and plateaued at 8
#      from try ~31, so it paid the full 76s AND still severed 8 streams at swap.
#      The cap+plateau cut that wasted wait to ≤30s (≈10s once plateaued) for the
#      same client outcome.
#      SKIPPED when the outgoing container is not `healthy`: a crash-looping /
#      unhealthy container has no in-flight to drain (Caddy already flipped it
#      out at /health!=200), and SIGUSR1 + the inflight-wait would block the
#      full ~30s on a container whose `docker exec wget` never answers —
#      starving the new container's health window and tripping the rollback
#      trap, which then restores the (broken) previous image. That exact loop
#      kept uk1 pinned to a crash-looping image on 2026-06-03; gating the drain
#      on old-container health lets a deploy recover an already-broken node.
#   4. `compose up -d --no-deps --force-recreate tokenkey`. The image is
#      already on disk from step 2, so this is just stop-old + start-new.
#      `--force-recreate` is load-bearing: step 3 already flipped drainFlag=true
#      on the running container, and there is no SetDrain(false) call site —
#      only a fresh process can clear drain. Without --force-recreate, a
#      same-tag re-deploy (sed is a no-op when $tag matches the live image)
#      would leave the container running with /health=503 indefinitely until
#      manual `docker restart`. Always recreate.
#   5. Wait for container.Health.Status=healthy; rollback on ERR (which
#      also uses --force-recreate for the same reason).
#
# What this script INTENTIONALLY DOES NOT DO:
#   - It does NOT wholesale-refresh /var/lib/tokenkey/docker-compose.yml.
#     (Exception: it DOES self-heal one specific, additive line — the
#     `SERVER_FRONTEND_URL=${SERVER_FRONTEND_URL:-}` env mapping in the tokenkey
#     service — when absent. Edges provisioned before that mapping was added to
#     the compose template carried a stale compose: the .env backfill below set
#     SERVER_FRONTEND_URL, but with no compose mapping the var never reached the
#     container, so alert cards showed node=unknown on us1/us2/us3/us4/us5/uk1
#     [2026-06-06]. The insert is guarded by `! grep -q SERVER_FRONTEND_URL`, so
#     it is a no-op on already-correct edges, and additive [one env line after
#     the TZ line], so it cannot break an otherwise-valid compose.)
#   - It does NOT refresh /var/lib/tokenkey/caddy/Caddyfile.
#   Both files are written once at instance launch by UserData (gzip+base64
#   decoded from SSM Parameter Store; see stage0-{single,edge}-ec2.yaml +
#   build-cfn.sh). After editing the source files in this repo, existing
#   prod/edge hosts still run the OLD copies on disk until the operator
#   either re-provisions the instance OR runs a manual sync. The image-tag
#   bump in step 1 alone is NOT enough to apply new compose/Caddy directives.
#   For Caddyfile directive changes (e.g. lb_try_duration) the deterministic
#   hot-sync path is ops/stage0/sync_caddyfile_via_ssm.sh, which re-renders the
#   repo Caddyfile on a LIVE host and `caddy reload`s with no connection drop.
#
# See deploy/aws/README.md § "升级 / 发版" and the per-PR change notes when
# the compose or Caddyfile diff matters for take-effect timing.

set -euo pipefail

# Shared SSM "resolve managed-instance after tag-targeted send" helper.
# shellcheck source=ssm_resolve_invocation_mi.inc.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/ssm_resolve_invocation_mi.inc.sh"

TAG="${1:-${INPUT_TAG:-}}"
INSTANCE_ID="${2:-${INSTANCE_ID:-}}"
COMMENT="${3:-${SSM_COMMENT:-deploy-stage0}}"
# Default bumped 300 -> 480 to cover pre-drain (≤ ~30s) + image pull + container
# start + healthcheck (≤ start_period 60s) + headroom on a slow link.
TIMEOUT_SECONDS="${STAGE0_SSM_TIMEOUT_SECONDS:-480}"
OUTPUT_DIR="${STAGE0_SSM_OUTPUT_DIR:-.}"

if [[ -z "${TAG}" ]]; then
  echo "stage0_deploy_via_ssm: tag is required" >&2
  exit 1
fi
if [[ -z "${INSTANCE_ID}" ]]; then
  echo "stage0_deploy_via_ssm: instance id is required" >&2
  exit 1
fi

ssm_region_args=()
if [[ -n "${AWS_REGION:-${AWS_DEFAULT_REGION:-}}" ]]; then
  ssm_region_args=(--region "${AWS_REGION:-${AWS_DEFAULT_REGION}}")
fi

mkdir -p "${OUTPUT_DIR}"
params_file="${OUTPUT_DIR}/ssm-params.json"
stdout_file="${OUTPUT_DIR}/stdout.txt"
stderr_file="${OUTPUT_DIR}/stderr.txt"

jq -n --arg tag "${TAG}" '{
  commands: [
    "set -euo pipefail",
    ("echo === deploy stage0 to tag=" + $tag + " ==="),
    ("BACKUP=/var/lib/tokenkey/.env.before-" + $tag),
    "sudo cp -a /var/lib/tokenkey/.env \"$BACKUP\"",
    "rollback() { rc=$?; echo \"::warning::deploy failed; restoring previous tokenkey image\"; if [ -f \"$BACKUP\" ]; then sudo cp -a \"$BACKUP\" /var/lib/tokenkey/.env; cd /var/lib/tokenkey && sudo docker compose --env-file .env up -d --no-deps --force-recreate tokenkey || true; for i in 1 2 3 4 5 6 7 8 9 10 11 12; do s=$(sudo docker inspect tokenkey --format '\''{{.State.Health.Status}}'\'' 2>/dev/null || echo missing); echo \"rollback try $i: $s\"; [ \"$s\" = healthy ] && break; sleep 5; done; [ \"$s\" = healthy ] || echo \"::error::rollback restored the previous image but it is ALSO not healthy (health=$s) — node requires MANUAL intervention; do NOT assume service is restored. next: deploy/aws/RUNBOOK-disaster-recovery.md — recovery decision tree + Agent handoff contract\"; sudo docker logs tokenkey --since 2m 2>&1 | tail -50 || true; fi; exit $rc; }",
    "trap rollback ERR",
    ("sudo sed -i '\''s|sub2api:[^[:space:]]*|sub2api:" + $tag + "|'\'' /var/lib/tokenkey/.env"),
    "if ! grep -q '\''^SERVER_FRONTEND_URL='\'' /var/lib/tokenkey/.env; then d=$(sed -n '\''s/^API_DOMAIN=//p'\'' /var/lib/tokenkey/.env | head -1); if [ -n \"$d\" ]; then echo \"SERVER_FRONTEND_URL=https://$d\" | sudo tee -a /var/lib/tokenkey/.env >/dev/null; echo \"ensured SERVER_FRONTEND_URL=https://$d\"; else echo \"API_DOMAIN empty; skip SERVER_FRONTEND_URL backfill\"; fi; else echo \"SERVER_FRONTEND_URL already present\"; fi",
    ("if [ -f /var/lib/tokenkey/docker-compose.yml ] && ! grep -q '\''SERVER_FRONTEND_URL'\'' /var/lib/tokenkey/docker-compose.yml; then sudo cp -a /var/lib/tokenkey/docker-compose.yml /var/lib/tokenkey/docker-compose.yml.compose-before-" + $tag + "; sudo sed -i '\''/^      - TZ=/a\\      - SERVER_FRONTEND_URL=${SERVER_FRONTEND_URL:-}'\'' /var/lib/tokenkey/docker-compose.yml; if grep -q '\''SERVER_FRONTEND_URL'\'' /var/lib/tokenkey/docker-compose.yml; then echo ensured-compose-SERVER_FRONTEND_URL-mapping; else echo '\''::warning::failed to insert compose SERVER_FRONTEND_URL mapping'\''; fi; else echo compose-SERVER_FRONTEND_URL-mapping-present-or-no-compose; fi"),
    "echo \"=== pull new image BEFORE drain (old container keeps serving 100% traffic) ===\"",
    "cd /var/lib/tokenkey && sudo docker compose --env-file .env pull tokenkey",
    "echo \"=== pre-drain: SIGUSR1 + wait in_flight=0 (only when outgoing container healthy) ===\"",
    "OLD_HEALTH=$(sudo docker inspect tokenkey --format '\''{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}'\'' 2>/dev/null || echo missing); echo \"pre-drain: outgoing container health=$OLD_HEALTH\"",
    "if [ \"$OLD_HEALTH\" = healthy ]; then sudo docker kill -s USR1 tokenkey 2>/dev/null || true; prev=-1; stall=0; for i in $(seq 1 15); do body=$(sudo docker exec tokenkey wget -q -T 3 -O - http://localhost:8080/health/inflight 2>/dev/null); n=$(printf '\''%s'\'' \"$body\" | sed -n '\''s/.*\"in_flight\":\\([0-9]*\\).*/\\1/p'\''); if printf '\''%s'\'' \"$body\" | grep -q '\''\"draining\":true'\''; then d=true; else d=false; fi; echo \"pre-drain: draining=$d in_flight=${n:-?} try=$i/15\"; [ \"$d\" = true ] && [ \"${n:-1}\" = 0 ] && break; if [ -n \"$n\" ]; then if [ \"$prev\" -ge 0 ] && [ \"$n\" -ge \"$prev\" ]; then stall=$((stall+1)); else stall=0; fi; prev=$n; [ \"$stall\" -ge 3 ] && { echo \"pre-drain: in_flight plateaued at $n for 3 tries (long-lived streams will not finish in the drain budget); stop waiting — swap will sever them\"; break; }; fi; sleep 2; done; else echo \"pre-drain SKIPPED: outgoing container not healthy (health=$OLD_HEALTH) — Caddy already flipped it out, nothing to drain; straight to recreate (avoids burning the ~30s drain budget on a crash-looping container, which previously starved the new container health window and tripped the rollback trap — uk1 2026-06-03)\"; fi",
    "echo \"=== swap: stop-old + start-new (image already on disk from pull above) ===\"",
    "cd /var/lib/tokenkey && sudo docker compose --env-file .env up -d --no-deps --force-recreate tokenkey",
    "for i in 1 2 3 4 5 6 7 8 9 10 11 12; do s=$(sudo docker inspect tokenkey --format '\''{{.State.Health.Status}}'\'' 2>/dev/null || echo missing); echo \"try $i: $s\"; [ \"$s\" = healthy ] && break; sleep 5; done",
    "FINAL=$(sudo docker inspect tokenkey --format '\''{{.State.Health.Status}}'\'' 2>/dev/null || echo missing)",
    "if [ \"$FINAL\" != \"healthy\" ]; then echo \"::error::container did not reach healthy state (final=$FINAL)\"; sudo docker logs tokenkey --since 2m 2>&1 | tail -50; exit 1; fi",
    "trap - ERR",
    "cd /var/lib/tokenkey && sudo docker compose ps",
    "sudo docker logs tokenkey --since 2m 2>&1 | tail -20"
  ]
}' > "${params_file}"

eff_instance_id="${INSTANCE_ID}"
if [[ "${INSTANCE_ID}" == mi-* && -n "${EDGE_ID:-}" ]]; then
  # Hybrid managed nodes minted via create-activation carry tags EdgeId + Platform
  # (see deploy/aws/lightsail/provision-edge.sh). Targeting by tag reaches the
  # live registration even when Parameter Store ssm_managed_instance_id lags.
  cmd_id="$(aws "${ssm_region_args[@]}" ssm send-command \
    --targets "Key=tag:EdgeId,Values=${EDGE_ID}" "Key=tag:Platform,Values=lightsail" \
    --document-name AWS-RunShellScript \
    --comment "${COMMENT} tag=${TAG}" \
    --parameters "file://${params_file}" \
    --query 'Command.CommandId' --output text)"
  eff_instance_id="$(ssm_resolve_invocation_mi "${AWS_REGION:-${AWS_DEFAULT_REGION:-}}" "${cmd_id}")"
  if [[ "${eff_instance_id}" != "${INSTANCE_ID}" ]]; then
    echo "::warning::SSM send resolved instance ${eff_instance_id}; caller passed ${INSTANCE_ID} (check SSM parameter /ssm_managed_instance_id)"
  fi
else
  cmd_id="$(aws "${ssm_region_args[@]}" ssm send-command \
    --instance-ids "${INSTANCE_ID}" \
    --document-name AWS-RunShellScript \
    --comment "${COMMENT} tag=${TAG}" \
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
