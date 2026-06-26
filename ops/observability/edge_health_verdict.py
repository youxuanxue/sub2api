#!/usr/bin/env python3
"""edge_health_verdict.py — turn the read-only edge probe output into a deterministic
edge-health verdict (healthy | thin | idle-thin | degraded | down | idle | no-accounts).

This is the *logic* half; the *transport* half is the read-only bash probe
`probe-edge-health.sh` (delivered to each edge host via run-probe.sh), and the
*fan-out* half is `scan-edge-health.sh` (loops the deployable-edge matrix locally).
Keeping the verdict here (pure Python, no AWS) makes it unit-testable with fixtures
(`--selftest`) and registerable in preflight, mirroring the determinism contract in
dev-rules-convention.mdc §"skill / command 确定性基线" and its sibling
data_layer_capacity_verdict.py.

WHY THIS EXISTS (2026-06-06 yace load test):
    prod's "upstream-429 by account" read 1272-1941 across ALL six mirror edges —
    looking uniformly bad — while the TRUE edge states were:
        us5  healthy   3 accts  served_200=2251  no_available_429=77
        uk1  degraded  1 acct   served_200=1375  no_available_429=3158
        us2  thin      1 acct   served_200=1394  no_available_429=111
        us3  DOWN      1 acct   served_200=0     no_available_429=33748  (dead 3.5h)
        us6  DOWN      1 acct   served_200=182   no_available_429=35055
        us7  DOWN      1 acct   served_200=253   no_available_429=30675
    prod's upstream-429 / recovered-200 are polluted by client-cancel tagging and
    failover smear and CANNOT tell a dead edge from a healthy one. The edge's OWN
    served_200 : no_available_429 ratio + schedulable-account count is the only
    reliable signal — that is what this verdict computes.

Input (stdin): the probe's tagged, field-named JSON lines:
    ACCT    {"id":1,"name":"...","platform":"anthropic","status":"active",
             "schedulable":true,"concurrency":16,"session_window_status":"allowed"}
    TRAFFIC {"since":"2h","served_200":2251,"all_429":77,"no_available_429":77,
             "wait_timeout":0,"client_cancel":992,"total_completed":3320}
    (ACCT repeated once per account; TRAFFIC exactly once. Lines in any order;
     non-tagged lines are ignored so the probe can also print human headers.)

Output (stdout): one JSON object with the computed metrics + "verdict".
Exit code is always 0 in normal mode (the verdict is in the payload, like
data_layer_capacity_verdict.py); --selftest exits 1 on fixture failure.
"""
from __future__ import annotations

import argparse
import json
import pathlib
import sys

_DEFAULT_THRESHOLDS = pathlib.Path(__file__).with_name("edge-health-thresholds.json")


def _load_thresholds(path: pathlib.Path) -> dict:
    data = json.loads(path.read_text(encoding="utf-8"))
    return data["thresholds"]


def triage_account(acct: dict) -> dict:
    """Derive WHICH of the sched=0 排查三连 steps applies to ONE unschedulable account,
    from fields the probe already emits (status / session_window_status). This is the
    状态派生 the operator was being told to do by hand: the raw verdict line said
    `sched=0/N` and then "go run the 3-step runbook and figure out which N are stuck and
    why". The signals are all already in the ACCT row — keeping them on the floor was the
    waste. Buckets map 1:1 onto the runbook:

      oauth  (step 3) status != active            — account-level death (OAuth 吊销/error)
      window (step 1) session_window_status=rejected — 5h/7d 窗口烧穿, will self-recover
      flag   (step 2) healthy but schedulable=false  — pinned off; the ONE instant fix

    Precedence is account-death > window-burn > flag-pin: a dead account is not fixed by
    flipping a flag, and a flag-pin is the only one a one-liner resolves. Probe limitation:
    a `schedulable=true` account silently in cooldown (temp_unschedulable_until) is not in
    this set — it still counts toward n_sched; that case surfaces as degraded/down-by-ratio,
    a different path."""
    status = (acct.get("status") or "").strip()
    window = (acct.get("session_window_status") or "").strip()
    if status == "error":
        bucket, hint = "oauth", "status=error（多为 OAuth 吊销 / refresh 报成功仍 401）→ 重新授权或换号，重刷无效"
    elif status and status != "active":
        bucket, hint = "oauth", f"status={status}（非 active）→ 确认是否应启用 / 重新授权"
    elif window == "rejected":
        bucket, hint = "window", "5h/7d 窗口烧穿（上游 rejected）→ 等窗口重置，勿清冷却、勿扩容"
    else:
        bucket, hint = "flag", "schedulable=false 被钉死（账号健康却调度关闭）→ 一键启用：remediate-schedulable-pool.sh MODE=edge-oauth-pool"
    return {
        "id": acct.get("id"),
        "name": acct.get("name"),
        "bucket": bucket,
        "hint": hint,
        "status": status or None,
        "window": window or None,
    }


