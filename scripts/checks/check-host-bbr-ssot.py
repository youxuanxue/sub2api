#!/usr/bin/env python3
"""Guard: prod host BBR SSOT stays wired; optional live check covers admin/proxies.

SSOT: deploy/host/ensure-tokenkey-bbr.sh
Static consumers (preflight default):
  - deploy/aws/stage0/stage0-ec2-bootstrap.sh (compact apply; keep ≤3 SSM parts)
  - deploy/hetzner/render-prod-bootstrap.sh (b64_gzip embed + install)
  - ops/stage0/sync-host-bbr-via-ssm.sh (live EC2 heal)
  - scripts/checks/proxy_bbr_live.py + ops/observability/probe-list-proxies.sh
    (admin/proxies egress hosts)

Live mode (`--live`):
  - also SSH-checks every active https://tokenkey.dev/admin/proxies host for bbr+fq

Usage:
  python3 scripts/checks/check-host-bbr-ssot.py
  python3 scripts/checks/check-host-bbr-ssot.py --quiet
  python3 scripts/checks/check-host-bbr-ssot.py --live
"""

from __future__ import annotations

import argparse
import importlib.util
import pathlib
import sys

REPO = pathlib.Path(__file__).resolve().parents[2]
SSOT = REPO / "deploy/host/ensure-tokenkey-bbr.sh"
EC2_BOOTSTRAP = REPO / "deploy/aws/stage0/stage0-ec2-bootstrap.sh"
HETZNER_PROD = REPO / "deploy/hetzner/render-prod-bootstrap.sh"
SYNC = REPO / "ops/stage0/sync-host-bbr-via-ssm.sh"
PROXY_LIVE = REPO / "scripts/checks/proxy_bbr_live.py"
PROXY_PROBE = REPO / "ops/observability/probe-list-proxies.sh"

BEGIN = "# --- 1c. bbr (TCP congestion control) ---"
END = "# --- 2a. attach + mount persistent data volume ---"

REQUIRED_SSOT_TOKENS = (
    "net.core.default_qdisc=fq",
    "net.ipv4.tcp_congestion_control=bbr",
    "tcp_bbr",
    "--check",
)

REQUIRED_EC2_TOKENS = (
    BEGIN,
    "/etc/modules-load.d/99-tokenkey-bbr.conf",
    "/etc/sysctl.d/91-tokenkey-bbr.conf",
    "tcp_bbr",
    "net.core.default_qdisc=fq",
    "net.ipv4.tcp_congestion_control=bbr",
    "modprobe tcp_bbr",
)


def _read(path: pathlib.Path) -> str:
    return path.read_text(encoding="utf-8")


def _rel(path: pathlib.Path) -> str:
    try:
        return str(path.relative_to(REPO))
    except ValueError:
        return str(path)


def _ec2_bbr_section(body: str) -> str | None:
    if BEGIN not in body or END not in body:
        return None
    return body.split(BEGIN, 1)[1].split(END, 1)[0]


