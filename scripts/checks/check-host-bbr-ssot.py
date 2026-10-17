#!/usr/bin/env python3
"""Guard: prod host BBR SSOT stays wired into bootstrap + live sync.

SSOT: deploy/host/ensure-tokenkey-bbr.sh
Consumers that must not silently drop BBR:
  - deploy/aws/stage0/stage0-ec2-bootstrap.sh (compact apply; keep ≤3 SSM parts)
  - deploy/hetzner/render-prod-bootstrap.sh (b64_gzip embed + install)
  - ops/stage0/sync-host-bbr-via-ssm.sh (live EC2 heal with full SSOT)

Usage:
  python3 scripts/checks/check-host-bbr-ssot.py
  python3 scripts/checks/check-host-bbr-ssot.py --quiet
"""

from __future__ import annotations

import argparse
import pathlib
import sys

REPO = pathlib.Path(__file__).resolve().parents[2]
SSOT = REPO / "deploy/host/ensure-tokenkey-bbr.sh"
EC2_BOOTSTRAP = REPO / "deploy/aws/stage0/stage0-ec2-bootstrap.sh"
HETZNER_PROD = REPO / "deploy/hetzner/render-prod-bootstrap.sh"
SYNC = REPO / "ops/stage0/sync-host-bbr-via-ssm.sh"

BEGIN = "# --- 1c. bbr (TCP congestion control) ---"
END = "# --- 2a. attach + mount persistent data volume ---"

REQUIRED_SSOT_TOKENS = (
    "net.core.default_qdisc=fq",
    "net.ipv4.tcp_congestion_control=bbr",
    "tcp_bbr",
    "--check",
)

# Must appear in the compact EC2 bootstrap block (byte-stable with SSOT comments).
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


def check(quiet: bool = False) -> int:
    errors: list[str] = []

    if not SSOT.is_file():
        errors.append(f"missing SSOT {_rel(SSOT)}")
        return _emit(errors, quiet)

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
            # Reject re-introducing a full-script heredoc that blows the 3-part
            # SSM Standard budget (build-cfn.sh / test_build_cfn.py).
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

    return _emit(errors, quiet)


def _emit(errors: list[str], quiet: bool) -> int:
    if errors:
        if not quiet:
            for e in errors:
                print(f"FAIL: {e}", file=sys.stderr)
        return 1
    if not quiet:
        print(
            "ok: host BBR SSOT wired into EC2 compact bootstrap, "
            "Hetzner prod renderer, and sync heal"
        )
    return 0


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--quiet", action="store_true")
    args = ap.parse_args()
    return check(quiet=args.quiet)


if __name__ == "__main__":
    sys.exit(main())