def compute_verdict(accounts: list, traffic: dict, thresholds: dict) -> dict:
    """Pure function: per-account schedulability + traffic counts + thresholds ->
    verdict payload. No I/O. `accounts` is a list of ACCT dicts (any platform; the
    schedulable count is over the platform the probe selected). `traffic` is the
    single TRAFFIC dict (may be {} if the probe found no access-log lines)."""
    min_healthy = int(thresholds["min_healthy_schedulable_accounts"]["value"])
    down_ratio = float(thresholds["served_ratio"]["down"])
    degraded_ratio = float(thresholds["served_ratio"]["degraded"])

    schedulable = [a for a in accounts if a.get("schedulable") is True]
    n_sched = len(schedulable)
    n_total = len(accounts)

    served_200 = int(traffic.get("served_200") or 0)
    no_avail = int(traffic.get("no_available_429") or 0)
    total_completed = int(traffic.get("total_completed") or 0)

    denom = served_200 + no_avail
    served_ratio = (served_200 / denom) if denom > 0 else None

    single_account_risk = n_sched <= 1

    # Verdict precedence — most-severe wins. Each branch carries a `reason` so the
    # table is self-explaining and the selftest pins the exact boundary that fired.
    #
    # total==0 is PROVISIONING POSTURE, not an incident — as long as nothing is being
    # rejected. uk2/uk3 (2026-06: registered Stage0 stacks awaiting the add-accounts vs
    # decommission decision) sat at total=0 with served_200≈720/2h — exactly the 10s
    # health-check cadence, zero real demand — yet read `down` and pinned every alert
    # cycle to critical. The moment demand DOES hit an unprovisioned edge
    # (no_available_429 > 0), it falls through to `down`: clients are eating 429s.
    if n_total == 0 and no_avail == 0:
        verdict, reason = "no-accounts", "no accounts provisioned and no rejected demand (posture: add accounts or decommission)"
    elif n_sched == 0:
        verdict, reason = "down", "no schedulable accounts (empty pool by construction)"
    elif denom == 0:
        # No served traffic AND no empty-pool signal in the window => nothing to judge
        # from traffic. Fall back to the structural account-count signal only.
        if total_completed == 0:
            verdict, reason = "idle", "no /v1/messages traffic in window; judged by account count only"
        else:
            verdict, reason = "idle", "traffic present but no served_200 / no_available_429 signal in window"
    elif served_ratio <= down_ratio:
        verdict, reason = "down", f"served_ratio {served_ratio:.3f} <= down {down_ratio} (serving ~nothing; near-total empty pool)"
    elif served_ratio < degraded_ratio:
        verdict, reason = "degraded", f"served_ratio {served_ratio:.3f} < degraded {degraded_ratio} (serving but bleeding empty-pool 429)"
    elif n_sched < min_healthy:
        verdict, reason = "thin", f"only {n_sched} schedulable account (< {min_healthy}); single point of failure even though ratio is fine"
    else:
        verdict, reason = "healthy", f"{n_sched} schedulable accounts, served_ratio {served_ratio:.3f}"

    # 'idle' edges that are also single-account should still surface the risk in the
    # verdict so a paused/dead backup isn't hidden behind 'idle'.
    if verdict == "idle" and single_account_risk:
        verdict = "idle-thin"
        reason += f"; only {n_sched} schedulable account (single point of failure)"

    return {
        "verdict": verdict,
        "reason": reason,
        "schedulable_accounts": n_sched,
        "total_accounts": n_total,
        "single_account_risk": single_account_risk,
        "served_200": served_200,
        "no_available_429": no_avail,
        "served_ratio": round(served_ratio, 4) if served_ratio is not None else None,
        "wait_timeout": int(traffic.get("wait_timeout") or 0),
        "client_cancel": int(traffic.get("client_cancel") or 0),
        "all_429": int(traffic.get("all_429") or 0),
        "total_completed": total_completed,
        "since": traffic.get("since"),
        "account_names": [a.get("name") for a in schedulable],
        # Per-account triage for the unschedulable accounts (the ones blocking a sched=0
        # pool). Derived here so the alert renders "account #5: schedulable=false 被钉死 →
        # 一键启用" instead of dumping the generic 3-step and saying "逐账号定位 yourself".
        "unschedulable": [triage_account(a) for a in accounts if a.get("schedulable") is not True],
    }


