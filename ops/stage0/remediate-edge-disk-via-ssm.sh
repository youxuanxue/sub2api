#!/usr/bin/env bash
# remediate-edge-disk-via-ssm.sh — emergency root-disk cleanup on Stage0 Lightsail edges.
#
# Frees space from stale GHCR tags, dangling Docker images, oversized container logs,
# journald, and QA stale rows/files. SSM is the default transport; when the host is
# full enough that SSM cannot spawn a shell (classic 100% root), falls back to
# Lightsail ephemeral SSH certificates (get-instance-access-details).
#
# After cleanup, the on-box tokenkey-disk-metrics timer will post a paired ✅ recovery
# Feishu once usage drops below TOKENKEY_DISK_RECOVER_THRESHOLD (see
# deploy/aws/lightsail/tokenkey-disk-metrics-edge.sh).
#
# Usage:
#   bash ops/stage0/remediate-edge-disk-via-ssm.sh --edge-id us3
#   bash ops/stage0/remediate-edge-disk-via-ssm.sh --edges us3,us4,us6
#   bash ops/stage0/remediate-edge-disk-via-ssm.sh --all-deployable
#   TOKENKEY_GHCR_KEEP_TAGS=2 bash ops/stage0/remediate-edge-disk-via-ssm.sh --edge-id us3
#
# Options:
#   --edge-id <id>       one deployable edge
#   --edges <csv>        comma-separated subset
#   --all-deployable     every edge with deployable=true in edge-targets-lightsail.json
#   --ssm-only           do not attempt Lightsail SSH fallback
#   --dry-run            resolve and print targets, no AWS mutations
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${HERE}/../.." && pwd)"
RESOLVE_EXEC="${HERE}/edge_ssm_execution.py"
RESOLVE_MATRIX="${REPO_ROOT}/deploy/aws/stage0/resolve-edge-target.py"
LIGHTSAIL_MATRIX="${REPO_ROOT}/deploy/aws/lightsail/edge-targets-lightsail.json"

GHCR_KEEP_TAGS="${TOKENKEY_GHCR_KEEP_TAGS:-3}"
SSM_TIMEOUT="${STAGE0_SSM_TIMEOUT_SECONDS:-300}"
RECOVER_THRESHOLD="${TOKENKEY_DISK_RECOVER_THRESHOLD:-80}"
SSM_ONLY=0
DRY_RUN=0
declare -a EDGE_IDS=()

usage() {
  sed -n '2,22p' "$0" | sed 's/^# \{0,1\}//'
}

while [ $# -gt 0 ]; do
  case "$1" in
    -h|--help) usage; exit 0 ;;
    --edge-id) EDGE_IDS+=("${2:-}"); shift 2 ;;
    --edges)
      IFS=',' read -r -a _csv <<< "${2:-}"
      for _e in "${_csv[@]}"; do
        [ -n "${_e}" ] && EDGE_IDS+=("${_e}")
      done
      shift 2
      ;;
    --all-deployable)
      while IFS= read -r _edge; do
        [ -n "${_edge}" ] && EDGE_IDS+=("${_edge}")
      done < <(python3 "${RESOLVE_MATRIX}" --list-deployable)
      shift
      ;;
    --ssm-only) SSM_ONLY=1; shift ;;
    --dry-run) DRY_RUN=1; shift ;;
    *) echo "remediate-edge-disk: unknown arg '$1'" >&2; exit 1 ;;
  esac
done

if [ "${#EDGE_IDS[@]}" -eq 0 ]; then
  echo "remediate-edge-disk: specify --edge-id, --edges, or --all-deployable" >&2
  usage >&2
  exit 1
fi

if ! [[ "${GHCR_KEEP_TAGS}" =~ ^[1-9][0-9]*$ ]]; then
  echo "remediate-edge-disk: invalid TOKENKEY_GHCR_KEEP_TAGS=${GHCR_KEEP_TAGS}" >&2
  exit 1
fi
if ! [[ "${SSM_TIMEOUT}" =~ ^[1-9][0-9]*$ ]]; then
  echo "remediate-edge-disk: invalid STAGE0_SSM_TIMEOUT_SECONDS=${SSM_TIMEOUT}" >&2
  exit 1
fi
if ! [[ "${RECOVER_THRESHOLD}" =~ ^[1-9][0-9]*$ ]] || [ "${RECOVER_THRESHOLD}" -gt 99 ]; then
  echo "remediate-edge-disk: invalid TOKENKEY_DISK_RECOVER_THRESHOLD=${RECOVER_THRESHOLD}" >&2
  exit 1
fi

