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
#   1 review    — digests disagree, an edge is not ready or has no Worker at all,
#                 or identity is missing
#   2 setup/transport failure — no edge could be reached, so there is nothing to
#                 say about the fleet. An edge that answers "no container" is a
#                 fleet finding (worker_absent), not a failure to observe.
#
# Usage:
#   bash ops/observability/check-gemini-web-worker-fleet.sh
#   EDGE_IDS=us3,us5 bash ops/observability/check-gemini-web-worker-fleet.sh
#   bash ops/observability/check-gemini-web-worker-fleet.sh --selftest
set -euo pipefail

if [ "${1:-}" = "-h" ] || [ "${1:-}" = "--help" ]; then
  # Print the whole header comment rather than a fixed line range: a range silently
  # truncated this help the moment the exit-code section grew.
  sed -n '2,/^set -euo pipefail$/p' "$0" | sed '$d' | sed 's/^# \{0,1\}//'
  exit 0
fi
SELFTEST=0
if [ "${1:-}" = "--selftest" ]; then
  SELFTEST=1
  shift
fi
if [ "$#" -gt 0 ]; then
  echo "check-gemini-web-worker-fleet: unknown arg: $1" >&2
  exit 2
fi

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

if [ "$SELFTEST" = 1 ]; then
  # Fixtures over the verdict boundary, which is the whole value of this checker:
  # "no Worker on this host" must never be reported as "the check could not run".
  # Runs with a fake probe, so it needs no AWS account and no edge.
  work="$(mktemp -d /tmp/tk-gemini-web-fleet-selftest.XXXXXX)"
  trap 'rm -rf "$work"' EXIT
  cat >"$work/fake-probe.sh" <<'FAKE'
#!/usr/bin/env bash
target=""
for ((i=1;i<=$#;i++)); do
  if [ "${!i}" = "--target" ]; then j=$((i+1)); target="${!j}"; fi
done
edge="${target#edge:}"
case ",${ABSENT_EDGES:-}," in *",$edge,"*)
  echo '{"schema_version":1,"verdict":"setup_error","container":"tokenkey-gemini-web","image":"","running":false,"health":"absent","status":"","build_digest":"","control_protocol_version":null}'
  exit 2 ;;
esac
case ",${UNREACHABLE_EDGES:-}," in *",$edge,"*)
  echo "fake-probe: delivery failed for ${edge}" >&2
  exit 2 ;;
esac
case ",${STALE_EDGES:-}," in *",$edge,"*)
  echo '{"schema_version":1,"verdict":"ok","container":"tokenkey-gemini-web","image":"tokenkey-gemini-web:1.8.253-147512b9d","running":true,"health":"healthy","status":"ready","build_digest":"aaaaaaaaaaaa","control_protocol_version":1}'
  exit 0 ;;
esac
case ",${OLD_PROTOCOL_EDGES:-}," in *",$edge,"*)
  echo '{"schema_version":1,"verdict":"ok","container":"tokenkey-gemini-web","image":"ghcr.io/x:bbbbbbbbbbbb","running":true,"health":"healthy","status":"ready","build_digest":"bbbbbbbbbbbb","control_protocol_version":0}'
  exit 0 ;;
esac
case ",${UNIDENTIFIED_EDGES:-}," in *",$edge,"*)
  echo '{"schema_version":1,"verdict":"review","container":"tokenkey-gemini-web","image":"tokenkey-gemini-web:legacy","running":true,"health":"healthy","status":"ready","build_digest":"","control_protocol_version":null}'
  exit 1 ;;
esac
echo '{"schema_version":1,"verdict":"ok","container":"tokenkey-gemini-web","image":"ghcr.io/x:bbbbbbbbbbbb","running":true,"health":"healthy","status":"ready","build_digest":"bbbbbbbbbbbb","control_protocol_version":1}'
FAKE
  chmod +x "$work/fake-probe.sh"

  selftest_failures=0
  expect() {
    # expect <label> <want-exit> <want-verdict> <want-findings-csv> [ENV=V ...]
    local label="$1" want_exit="$2" want_verdict="$3" want_findings="$4"
    shift 4
    local out code
    # A review verdict exits 1 by design, which under `set -e` would abort the
    # selftest before it could compare anything.
    set +e
    out="$(env "$@" EDGE_IDS=us3,us4,us5,us6 \
      GEMINI_WEB_WORKER_RUN_PROBE="$work/fake-probe.sh" \
      bash "$ROOT/ops/observability/check-gemini-web-worker-fleet.sh" 2>/dev/null)"
    code=$?
    set -e
    local got
    got="$(printf '%s' "$out" | python3 -c 'import json,sys
d = json.load(sys.stdin)
print(d["verdict"], ",".join(sorted(f["code"] for f in d["findings"])) or "-")')"
    if [ "$code" != "$want_exit" ] || [ "$got" != "$want_verdict ${want_findings}" ]; then
      echo "FAIL ${label}: exit=${code} got='${got}' want exit=${want_exit} '${want_verdict} ${want_findings}'" >&2
      selftest_failures=$((selftest_failures + 1))
    else
      echo "ok   ${label}"
    fi
  }

  expect "converged fleet" 0 converged - NOTHING=1
  # The finding this checker exists to surface, and the bug this selftest pins:
  # a host answering "no container" is the worst fleet state, not an unobservable
  # one. Reported as setup_error it read as "tooling broke" and was ignored.
  expect "absent worker is a review finding" 1 review worker_absent ABSENT_EDGES=us4
  expect "unreachable host is a finding too" 1 review probe_unreadable UNREACHABLE_EDGES=us4
  expect "absent beside unreachable" 1 review probe_unreadable,worker_absent \
    ABSENT_EDGES=us4 UNREACHABLE_EDGES=us6
  # Only a fleet nobody could observe is a setup_error.
  expect "no edge reachable is setup_error" 2 setup_error \
    probe_unreadable,probe_unreadable,probe_unreadable,probe_unreadable \
    UNREACHABLE_EDGES=us3,us4,us5,us6
  expect "every host lost its worker" 1 review \
    worker_absent,worker_absent,worker_absent,worker_absent \
    ABSENT_EDGES=us3,us4,us5,us6
  expect "digest divergence" 1 review build_digest_divergence STALE_EDGES=us4
  # An unreachable host must never mask a real finding behind exit 2.
  expect "divergence survives an unreachable host" 1 review \
    build_digest_divergence,probe_unreadable UNREACHABLE_EDGES=us6 STALE_EDGES=us4
  expect "unidentified worker is a rollout gap" 1 review identity_missing UNIDENTIFIED_EDGES=us4
  # A fleet that agrees with itself but not with the backend is the half-converged
  # state a paired protocol release must never be left in. The parity gate cannot
  # see this: it passes the moment both sides are bumped in one commit, which is
  # before any edge has been deployed.
  expect "whole fleet behind the backend" 1 review control_protocol_behind_backend \
    OLD_PROTOCOL_EDGES=us3,us4,us5,us6
  expect "one edge behind the backend" 1 review \
    control_protocol_behind_backend,control_protocol_divergence OLD_PROTOCOL_EDGES=us4

  if [ "$selftest_failures" -ne 0 ]; then
    echo "check-gemini-web-worker-fleet selftest: FAIL (${selftest_failures})" >&2
    exit 1
  fi
  echo "check-gemini-web-worker-fleet selftest: PASS"
  exit 0