def parse_probe_stream(lines) -> tuple:
    """Extract ACCT[] and TRAFFIC{} from the probe's tagged lines. Non-tagged lines
    (human headers) are ignored. A malformed tagged line is skipped, not fatal."""
    accounts: list = []
    traffic: dict = {}
    for raw in lines:
        line = raw.strip()
        if line.startswith("ACCT "):
            try:
                accounts.append(json.loads(line[len("ACCT "):]))
            except json.JSONDecodeError:
                continue
        elif line.startswith("TRAFFIC "):
            try:
                traffic = json.loads(line[len("TRAFFIC "):])
            except json.JSONDecodeError:
                continue
    return accounts, traffic


# --- selftest fixtures: the six real edges from the 2026-06-06 yace burst -------
# Each tuple is (label, accounts, traffic, expected_verdict). These pin the exact
# boundaries so a threshold or logic regression fails preflight, not production.
_SELFTEST_CASES = [
    (
        "us5 healthy (3 accounts, high ratio)",
        [{"schedulable": True, "name": "a1"}, {"schedulable": True, "name": "a2"}, {"schedulable": True, "name": "a3"}],
        {"served_200": 2251, "no_available_429": 77, "total_completed": 3320, "since": "burst"},
        "healthy",
    ),
    (
        "uk1 degraded (single account, ratio 0.30)",
        [{"schedulable": True, "name": "a2"}, {"schedulable": False, "name": "a1-paused"}],
        {"served_200": 1375, "no_available_429": 3158, "total_completed": 4533, "since": "burst"},
        "degraded",
    ),
    (
        "us2 thin (single account, ratio 0.93 but SPOF)",
        [{"schedulable": True, "name": "a1"}, {"schedulable": False, "name": "a2-oauth-dead"}],
        {"served_200": 1394, "no_available_429": 111, "wait_timeout": 551, "total_completed": 1505, "since": "burst"},
        "thin",
    ),
    (
        "us3 down (served nothing, all empty-pool)",
        [{"schedulable": True, "name": "a1-session-rejected"}],
        {"served_200": 0, "no_available_429": 33748, "total_completed": 33748, "since": "burst"},
        "down",
    ),
    (
        "us6 down (ratio 0.005)",
        [{"schedulable": True, "name": "a1"}],
        {"served_200": 182, "no_available_429": 35055, "total_completed": 35237, "since": "burst"},
        "down",
    ),
    (
        "us7 down (ratio 0.008)",
        [{"schedulable": True, "name": "a1"}, {"schedulable": False, "name": "a2-error"}],
        {"served_200": 253, "no_available_429": 30675, "total_completed": 30928, "since": "burst"},
        "down",
    ),
    (
        "no schedulable accounts => down",
        [{"schedulable": False, "name": "a1"}, {"schedulable": False, "name": "a2"}],
        {"served_200": 0, "no_available_429": 0, "total_completed": 0},
        "down",
    ),
    (
        "uk2 shape: zero accounts, health-check 200s only, no rejected demand => no-accounts",
        [],
        {"served_200": 719, "no_available_429": 0, "total_completed": 719, "since": "2h"},
        "no-accounts",
    ),
    (
        "zero accounts, fully idle => no-accounts",
        [],
        {"served_200": 0, "no_available_429": 0, "total_completed": 0, "since": "2h"},
        "no-accounts",
    ),
    (
        "zero accounts BUT demand being rejected => still down (clients eating 429)",
        [],
        {"served_200": 0, "no_available_429": 412, "total_completed": 412, "since": "2h"},
        "down",
    ),
    (
        "idle multi-account (no traffic, >=2 accounts) => idle",
        [{"schedulable": True, "name": "a1"}, {"schedulable": True, "name": "a2"}],
        {"served_200": 0, "no_available_429": 0, "total_completed": 0, "since": "2h"},
        "idle",
    ),
    (
        "idle single-account (no traffic, 1 account) => idle-thin",
        [{"schedulable": True, "name": "a1"}],
        {"served_200": 0, "no_available_429": 0, "total_completed": 0, "since": "2h"},
        "idle-thin",
    ),
    (
        "healthy boundary: 2 accounts at exactly degraded ratio 0.80",
        [{"schedulable": True, "name": "a1"}, {"schedulable": True, "name": "a2"}],
        {"served_200": 80, "no_available_429": 20, "total_completed": 100},
        "healthy",
    ),
    (
        "degraded boundary: 2 accounts just under 0.80",
        [{"schedulable": True, "name": "a1"}, {"schedulable": True, "name": "a2"}],
        {"served_200": 79, "no_available_429": 21, "total_completed": 100},
        "degraded",
    ),
    (
        "down boundary: ratio exactly 0.05 => down",
        [{"schedulable": True, "name": "a1"}],
        {"served_200": 5, "no_available_429": 95, "total_completed": 100},
        "down",
    ),
]


