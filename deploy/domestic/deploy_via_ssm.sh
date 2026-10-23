#!/usr/bin/env bash
# cn-prod release image + control sync via AWS SSM Hybrid (default path).
#
# Default: CI-verified private S3 archive. GHCR is an explicit fallback.
# Neither path opens SSH or changes running containers.
#
# Usage:
#   bash deploy/domestic/deploy_via_ssm.sh pull --tag 1.8.306|v1.8.306 --revision FULL_SHA
#   bash deploy/domestic/deploy_via_ssm.sh sync-controls
#   bash deploy/domestic/deploy_via_ssm.sh prepare --tag 1.8.306 --revision FULL_SHA \
#        --image tokenkey-domestic/tokenkey:1.8.306-2d4b4cdbf
#
# Identity: deploy/domestic/prod-target.json (ssm_region + ssm_instance_id).
# Does not mutate AWS prod, DNS, or the public Caddy route (prepare only).
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
MANIFEST="${REPO_ROOT}/deploy/domestic/prod-target.json"
CONTROLS_DIR="/opt/tokenkey/releases/artifacts-ssm"
IMAGE_REPO="${CN_PROD_IMAGE_REPO:-ghcr.io/youxuanxue/sub2api}"
TIMEOUT_SECONDS="${CN_PROD_SSM_TIMEOUT_SECONDS:-1260}"
EXECUTION_TIMEOUT="${CN_PROD_SSM_EXECUTION_TIMEOUT:-1200}"
COMMENT_PREFIX="${CN_PROD_SSM_COMMENT:-cn-prod-deploy}"

ACTION=""
TAG=""
REVISION=""
IMAGE=""
DIGEST=""
TRANSPORT="s3"

usage() {
  sed -n '2,15p' "$0" | sed 's/^# \{0,1\}//'
  exit 1
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    pull|sync-controls|prepare)
      ACTION="$1"
      shift
      ;;
    --tag) TAG="${2:-}"; shift 2 ;;
    --revision) REVISION="${2:-}"; shift 2 ;;
    --image) IMAGE="${2:-}"; shift 2 ;;
    --transport) TRANSPORT="${2:-}"; shift 2 ;;
    --digest) DIGEST="${2:-}"; shift 2 ;;
    -h|--help) usage ;;
    *)
      echo "deploy_via_ssm: unknown argument: $1" >&2
      usage
      ;;
  esac
done

if [[ -z "${ACTION}" ]]; then
  echo "deploy_via_ssm: action required (pull|sync-controls|prepare)" >&2
  usage
fi

if [[ ! -f "${MANIFEST}" ]]; then
  echo "deploy_via_ssm: missing ${MANIFEST}" >&2
  exit 1
fi

read -r SSM_REGION SSM_INSTANCE_ID PUBLIC_ORIGIN < <(python3 - "${MANIFEST}" <<'PY'
import json, sys
with open(sys.argv[1], encoding="utf-8") as handle:
    target = json.load(handle)
print(
    str(target.get("ssm_region") or ""),
    str(target.get("ssm_instance_id") or ""),
    str(target.get("public_origin") or ""),
)
PY
)

if [[ ! "${SSM_INSTANCE_ID}" =~ ^mi-[0-9a-f]{17}$ ]]; then
  echo "deploy_via_ssm: invalid ssm_instance_id in manifest: ${SSM_INSTANCE_ID}" >&2
  exit 1
fi
if [[ ! "${SSM_REGION}" =~ ^[a-z0-9-]+$ ]]; then
  echo "deploy_via_ssm: invalid ssm_region in manifest: ${SSM_REGION}" >&2
  exit 1
fi

SSM_INFO="$(aws ssm describe-instance-information \
  --region "${SSM_REGION}" \
  --filters "Key=InstanceIds,Values=${SSM_INSTANCE_ID}" \
  --output json)"
SSM_STATE="$(printf '%s' "${SSM_INFO}" | python3 -c '
import json, sys
items = json.load(sys.stdin).get("InstanceInformationList", [])
if len(items) != 1:
    raise SystemExit("expected exactly one managed instance")
