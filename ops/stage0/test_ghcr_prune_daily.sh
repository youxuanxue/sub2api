#!/usr/bin/env bash
# Behavioral and generated-artifact checks for daily GHCR prune timer rollout.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
DAILY="${ROOT}/deploy/aws/stage0/tokenkey-ghcr-prune-daily.sh"
BOOTSTRAP="${ROOT}/deploy/aws/stage0/stage0-ec2-bootstrap.sh"
EDGE_SYNC="${ROOT}/ops/stage0/sync-edge-host-units-via-ssm.sh"
PROD_SYNC="${ROOT}/ops/stage0/sync-ghcr-prune-timer-via-ssm.sh"
CFN="${ROOT}/deploy/aws/cloudformation/stage0-single-ec2.yaml"

fail=0

# Exported functions shadow host commands in the child bash, so this test never
# reaches the developer machine's Docker daemon. Both missing tag-prune logic
# and a failed dangling-image prune must propagate to systemd as nonzero.
docker() { return 1; }
logger() { :; }
export -f docker logger
if bash "${DAILY}"; then
  echo "FAIL: daily prune returned success after cleanup dependencies failed" >&2
  fail=1
fi

if TOKENKEY_GHCR_KEEP_TAGS='3;false' bash "${DAILY}" --selftest; then
  echo "FAIL: daily prune accepted a non-integer TOKENKEY_GHCR_KEEP_TAGS" >&2
  fail=1
fi

tmp="$(mktemp -d)"
trap 'rm -rf "${tmp}"' EXIT
if ! bash -c 'source "$1"; install_ghcr_prune_daily_units "$2"' _ "${DAILY}" "${tmp}"; then
  echo "FAIL: canonical daily script cannot install its systemd units" >&2
  fail=1
else
  if ! grep -q '^ExecStart=/usr/local/bin/tokenkey-ghcr-prune-daily.sh$' "${tmp}/tokenkey-ghcr-prune-daily.service"; then
    echo "FAIL: installed service does not execute the canonical daily script" >&2
    fail=1
  fi
  if ! grep -q '^OnCalendar=\*-\*-\* 05:00:00$' "${tmp}/tokenkey-ghcr-prune-daily.timer"; then
    echo "FAIL: installed timer does not use the expected daily schedule" >&2
    fail=1
  fi
fi

cat >"${tmp}/prune-ok" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
chmod +x "${tmp}/prune-ok"
if ! bash -c '
  docker() { return 0; }
  logger() { :; }
  export -f docker logger
  source "$1"
  run_ghcr_prune_daily "$2"
' _ "${DAILY}" "${tmp}/prune-ok"; then
  echo "FAIL: daily prune success path did not execute the selected prune implementation" >&2
  fail=1
fi

for consumer in "${BOOTSTRAP}" "${EDGE_SYNC}" "${PROD_SYNC}"; do
  if ! grep -q -- '--install-units' "${consumer}"; then
    echo "FAIL: ${consumer} does not delegate unit rendering to the canonical installer" >&2
    fail=1
  fi
done

daily_b64="$(awk -v q="'" '
  />>> GHCR_PRUNE_DAILY_B64_PARAM START/ { in_marker = 1; next }
  />>> GHCR_PRUNE_DAILY_B64_PARAM END/ { in_marker = 0 }
  in_marker && /Value:/ {
    value = $0
    sub("^[[:space:]]*Value:[[:space:]]*" q, "", value)
    sub(q "[[:space:]]*$", "", value)
    print value
    exit
  }
' "${CFN}")"
if [ -z "${daily_b64}" ] || ! printf '%s' "${daily_b64}" | base64 -d 2>/dev/null | cmp -s - "${DAILY}"; then
  echo "FAIL: CloudFormation daily-prune artifact is missing or differs from its source" >&2
  fail=1
fi

if ! python3 "${ROOT}/scripts/checks/check-ghcr-prune-workflow.py"; then
  fail=1
fi

if [ "${fail}" -ne 0 ]; then
  exit 1
fi

echo "test_ghcr_prune_daily: ok"
