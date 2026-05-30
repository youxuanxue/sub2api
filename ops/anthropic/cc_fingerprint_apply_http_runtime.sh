#!/usr/bin/env bash
# Post-merge: push HTTP mimicry + UA from repo baselines to all deployable edges + prod.
# No image release required — settings take effect on the next OAuth forward.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
MGR="$REPO_ROOT/ops/anthropic/manage-anthropic-config.py"
JOBDIR="${TOKENKEY_CC_RUNTIME_JOBDIR:-$REPO_ROOT/.tls_list/runtime-sync-$(date -u +%Y%m%dT%H%M%SZ)}"
mkdir -p "$JOBDIR"

cd "$REPO_ROOT"

echo "cc_fingerprint_apply_http_runtime: snapshot → sync-runtime (all deployable + prod)" >&2
python3 "$MGR" snapshot --out "$JOBDIR/snap.json"

python3 "$MGR" sync-runtime \
  --target all-deployable-and-prod \
  --snapshot "$JOBDIR/snap.json" \
  --job-dir "$JOBDIR" \
  --out "$JOBDIR/sync-runtime-report.json" \
  --json

echo "cc_fingerprint_apply_http_runtime: report=$JOBDIR/sync-runtime-report.json" >&2