item = items[0]
print("\t".join(str(item.get(k) or "") for k in ("PingStatus", "Name", "PlatformType")))
')"
IFS=$'\t' read -r SSM_PING_STATUS SSM_NAME SSM_PLATFORM <<<"${SSM_STATE}"
if [[ "${SSM_PING_STATUS}" != "Online" || ( "${SSM_NAME}" != "cn-prod" && "${SSM_NAME}" != "tokenkey-cn-prod" ) || "${SSM_PLATFORM}" != "Linux" ]]; then
  echo "deploy_via_ssm: SSM identity not ready (name=${SSM_NAME} status=${SSM_PING_STATUS} platform=${SSM_PLATFORM})" >&2
  exit 2
fi
echo "deploy_via_ssm: target=cn-prod ssm_region=${SSM_REGION} instance_id=${SSM_INSTANCE_ID} public_origin=${PUBLIC_ORIGIN}" >&2

run_ssm() {
  local comment="$1"
  local remote_script="$2"
  local params_file stdout_file stderr_file cmd_id status deadline
  # AWS-RunShellScript starts under /bin/sh (dash on cn-prod). Invoke Bash
  # explicitly; a quoted heredoc preserves the generated script verbatim.
  remote_script="$(printf "bash <<'TOKENKEY_CN_DEPLOY_SCRIPT'\n%s\nTOKENKEY_CN_DEPLOY_SCRIPT\n" "${remote_script}")"
  if [[ "${CN_PROD_SSM_DRY_RUN:-}" == "1" ]]; then
    printf '%s\n' "${remote_script}"
    return 0
  fi
  params_file="$(mktemp)"
  stdout_file="$(mktemp)"
  stderr_file="$(mktemp)"
  # shellcheck disable=SC2064
  trap "rm -f '${params_file}' '${stdout_file}' '${stderr_file}'" RETURN

  jq -n --arg cmd "${remote_script}" --arg timeout "${EXECUTION_TIMEOUT}" \
    '{commands:[$cmd],executionTimeout:[$timeout]}' >"${params_file}"

  cmd_id="$(aws ssm send-command \
    --region "${SSM_REGION}" \
    --instance-ids "${SSM_INSTANCE_ID}" \
    --document-name AWS-RunShellScript \
    --comment "${comment}" \
    --parameters "file://${params_file}" \
    --query 'Command.CommandId' --output text)"
  echo "deploy_via_ssm: ssm command-id=${cmd_id}" >&2

  deadline=$(( $(date +%s) + TIMEOUT_SECONDS ))
  status="InProgress"
  while true; do
    status="$(aws ssm get-command-invocation \
      --region "${SSM_REGION}" \
      --command-id "${cmd_id}" \
      --instance-id "${SSM_INSTANCE_ID}" \
      --query 'Status' --output text 2>/dev/null || echo InProgress)"
    case "${status}" in Success|Failed|TimedOut|Cancelled) break ;; esac
    if [[ $(date +%s) -ge ${deadline} ]]; then
      status="TimedOut"
      break
    fi
    sleep 4
  done

  aws ssm get-command-invocation \
    --region "${SSM_REGION}" \
    --command-id "${cmd_id}" \
    --instance-id "${SSM_INSTANCE_ID}" \
    --query 'StandardOutputContent' --output text >"${stdout_file}" || true
  aws ssm get-command-invocation \
    --region "${SSM_REGION}" \
    --command-id "${cmd_id}" \
    --instance-id "${SSM_INSTANCE_ID}" \
    --query 'StandardErrorContent' --output text >"${stderr_file}" || true

  echo '--- ssm stdout (last 8KB) ---' >&2
  tail -c 8192 "${stdout_file}" >&2 || true
  echo >&2
  echo '--- ssm stderr (last 8KB) ---' >&2
  tail -c 8192 "${stderr_file}" >&2 || true
  echo >&2

  if [[ "${status}" != "Success" ]]; then
    echo "deploy_via_ssm: SSM status=${status}" >&2
    exit 1
  fi
  cat "${stdout_file}"
}

