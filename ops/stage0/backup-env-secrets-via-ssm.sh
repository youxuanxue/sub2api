#!/usr/bin/env bash
#
# TOMBSTONE (closeout C3/C5): SecureString ops-key writes are retired.
# SSOT: host root-only `.env` + 腾讯 COS + `tencent-ops` secrets.
# Historical PutParameter path removed 2026-11-18 after C5 parameter deletion.
set -euo pipefail

cat >&2 <<'EOF'
backup_env_secrets_via_ssm: retired (closeout C5).
Ops secrets are not written to AWS SSM SecureString.
SSOT: host root-only .env + 腾讯 COS + tencent-ops secrets.
C5 delete owner: ops/stage0/delete-legacy-securestring-params.sh
EOF
exit 2