fi

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

# The backend's constant is the version the fleet must speak. A paired release
# bumps both sides in one commit, so the parity gate passes while no edge has been
# deployed yet — and a uniformly stale fleet looks converged to a check that only
# compares Workers with each other.
EXPECTED_PROTOCOL="$(sed -n 's/^const GeminiWebControlProtocolVersion = \([0-9]\{1,\}\)$/\1/p' \
  "$ROOT/backend/internal/handler/gemini_web_session_handler.go" | head -1)"
if [ -z "$EXPECTED_PROTOCOL" ]; then
  echo '{"schema_version":1,"verdict":"setup_error","error":"backend_protocol_constant_unreadable"}'
  exit 2
fi

python3 - "$REPORT_DIR" "$EXPECTED_PROTOCOL" "${EDGES[@]}" <<'PY'
import json
import pathlib
import sys

report_dir = pathlib.Path(sys.argv[1])
expected_protocol = int(sys.argv[2])
edges = [e for e in sys.argv[3:] if e]
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
        # Nothing parseable came back: the probe never reached this host, so the
        # check has no opinion about it. This is the only kind of setup_error.
        findings.append({'edge_id': edge, 'code': 'probe_unreadable'})
        observed.append({'edge_id': edge, 'verdict': 'setup_error', 'reached': False})
        continue
    entry = {
        'edge_id': edge,
        'verdict': record.get('verdict'),
        'image': record.get('image'),
        'status': record.get('status'),
        'build_digest': record.get('build_digest') or None,
        'control_protocol_version': record.get('control_protocol_version'),
        # The host answered, so the check does have an opinion about it, whatever
        # that answer was.
        'reached': True,
    }
    observed.append(entry)
    if record.get('health') == 'absent' or not record.get('container'):
        # The host answered that it has no Worker container at all. That is the
        # worst fleet state there is, not a failure to observe: a failed deploy
        # whose restore also failed leaves exactly this. Reporting it as
        # setup_error told an operator "the check could not run" and hid it.
        findings.append({'edge_id': edge, 'code': 'worker_absent',
                         'image': entry['image']})
    elif not entry['build_digest']:
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

# Agreeing with each other is not enough: the fleet must agree with the backend it
# calls. A Worker refuses a control response whose version it does not recognise,
# so a stale fleet takes itself out of service — exactly the half-converged state a
# paired release must not be left in.
behind = sorted({v for v in protocols if v != expected_protocol})
if behind:
    findings.append({'code': 'control_protocol_behind_backend',
                     'backend_version': expected_protocol,
                     'worker_versions': behind})

# Tags are operator claims; a shared digest under differing tags is still converged.
tags = sorted({e['image'] for e in observed if e.get('image')})

# setup_error means "this check could not observe the fleet", so it requires that
# no edge was reached at all. If even one edge answered, the findings are facts
# about the fleet and the verdict must be review — otherwise an unreachable host
# could mask a real problem behind an exit code operators read as "tooling broke".
reached = [e for e in observed if e.get('reached')]
if not findings:
    verdict = 'converged'
elif not reached:
    verdict = 'setup_error'
else:
    verdict = 'review'

print(json.dumps({
    'schema_version': 1,
    'verdict': verdict,
    'basis': 'worker_reported_build_digest_over_deployable_edges',
    'summary': {
        'edge_count': len(edges),
        # Serving and identified are different facts: an unidentified Worker may
        # still be serving traffic normally.
        'serving_count': sum(1 for e in observed if e.get('status') == 'ready'),
        'reached_count': len(reached),
        'identified_count': sum(1 for e in observed if e.get('build_digest')),
        'distinct_build_digests': len(digests),
        'distinct_image_tags': len(tags),
        'backend_control_protocol_version': expected_protocol,
        'prod_runs_worker': False,
    },
    'build_digests': digests,
    'image_tags': tags,
    'observed': observed,
    'findings': findings,
}, ensure_ascii=False))

sys.exit(0 if verdict == 'converged' else (2 if verdict == 'setup_error' else 1))
PY
