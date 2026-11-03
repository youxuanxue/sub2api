#!/usr/bin/env python3
"""Alert from node1 itself when the COS database mirror fails or hangs.

Why this exists: tokenkey-node1-db-mirror.timer (ops/backup/install_node1_db_mirror.py)
runs on node1, not on prod or cn-prod, so host_https_watch.py never sees it --
that script only ever runs on the two production hosts, and its coverage plan
(build_config) is keyed to exactly those two roles. The third backup copy this
mirror makes could stop working silently.

Rather than have prod or cn-prod reach into node1 over the jump host to check
it -- another moving part, and one more thing that breaks if the jump host or
node1's SSH is briefly down -- node1 posts to Feishu itself, reusing the same
job_fact/next_state logic and signed delivery as the production watch so a
failure means the same thing in both places. Credentials already exist on
node1 at /etc/tokenkey/node1-alerts.env, provisioned 2026-10-16 for the Tokyo
precutover and otherwise idle.
"""
from __future__ import annotations

import argparse
import json
import os
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from feishu_delivery import _atomic_write, post_feishu  # noqa: E402
from host_https_watch import JOB_LIMITS, next_state, probe_job  # noqa: E402

JOB = "tokenkey-node1-db-mirror"


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--env-file", type=Path, default=Path("/etc/tokenkey/node1-alerts.env"))
    parser.add_argument("--state", type=Path,
                        default=Path("/var/lib/tokenkey/node1-alert/state.json"))
    parser.add_argument("--dry-run", action="store_true")
    args = parser.parse_args()
    os.umask(0o077)

    if JOB not in JOB_LIMITS:
        raise ValueError(f"{JOB} is not registered in JOB_LIMITS")

    env = {}
    for line in args.env_file.read_text().splitlines():
        if "=" in line and not line.startswith("#"):
            key, value = line.split("=", 1)
            if key in {"TOKENKEY_FEISHU_WEBHOOK_URL", "TOKENKEY_FEISHU_WEBHOOK_SECRET"}:
                env[key] = value.strip().strip('"').strip("'")
    if not all(env.get(k) for k in ("TOKENKEY_FEISHU_WEBHOOK_URL", "TOKENKEY_FEISHU_WEBHOOK_SECRET")):
        raise ValueError("node1 alert credentials are missing")

    args.state.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    previous = json.loads(args.state.read_text()) if args.state.exists() else {}

    fact = probe_job(JOB)
    import time
    now = int(time.time())
    updated, action = next_state(previous.get(JOB, {}), fact["healthy"], now)
    updated["reason"] = fact["reason"]
    sent = False
    if action:
        message = (f"TokenKey node1 job {action}: {JOB}; {fact['reason']}."
                   if action == "unhealthy" else
                   f"TokenKey node1 job recovered: {JOB}; {fact['reason']}.")
        if not args.dry_run:
            post_feishu(message, webhook_url=env["TOKENKEY_FEISHU_WEBHOOK_URL"],
                       signing_secret=env["TOKENKEY_FEISHU_WEBHOOK_SECRET"])
        sent = True
        updated.update(active=action == "unhealthy", sent_at=now)
    previous[JOB] = updated
    if not args.dry_run:
        _atomic_write(args.state, json.dumps(previous, sort_keys=True) + "\n")
    print(json.dumps({"dry_run": args.dry_run, "healthy": fact["healthy"],
                      "reason": fact["reason"], "action": action, "sent": sent}))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
