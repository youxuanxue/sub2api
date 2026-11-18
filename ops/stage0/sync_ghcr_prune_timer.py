#!/usr/bin/env python3
"""Push tokenkey-ghcr-prune-daily onto prod app nodes via TAT (default).

SSOT body: deploy/stage0/tokenkey-ghcr-prune-daily.sh
Control plane: docs/approved/design-tencent-ops-control-plane.md

Replaces the workflow default that called sync-ghcr-prune-timer-via-ssm.sh.
The SSM script remains as an explicit break-glass shell; do not wire it back
into the workflow default.
"""

from __future__ import annotations

import argparse
import base64
import json
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
if str(ROOT) not in sys.path:
    sys.path.insert(0, str(ROOT))

from deploy.domestic import remote_exec  # noqa: E402

DEFAULT_MANIFEST = ROOT / "deploy/tencent/candidate-usw-20261022.json"
DAILY_SRC = ROOT / "deploy/stage0/tokenkey-ghcr-prune-daily.sh"
REMOTE_BIN = "/usr/local/bin/tokenkey-ghcr-prune-daily.sh"


def build_sync_script(*, daily: Path = DAILY_SRC, template_sha: str = "local") -> str:
    if not daily.is_file():
        raise remote_exec.RemoteExecError(f"missing {daily}")
    payload_b64 = base64.b64encode(daily.read_bytes()).decode("ascii")
    sha = template_sha.strip() or "local"
    # Keep the remote body close to the former SSM commands list.
    return "\n".join(
        [
            "set -euo pipefail",
            "echo === ghcr-prune-daily timer sync ===",
            f"echo {payload_b64} | base64 -d | sudo tee {REMOTE_BIN} > /dev/null",
            f"sudo chmod +x {REMOTE_BIN}",
            f"sudo {REMOTE_BIN} --selftest",
            f"sudo {REMOTE_BIN} --install-units",
            "sudo systemctl daemon-reload",
            "sudo systemctl enable --now tokenkey-ghcr-prune-daily.timer",
            "sudo systemctl restart tokenkey-ghcr-prune-daily.timer",
            "echo --- timer ---",
            "sudo systemctl list-timers tokenkey-ghcr-prune-daily.timer --no-pager || true",
            f'echo "Live ghcr-prune-daily timer now matches deploy/stage0@{sha} on $(hostname)"',
            "",
        ]
    )


def sync_apps(
    *,
    manifest: Path = DEFAULT_MANIFEST,
    via: str = "tat",
    timeout: int = 300,
    template_sha: str = "local",
    runner: remote_exec.Runner | None = None,
) -> list[dict]:
    if via not in ("tat", "ssm"):
        raise remote_exec.RemoteExecError(f"via must be 'tat' or 'ssm' (got {via!r})")
    script = build_sync_script(template_sha=template_sha)
    rows: list[dict] = []
    for node in remote_exec.app_nodes(manifest):
        result = remote_exec.run(
            node, script, timeout=timeout, via=via, runner=runner
        )
        row = {
            "node": node.name,
            "channel": result.channel,
            "status": result.status,
            "exit_code": result.exit_code,
        }
        rows.append(row)
        print(
            f"sync_ghcr_prune_timer: node={node.name} channel={result.channel} "
            f"status={result.status} exit_code={result.exit_code}",
            file=sys.stderr,
        )
        if result.status != "Success":
            raise remote_exec.RemoteExecError(
                f"ghcr-prune timer sync failed on {node.name}: "
                f"status={result.status} exit_code={result.exit_code} "
                f"stderr={result.stderr_tail or result.stdout_tail}"
            )
    return rows


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--manifest",
        type=Path,
        default=DEFAULT_MANIFEST,
        help="fleet manifest (default: prod USW)",
    )
    parser.add_argument("--via", choices=("tat", "ssm"), default="tat")
    parser.add_argument("--timeout", type=int, default=300)
    parser.add_argument(
        "--template-sha",
        default="",
        help="commit/sha stamped into the remote echo line",
    )
    parser.add_argument("--receipt-out", type=Path, default=None)
    args = parser.parse_args(argv)
    try:
        rows = sync_apps(
            manifest=args.manifest,
            via=args.via,
            timeout=args.timeout,
            template_sha=args.template_sha or "local",
        )
    except remote_exec.RemoteExecError as exc:
        print(f"sync_ghcr_prune_timer: error: {exc}", file=sys.stderr)
        return 2
    if args.receipt_out is not None:
        args.receipt_out.write_text(
            json.dumps({"nodes": rows, "via": args.via}, indent=2) + "\n",
            encoding="utf-8",
        )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
