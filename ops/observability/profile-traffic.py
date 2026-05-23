#!/usr/bin/env python3
"""profile-traffic.py — TokenKey read-only per-minute traffic rebuild from gateway logs.

Designed to run INSIDE the TokenKey host (prod or edge), after `docker logs tokenkey
--since <window>` has been pre-filtered into:

    /tmp/acc.txt   lines containing  "http request completed"  AND  the configured path
    /tmp/sse.txt   lines containing  "sticky.scheduler_entry"

These two pre-filtered files are produced by `probe-traffic-logs.sh` or by the operator
ahead of time (we keep that step in shell because `docker logs` semantics live there).

Inputs (env):
    ACCTS      comma-separated active account ids (e.g. "1,3,4"). Required.
    IDLE_MIN   session idle minutes — trailing window for activeSess reconstruction.
               Use MAX(session_idle_timeout_minutes) over the platform's active accounts;
               宁宽勿窄. Defaults to 5.
    PATH_KEY   path filter to enforce on http request completed entries
               (default: /v1/messages).
    FMT        strftime format for the minute-bucket label only (default: '%H:%M').
               Bucket granularity is always one minute — strftime cannot express
               5-min buckets. For coarser buckets, roll up the printed table.

Output (stdout): one header row + one row per UTC minute with columns

    min(UTC) | aN :rpm/sR/conc/ok/bad … | nonStk actSess(g)

followed by per-account `totals` rows. activeSess(global) is the trailing-IDLE_MIN
unique-session_hash count — it is an UPPER BOUND (see SKILL §0 坑 5), not a touched-cap
proof. Combine with same-minute 503 counts and "粘性 200 / 非粘性 503" symptom before
declaring max_sessions saturation.
"""

from __future__ import annotations

import collections
import datetime as dt
import json
import os
import re
import sys

ACCTS = [int(x) for x in os.environ.get("ACCTS", "").split(",") if x.strip()]
IDLE_MIN = int(os.environ.get("IDLE_MIN", "5"))
FMT = os.environ.get("FMT", "%H:%M")
PATH_KEY = os.environ.get("PATH_KEY", "/v1/messages")

if not ACCTS:
    sys.stderr.write("ACCTS env required (comma-separated account ids)\n")
    sys.exit(2)


def parse(fn: str):
    out = []
    try:
        with open(fn) as f:
            for ln in f:
                m = re.search(r"\{.*\}$", ln)
                if not m:
                    continue
                try:
                    o = json.loads(m.group(0))
                except json.JSONDecodeError:
                    continue
                out.append((ln[:19], o))
    except FileNotFoundError:
        pass
    return out


# --- access log: RPM (按 start 分钟) + 峰值并发 + 逐分钟 200/bad ---
iv = {a: [] for a in ACCTS}
rpm = {a: collections.Counter() for a in ACCTS}
st = {a: collections.Counter() for a in ACCTS}
for _, o in parse("/tmp/acc.txt"):
    if o.get("path") != PATH_KEY:
        continue
    a = o.get("account_id")
    lat = o.get("latency_ms")
    ca = o.get("completed_at")
    sc = o.get("status_code")
    if a not in ACCTS or not ca or not isinstance(lat, (int, float)):
        continue
    end = dt.datetime.fromisoformat(ca.replace("Z", "+00:00"))
    start = end - dt.timedelta(milliseconds=lat)
    iv[a].append((start, end, sc))
    rpm[a][start.strftime(FMT)] += 1
    st[a][sc] += 1


def peak_conc(intervals):
    res = collections.Counter()
    if not intervals:
        return res
    lo = min(s for s, _, _ in intervals).replace(second=0, microsecond=0)
    hi = max(e for _, e, _ in intervals)
    t = lo
    while t <= hi:
        c = sum(1 for s, e, _ in intervals if s <= t < e)
        res[t.strftime(FMT)] = max(res[t.strftime(FMT)], c)
        t += dt.timedelta(seconds=5)
    return res


pc = {a: peak_conc(iv[a]) for a in ACCTS}

ok = {a: collections.Counter() for a in ACCTS}
bad = {a: collections.Counter() for a in ACCTS}
for a in ACCTS:
    for s, _, sc in iv[a]:
        k = s.strftime(FMT)
        if sc == 200:
            ok[a][k] += 1
        else:
            bad[a][k] += 1

# --- sticky vs non-sticky RPM split ---
srpm = {a: collections.Counter() for a in ACCTS}
nrpm = collections.Counter()
for ts, o in parse("/tmp/sse.txt"):
    try:
        t = dt.datetime.strptime(ts, "%Y-%m-%dT%H:%M:%S")
    except ValueError:
        continue
    k = t.strftime(FMT)
    aid = o.get("sticky_account_id")
    if aid in ACCTS:
        srpm[aid][k] += 1
    elif aid in (0, None):
        nrpm[k] += 1

# --- 全局活跃会话（trailing IDLE_MIN 窗，按 session_hash 去重）。
# 这是上界（见 SKILL §0 坑 5），不是触顶证据。
rows = []
for ts, o in parse("/tmp/sse.txt"):
    sh = o.get("session_hash")
    try:
        t = dt.datetime.strptime(ts, "%Y-%m-%dT%H:%M:%S")
    except ValueError:
        continue
    if sh:
        rows.append((t, sh))
sess = collections.Counter()
if rows:
    lo = min(r[0] for r in rows).replace(second=0)
    hi = max(r[0] for r in rows).replace(second=0)
    w = dt.timedelta(minutes=IDLE_MIN)
    t = lo
    while t <= hi:
        seen = {sh for rt, sh in rows if t - w < rt <= t + dt.timedelta(seconds=59)}
        sess[t.strftime(FMT)] = len(seen)
        t += dt.timedelta(minutes=1)

mins = sorted(
    set().union(
        *[set(rpm[a]) for a in ACCTS],
        *[set(pc[a]) for a in ACCTS],
        set(sess),
        set(nrpm),
    )
)

# Header. Per-account block columns are space-padded so they align under the header
# but each block carries its account id (a4) — readers should still index by name.
print(
    "min(UTC) | "
    + " ".join(f"a{a:<3d}:rpm/sR/conc/ok/bad" for a in ACCTS)
    + " | nonStk actSess(g)"
)
for mn in mins:
    seg = " ".join(
        f"{rpm[a][mn]:2d}/{srpm[a][mn]:2d}/{pc[a][mn]:2d}/{ok[a][mn]:2d}/{bad[a][mn]:2d}"
        for a in ACCTS
    )
    print(f"{mn}     | {seg} | {nrpm[mn]:3d}   {sess[mn]:3d}")

for a in ACCTS:
    print(
        f"acct{a} totals reqs={len(iv[a])}"
        f" rpm_max={max(rpm[a].values() or [0])}"
        f" conc_max={max(pc[a].values() or [0])}"
        f" statuses={dict(st[a])}"
    )
