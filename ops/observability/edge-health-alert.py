#!/usr/bin/env python3
"""edge-health-alert.py — turn scan-edge-health.sh --json output into an alert
decision + a Feishu message, with cross-run dedup via a state key.

This is the *decision* half of the edge-health-watch loop; the *transport* halves
are scan-edge-health.sh --json (read-only SSM fleet sweep) and the
.github/workflows/edge-health-watch.yml step that signs + POSTs to Feishu. Keeping
the decision here (pure Python, no HTTP/AWS) makes it unit-testable with fixtures
(--selftest) and registerable in preflight, mirroring data_layer_capacity_verdict.py
and edge_health_verdict.py.

WHY (2026-06-07 incident): 4 edges went to 0 schedulable accounts and the one healthy
multi-account edge (us5) buckled under concentrated failover load; ~3445 clients ate
429s across two spikes — and ZERO alerts fired, because the truth tool
(scan-edge-health.sh, #640) only runs when a human remembers to run it. This turns
"someone has to go look" into "Feishu shouts the moment an edge dies".

Trigger model: the ACTIONABLE set is the edges whose verdict is down / degraded /
unreachable / parse-error (a `thin` single-account edge is shown for context but is
chronic for most of the fleet, so it does NOT trigger on its own). The state key is a
stable digest of that set; the workflow alerts ONLY when the key changes vs the
previous run — new breakage, escalation, OR full recovery — so a multi-hour incident
does not re-spam every cycle, and a recovery posts a green all-clear.

Input (stdin): one verdict JSON object per line (scan-edge-health.sh --json).
Args:
  --prev-key-file <path>   file holding the previous run's state key (may be missing/empty)
  --window <str>           traffic window label to show in the message (default "2h")
  --selftest               run fixtures (exit 1 on failure)

Output (stdout): one JSON object:
  {"key","actionable_count","changed","should_alert","severity","message"}
The workflow writes `key` back to the cache, and POSTs `message` to Feishu iff
`should_alert` is true.
"""
from __future__ import annotations

import argparse
import json
import sys

ACTIONABLE = ("down", "degraded", "unreachable", "parse-error")
_SEV_ORDER = {"down": 0, "unreachable": 0, "parse-error": 0, "degraded": 1}


def _num(v, default=0):
    try:
        return int(v)
    except (TypeError, ValueError):
        return default


def build_decision(rows: list, prev_key: str, window: str) -> dict:
    """Pure: verdict rows + previous state key -> alert decision + message."""
    by_verdict: dict = {}
    for r in rows:
        by_verdict.setdefault(r.get("verdict"), []).append(r)

    actionable = [r for r in rows if r.get("verdict") in ACTIONABLE]
    # Stable key: sorted "verdict:edge" of the actionable set. Same incident => same
    # key => no re-alert; any change (new edge, recovery) flips it.
    key_parts = sorted(f"{r.get('verdict')}:{r.get('edge')}" for r in actionable)
    key = "|".join(key_parts)  # "" when the fleet is clean

    changed = key != (prev_key or "")
    should_alert = changed  # breakage, escalation, AND recovery all flip the key

    thin = sorted(r.get("edge") for r in by_verdict.get("thin", []))
    healthy = sorted(
        r.get("edge") for r in rows if r.get("verdict") in ("healthy", "idle")
    )

    if not actionable:
        severity = "recovery" if (prev_key or "") else "ok"
    elif any(r.get("verdict") in ("down", "unreachable", "parse-error") for r in actionable):
        severity = "critical"
    else:
        severity = "warning"

    message = _format_message(actionable, thin, healthy, window, severity)
    return {
        "key": key,
        "actionable_count": len(actionable),
        "changed": changed,
        "should_alert": should_alert,
        "severity": severity,
        "message": message,
    }


def _format_message(actionable: list, thin: list, healthy: list, window: str, severity: str) -> str:
    if not actionable:
        head = "✅ TokenKey Edge Health — all edges recovered (no down/degraded)"
    else:
        head = f"🔴 TokenKey Edge Health — {len(actionable)} edge(s) need action"
    lines = [head, ""]

    grouped: dict = {}
    for r in sorted(actionable, key=lambda r: (_SEV_ORDER.get(r.get("verdict"), 9), r.get("edge") or "")):
        grouped.setdefault(r.get("verdict"), []).append(r)

    for verdict in ("down", "unreachable", "parse-error", "degraded"):
        rs = grouped.get(verdict)
        if not rs:
            continue
        lines.append(f"{verdict.upper()}:")
        for r in rs:
            if verdict in ("unreachable", "parse-error"):
                lines.append(f"  • {r.get('edge')}")
            else:
                sched = _num(r.get("schedulable_accounts"))
                served = _num(r.get("served_200"))
                noavail = _num(r.get("no_available_429"))
                ratio = r.get("served_ratio")
                wait = _num(r.get("wait_timeout"))
                tail = f"sched={sched} served200={served} noavail429={noavail}"
                if ratio is not None:
                    tail += f" ratio={ratio}"
                if wait:
                    tail += f" wait_to={wait}"
                lines.append(f"  • {r.get('edge')}  {tail}")

    if thin:
        lines.append("")
        lines.append(f"thin/SPOF (single account): {', '.join(thin)}")
    if healthy:
        lines.append(f"healthy: {', '.join(healthy)}")
    lines.append("")
    lines.append(f"window={window} • detail: ops/observability/scan-edge-health.sh --with-prod")
    return "\n".join(lines)


