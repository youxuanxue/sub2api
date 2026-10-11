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

**Why this does more than grep for two strings.** An adversarial review of the
first version of this gate found that every one of these passed clean, because
the patterns matched raw file text with no shell awareness:

  - both lines commented out (`# systemctl mask …`)
  - wrapped in `if false; then … fi`, or in a function nobody calls
  - quoted inside an `echo "Fix: systemctl mask …"` hint string
  - pin written to `/tmp/junk`, or into `20auto-upgrades` itself (which the
    next `unattended-upgrades` package upgrade overwrites)
  - mask placed *after* `apt-get install`, leaving a live window during boot

The realistic regressions are the last two: someone "simplifying" the block into
`20auto-upgrades`, or moving it below the apt calls. Both would have shipped
green. So this gate now strips comments and heredoc bodies, ignores quoted
occurrences, rejects dead blocks, pins the exact conf path, and requires the
mask to precede the first `apt-get`.
"""

from __future__ import annotations

import argparse
import re
import sys
from pathlib import Path

# Host bootstraps that provision a long-lived fleet node from an Ubuntu image.
# A new one must be added here, or it inherits the default and nobody notices.
# AWS/Lightsail edges run Amazon Linux 2023 (dnf, no apt timer) and are
# deliberately out of scope.
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

# The conf file must be this exact path: a `99-` name outranks the installer's
# `20auto-upgrades` (apt reads apt.conf.d in lexical order), and writing into
# `20auto-upgrades` instead would be reverted by the next package upgrade.
PIN_PATH = "/etc/apt/apt.conf.d/99tokenkey-no-auto-upgrade"

MASK_TIMER = re.compile(r"systemctl\s+(?:--\S+\s+)*mask\s+apt-daily-upgrade\.timer")
PERIODIC_LINE = re.compile(r'APT::Periodic::Unattended-Upgrade\s+"([^"]*)"\s*;')
PIN_WRITE = re.compile(re.escape(PIN_PATH))
# `render-*-bootstrap.sh` emits the bootstrap as a heredoc payload; that payload is
# the code that runs on the host, so it must be inspected, not discarded as data.
GENERATED_PAYLOAD = re.compile(r"<<-?\s*'?\"?(?:LAUNCH_HEAD|LAUNCH_BODY|BOOTSTRAP)\b")
FIRST_APT = re.compile(r"\bapt-get\s+(?:update|install|upgrade|full-upgrade|dist-upgrade)\b")

# Any route back to an enabled timer, including the `systemctl preset` path the
# bootstrap comments cite as the reason to prefer `mask` over `disable`, and an
# `rm` of the pin file after writing it. Flags are allowed anywhere.
RE_ENABLE = re.compile(
    r"systemctl\s+(?:--\S+\s+)*(?:unmask|enable|preset|reenable)\s+"
    r"(?:--\S+\s+)*apt-daily-upgrade\.timer"
)
PIN_REMOVED = re.compile(r"\brm\b[^\n;&|]*" + re.escape(PIN_PATH))


def _blank_quotes(line: str) -> str:
    """Blank quoted spans, keeping the quote marks so byte offsets stay stable.

    A quoted `systemctl mask …` is output, not an action, so it must not satisfy
    the check. Offsets matter because `mask-too-late` compares positions.
    """
    line = re.sub(r'"(?:[^"\\]|\\.)*"', lambda m: '"' + " " * (len(m.group(0)) - 2) + '"', line)
    return re.sub(r"'(?:[^'])*'", lambda m: "'" + " " * (len(m.group(0)) - 2) + "'", line)


def effective_text(text: str) -> str:
    """Strip what the shell would never execute as a command.

    Removes comment lines, heredoc bodies (a `cat <<'X'` payload is data, not
    code — except that we keep the pin heredoc's own content, see below), and
    blanks out double/single-quoted spans so an `echo "systemctl mask …"` hint
    string cannot satisfy the check.

    Two kinds of heredoc body are kept rather than dropped:

      - the pin path's own heredoc, because that body IS the configuration
        under inspection;
      - a renderer's generated-script payload (`cat >"${OUT}.tmp" <<'LAUNCH_HEAD'`),
        because in `render-*-bootstrap.sh` the bootstrap code *is* that payload.
        Dropping it would make every renderer look like it never masks anything.

    Every other heredoc (compose files, unit files, help text) is data and gets
    dropped, which is what stops an `echo`/heredoc hint string from satisfying
    the check.
    """
    lines = text.splitlines()
    out: list[str] = []
    i = 0

    def walk(end: int, delim: str | None, emit: bool) -> None:
        """Consume lines up to `end`, or to `delim` when inside a heredoc.

        `emit` says whether the current region's lines are executed shell source.
        A renderer's payload heredoc is source, so it is walked recursively — which
        matters because the pin heredoc is nested *inside* that payload, and its
        body is apt config whose quotes must survive verbatim.
        """
        nonlocal i
        while i < end:
            line = lines[i]
            if delim is not None and line.strip() == delim:
                i += 1
                return

            if line.lstrip().startswith("#"):
                i += 1
                continue

            heredoc = re.search(r"<<-?\s*'?\"?([A-Za-z_][A-Za-z0-9_]*)'?\"?", line)
            if heredoc:
                inner = heredoc.group(1)
                is_config = PIN_WRITE.search(line) is not None
                is_payload = GENERATED_PAYLOAD.search(line) is not None
                if emit:
                    out.append(_blank_quotes(line))
                i += 1
                if is_config:
                    # apt config: keep bytes as-is so the pinned value is readable.
                    while i < end and lines[i].strip() != inner:
                        if emit:
                            out.append(lines[i])
                        i += 1
                    i += 1
                else:
                    # Payload → recurse as source; any other heredoc → drop as data.
                    walk(end, inner, emit and is_payload)
                continue

            if emit:
                out.append(_blank_quotes(line))
            i += 1

    walk(len(lines), None, True)
    return "\n".join(out)


def dead_region(text: str, pos: int) -> bool:
    """True when `pos` sits inside a block the shell would not run at bootstrap.

    Covers `if false; then … fi` / `while false` guards and function bodies: a
    mask line inside a function that nothing calls never executes. This is a
    heuristic, not a shell parser — it is deliberately biased toward reporting a
    finding, because a false FAIL costs a developer one minute and a false PASS
    costs an outage.
    """
    before = text[:pos]

    # Walk forward from each `if false` / `while false` and track block depth, so an
    # unrelated earlier `fi` cannot cancel the guard. Counting all closers against
    # only false-openers let `if false; then <mask> fi` pass clean.
    for m in re.finditer(r"\b(?:if|while|until)\s+(?:false|/bin/false)\b", before):
        depth = 0
        for line in before[m.end():].splitlines():
            s = line.strip()
            if re.match(r"(?:if|while|until)\b", s):
                depth += 1
            elif re.match(r"(?:fi|done)\b", s):
                if depth == 0:
                    break  # guard closed before `pos`; this one does not cover it
                depth -= 1
        else:
            return True  # never closed before pos → pos is inside the dead block

    # Inside a function body: last `name() {` has no matching close before pos.
    for m in re.finditer(r"^\s*(?:function\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*\(\)\s*\{", before, re.M):
        tail = before[m.end():]
        if tail.count("{") + 1 > tail.count("}"):
            return True
    return False


def audit(path: Path) -> list[str]:
    """Return a list of (code, message) findings as 'code: message' strings.

    The code prefix lets the selftest assert on finding *identity* rather than
    count — a count-only assertion cannot tell "the right check fired" from
    "two checks swapped bugs".
    """
    if not path.is_file():
        return [f"missing: {path}: missing (expected a fleet host bootstrap)"]

    raw = path.read_text(encoding="utf-8", errors="replace")
    text = effective_text(raw)
    findings: list[str] = []

    mask = MASK_TIMER.search(text)
    if not mask:
        findings.append(
            f"no-mask: {path}: does not mask apt-daily-upgrade.timer (as an executed "
            f"command — commented out, quoted, or inside a heredoc does not count). The host "
            f"would run Ubuntu's unattended-upgrades at 06:00-07:00 UTC (14:00-15:00 CST peak)"
        )
    elif dead_region(text, mask.start()):
        findings.append(
            f"dead-mask: {path}: masks apt-daily-upgrade.timer only inside a block that "
            f"does not execute at bootstrap (false-guard or uncalled function)"
        )

    pin = PIN_WRITE.search(text)
    periodic_off = [m for m in PERIODIC_LINE.finditer(text) if m.group(1) == "0"]
    if not pin:
        findings.append(
            f"no-pin-path: {path}: does not write {PIN_PATH}. A 99- file is what "
            f"outranks the installer's 20auto-upgrades; writing elsewhere (or into "
            f"20auto-upgrades, which the next package upgrade reverts) does not hold"
        )
    if not periodic_off:
        findings.append(
            f'no-pin-value: {path}: does not pin APT::Periodic::Unattended-Upgrade "0" '
            f"(the installer's 20auto-upgrades sets it to 1)"
        )

    # Any non-zero value re-enables it. "[^0]" would have missed "10" and "01".
    for m in PERIODIC_LINE.finditer(text):
        if m.group(1) != "0":
            findings.append(
                f'periodic-on: {path}: sets APT::Periodic::Unattended-Upgrade "{m.group(1)}" '
                f"— any non-zero value re-enables auto-upgrades (apt reads it as a day "
                f"interval); patching must stay a blue/green release"
            )
    if RE_ENABLE.search(text):
        findings.append(
            f"re-enabled: {path}: unmasks / enables / presets apt-daily-upgrade.timer"
        )
    if PIN_REMOVED.search(text):
        findings.append(f"pin-removed: {path}: removes {PIN_PATH} after writing it")

    # Ordering: masking after the first apt call leaves a live window during
    # bootstrap, exactly when the timer may already be mid-run.
    apt = FIRST_APT.search(text)
    if mask and apt and mask.start() > apt.start():
        findings.append(
            f"mask-too-late: {path}: masks apt-daily-upgrade.timer only after the first "
            f"apt-get call — the timer can fire during bootstrap and contend for the dpkg lock"
        )
    return findings


def _selftest() -> int:
    import tempfile

    good = (
        "systemctl mask apt-daily-upgrade.timer >/dev/null 2>&1 || true\n"
        f"cat >{PIN_PATH} <<'APTEOF'\n"
        'APT::Periodic::Unattended-Upgrade "0";\n'
        "APTEOF\n"
        "apt-get update -y\n"
    )
    pin_only = f"cat >{PIN_PATH} <<'E'\nAPT::Periodic::Unattended-Upgrade \"0\";\nE\n"

    # (body, expected finding codes) — identity, not count.
    cases: list[tuple[str, set[str]]] = [
        (good, set()),
        ("apt-get update -y\n", {"no-mask", "no-pin-path", "no-pin-value"}),
        (pin_only, {"no-mask"}),
        ("systemctl mask apt-daily-upgrade.timer\n", {"no-pin-path", "no-pin-value"}),
        # Regressions the first version of this gate passed clean:
        ("# " + good.replace("\n", "\n# "), {"no-mask", "no-pin-path", "no-pin-value"}),
        ("if false; then\n" + good + "fi\n", {"dead-mask"}),
        ("harden() {\n" + good + "}\n", {"dead-mask"}),
        (f'echo "systemctl mask apt-daily-upgrade.timer"\n{pin_only}', {"no-mask"}),
        (
            "systemctl mask apt-daily-upgrade.timer\n"
            "cat >/tmp/junk <<'E'\nAPT::Periodic::Unattended-Upgrade \"0\";\nE\n",
            {"no-pin-path", "no-pin-value"},
        ),
        (
            "systemctl mask apt-daily-upgrade.timer\n"
            "cat >/etc/apt/apt.conf.d/20auto-upgrades <<'E'\n"
            'APT::Periodic::Unattended-Upgrade "0";\nE\n',
            {"no-pin-path", "no-pin-value"},
        ),
        ("apt-get update -y\n" + good, {"mask-too-late"}),
        # Multi-character re-enabling values the "[^0]" class missed.
        (good + 'APT::Periodic::Unattended-Upgrade "1";\n', {"periodic-on"}),
        (good + 'APT::Periodic::Unattended-Upgrade "10";\n', {"periodic-on"}),
        (good + 'APT::Periodic::Unattended-Upgrade "01";\n', {"periodic-on"}),
        # Re-enable routes, including flag-first and preset.
        (good + "systemctl unmask apt-daily-upgrade.timer\n", {"re-enabled"}),
        (good + "systemctl --now enable apt-daily-upgrade.timer\n", {"re-enabled"}),
        (good + "systemctl preset apt-daily-upgrade.timer\n", {"re-enabled"}),
        (good + f"rm -f {PIN_PATH}\n", {"pin-removed"}),
        # Masking apt-daily.timer instead blinds the patch-debt probe.
        (
            "systemctl mask apt-daily.timer\n" + pin_only,
            {"no-mask"},
        ),
    ]

    failures = 0
    with tempfile.TemporaryDirectory() as tmp:
        for i, (body, want) in enumerate(cases):
            p = Path(tmp) / f"case{i}.sh"
            p.write_text(body, encoding="utf-8")
            got = {f.split(":", 1)[0] for f in audit(p)}
            if got != want:
                print(
                    f"  selftest case {i}: expected {sorted(want) or '[]'}, "
                    f"got {sorted(got) or '[]'}",
                    file=sys.stderr,
                )
                failures += 1
        if {f.split(":", 1)[0] for f in audit(Path(tmp) / "absent.sh")} != {"missing"}:
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
            "\nFix: in each bootstrap, BEFORE the first apt-get call, mask the timer and\n"
            f"pin Periodic=0 by writing {PIN_PATH}\n"
            "(the mask must be an executed command, not a comment or a quoted string),\n"
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