REMOTE_CLEANUP=$(cat <<'REMOTE'
set -u
cleanup_failures=0
disk_used_percent() {
  df -P / 2>/dev/null | awk 'NR==2 {gsub(/%/,"",$5); print $5}'
}
echo "=== df before ==="
if ! df -h /; then echo "WARN: df before failed" >&2; fi
PRUNE=/usr/local/bin/tokenkey-prune-ghcr-app-tags-core.sh
[ -x "$PRUNE" ] || PRUNE=/usr/local/bin/tokenkey-prune-ghcr-app-tags.sh
if [ -x "$PRUNE" ]; then
  if ! sudo env TOKENKEY_GHCR_KEEP_TAGS="__KEEP__" "$PRUNE"; then
    echo "WARN: GHCR tag prune failed" >&2
    cleanup_failures=$((cleanup_failures + 1))
  fi
else
  echo "WARN: no GHCR tag prune script installed" >&2
  cleanup_failures=$((cleanup_failures + 1))
fi
if ! sudo docker image prune -af; then
  echo "WARN: docker image prune failed" >&2
  cleanup_failures=$((cleanup_failures + 1))
fi
if ! sudo find /var/lib/docker/containers -name '*-json.log' -size +10M -exec truncate -s 0 {} + 2>/dev/null; then
  echo "WARN: container log truncation failed" >&2
  cleanup_failures=$((cleanup_failures + 1))
fi
if ! sudo journalctl --vacuum-size=100M 2>/dev/null; then
  echo "WARN: journal vacuum failed" >&2
  cleanup_failures=$((cleanup_failures + 1))
fi
# TRANSITIONAL / NON-SSOT: Phase 1 removes this QA action with Edge QA wiring.
if [ -x /usr/local/bin/tokenkey-qa-stale-cleanup.sh ]; then
  if ! sudo /usr/local/bin/tokenkey-qa-stale-cleanup.sh; then
    echo "WARN: QA stale cleanup failed" >&2
    cleanup_failures=$((cleanup_failures + 1))
  fi
fi
echo "=== df after ==="
if ! df -h /; then echo "WARN: df after failed" >&2; fi
if ! docker system df 2>/dev/null; then echo "WARN: docker system df failed" >&2; fi
used_after="$(disk_used_percent)"
case "${used_after}" in
  ''|*[!0-9]*) echo "FAIL: could not read final root usage" >&2; exit 1 ;;
esac
if [ "${used_after}" -ge "__RECOVER__" ]; then
  echo "FAIL: root usage remains ${used_after}% (must be below __RECOVER__%)" >&2
  exit 1
fi
if [ "${cleanup_failures}" -gt 0 ]; then
  echo "WARN: ${cleanup_failures} cleanup stage(s) failed, but root usage recovered to ${used_after}%" >&2
fi
REMOTE
)
REMOTE_CLEANUP="${REMOTE_CLEANUP/__KEEP__/${GHCR_KEEP_TAGS}}"
REMOTE_CLEANUP="${REMOTE_CLEANUP//__RECOVER__/${RECOVER_THRESHOLD}}"

lightsail_ssh_run() {
  local lightsail_region="$1"
  local instance_name="$2"
  local remote_cmd="$3"
  python3 - "${lightsail_region}" "${instance_name}" "${remote_cmd}" <<'PY'
import json, os, subprocess, sys, tempfile

region, instance_name, remote = sys.argv[1:4]
proc = subprocess.run(
    [
        "aws", "lightsail", "get-instance-access-details",
        "--region", region,
        "--instance-name", instance_name,
        "--protocol", "ssh",
        "--output", "json",
    ],
    capture_output=True,
    text=True,
    check=True,
)
access = json.loads(proc.stdout)["accessDetails"]
with tempfile.TemporaryDirectory(prefix="tk-edge-ssh-") as td:
    key_path = os.path.join(td, "temp_key.pem")
    cert_path = key_path + "-cert.pub"
    known_hosts_path = os.path.join(td, "known_hosts")
    with open(key_path, "w", encoding="utf-8") as fh:
        fh.write(access["privateKey"].strip() + "\n")
    with open(cert_path, "w", encoding="utf-8") as fh:
        fh.write(access["certKey"].strip() + "\n")
    os.chmod(key_path, 0o600)
    os.chmod(cert_path, 0o600)
    target = f"{access['username']}@{access['ipAddress']}"
    cmd = [
        "ssh",
        "-o", "StrictHostKeyChecking=accept-new",
        "-o", f"UserKnownHostsFile={known_hosts_path}",
        "-o", "ConnectTimeout=60",
        "-o", "ServerAliveInterval=15",
        "-i", key_path,
        target,
        remote,
    ]
    run = subprocess.run(cmd, capture_output=True, text=True, timeout=900)
    sys.stdout.write(run.stdout)
    if run.stderr:
        sys.stderr.write(run.stderr)
    raise SystemExit(run.returncode)
PY
}

