#!/usr/bin/env python3
"""Fail when a fleet host bootstrap leaves Ubuntu's unattended-upgrades enabled.

Ubuntu Server images ship `apt-daily-upgrade.timer` enabled
(`OnCalendar=*-*-* 6:00`, `RandomizedDelaySec=60m`) together with
`APT::Periodic::Unattended-Upgrade "1"` in `/etc/apt/apt.conf.d/20auto-upgrades`.
Nobody writes that line; it arrives with the installer image and survives every
host we build from it.

On a TokenKey gateway host the consequences are specific, not theoretical:

1. The 06:00-07:00 UTC window is 14:00-15:00 CST — peak traffic.
2. A glibc upgrade's postinst runs `systemctl daemon-reexec`
   (`/var/lib/dpkg/info/libc6:arm64.postinst`), which restarts
   systemd-networkd/resolved/timesyncd. Containers keep running, but every
   in-flight upstream TLS connection is severed mid-stream. Observed
   2026-10-11 06:46:05 UTC on tokenkey-prod-hz-cax21: ~56 user-visible 502s
   across newapi/openai/anthropic upstreams in nine minutes, from one host
   event, because they all share eth0.
3. `Unattended-Upgrade::Automatic-Reboot` stays false (correctly — auto-rebooting
   a gateway is worse), so the host installs but never activates: glibc and the
   kernel sit patched on disk while long-lived processes keep mapping the old
   ones. Patched on disk, unpatched in memory, and it paid an outage for it.

So patching is a release here: performed blue/green on the standby side with a
real reboot, then cut over. The bootstraps must mask the timer (`mask`, not
`disable` — `disable` is silently undone by package upgrades and systemd
presets) and pin `APT::Periodic::Unattended-Upgrade "0"` in a `99-` conf file
that outranks the installer's `20auto-upgrades`.

`apt-daily.timer` is deliberately left enabled: it only refreshes package lists
and restarts nothing, which is what lets a patch-debt probe report what is
pending. Flagging it would push someone to blind the fleet instead.

This check is static — it reads the bootstrap scripts in-repo. Live-host
enforcement is the fleet patch-debt probe, not preflight.
"""

from __future__ import annotations

import argparse
import re
import sys
from pathlib import Path

# Host bootstraps that provision a long-lived fleet node from an Ubuntu image.
# A new one must be added here, or it inherits the default and nobody notices.
BOOTSTRAPS = (
    Path("deploy/hetzner/render-bootstrap.sh"),
    Path("deploy/hetzner/render-prod-bootstrap.sh"),
)

# Generated artifacts committed alongside the renderers; they are what cloud-init
# actually executes, so drift between renderer and artifact must also fail.
GENERATED = (
    Path("deploy/hetzner/generated-user-data.sh"),
    Path("deploy/hetzner/generated-prod-user-data.sh"),
)

MASK_TIMER = re.compile(
    r"systemctl\s+mask\s+apt-daily-upgrade\.timer", re.MULTILINE
)
PERIODIC_OFF = re.compile(
    r'APT::Periodic::Unattended-Upgrade\s+"0"\s*;', re.MULTILINE
)
# Re-enabling by any route defeats the gate.
PERIODIC_ON = re.compile(
    r'APT::Periodic::Unattended-Upgrade\s+"[^0]"\s*;', re.MULTILINE
)
UNMASK = re.compile(
    r"systemctl\s+(?:unmask|enable)\s+(?:--now\s+)?apt-daily-upgrade\.timer",
    re.MULTILINE,
)


def audit(path: Path) -> list[str]:
    if not path.is_file():
        return [f"{path}: missing (expected a fleet host bootstrap)"]

    text = path.read_text(encoding="utf-8", errors="replace")
    findings: list[str] = []

    if not MASK_TIMER.search(text):
        findings.append(
            f"{path}: does not mask apt-daily-upgrade.timer — the host will "
            f"run Ubuntu's unattended-upgrades at 06:00-07:00 UTC (14:00-15:00 CST peak)"
        )
    if not PERIODIC_OFF.search(text):
        findings.append(
            f'{path}: does not pin APT::Periodic::Unattended-Upgrade "0" '
            f"(the installer's 20auto-upgrades sets it to 1)"
        )
    if PERIODIC_ON.search(text):
        findings.append(
            f"{path}: re-enables APT::Periodic::Unattended-Upgrade — patching "
            f"must stay a blue/green release"
        )
    if UNMASK.search(text):
        findings.append(
            f"{path}: unmasks or re-enables apt-daily-upgrade.timer"
        )
    return findings


def _selftest() -> int:
    import tempfile

    good = (
        "systemctl mask apt-daily-upgrade.timer >/dev/null 2>&1 || true\n"
        'APT::Periodic::Unattended-Upgrade "0";\n'
    )
    cases: list[tuple[str, int]] = [
        (good, 0),
        ("apt-get update -y\n", 2),  # neither mask nor pin
        ('APT::Periodic::Unattended-Upgrade "0";\n', 1),  # no mask
        ("systemctl mask apt-daily-upgrade.timer\n", 1),  # no pin
        (good + 'APT::Periodic::Unattended-Upgrade "1";\n', 1),  # re-enabled
        (good + "systemctl unmask apt-daily-upgrade.timer\n", 1),  # unmasked
    ]
    failures = 0
    with tempfile.TemporaryDirectory() as tmp:
        for i, (body, want) in enumerate(cases):
            p = Path(tmp) / f"case{i}.sh"
            p.write_text(body, encoding="utf-8")
            got = len(audit(p))
            if got != want:
                print(
                    f"  selftest case {i}: expected {want} finding(s), got {got}",
                    file=sys.stderr,
                )
                failures += 1
        missing = audit(Path(tmp) / "absent.sh")
        if len(missing) != 1:
            print("  selftest: missing-file case did not report", file=sys.stderr)
            failures += 1
    return 1 if failures else 0


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--quiet", action="store_true")
    ap.add_argument("--selftest", action="store_true")
    args = ap.parse_args()

    if args.selftest:
        return _selftest()

    findings: list[str] = []
    for path in BOOTSTRAPS + GENERATED:
        findings.extend(audit(path))

    if findings:
        print("fleet host bootstraps leave Ubuntu auto-upgrades enabled:", file=sys.stderr)
        for finding in findings:
            print(f"  - {finding}", file=sys.stderr)
        print(
            "\nFix: in each bootstrap, before the first apt-get call, add\n"
            "  systemctl mask apt-daily-upgrade.timer >/dev/null 2>&1 || true\n"
            "  systemctl stop apt-daily-upgrade.timer >/dev/null 2>&1 || true\n"
            '  echo \'APT::Periodic::Unattended-Upgrade "0";\' > '
            "/etc/apt/apt.conf.d/99tokenkey-no-auto-upgrade\n"
            "then re-run the renderer so the generated user-data matches.\n"
            "Leave apt-daily.timer enabled — it only refreshes package lists.\n"
            "Patching is a blue/green release: upgrade + reboot the standby side, "
            "health-check, then cut over.",
            file=sys.stderr,
        )
        return 1

    if not args.quiet:
        print("host-auto-upgrade-disabled: OK")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
