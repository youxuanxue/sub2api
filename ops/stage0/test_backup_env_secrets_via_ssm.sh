#!/usr/bin/env bash
# Tombstone check: SecureString writer is permanently retired (closeout C5).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SCRIPT="${ROOT}/ops/stage0/backup-env-secrets-via-ssm.sh"

if bash "${SCRIPT}" mi-test 2>"${TMPDIR:-/tmp}/backup-env-refuse.err"; then
  echo "expected exit 2 from retired backup-env-secrets script" >&2
  exit 1
fi
grep -q "retired (closeout C5)" "${TMPDIR:-/tmp}/backup-env-refuse.err"

# Break-glass write flag must also refuse — PutParameter path is gone.
if TOKENKEY_ALLOW_SECURESTRING_WRITE=1 bash "${SCRIPT}" mi-test 2>"${TMPDIR:-/tmp}/backup-env-refuse2.err"; then
  echo "expected exit 2 even with TOKENKEY_ALLOW_SECURESTRING_WRITE=1" >&2
  exit 1
fi
grep -q "retired (closeout C5)" "${TMPDIR:-/tmp}/backup-env-refuse2.err"

echo "test_backup_env_secrets_via_ssm: ok"
