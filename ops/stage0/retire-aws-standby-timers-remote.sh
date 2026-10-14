#!/bin/bash
# Deliver with run-probe --with ops/stage0/retire_aws_standby_timers.py.
set -euo pipefail
args=(--expected-instance "${EXPECTED_STANDBY_INSTANCE_ID:?explicit AWS standby identity required}"
      --operation "${OPERATION:-plan}")
if [ -n "${BACKUP:-}" ]; then
  args+=(--backup "${BACKUP}")
fi
exec python3 "$(dirname "$0")/retire_aws_standby_timers.py" "${args[@]}"
