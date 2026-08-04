#!/usr/bin/env python3
"""Canonical app-container resolver for the non-sourceable delivery shapes.

`ops/lib/resolve-app-container.sh` is the owner of this contract for probes that
run as real files on the host. Two delivery shapes cannot source a file:

  * inline SSM command arrays (`ops-daily-diagnostics.yml`, the guarded
    partition-maintenance controller) — the remote side receives a command
    string, not a checkout;
  * python heredocs embedded in probe scripts.

Rather than let those shapes keep hand-rolled copies (which is exactly how the
stopped-container bug spread), they render or import from here, so the resolution
rules live in one place and `scripts/checks/app-container-resolver.py` can prove
no fourth copy exists.

Contract is identical to the shell owner: explicit name must be running;
active-color wins when it names blue|green and that container runs; otherwise
accept a single running candidate and treat zero-or-several as unknown.
"""

from __future__ import annotations

import pathlib
import shutil
import subprocess

ACTIVE_COLOR_PATH = "/var/lib/tokenkey/active-color"
CANDIDATES = ("tokenkey", "tokenkey-blue", "tokenkey-green")


def remote_shell_snippet(*, docker: str = "docker", variable: str = "APP_CONTAINER") -> list[str]:
    """Return SSM-ready shell lines that set `variable` or leave it empty.

    Emitted as a list of standalone lines so callers can splice them into an
    AWS-RunShellScript `commands` array. Ambiguity leaves the variable empty;
    callers decide whether that is a hard failure or a degraded signal, but they
    must not fall back to a positional guess.
    """
    return [
        f"{variable}=''",
        f"tk_running() {{ [ \"$({docker} inspect --format '{{{{.State.Running}}}}' \"$1\" 2>/dev/null)\" = true ]; }}",
        (
            f'if [ -r {ACTIVE_COLOR_PATH} ]; then '
            f'color=$(tr -d "[:space:]" < {ACTIVE_COLOR_PATH} 2>/dev/null || true); '
            f'case "$color" in blue|green) '
            f'if tk_running "tokenkey-$color"; then {variable}="tokenkey-$color"; fi ;; esac; fi'
        ),
        (
            f'if [ -z "${variable}" ]; then tk_found=""; tk_count=0; '
            f'for candidate in {" ".join(CANDIDATES)}; do '
            f'if tk_running "$candidate"; then tk_found="$candidate"; tk_count=$((tk_count + 1)); fi; done; '
            f'if [ "$tk_count" -eq 1 ]; then {variable}="$tk_found"; fi; fi'
        ),
    ]


def _running(name: str, docker: tuple[str, ...]) -> bool:
    if not name:
        return False
    if shutil.which(docker[0]) is None:
        return False
    try:
        completed = subprocess.run(
            [*docker, "inspect", "--format", "{{.State.Running}}", name],
            capture_output=True,
            text=True,
            timeout=15,
            check=False,
        )
    except (OSError, subprocess.SubprocessError):
        return False
    return completed.returncode == 0 and completed.stdout.strip() == "true"


def resolve(
    requested: str = "auto",
    *,
    active_color_file: str | pathlib.Path = ACTIVE_COLOR_PATH,
    docker: tuple[str, ...] = ("docker",),
) -> tuple[str | None, list[str]]:
    """Resolve the live app container. Returns (name_or_None, human notes)."""
    notes: list[str] = []

    if requested and requested != "auto":
        if _running(requested, docker):
            return requested, notes + [f"explicit container {requested} is running"]
        return None, notes + [f"explicit container {requested} is not running"]

    path = pathlib.Path(active_color_file)
    try:
        color = "".join(path.read_text(encoding="utf-8").split())
    except OSError:
        color = ""
        notes.append("active-color missing")
    else:
        notes.append(f"active-color={color or '<empty>'}")

    if color in {"blue", "green"}:
        candidate = f"tokenkey-{color}"
        if _running(candidate, docker):
            return candidate, notes + [f"{candidate} is running"]
        notes.append(f"{candidate} is not running")

    running = [name for name in CANDIDATES if _running(name, docker)]
    if len(running) == 1:
        return running[0], notes + [f"unique running candidate {running[0]}"]
    notes.append(f"running candidates ambiguous: {running or 'none'}")
    return None, notes
