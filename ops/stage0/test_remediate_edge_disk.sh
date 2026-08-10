#!/usr/bin/env bash
# Behavioral regression checks for edge disk remediation and paired recovery.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SCRIPT="${ROOT}/ops/stage0/remediate-edge-disk-via-ssm.sh"
DISK="${ROOT}/deploy/aws/lightsail/tokenkey-disk-metrics-edge.sh"

text="$(cat "${SCRIPT}")"
disk="$(cat "${DISK}")"

fail=0

# Exercise the real recovery-post function with a failed curl. The caller relies
# on its status to decide whether the active/cooldown latch may be cleared.
post_fn="$(awk '
  /^tk_feishu_post_now\(\) \{/ { in_fn = 1 }
  in_fn { print }
  in_fn && /^}/ { exit }
' "${DISK}")"
if bash -c '
  WEBHOOK=https://example.test/hook
  SECRET=
  curl() { return 22; }
  export -f curl
  eval "$1"
  tk_feishu_post_now recovery
' _ "${post_fn}"; then
  echo "FAIL: rejected recovery delivery must return nonzero so the latch is retained" >&2
  fail=1
fi

alert_fn="$(awk '
  /^tk_feishu_alert\(\) \{/ { in_fn = 1 }
  in_fn { print }
  in_fn && /^}/ { exit }
' "${DISK}")"
if bash -c '
  WEBHOOK=https://example.test/hook
  SECRET=
  COOLDOWN=1800
  curl() { return 22; }
  export -f curl
  eval "$1"
  tk_feishu_alert /tmp/tokenkey-test-alert-stamp alert
' _ "${alert_fn}"; then
  echo "FAIL: rejected alert delivery must not arm the recovery latch" >&2
  fail=1
fi

state_fn="$(awk '
  /^handle_disk_state\(\) \{/ { in_fn = 1 }
  in_fn { print }
  in_fn && /^}/ { exit }
' "${DISK}")"
state_tmp="$(mktemp -d)"
touch "${state_tmp}/active" "${state_tmp}/cooldown"
if [ -z "${state_fn}" ] || ! bash -c '
  NODE=edge-test
  eval "$1"
  tk_feishu_post_now() { return 1; }
  handle_disk_state 75 85 80 "$2/active" "$2/cooldown"
  [ -f "$2/active" ] && [ -f "$2/cooldown" ]
' _ "${state_fn}" "${state_tmp}"; then
  echo "FAIL: failed recovery delivery must retain active and cooldown stamps" >&2
  fail=1
fi
if [ -n "${state_fn}" ] && ! bash -c '
  NODE=edge-test
  eval "$1"
  tk_feishu_post_now() { return 0; }
  handle_disk_state 75 85 80 "$2/active" "$2/cooldown"
  [ ! -e "$2/active" ] && [ ! -e "$2/cooldown" ]
' _ "${state_fn}" "${state_tmp}"; then
  echo "FAIL: accepted recovery delivery must clear active and cooldown stamps" >&2
  fail=1
fi
rm -rf "${state_tmp}"

# Execute the exact rendered remote body with harmless command stubs. A host
# still at 95% after every cleanup attempt must make the transport fail.
remote_cleanup="$(awk '
  /^REMOTE_CLEANUP=\$\(cat <<'\''REMOTE'\''$/ { in_body = 1; next }
  in_body && /^REMOTE$/ { exit }
  in_body { print }
' "${SCRIPT}")"
remote_cleanup="${remote_cleanup//__KEEP__/3}"
remote_cleanup="${remote_cleanup//__RECOVER__/80}"
if bash -c '
  df() {
    printf "%s\n" \
      "Filesystem 1024-blocks Used Available Capacity Mounted" \
      "/dev/root 100 95 5 95% /"
  }
  sudo() { return 1; }
  docker() { return 1; }
  export -f df sudo docker
  eval "$1"
' _ "${remote_cleanup}"; then
  echo "FAIL: remediation must fail while root usage remains above recovery threshold" >&2
  fail=1
fi

# Run the embedded SSH helper against fake aws/ssh executables and assert its
# TemporaryDirectory lifecycle removes both the private key and certificate.
tmp="$(mktemp -d)"
trap 'rm -rf "${tmp}"' EXIT
mkdir -p "${tmp}/bin" "${tmp}/work"
cat >"${tmp}/bin/aws" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' '{"accessDetails":{"privateKey":"dummy-private","certKey":"dummy-cert","username":"ec2-user","ipAddress":"127.0.0.1"}}'
EOF
cat >"${tmp}/bin/ssh" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
chmod +x "${tmp}/bin/aws" "${tmp}/bin/ssh"
ssh_python="$(awk '
  /python3 - "\$\{lightsail_region\}" "\$\{instance_name\}" "\$\{remote_cmd\}" <<'\''PY'\''/ { in_py = 1; next }
  in_py && /^PY$/ { exit }
  in_py { print }
' "${SCRIPT}")"
if ! TMPDIR="${tmp}/work" PATH="${tmp}/bin:${PATH}" \
  python3 - us-east-2 edge-test true <<<"${ssh_python}"; then
  echo "FAIL: embedded Lightsail SSH helper did not complete with fake commands" >&2
  fail=1
elif find "${tmp}/work" -mindepth 1 -print -quit | grep -q .; then
  echo "FAIL: Lightsail SSH helper left ephemeral credential files behind" >&2
  fail=1
fi

for needle in \
  'remediate-edge-disk-via-ssm.sh' \
  'get-instance-access-details' \
  'tokenkey-prune-ghcr-app-tags-core.sh' \
  'edge_ssm_execution.py' \
  'all-deployable'; do
  if ! printf '%s' "${text}" | grep -F -e "${needle}" >/dev/null; then
    echo "FAIL: remediate script missing ${needle}" >&2
    exit 1
  fi
done

for needle in \
  'DISK_ACTIVE_STAMP' \
  'tk_feishu_post_now' \
  '磁盘压力已恢复' \
  'TOKENKEY_DISK_RECOVER_THRESHOLD' \
  'tokenkey-disk-alert.stamp' \
  'legacy_has_cooldown'; do
  if ! printf '%s' "${disk}" | grep -F -e "${needle}" >/dev/null; then
    echo "FAIL: disk-metrics-edge missing recovery anchor ${needle}" >&2
    exit 1
  fi
done

if [ "${fail}" -ne 0 ]; then
  exit 1
fi

echo "test_remediate_edge_disk: ok"