normalize_tag() {
  bash "${REPO_ROOT}/ops/stage0/normalize-deploy-tag.sh" "$1"
}

case "${ACTION}" in
  pull)
    if [[ -z "${TAG}" ]]; then
      echo "deploy_via_ssm pull: --tag required" >&2
      exit 1
    fi
    TAG="$(normalize_tag "${TAG}")"
    if [[ "${TRANSPORT}" == "s3" ]]; then
      if [[ ! "${REVISION}" =~ ^[a-f0-9]{40}$ ]]; then
        echo "deploy_via_ssm pull: S3 requires --revision FULL_SHA" >&2
        exit 1
      fi
      if [[ "${CN_PROD_SSM_DRY_RUN:-}" == "1" ]]; then
        echo "deploy_via_ssm pull: S3 dry-run refuses to print signed URLs; use unit tests" >&2
        exit 1
      fi
      REMOTE="$(python3 "${REPO_ROOT}/deploy/domestic/image_distribution.py" render \
        --tag "${TAG}" --revision "${REVISION}" --digest "${DIGEST}")"
      run_ssm "${COMMENT_PREFIX} prewarm ${TAG}" "${REMOTE}"
      exit 0
    fi
    if [[ "${TRANSPORT}" != "ghcr" ]]; then
      echo "deploy_via_ssm pull: --transport must be s3 or ghcr" >&2
      exit 1
    fi
    SOURCE="${IMAGE_REPO}:${TAG}"
    if [[ -n "${DIGEST}" ]]; then
      if [[ ! "${DIGEST}" =~ ^sha256:[a-f0-9]{64}$ ]]; then
        echo "deploy_via_ssm pull: --digest must be sha256:<64 hex>" >&2
        exit 1
      fi
      SOURCE="${IMAGE_REPO}@${DIGEST}"
    fi
    REMOTE="$(python3 - "${SOURCE}" "${TAG}" <<'PY'
import json, sys
source, tag = sys.argv[1], sys.argv[2]
print(f'''set -euo pipefail
export SOURCE={json.dumps(source)}
export EXPECT_TAG={json.dumps(tag)}
docker pull "$SOURCE"
export IMG_ID=$(docker image inspect "$SOURCE" --format '{{{{.Id}}}}')
export ARCH=$(docker image inspect "$SOURCE" --format '{{{{.Architecture}}}}')
export REV=$(docker image inspect "$SOURCE" --format '{{{{index .Config.Labels "org.opencontainers.image.revision"}}}}')
export VER=$(docker image inspect "$SOURCE" --format '{{{{index .Config.Labels "org.opencontainers.image.version"}}}}')
test "$ARCH" = amd64
test "$VER" = "$EXPECT_TAG"
test -n "$REV"
SHORT=$(printf '%s' "$REV" | cut -c1-9)
export LOCAL="tokenkey-domestic/tokenkey:${{EXPECT_TAG}}-${{SHORT}}"
docker tag "$IMG_ID" "$LOCAL"
python3 -c "import json,os; print(json.dumps({{'tag':os.environ['EXPECT_TAG'],'source':os.environ['SOURCE'],'revision':os.environ['REV'],'architecture':'amd64','image_id':os.environ['IMG_ID'],'local_tag':os.environ['LOCAL']}}, indent=2))"
''')
PY
)"
    run_ssm "${COMMENT_PREFIX} pull ${TAG}" "${REMOTE}"
    ;;

  sync-controls)
    BUNDLE="$(mktemp -d)"
    # shellcheck disable=SC2064
    trap "rm -rf '${BUNDLE}'" EXIT
    mkdir -p "${BUNDLE}/controls"
    cp \
      "${REPO_ROOT}/deploy/domestic/bluegreen.py" \
      "${REPO_ROOT}/deploy/domestic/data_layout.py" \
      "${REPO_ROOT}/deploy/domestic/post_release.py" \
      "${REPO_ROOT}/deploy/domestic/prod-target.json" \
      "${REPO_ROOT}/ops/stage0/deploy_via_ssm_bluegreen.sh" \
      "${REPO_ROOT}/scripts/release_post_check.py" \
      "${REPO_ROOT}/ops/observability/probe-post-release-tick.sh" \
      "${REPO_ROOT}/ops/lib/resolve_app_container.py" \
      "${REPO_ROOT}/ops/stage0/notify-feishu-release.sh" \
      "${BUNDLE}/controls/"
    chmod 600 "${BUNDLE}/controls/"*
    TAR_B64="$(tar -C "${BUNDLE}/controls" -czf - . | base64 | tr -d '\n')"
    REMOTE="$(python3 - "${CONTROLS_DIR}" "${TAR_B64}" <<'PY'