ssm_cleanup() {
  local region="$1"
  local instance_id="$2"
  local edge_id="$3"
  local params_file
  params_file="$(mktemp /tmp/tk-edge-disk-ssm.XXXXXX.json)"
  jq -n --arg remote "${REMOTE_CLEANUP}" --arg timeout "${SSM_TIMEOUT}" '{
    commands: ($remote | split("\n")),
    executionTimeout: [$timeout]
  }' > "${params_file}"
  local cmd_id
  cmd_id="$(aws ssm send-command \
    --region "${region}" \
    --instance-ids "${instance_id}" \
    --document-name AWS-RunShellScript \
    --comment "remediate-edge-disk-${edge_id}" \
    --timeout-seconds "${SSM_TIMEOUT}" \
    --parameters "file://${params_file}" \
    --query 'Command.CommandId' --output text)"
  rm -f "${params_file}"
  echo "  ssm command_id=${cmd_id}" >&2
  local deadline=$(( $(date +%s) + SSM_TIMEOUT ))
  local status="InProgress"
  while [ "$(date +%s)" -lt "${deadline}" ]; do
    status="$(aws ssm get-command-invocation \
      --region "${region}" \
      --command-id "${cmd_id}" \
      --instance-id "${instance_id}" \
      --query 'Status' \
      --output text 2>/dev/null || echo InProgress)"
    case "${status}" in Success|Failed|Cancelled|TimedOut) break ;; esac
    sleep 5
  done
  aws ssm get-command-invocation \
    --region "${region}" \
    --command-id "${cmd_id}" \
    --instance-id "${instance_id}" \
    --query 'StandardOutputContent' \
    --output text
  local rc=0
  [ "${status}" = "Success" ] || rc=1
  echo "  ssm status=${status}" >&2
  return "${rc}"
}

resolve_lightsail() {
  local edge_id="$1"
  python3 - "${edge_id}" "${LIGHTSAIL_MATRIX}" <<'PY'
import json, sys
edge_id, matrix_path = sys.argv[1:3]
data = json.load(open(matrix_path, encoding="utf-8"))
target = (data.get("targets") or {}).get(edge_id) or {}
if not target:
    raise SystemExit(f"missing edge target {edge_id}")
print(target["lightsail_region"])
print(target["instance_name"])
PY
}

failures=0
for edge_id in "${EDGE_IDS[@]}"; do
  echo "=== remediate-edge-disk: ${edge_id} ==="
  identity="$(python3 "${RESOLVE_EXEC}" --edge-id "${edge_id}" --format json)"
  region="$(printf '%s' "${identity}" | python3 -c 'import json,sys; print(json.load(sys.stdin)["region"])')"
  instance_id="$(printf '%s' "${identity}" | python3 -c 'import json,sys; print(json.load(sys.stdin)["instance_id"])')"
  domain="$(printf '%s' "${identity}" | python3 -c 'import json,sys; print(json.load(sys.stdin)["domain"])')"
  echo "  target=${domain} region=${region} instance=${instance_id}"

  if [ "${DRY_RUN}" = "1" ]; then
    echo "  dry-run: would run remote cleanup via SSM (SSH fallback if SSM fails)"
    continue
  fi

  if ssm_cleanup "${region}" "${instance_id}" "${edge_id}"; then
    continue
  fi

  if [ "${SSM_ONLY}" = "1" ]; then
    echo "  FAIL: SSM cleanup failed and --ssm-only set" >&2
    failures=$((failures + 1))
    continue
  fi

  _ls_out="$(resolve_lightsail "${edge_id}")"
  lightsail_region="$(printf '%s\n' "${_ls_out}" | sed -n '1p')"
  instance_name="$(printf '%s\n' "${_ls_out}" | sed -n '2p')"
  if [ -z "${lightsail_region}" ] || [ -z "${instance_name}" ]; then
    echo "  FAIL: could not resolve Lightsail target for ${edge_id}" >&2
    failures=$((failures + 1))
    continue
  fi
  echo "  SSM failed — trying Lightsail SSH (${instance_name} @ ${lightsail_region})" >&2
  if lightsail_ssh_run "${lightsail_region}" "${instance_name}" "${REMOTE_CLEANUP}"; then
    echo "  ok: SSH cleanup succeeded" >&2
  else
    echo "  FAIL: SSH cleanup failed" >&2
    failures=$((failures + 1))
  fi
done

if [ "${failures}" -gt 0 ]; then
  echo "remediate-edge-disk: ${failures} edge(s) failed" >&2
  exit 1
fi

echo "remediate-edge-disk: ok (${#EDGE_IDS[@]} edge(s))"