def check_static(quiet: bool = False) -> list[str]:
    errors: list[str] = []

    if not SSOT.is_file():
        errors.append(f"missing SSOT {_rel(SSOT)}")
        return errors

    ssot = _read(SSOT)
    for needle in REQUIRED_SSOT_TOKENS:
        if needle not in ssot:
            errors.append(f"SSOT missing required token: {needle}")

    if not EC2_BOOTSTRAP.is_file():
        errors.append(f"missing {_rel(EC2_BOOTSTRAP)}")
    else:
        body = _read(EC2_BOOTSTRAP)
        section = _ec2_bbr_section(body)
        if section is None:
            errors.append(
                f"{_rel(EC2_BOOTSTRAP)} missing 1c BBR section "
                f"(markers {BEGIN!r} .. {END!r})"
            )
        else:
            if "<<'BBREOF'" in section:
                errors.append(
                    f"{_rel(EC2_BOOTSTRAP)} embeds full BBR script heredoc; "
                    "use the compact sysctl/modprobe block (SSOT lives in deploy/host/)"
                )
            window = BEGIN + section
            for needle in REQUIRED_EC2_TOKENS:
                if needle not in window:
                    errors.append(
                        f"{_rel(EC2_BOOTSTRAP)} BBR section missing {needle!r}"
                    )

    if not HETZNER_PROD.is_file():
        errors.append(f"missing {_rel(HETZNER_PROD)}")
    else:
        hz = _read(HETZNER_PROD)
        if "deploy/host/ensure-tokenkey-bbr.sh" not in hz:
            errors.append(
                f"{_rel(HETZNER_PROD)} does not reference deploy/host/ensure-tokenkey-bbr.sh"
            )
        if "BBR_GZB64" not in hz:
            errors.append(f"{_rel(HETZNER_PROD)} missing BBR_GZB64 embed")
        if "ensure-tokenkey-bbr.sh --apply" not in hz:
            errors.append(
                f"{_rel(HETZNER_PROD)} does not run ensure-tokenkey-bbr.sh --apply"
            )
        if "ensure-tokenkey-bbr.sh --check" not in hz:
            errors.append(
                f"{_rel(HETZNER_PROD)} does not run ensure-tokenkey-bbr.sh --check"
            )

    if not SYNC.is_file():
        errors.append(f"missing {_rel(SYNC)}")
    else:
        sync = _read(SYNC)
        if "deploy/host/ensure-tokenkey-bbr.sh" not in sync:
            errors.append(f"{_rel(SYNC)} does not reference SSOT path")
        if "ensure-tokenkey-bbr.sh --apply" not in sync:
            errors.append(f"{_rel(SYNC)} does not run ensure --apply")

    if not PROXY_LIVE.is_file():
        errors.append(f"missing {_rel(PROXY_LIVE)} (admin/proxies live BBR checker)")
    else:
        live_src = _read(PROXY_LIVE)
        for needle in (
            "probe-list-proxies.sh",
            "tcp_congestion_control",
            "bbr",
            "map_hosts_to_lightsail",
        ):
            if needle not in live_src:
                errors.append(f"{_rel(PROXY_LIVE)} missing required token: {needle}")

    if not PROXY_PROBE.is_file():
        errors.append(f"missing {_rel(PROXY_PROBE)} (admin/proxies inventory probe)")
    else:
        probe_src = _read(PROXY_PROBE)
        if "FROM proxies" not in probe_src and "from proxies" not in probe_src.lower():
            errors.append(f"{_rel(PROXY_PROBE)} does not query proxies table")
        if "status = 'active'" not in probe_src:
            errors.append(f"{_rel(PROXY_PROBE)} must filter status=active")

    return errors


def check_live(quiet: bool = False) -> list[str]:
    spec = importlib.util.spec_from_file_location("proxy_bbr_live", PROXY_LIVE)
    if spec is None or spec.loader is None:
        return [f"cannot import {_rel(PROXY_LIVE)}"]
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    try:
        return list(mod.check_proxies_live())
    except Exception as exc:  # noqa: BLE001
        return [f"proxy BBR live check aborted: {exc}"]


def check(quiet: bool = False, live: bool = False) -> int:
    errors = check_static(quiet=quiet)
    if live:
        errors.extend(check_live(quiet=quiet))
    return _emit(errors, quiet, live=live)


def _emit(errors: list[str], quiet: bool, live: bool = False) -> int:
    if errors:
        if not quiet:
            for e in errors:
                print(f"FAIL: {e}", file=sys.stderr)
        return 1
    if not quiet:
        msg = (
            "ok: host BBR SSOT wired (EC2 compact + Hetzner prod + sync heal + "
            "admin/proxies live checker)"
        )
        if live:
            msg = "ok: host BBR SSOT wired and all active admin/proxies hosts have BBR"
        print(msg)
    return 0


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--quiet", action="store_true")
    ap.add_argument(
        "--live",
        action="store_true",
        help="also verify BBR on every active admin/proxies host (needs AWS+SSH)",
    )
    args = ap.parse_args()
    return check(quiet=args.quiet, live=args.live)


if __name__ == "__main__":
    sys.exit(main())
