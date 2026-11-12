#!/usr/bin/env bash
# Persist the host SSH posture used on prod and cn-prod.
# Cloud-init writes PasswordAuthentication yes in 50-cloud-init.conf, and sshd
# keeps the first value it sees. This drop-in sorts first, so a reinstall does
# not turn the host back into a password-reachable jump host.
set -euo pipefail

dropin="${TOKENKEY_SSHD_DROPIN:-/etc/ssh/sshd_config.d/00-tokenkey-lockdown.conf}"
install -d -m 0755 "$(dirname "$dropin")"
cat > "$dropin" << 'EOF'
PasswordAuthentication no
KbdInteractiveAuthentication no
PermitRootLogin no
AllowTcpForwarding no
EOF
chmod 0644 "$dropin"

if [[ "${TOKENKEY_SSH_LOCK_DRY:-}" == 1 ]]; then
  exit 0
fi

[[ $EUID -eq 0 ]] || { echo "need root" >&2; exit 1; }
if command -v sshd >/dev/null 2>&1; then
  sshd -t
fi
if systemctl is-active --quiet ssh 2>/dev/null; then
  systemctl reload ssh
elif systemctl is-active --quiet sshd 2>/dev/null; then
  systemctl reload sshd
fi
passwd -l root >/dev/null
