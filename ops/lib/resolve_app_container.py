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


# Data-layer containers. Stage0 names them tokenkey-postgres / tokenkey-redis; the
# Tencent USW layout exception keeps tk-archive-standby / tk-cutover-prod-redis and
# only adds network aliases, which never work for a host-side `docker exec`.
# Mirrors tk_resolve_postgres_container / tk_resolve_redis_container in the shell
# owner: explicit name must run; otherwise exactly one running candidate.
POSTGRES_CANDIDATES = ("tokenkey-postgres", "tk-archive-standby")
REDIS_CANDIDATES = ("tokenkey-redis", "tk-cutover-prod-redis")


def _resolve_single_running(
    requested: str, candidates: tuple[str, ...], docker: tuple[str, ...]
) -> str | None:
    if requested and requested != "auto":
        return requested if _running(requested, docker) else None
    running = [name for name in candidates if _running(name, docker)]
    return running[0] if len(running) == 1 else None


def resolve_postgres(requested: str = "auto", *, docker: tuple[str, ...] = ("docker",)) -> str | None:
    """Return the running PostgreSQL container, or None when ambiguous/absent."""
    return _resolve_single_running(requested, POSTGRES_CANDIDATES, docker)


def resolve_redis(requested: str = "auto", *, docker: tuple[str, ...] = ("docker",)) -> str | None:
    """Return the running Redis container, or None when ambiguous/absent."""
    return _resolve_single_running(requested, REDIS_CANDIDATES, docker)


def data_container_shell_snippet(
    kind: str, *, docker: str = "docker", variable: str | None = None, required: bool = True
) -> str:
    """Return one SSM-ready shell line that sets `variable` to the running container.

    `kind` is "postgres" or "redis". With `required` (the default) an unresolved
    container exits 1 with a readable error, so callers splicing this into a
    remote script fail closed instead of `docker exec`-ing a stale or absent name.
    """
    candidates = {"postgres": POSTGRES_CANDIDATES, "redis": REDIS_CANDIDATES}[kind]
    var = variable or ("TK_PG" if kind == "postgres" else "TK_REDIS")
    line = (
        f"{var}=''; tk_n=0; for tk_c in {' '.join(candidates)}; do "
        f"if [ \"$({docker} inspect --format '{{{{.State.Running}}}}' \"$tk_c\" 2>/dev/null)\" = true ]; "
        f"then {var}=\"$tk_c\"; tk_n=$((tk_n + 1)); fi; done; "
        f"[ \"$tk_n\" -eq 1 ] || {var}=''"
    )
    if required:
        line += (
            f"; [ -n \"${var}\" ] || {{ echo '{kind} container unresolved "
            f"(want exactly one running of: {' '.join(candidates)})' >&2; exit 1; }}"
        )
    return line


def data_container_preamble(*, docker: str = "sudo docker", redis: bool = False) -> str:
    """Shell preamble for remote scripts: resolve and export TK_PG (and TK_REDIS).

    Local SSM drivers prepend this to the script they ship so remote PSQL /
    redis-cli strings can reference "$TK_PG" / "$TK_REDIS" and embedded python can
    read os.environ["TK_PG"]. Unresolved containers abort the remote script.
    """
    lines = [data_container_shell_snippet("postgres", docker=docker), "export TK_PG"]
    if redis:
        lines += [data_container_shell_snippet("redis", docker=docker), "export TK_REDIS"]
    return "\n".join(lines) + "\n"


def _main(argv: list[str]) -> int:
    """`resolve_app_container.py snippet postgres|redis [--docker CMD] [--var NAME]`.

    Lets shell-side SSM drivers splice the canonical remote resolution line into
    an inline command array instead of hand-copying the candidate loop.
    """
    import argparse

    parser = argparse.ArgumentParser(prog="resolve_app_container.py")
    sub = parser.add_subparsers(dest="action", required=True)
    snippet = sub.add_parser("snippet")
    snippet.add_argument("kind", choices=("postgres", "redis"))
    snippet.add_argument("--docker", default="docker")
    snippet.add_argument("--var", default=None)
    args = parser.parse_args(argv)
    print(data_container_shell_snippet(args.kind, docker=args.docker, variable=args.var))
    return 0


if __name__ == "__main__":
    import sys

    raise SystemExit(_main(sys.argv[1:]))