import sys
controls_dir, b64 = sys.argv[1], sys.argv[2]
chunk = 60000
parts = [b64[i:i + chunk] for i in range(0, len(b64), chunk)]
lines = [
    "set -euo pipefail",
    f"CONTROLS={controls_dir!r}",
    "mkdir -p \"$CONTROLS\"",
    "chmod 700 \"$CONTROLS\"",
    "rm -f /tmp/cn-prod-controls.tgz.b64",
]
for part in parts:
    lines.append(f"printf %s {part!r} >> /tmp/cn-prod-controls.tgz.b64")
lines += [
    "base64 -d /tmp/cn-prod-controls.tgz.b64 | tar -xzf - -C \"$CONTROLS\"",
    "rm -f /tmp/cn-prod-controls.tgz.b64",
    "chmod 600 \"$CONTROLS\"/*",
    "python3 - <<'END'",
    "import json, pathlib",
    f"root = pathlib.Path({controls_dir!r})",
    "files = sorted(p.name for p in root.iterdir() if p.is_file())",
    "print(json.dumps({'controls_dir': str(root), 'files': files, 'count': len(files)}, indent=2))",
    "END",
]
print("\n".join(lines))
PY
)"
    run_ssm "${COMMENT_PREFIX} sync-controls" "${REMOTE}"
    ;;

  prepare)
    if [[ -z "${TAG}" || -z "${REVISION}" || -z "${IMAGE}" ]]; then
      echo "deploy_via_ssm prepare: --tag --revision --image required" >&2
      exit 1
    fi
    TAG="$(normalize_tag "${TAG}")"
    if [[ ! "${REVISION}" =~ ^[a-f0-9]{40}$ ]]; then
      echo "deploy_via_ssm prepare: --revision must be full 40-char git SHA" >&2
      exit 1
    fi
    SHORT="$(printf '%s' "${REVISION}" | cut -c1-9)"
    EXPECTED_LOCAL="tokenkey-domestic/tokenkey:${TAG}-${SHORT}"
    if [[ "${IMAGE}" != "${EXPECTED_LOCAL}" ]]; then
      echo "deploy_via_ssm prepare: --image must be ${EXPECTED_LOCAL} (got ${IMAGE})" >&2
      exit 1
    fi
    STATE_NAME="${TAG}-${SHORT}"
    SHARED_SOURCE="${CONTROLS_DIR}/deploy_via_ssm_bluegreen.sh"
    REMOTE="$(python3 - "${CONTROLS_DIR}" "${STATE_NAME}" "${IMAGE}" "${REVISION}" "${SHARED_SOURCE}" <<'PY'
import sys
controls, state, image, revision, shared = sys.argv[1:6]
print(f'''set -euo pipefail
cd {controls!r}
test -f bluegreen.py
test -f deploy_via_ssm_bluegreen.sh
python3 bluegreen.py prepare \\
  --state /opt/tokenkey/releases/{state} \\
  --image {image!r} \\
  --revision {revision!r} \\
  --shared-source {shared!r}
''')
PY
)"
    run_ssm "${COMMENT_PREFIX} prepare ${STATE_NAME}" "${REMOTE}"
    ;;
esac
