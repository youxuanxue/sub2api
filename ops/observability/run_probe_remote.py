#!/usr/bin/env python3
"""TAT (default) / SSM break-glass transport for run-probe.sh.

Reads a remote shell body on stdin (the same REMOTE_LINE run-probe builds for
SSM) and executes it through deploy.domestic.remote_exec.
"""
from __future__ import annotations

import argparse
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
if str(ROOT) not in sys.path:
    sys.path.insert(0, str(ROOT))

from deploy.domestic import remote_exec  # noqa: E402

PROD_MANIFEST = ROOT / "deploy/tencent/candidate-usw-20261022.json"
CN_MANIFEST = ROOT / "deploy/domestic/prod-target.json"
# Probes may emit JSON receipts larger than the rolling 8KiB tail.
PROBE_OUTPUT_BYTES = 512 * 1024


def _manifest_for(target: str) -> Path:
    if target in ("prod", "prod-data"):
        return PROD_MANIFEST
    if target in ("cn-prod", "cn-prod-data"):
        return CN_MANIFEST
    raise remote_exec.RemoteExecError(f"unsupported target {target!r}")


def _node_name(target: str, node: str) -> str:
    if target.endswith("-data"):
        return "data"
    if not node:
        raise remote_exec.RemoteExecError(f"{target} requires --node for TAT delivery")
    return node


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--target", required=True)
    parser.add_argument("--node", default="")
    parser.add_argument("--via", choices=("tat", "ssm"), default="tat")
    parser.add_argument("--timeout-seconds", type=int, default=120)
    parser.add_argument("--comment", default="run-probe")
    args = parser.parse_args()

    script = sys.stdin.read()
    if not script.strip():
        print("[run-probe] ERROR: empty remote script body", file=sys.stderr)
        return 1

    try:
        nodes = remote_exec.load_nodes(_manifest_for(args.target))
        name = _node_name(args.target, args.node)
        if name not in nodes:
            raise remote_exec.RemoteExecError(
                f"unknown node {name!r} for {args.target}; have {sorted(nodes)}"
            )
        node = nodes[name]
        print(
            f"[run-probe] resolved target={args.target} node={node.name} "
            f"via={args.via} lhins={node.lighthouse_instance_id} "
            f"comment={args.comment}",
            file=sys.stderr,
        )
        result = remote_exec.run(
            node,
            script,
            timeout=args.timeout_seconds,
            via=args.via,
            output_bytes=PROBE_OUTPUT_BYTES,
        )
    except remote_exec.RemoteExecError as exc:
        print(f"[run-probe] ERROR: {exc}", file=sys.stderr)
        return 2

    print(
        f"[run-probe] status={result.status} channel={result.channel} "
        f"exit_code={result.exit_code}",
        file=sys.stderr,
    )
    if result.stderr_tail:
        for line in result.stderr_tail.splitlines():
            print(f"[remote-stderr] {line}", file=sys.stderr)
    sys.stdout.write(result.stdout_tail or "")
    if result.stdout_tail and not result.stdout_tail.endswith("\n"):
        sys.stdout.write("\n")
    if result.status != "Success" or (result.exit_code not in (0, None)):
        return 3
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
