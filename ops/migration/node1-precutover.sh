#!/usr/bin/env bash
# Explicit Node1 preparation dispatcher. It never moves the public IP or cuts traffic.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
phase="${1:-}"
shift || true

case "$phase" in
  candidate)
    exec python3 "$ROOT/ops/migration/node1_candidate.py" "$@" ;;
  delta-check)
    exec python3 "$ROOT/ops/migration/check-node1-usage-delta-postgres.py" "$@" ;;
  delta-rehearsal)
    exec python3 "$ROOT/ops/migration/node1_delta_rehearsal.py" "$@" ;;
  history-rehearsal)
    exec python3 "$ROOT/ops/migration/node1_history_rehearsal.py" "$@" ;;
  history-verify)
    exec python3 "$ROOT/ops/migration/node1_history_verify.py" "$@" ;;
  usage-audit)
    exec python3 "$ROOT/ops/migration/usage_history_audit.py" "$@" ;;
  backup-rehearsal)
    exec python3 "$ROOT/ops/migration/node1_backup_rehearsal.py" "$@" ;;
  precious-backup)
    exec python3 "$ROOT/ops/migration/node1_precious_backup.py" "$@" ;;
  ops-prepare)
    exec python3 "$ROOT/ops/migration/node1_ops_prepare.py" "$@" ;;
  *)
    cat >&2 <<USAGE
usage: $0 {candidate|delta-check|delta-rehearsal|history-rehearsal|history-verify|usage-audit|backup-rehearsal|precious-backup|ops-prepare} [args...]
This dispatcher is preparation-only. It never moves 106.75.29.139 or activates traffic.
USAGE
    exit 2
    ;;
esac
