#!/usr/bin/env python3
"""Couple the Gemini Web Worker to the backend by protocol, not by release.

The Worker has its own deploy cadence: prod runs none, four edges do, and nothing
ties a Worker deploy to a gateway release. That independence is deliberate, but it
means a backend change to the control plane can reach the edges hours before or
after the Worker that understands it.

So couple the one thing that must not drift. The Worker refuses a control response
whose protocol_version it does not recognise (worker.py check_control), which
takes it out of service rather than letting it guess at session semantics. This
check makes that drift fail preflight instead of failing in production:

  backend  handler.GeminiWebControlProtocolVersion
  worker   ops/gemini-web/worker.py CONTROL_PROTOCOL_VERSION

Bumping either alone fails. Bumping both is the signal that this release must be
paired: ship the backend, then deploy every edge Worker (or the reverse), and do
not leave the fleet half-converged.

This check sees source, not the fleet. It passes the moment both constants move in
one commit — which is before any edge has been deployed. The fleet side of the same
contract is check-gemini-web-worker-fleet.sh, which compares what each Worker
reports against this backend constant and calls a uniformly stale fleet drift
rather than convergence.
"""
from __future__ import annotations

import pathlib
import re
import sys

REPO = pathlib.Path(__file__).resolve().parents[2]
BACKEND = REPO / "backend/internal/handler/gemini_web_session_handler.go"
WORKER = REPO / "ops/gemini-web/worker.py"

BACKEND_PATTERN = re.compile(
    r"^const GeminiWebControlProtocolVersion = (\d+)$",
    re.MULTILINE,
)
WORKER_PATTERN = re.compile(r"^CONTROL_PROTOCOL_VERSION = (\d+)$", re.MULTILINE)
# The handler must serve the named constant. A literal here is how the two sides
# drifted silently before: nothing could compare an anonymous 1 to anything.
SERVED_LITERAL = '"protocol_version": GeminiWebControlProtocolVersion'


def _read(path: pathlib.Path) -> str | None:
    try:
        return path.read_text(encoding="utf-8")
    except OSError as exc:
        print(f"FAIL: cannot read {path.relative_to(REPO)}: {exc}", file=sys.stderr)
        return None


def _version(text: str, pattern: re.Pattern[str], path: pathlib.Path, label: str) -> int | None:
    match = pattern.search(text)
    if match is None:
        print(
            f"FAIL: {label} declaration not found in {path.relative_to(REPO)}; "
            "the Gemini Web control protocol must stay a single named constant on "
            "each side so drift is detectable",
            file=sys.stderr,
        )
        return None
    return int(match.group(1))


def main() -> int:
    backend_text = _read(BACKEND)
    worker_text = _read(WORKER)
    if backend_text is None or worker_text is None:
        return 1

    backend_version = _version(
        backend_text, BACKEND_PATTERN, BACKEND, "GeminiWebControlProtocolVersion"
    )
    worker_version = _version(
        worker_text, WORKER_PATTERN, WORKER, "CONTROL_PROTOCOL_VERSION"
    )
    if backend_version is None or worker_version is None:
        return 1

    failed = False

    if SERVED_LITERAL not in backend_text:
        print(
            "FAIL: /warm-accounts must serve GeminiWebControlProtocolVersion, not a "
            f"literal — expected `{SERVED_LITERAL}` in "
            f"{BACKEND.relative_to(REPO)}",
            file=sys.stderr,
        )
        failed = True

    # The Worker must keep rejecting an unknown version. Without this the two
    # constants can agree in source while the running Worker accepts anything.
    if "!= CONTROL_PROTOCOL_VERSION" not in worker_text:
        print(
            "FAIL: worker.py no longer rejects a mismatched control protocol_version; "
            "a Worker that accepts any version makes this check cosmetic",
            file=sys.stderr,
        )
        failed = True

    if backend_version != worker_version:
        print(
            "FAIL: Gemini Web control protocol drift — backend serves "
            f"{backend_version}, worker speaks {worker_version}.\n"
            "      Both sides must move together, and that release must be paired: "
            "ship the backend and deploy every edge Worker\n"
            "      (deploy-gemini-web-worker.yml per edge), then confirm with "
            "ops/observability/check-gemini-web-worker-fleet.sh.",
            file=sys.stderr,
        )
        failed = True

    if failed:
        return 1

    print(
        f"ok: Gemini Web control protocol v{backend_version} agreed "
        "(backend handler == worker constant)"
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