# --- selftest fixtures: the 2026-06-07 incident shapes + dedup behavior ----------
def _rows(*specs):
    out = []
    for s in specs:
        out.append(s)
    return out


_SELFTEST = []


def _case(name, rows, prev_key, expect):
    _SELFTEST.append((name, rows, prev_key, expect))


_case(
    "incident: 3 down + 1 degraded => critical, alert from clean",
    [
        {"edge": "uk2", "verdict": "down", "schedulable_accounts": 0, "served_200": 712, "no_available_429": 0},
        {"edge": "uk3", "verdict": "down", "schedulable_accounts": 0, "served_200": 882, "no_available_429": 0},
        {"edge": "us3", "verdict": "down", "schedulable_accounts": 0, "served_200": 170, "no_available_429": 0},
        {"edge": "us5", "verdict": "degraded", "schedulable_accounts": 3, "served_200": 11102, "no_available_429": 4841, "served_ratio": 0.696, "wait_timeout": 5656},
        {"edge": "us2", "verdict": "thin", "schedulable_accounts": 1},
        {"edge": "prod", "verdict": "healthy", "schedulable_accounts": 14},
    ],
    "",
    {"should_alert": True, "severity": "critical", "actionable_count": 4},
)
_case(
    "same incident set unchanged => NO re-alert",
    [
        {"edge": "uk2", "verdict": "down"},
        {"edge": "uk3", "verdict": "down"},
        {"edge": "us3", "verdict": "down"},
        {"edge": "us5", "verdict": "degraded"},
    ],
    "degraded:us5|down:uk2|down:uk3|down:us3",
    {"should_alert": False, "severity": "critical"},
)
_case(
    "escalation: a new edge goes down => alert (key changed)",
    [
        {"edge": "uk2", "verdict": "down"},
        {"edge": "uk3", "verdict": "down"},
        {"edge": "us3", "verdict": "down"},
        {"edge": "us6", "verdict": "down"},
        {"edge": "us5", "verdict": "degraded"},
    ],
    "degraded:us5|down:uk2|down:uk3|down:us3",
    {"should_alert": True, "severity": "critical"},
)
_case(
    "full recovery => green all-clear alert",
    [
        {"edge": "uk2", "verdict": "healthy", "schedulable_accounts": 2},
        {"edge": "us5", "verdict": "healthy", "schedulable_accounts": 3},
    ],
    "degraded:us5|down:uk2|down:uk3|down:us3",
    {"should_alert": True, "severity": "recovery", "actionable_count": 0},
)
_case(
    "steady healthy => no alert, no key",
    [
        {"edge": "uk2", "verdict": "healthy"},
        {"edge": "us5", "verdict": "healthy"},
    ],
    "",
    {"should_alert": False, "severity": "ok", "actionable_count": 0},
)
_case(
    "chronic thin alone does NOT trigger",
    [
        {"edge": "uk1", "verdict": "thin", "schedulable_accounts": 1},
        {"edge": "us2", "verdict": "thin", "schedulable_accounts": 1},
        {"edge": "prod", "verdict": "healthy"},
    ],
    "",
    {"should_alert": False, "actionable_count": 0},
)
_case(
    "unreachable edge => critical actionable",
    [
        {"edge": "fra1", "verdict": "unreachable"},
        {"edge": "prod", "verdict": "healthy"},
    ],
    "",
    {"should_alert": True, "severity": "critical", "actionable_count": 1},
)


def _run_selftest() -> int:
    failures = 0
    for name, rows, prev_key, expect in _SELFTEST:
        got = build_decision(rows, prev_key, "2h")
        for k, v in expect.items():
            if got.get(k) != v:
                failures += 1
                print(f"  [FAIL] {name}: {k} expected={v} got={got.get(k)}", file=sys.stderr)
                break
        else:
            print(f"  [ok] {name}", file=sys.stderr)
    if failures:
        print(f"edge-health-alert selftest: {failures} FAILED", file=sys.stderr)
        return 1
    print(f"edge-health-alert selftest: all {len(_SELFTEST)} cases passed", file=sys.stderr)
    return 0


def main() -> int:
    ap = argparse.ArgumentParser(description="Edge-health alert decision from scan-edge-health.sh --json.")
    ap.add_argument("--prev-key-file", default=None)
    ap.add_argument("--window", default="2h")
    ap.add_argument("--selftest", action="store_true")
    args = ap.parse_args()

    if args.selftest:
        return _run_selftest()

    prev_key = ""
    if args.prev_key_file:
        try:
            with open(args.prev_key_file, encoding="utf-8") as fh:
                prev_key = fh.read().strip()
        except FileNotFoundError:
            prev_key = ""

    rows = []
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        try:
            rows.append(json.loads(line))
        except json.JSONDecodeError:
            continue

    print(json.dumps(build_decision(rows, prev_key, args.window), ensure_ascii=False))
    return 0


if __name__ == "__main__":
    sys.exit(main())
