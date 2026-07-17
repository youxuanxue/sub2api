#!/usr/bin/env bash
#
# Stage0 SSM image-prewarm primitive.
#
# Scope: pull a tag's image onto a Stage0 host AHEAD of the actual deploy, so
# that when `deploy_via_ssm.sh` later runs `docker compose pull tokenkey` the
# layers are already on disk and the in-band pull collapses to a ~3s manifest
# no-op. The image pull is the single largest variable in the deploy SSM step
# (a cold pull of the multi-arch Go image measured ~150s on a prod t4g.small,
# 2026-06-13); moving it into a separate earlier round-trip takes it off the
# deploy-stage0 critical path.
#
# What this script does (and ONLY this — it is deliberately read-only):
#   1. Read /var/lib/tokenkey/.env to learn the EXACT image repo the host runs
#      (TOKENKEY_IMAGE=<registry>/<owner>/sub2api:<oldtag>). Deriving the repo
#      from the host — instead of hard-coding ghcr.io — means we warm whatever
#      registry the host actually pulls from (ghcr / dockerhub / mirror), so the
#      warmed layers are the same blobs the deploy will reuse.
#   2. `docker pull <repo>:<TAG>` for the new tag.
#
# What it INTENTIONALLY DOES NOT do — the entire point is that it is safe to run
# at any time against a live host with zero client impact:
#   - It does NOT edit /var/lib/tokenkey/.env (the live container keeps pointing
#     at its current image; only the on-disk layer cache grows).
#   - It does NOT stop / recreate / restart the tokenkey container.
#   - It does NOT drain, swap, or health-check. There is nothing to roll back,
#     so there is no rollback trap.
# A failed warm is non-fatal to the deploy: the later in-band `compose pull`
# simply pays the full pull as it does today.
#
# Targeting mirrors deploy_via_ssm.sh's prod path: a single EC2 instance-id
# (i-*). Edge (Lightsail mi-*) prewarm is a deliberate follow-up — not wired
# here so this script carries no unused targeting branch.

set -euo pipefail

TAG="${1:-${INPUT_TAG:-}}"
INSTANCE_ID="${2:-${INSTANCE_ID:-}}"
COMMENT="${3:-${SSM_COMMENT:-warm-image}}"
# Bound to image pull + extract on a slow link/disk; well under the deploy
# timeout since there is no drain/health window here.
TIMEOUT_SECONDS="${STAGE0_WARM_SSM_TIMEOUT_SECONDS:-300}"
OUTPUT_DIR="${STAGE0_SSM_OUTPUT_DIR:-.}"

if [[ -z "${TAG}" ]]; then
  echo "stage0_warm_pull_via_ssm: tag is required" >&2
  exit 1
fi
if [[ -z "${INSTANCE_ID}" ]]; then
  echo "stage0_warm_pull_via_ssm: instance id is required" >&2
  exit 1
fi

ssm_region_args=()
if [[ -n "${AWS_REGION:-${AWS_DEFAULT_REGION:-}}" ]]; then
  ssm_region_args=(--region "${AWS_REGION:-${AWS_DEFAULT_REGION}}")
fi

mkdir -p "${OUTPUT_DIR}"
params_file="${OUTPUT_DIR}/warm-ssm-params.json"
stdout_file="${OUTPUT_DIR}/warm-stdout.txt"
stderr_file="${OUTPUT_DIR}/warm-stderr.txt"

jq -n --arg tag "${TAG}" '{
  commands: [
    "set -euo pipefail",
    ("echo \"=== warm image for tag=" + $tag + " (read-only pull; no .env edit, no container restart) ===\""),
    "CUR=$(sed -n '\''s/^TOKENKEY_IMAGE=//p'\'' /var/lib/tokenkey/.env | head -1)",
    "if [ -z \"$CUR\" ]; then echo \"::error::TOKENKEY_IMAGE not found in /var/lib/tokenkey/.env\"; exit 1; fi",
    "REPO=\"${CUR%:*}\"",
    "if [ -z \"$REPO\" ] || [ \"$REPO\" = \"$CUR\" ]; then echo \"::error::could not parse repo from TOKENKEY_IMAGE=$CUR\"; exit 1; fi",
    ("IMG=\"${REPO}:" + $tag + "\""),
    "echo \"warming $IMG (host currently runs $CUR)\"",
    "sudo docker pull \"$IMG\"",
    "echo \"=== warm complete: $IMG on disk ===\""
  ]
}' > "${params_file}"

cmd_id="$(aws "${ssm_region_args[@]}" ssm send-command \
  --instance-ids "${INSTANCE_ID}" \
  --document-name AWS-RunShellScript \
  --comment "${COMMENT} tag=${TAG}" \
  --parameters "file://${params_file}" \
  --query 'Command.CommandId' --output text)"

echo "ssm warm command-id=${cmd_id}"
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
    echo "::warning::ssm warm timeout (non-fatal — deploy will pay the in-band pull)" >&2
    status="TimedOut"
    break
  fi
  sleep 5
done

aws "${ssm_region_args[@]}" ssm get-command-invocation \
  --command-id "${cmd_id}" --instance-id "${INSTANCE_ID}" \
  --query 'StandardOutputContent' --output text > "${stdout_file}" 2>/dev/null || true  # preflight-allow: swallow (diagnostic fetch; warm is non-fatal)
aws "${ssm_region_args[@]}" ssm get-command-invocation \
  --command-id "${cmd_id}" --instance-id "${INSTANCE_ID}" \
  --query 'StandardErrorContent' --output text > "${stderr_file}" 2>/dev/null || true  # preflight-allow: swallow (diagnostic fetch; warm is non-fatal)

echo '--- ssm warm stdout (last 4KB) ---'
tail -c 4096 "${stdout_file}" 2>/dev/null || true  # preflight-allow: swallow (display only)
echo
echo '--- ssm warm stderr (last 4KB) ---'
tail -c 4096 "${stderr_file}" 2>/dev/null || true  # preflight-allow: swallow (display only)
echo

# A warm failure is non-fatal: the deploy still works, it just pays the pull.
# Exit 0 on TimedOut (already warned); only a hard SSM Failed is worth a
# non-zero so the caller can surface it, but even then the deploy is safe.
if [[ "${status}" == "Failed" ]]; then
  echo "::warning::ssm warm status=Failed (non-fatal — deploy will pay the in-band pull)" >&2
fi
exit 0
