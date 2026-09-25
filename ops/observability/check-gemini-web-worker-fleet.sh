#!/usr/bin/env bash
# check-gemini-web-worker-fleet.sh — read-only Gemini Web Worker fleet convergence.
#
# The Worker runs on deployable edges only; prod does not run it, so prod is not
# part of the denominator and its absence is never drift.
#
# Answers one question a release operator cannot otherwise answer without
# SSH-ing into every edge: do all Worker hosts run the same build, and which?
# Identity comes from each Worker's own /readyz (a digest over its shipped
# sources), so a hand-built image reusing someone else's tag is still detected.
#
# Checker-style exit codes, matching check-account-group-bindings.sh:
#   0 converged — every edge ready on one shared build digest
#   1 review    — digests disagree, an edge is not ready, or identity missing
#   2 setup/transport failure
#
# Usage:
#   bash ops/observability/check-gemini-web-worker-fleet.sh
#   EDGE_IDS=us3,us5 bash ops/observability/check-gemini-web-worker-fleet.sh
set -euo pipefail

if [ "${1:-}" = "-h" ] || [ "${1:-}" = "--help" ]; then
  sed -n '2,19p' "$0" | sed 's/^# \{0,1\}//'
  exit 0
fi
if [ "$#" -gt 0 ]; then
  echo "check-gemini-web-worker-fleet: unknown arg: $1" >&2
  exit 2
fi

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

RUN_PROBE="${GEMINI_WEB_WORKER_RUN_PROBE:-$ROOT/ops/observability/run-probe.sh}"
PROBE_SCRIPT="$ROOT/ops/observability/probe-gemini-web-worker-build.sh"
TIMEOUT_SECONDS="${TIMEOUT_SECONDS:-120}"

EDGES=()
if [ -n "${EDGE_IDS:-}" ]; then
  IFS=',' read -r -a EDGES <<< "$EDGE_IDS"
else
  # Portable across bash 3.2 (macOS operator laptops) — no mapfile.
  while IFS= read -r line; do
    [ -n "$line" ] && EDGES+=("$line")
  done < <(python3 deploy/aws/stage0/resolve-edge-target.py --list-deployable)
fi
if [ "${#EDGES[@]}" -eq 0 ]; then
  echo '{"schema_version":1,"verdict":"setup_error","error":"no_deployable_edges"}'
  exit 2
fi

REPORT_DIR="$(mktemp -d /tmp/tk-gemini-web-fleet.XXXXXX)"
trap 'rm -rf "$REPORT_DIR"' EXIT

for edge in "${EDGES[@]}"; do
  [ -n "$edge" ] || continue
  if ! "$RUN_PROBE" --target "edge:$edge" --script "$PROBE_SCRIPT" \
      --timeout-seconds "$TIMEOUT_SECONDS" >"$REPORT_DIR/$edge.json" 2>"$REPORT_DIR/$edge.err"; then
    : # A probe verdict of review/setup_error exits non-zero by design; classify below.
  fi
done

python3 - "$REPORT_DIR" "${EDGES[@]}" <<'PY'
import json
import pathlib
import sys

report_dir = pathlib.Path(sys.argv[1])
edges = [e for e in sys.argv[2:] if e]
observed, findings = [], []

for edge in edges:
    path = report_dir / f'{edge}.json'
    record = None
    if path.exists():
        for line in reversed(path.read_text().splitlines()):
            line = line.strip()
            if line.startswith('{'):
                try:
                    record = json.loads(line)
                except ValueError:
                    record = None
                if record is not None:
                    break
    if record is None:
        findings.append({'edge_id': edge, 'code': 'probe_unreadable'})
        observed.append({'edge_id': edge, 'verdict': 'setup_error'})
        continue
    entry = {
        'edge_id': edge,
        'verdict': record.get('verdict'),
        'image': record.get('image'),
        'status': record.get('status'),
        'build_digest': record.get('build_digest') or None,
        'control_protocol_version': record.get('control_protocol_version'),
    }
    observed.append(entry)
    if not entry['build_digest']:
        # A Worker that serves traffic but reports no identity predates identity
        # reporting; that is a rollout gap, not an unhealthy process.
        findings.append({'edge_id': edge, 'code': 'identity_missing',
                         'status': entry['status'], 'image': entry['image']})
    elif entry['verdict'] != 'ok':
        findings.append({'edge_id': edge, 'code': 'worker_not_ready',
                         'verdict': entry['verdict'], 'status': entry['status']})

digests = sorted({e['build_digest'] for e in observed if e.get('build_digest')})
protocols = sorted({e['control_protocol_version'] for e in observed
                    if e.get('control_protocol_version') is not None})
if len(digests) > 1:
    findings.append({'code': 'build_digest_divergence', 'digests': digests})
if len(protocols) > 1:
    findings.append({'code': 'control_protocol_divergence', 'versions': protocols})

# Tags are operator claims; a shared digest under differing tags is still converged.
tags = sorted({e['image'] for e in observed if e.get('image')})
transport_failed = any(e.get('verdict') == 'setup_error' for e in observed)
verdict = 'converged' if not findings else ('setup_error' if transport_failed and len(findings) == sum(
    1 for e in observed if e.get('verdict') == 'setup_error') else 'review')

print(json.dumps({
    'schema_version': 1,
    'verdict': verdict,
    'basis': 'worker_reported_build_digest_over_deployable_edges',
    'summary': {
        'edge_count': len(edges),
        # Serving and identified are different facts: an unidentified Worker may
        # still be serving traffic normally.
        'serving_count': sum(1 for e in observed if e.get('status') == 'ready'),
        'identified_count': sum(1 for e in observed if e.get('build_digest')),
        'distinct_build_digests': len(digests),
        'distinct_image_tags': len(tags),
        'prod_runs_worker': False,
    },
    'build_digests': digests,
    'image_tags': tags,
    'observed': observed,
    'findings': findings,
}, ensure_ascii=False))

sys.exit(0 if verdict == 'converged' else (2 if verdict == 'setup_error' else 1))
PY
