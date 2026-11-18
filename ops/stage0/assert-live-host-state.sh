#!/usr/bin/env bash
#
# Stage0 prod live-host STATE drift check (read-only, ADVISORY).
# Daily transport: TAT via execute_remote_params.py (TOKENKEY_REMOTE_VIA default).
# Break-glass: TOKENKEY_REMOTE_VIA=ssm.
#
# Why this exists:
#   A Stage0 prod host's running state is deliberately decoupled from the CFN/repo
#   baseline and kept current by imperative healers — the image tag is hot-deployed
#   via SSM (deploy_via_ssm.sh) so the CFN ImageTag param intentionally lags
#   (changing it would REPLACE the instance), and prod-only env
#   (SERVER_FRONTEND_URL, TOKENKEY_GHCR_KEEP_TAGS) is injected
#   onto the host, NOT carried in the shared compose (avoids
#   the edge 14 KiB launch-script limit). The decoupling is by design — but until
#   this check, NOTHING watched the live host, so every drift (a deploy-sed that
#   wrote the wrong content, a manual host edit, a silent tag rollback) was caught
#   only by a human SSM-probing by hand (the 2026-06 "3× repeat" incident). This
#   makes that drift an automatic ::warning:: post-deploy and a daily-audit signal.
#
# What it asserts (the verdict logic + fixtures live in live_host_state_verdict.py):
#   - the running container image tag == the expected/deployed tag (when given);
#   - the deploy_via_ssm.sh-injected env keys are actually present + non-empty in
#     the running container;
#   - first-output timeout env values match first_output_timeout_ssot.py
#     unless EXPECT_ENV overrides.
#
# Usage:
#   ops/stage0/assert-live-host-state.sh <instance_id> [expected_tag] [comment]
# Env:
#   AWS_REGION / AWS_DEFAULT_REGION   region (else AWS default chain)
#   APP_CONTAINER                     app container name, or auto (default auto:
#                                     active-color -> tokenkey-blue/green,
#                                     fallback tokenkey)
#   REQUIRE_ENV                       override the required-env list (comma-separated)
#   EXPECT_ENV                        comma-separated KEY=VALUE values that must match
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
OUTPUT_DIR="${STAGE0_SSM_OUTPUT_DIR:-.}"
mkdir -p "${OUTPUT_DIR}"
params_file="${OUTPUT_DIR}/ssm-params-live-host.json"
stdout_file="${OUTPUT_DIR}/stdout-live-host.txt"
stderr_file="${OUTPUT_DIR}/stderr-live-host.txt"

collection_args=()
[[ -n "${REQUIRE_ENV+x}" ]] && collection_args+=(--require-env "$REQUIRE_ENV")
[[ -n "${EXPECT_ENV+x}" ]] && collection_args+=(--expect-env "$EXPECT_ENV")
ENV_FILTER="$(python3 "$SCRIPT_DIR/live_host_state_verdict.py" --print-collection-filter ${collection_args[@]+"${collection_args[@]}"})"

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
  | grep -E '${ENV_FILTER}' \
  | while IFS='=' read -r k v; do printf 'ENV {"key":"%s","value":"%s"}\n' "\$k" "\$v"; done
PROBE

REMOTE_B64="$(printf '%s' "${REMOTE_PROBE}" | base64 | tr -d '\n')"
jq -n --arg b64 "${REMOTE_B64}" '{commands: ["echo \($b64) | base64 -d | bash"]}' >"${params_file}"
python3 "${SCRIPT_DIR}/ssm_wrap_bash_commands.py" "${params_file}"

if [[ -n "${STAGE0_SSM_OUTPUT_DIR:-}" || "${STAGE0_RENDER_ONLY:-}" == "1" ]]; then
  echo "[info] wrote ${params_file} (render-only; no remote)"
  exit 0
fi

# Transport failure must NOT make this advisory check exit non-zero — ::warning:: and stop.
export TOKENKEY_REMOTE_VIA="${TOKENKEY_REMOTE_VIA:-tat}"
EXECUTE_REMOTE="${STAGE0_EXECUTE_REMOTE_PARAMS:-${SCRIPT_DIR}/execute_remote_params.py}"
if ! python3 "${EXECUTE_REMOTE}" \
  --params-file "${params_file}" \
  --instance-id "${INSTANCE_ID}" \
  --comment "${COMMENT}" \
  --stdout-file "${stdout_file}" \
  --stderr-file "${stderr_file}"; then
  echo "::warning::live-host assert could not start remote command on ${INSTANCE_ID}; skipping verdict"
  exit 0
fi
probe_out="$(cat "${stdout_file}")"

if [[ -z "${probe_out}" ]]; then
  echo "::warning::live-host assert could not read host state (empty stdout); skipping verdict"
  exit 0
fi

verdict_args=(${collection_args[@]+"${collection_args[@]}"})
[[ -n "${EXPECTED_TAG}" ]] && verdict_args+=(--expected-tag "${EXPECTED_TAG}")

set +e
verdict_out="$(printf '%s\n' "${probe_out}" | python3 "${SCRIPT_DIR}/live_host_state_verdict.py" ${verdict_args[@]+"${verdict_args[@]}"})"
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