# --- triage fixtures: lock the per-account sched=0 cause derivation (the 状态派生 that
# replaces "go run the 3-step runbook by hand"). Each tuple is (label, ACCT dict,
# expected_bucket) — the exact bucket the alert turns into a per-account action line. ---
_TRIAGE_CASES = [
    ("healthy-but-pinned => flag (the instant fix)",
     {"id": 5, "name": "cc-foo", "status": "active", "schedulable": False, "session_window_status": "allowed"}, "flag"),
    ("5h window burnt => window (wait, do not clear cooldown)",
     {"id": 6, "name": "cc-bar", "status": "active", "schedulable": False, "session_window_status": "rejected"}, "window"),
    ("status=error => oauth (re-auth, refresh won't help)",
     {"id": 7, "name": "cc-baz", "status": "error", "schedulable": False, "session_window_status": "allowed"}, "oauth"),
    ("status=disabled => oauth bucket (account-level, not a flag flip)",
     {"id": 8, "name": "cc-qux", "status": "disabled", "schedulable": False}, "oauth"),
    ("account death dominates a coincident window-burn => oauth",
     {"id": 9, "name": "cc-dead", "status": "error", "schedulable": False, "session_window_status": "rejected"}, "oauth"),
]


def _run_selftest(thresholds: dict) -> int:
    failures = 0
    for label, accounts, traffic, expected in _SELFTEST_CASES:
        got = compute_verdict(accounts, traffic, thresholds)["verdict"]
        ok = got == expected
        if not ok:
            failures += 1
        print(f"  [{'ok' if ok else 'FAIL'}] {label}: expected={expected} got={got}", file=sys.stderr)
    for label, acct, expected in _TRIAGE_CASES:
        got = triage_account(acct)["bucket"]
        ok = got == expected
        if not ok:
            failures += 1
        print(f"  [{'ok' if ok else 'FAIL'}] triage/{label}: expected={expected} got={got}", file=sys.stderr)
    total = len(_SELFTEST_CASES) + len(_TRIAGE_CASES)
    if failures:
        print(f"edge_health_verdict selftest: {failures} FAILED", file=sys.stderr)
        return 1
    print(f"edge_health_verdict selftest: all {total} cases passed", file=sys.stderr)
    return 0


def main() -> int:
    ap = argparse.ArgumentParser(description="Edge-health verdict from probe-edge-health.sh output.")
    ap.add_argument("--thresholds", type=pathlib.Path, default=_DEFAULT_THRESHOLDS)
    ap.add_argument("--selftest", action="store_true", help="run fixture selftest (exit 1 on failure)")
    ap.add_argument("--label", default=None, help="edge id/label to embed in the output JSON")
    args = ap.parse_args()

    thresholds = _load_thresholds(args.thresholds)

    if args.selftest:
        return _run_selftest(thresholds)

    accounts, traffic = parse_probe_stream(sys.stdin)
    out = compute_verdict(accounts, traffic, thresholds)
    if args.label:
        out = {"edge": args.label, **out}
    print(json.dumps(out, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    sys.exit(main())
