#!/usr/bin/env bash
#
# Stage0 prod live-host STATE drift check (read-only, ADVISORY).
#
# Why this exists:
#   A Stage0 prod host's running state is deliberately decoupled from the CFN/repo
#   baseline and kept current by imperative healers — the image tag is hot-deployed
#   via SSM (deploy_via_ssm.sh) so the CFN ImageTag param intentionally lags
#   (changing it would REPLACE the instance), and prod-only env
#   (SERVER_FRONTEND_URL, TOKENKEY_GHCR_KEEP_TAGS, the four QA_CAPTURE_EXPORT_STORAGE_*
#   vars) is sed-injected onto the host, NOT carried in the shared compose (avoids
#   the edge 14 KiB launch-script limit). The decoupling is by design — but until
#   this check, NOTHING watched the live host, so every drift (a deploy-sed that
#   wrote the wrong content, a manual host edit, a silent tag rollback) was caught
#   only by a human SSM-probing by hand (the 2026-06 "3× repeat" incident). This
#   makes that drift an automatic ::warning:: post-deploy and a daily-audit signal.
#
# What it asserts (the verdict logic + fixtures live in live_host_state_verdict.py):
#   - the running container image tag == the expected/deployed tag (when given);
#   - the deploy_via_ssm.sh-injected env keys are actually present + non-empty in
#     the running container.
#
# Usage:
#   ops/stage0/assert-live-host-state.sh <instance_id> [expected_tag] [comment]
# Env:
#   AWS_REGION / AWS_DEFAULT_REGION   region (else AWS default chain)
#   APP_CONTAINER                     app container name, or auto (default auto:
#                                     active-color -> tokenkey-blue/green,
#                                     fallback tokenkey)
#   REQUIRE_ENV                       override the required-env list (comma-separated)
#   SSM_TIMEOUT_SECONDS               invocation wait budget (default 120)
#
# Exit status: ALWAYS 0 except usage error (missing instance_id). Drift is surfaced
# via ::warning:: lines, never via a non-zero exit — a working deploy must not be
# failed by a state observation (mirrors check_exclusive_group_orphans_via_ssm.sh).

set -euo pipefail

INSTANCE_ID="${1:-${INSTANCE_ID:-}}"
EXPECTED_TAG="${2:-${EXPECTED_TAG:-}}"
COMMENT="${3:-stage0 live-host state assert}"
APP_CONTAINER="${APP_CONTAINER:-auto}"
SSM_TIMEOUT_SECONDS="${SSM_TIMEOUT_SECONDS:-120}"

if [[ -z "${INSTANCE_ID}" ]]; then
  echo "usage: $0 <instance_id> [expected_tag] [comment]" >&2
  exit 2
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

ssm_region_args=()
_region="${AWS_REGION:-${AWS_DEFAULT_REGION:-}}"
[[ -n "${_region}" ]] && ssm_region_args=(--region "${_region}")

# Read-only remote probe: emit tagged, field-named JSON the verdict parses. Only
# emits ENV lines for keys actually present, so a missing required key yields no
# line → the verdict flags it. base64-delivered to dodge SSM/JSON quoting.
read -r -d '' REMOTE_PROBE <<PROBE || true
set -u
app_container='${APP_CONTAINER}'
if [ "\$app_container" = auto ]; then
  if [ -r /var/lib/tokenkey/active-color ]; then
    color=\$(sed -n '1p' /var/lib/tokenkey/active-color 2>/dev/null | tr -d '[:space:]')
    case "\$color" in
      blue|green) app_container="tokenkey-\$color" ;;
      *) app_container=tokenkey ;;
    esac
  else
    app_container=tokenkey
  fi
fi
printf 'APPCONTAINER {"name":"%s"}\n' "\$app_container"
img=\$(docker inspect "\$app_container" --format '{{.Config.Image}}' 2>/dev/null)
printf 'RUNIMAGE {"image":"%s"}\n' "\$img"
docker exec "\$app_container" printenv 2>/dev/null \
  | grep -E '^(SERVER_FRONTEND_URL|QA_CAPTURE_EXPORT_STORAGE_(DRIVER|REGION|BUCKET|PREFIX)|QA_CAPTURE_AUTO_EXPORT_ENABLED)=' \
  | while IFS='=' read -r k v; do printf 'ENV {"key":"%s","value":"%s"}\n' "\$k" "\$v"; done
ret=\$(sed -n 's/^TOKENKEY_QA_STALE_RETENTION_DAYS=//p' /etc/tokenkey/qa-stale-retention.env 2>/dev/null | head -1)
printf 'RETENTION {"value":"%s"}\n' "\$ret"
PROBE

REMOTE_B64="$(printf '%s' "${REMOTE_PROBE}" | base64 | tr -d '\n')"

# Guard send-command: an SSM transport failure must NOT make this advisory check
# exit non-zero (the contract above) — surface it as a ::warning:: and stop.
if ! cmd_id="$(aws ssm send-command "${ssm_region_args[@]}" \
  --instance-ids "${INSTANCE_ID}" \
  --document-name AWS-RunShellScript \
  --comment "${COMMENT}" \
  --parameters "commands=[\"echo ${REMOTE_B64} | base64 -d | bash\"]" \
  --query 'Command.CommandId' --output text 2>/dev/null)" || [[ -z "${cmd_id}" ]]; then
  echo "::warning::live-host assert could not start SSM command on ${INSTANCE_ID}; skipping verdict"
  exit 0
fi

# Poll for completion (the CLI waiter can be flaky on very short commands).
deadline=$(( $(date +%s) + SSM_TIMEOUT_SECONDS ))
status="Pending"
while :; do
  status="$(aws ssm get-command-invocation "${ssm_region_args[@]}" \
    --command-id "${cmd_id}" --instance-id "${INSTANCE_ID}" \
    --query 'Status' --output text 2>/dev/null || echo Pending)"
  case "${status}" in
    Success|Failed|Cancelled|TimedOut) break ;;
  esac
  [[ $(date +%s) -ge ${deadline} ]] && break
  sleep 2
done

probe_out="$(aws ssm get-command-invocation "${ssm_region_args[@]}" \
  --command-id "${cmd_id}" --instance-id "${INSTANCE_ID}" \
  --query 'StandardOutputContent' --output text 2>/dev/null || true)"

if [[ "${status}" != "Success" || -z "${probe_out}" ]]; then
  echo "::warning::live-host assert could not read host state (ssm status=${status}); skipping verdict"
  exit 0
fi

verdict_args=()
[[ -n "${EXPECTED_TAG}" ]] && verdict_args+=(--expected-tag "${EXPECTED_TAG}")
[[ -n "${REQUIRE_ENV:-}" ]] && verdict_args+=(--require-env "${REQUIRE_ENV}")

set +e
verdict_out="$(printf '%s\n' "${probe_out}" | python3 "${SCRIPT_DIR}/live_host_state_verdict.py" "${verdict_args[@]}")"
verdict_rc=$?
set -e

echo "${verdict_out}"
if [[ ${verdict_rc} -ne 0 ]]; then
  # Surface every drift line as a ::warning:: so it shows in the GHA annotations.
  while IFS= read -r line; do
    [[ "${line}" == "  - "* ]] && echo "::warning::live-host drift:${line#  -}"
  done <<< "${verdict_out}"
fi

exit 0
