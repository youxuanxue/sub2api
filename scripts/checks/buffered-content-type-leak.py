#!/usr/bin/env python3
"""
check-buffered-content-type-leak.py — guard against re-introducing the
upstream-SSE-Content-Type-leak-onto-JSON-body bug (Wei-Shaw/sub2api#1311).

Root cause:
  gin's render.writeContentType (used by both c.JSON and c.Data) only writes
  Content-Type when the response header map is EMPTY. If responseheaders.
  WriteFilteredHeaders already propagated the upstream `Content-Type:
  text/event-stream` (which it does for any SSE upstream), then a subsequent
  c.JSON(...) or c.Data(_, "application/json...", _) call will NOT overwrite
  it — the client receives a JSON body labeled as SSE.

This script enforces the fix mechanically. It scans every Go source file under
backend/internal/service for the antipattern:

    responseheaders.WriteFilteredHeaders(c.Writer.Header(), ...)
    ... (no `c.Writer.Header().Set("Content-Type"` / `c.Header("Content-Type"`
         line within the next N lines) ...
    c.JSON(...)                                  OR
    c.Data(..., "application/json...", ...)       (hardcoded application/json)

False-positive safe: passthrough proxy paths that use `c.Data(..., contentType,
...)` with a variable Content-Type (typically derived from the upstream Content-
Type via resp.Header.Get) are NOT flagged. Those sites intentionally preserve
the upstream Content-Type; the bug doesn't manifest there because the leaked
header IS the intended one.

Streaming paths that explicitly set `Content-Type: text/event-stream` between
WriteFilteredHeaders and any downstream call are also exempt — they're using
WriteFilteredHeaders only to propagate rate-limit / request-id headers.

Exit codes:
  0 — no leak antipattern detected
  1 — at least one antipattern found
  2 — execution failure (missing source tree, etc.)
"""
from __future__ import annotations

import argparse
import re
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent.parent
SERVICE_DIR = REPO_ROOT / "backend" / "internal" / "service"

WRITE_FILTERED_RE = re.compile(r"responseheaders\.WriteFilteredHeaders\(")
EXPLICIT_SET_RE = re.compile(
    r"""(?x)
    (?:
        c\.Writer\.Header\(\)\.Set\(\s*["']Content-Type["']     # canonical fix
      | c\.Header\(\s*["']Content-Type["']                       # gin sugar form
      | dst\.Set\(\s*["']Content-Type["']                        # passthrough helpers
    )
    """
)
JSON_OUTPUT_RE = re.compile(r"\bc\.JSON\(")
DATA_HARDCODED_JSON_RE = re.compile(
    r"""\bc\.Data\(\s*[^,]+,\s*["']application/json[^"']*["']"""
)
COMMENT_LINE_RE = re.compile(r"^\s*//")

# Look-ahead window: WriteFilteredHeaders and the corresponding output are
# almost always in the same function and usually 1-20 lines apart. 60 lines
# gives generous margin without producing spurious cross-function matches.
LOOK_AHEAD_LINES = 60


def scan_file(path: Path) -> list[str]:
    """Return human-readable failure messages for the file (empty if clean)."""
    text = path.read_text(encoding="utf-8", errors="replace")
    lines = text.splitlines()
    failures: list[str] = []

    for idx, line in enumerate(lines):
        if not WRITE_FILTERED_RE.search(line):
            continue

        # Skip the macro line itself; check subsequent lines.
        explicit_set_seen = False
        rel_path = path.relative_to(REPO_ROOT)

        end = min(len(lines), idx + 1 + LOOK_AHEAD_LINES)
        for j in range(idx + 1, end):
            scan_line = lines[j]

            # Comments are ignored for both the override and the output match
            # (the antipattern needs an actual call, and the override likewise
            # needs to be live code).
            if COMMENT_LINE_RE.match(scan_line):
                continue

            if EXPLICIT_SET_RE.search(scan_line):
                explicit_set_seen = True
                # Don't break — the override applies to all subsequent outputs
                # in this stretch; but if there are further WriteFilteredHeaders
                # calls before the next output, those start a fresh window.
                # In practice WriteFilteredHeaders is called once per response.
                continue

            if JSON_OUTPUT_RE.search(scan_line):
                if not explicit_set_seen:
                    failures.append(
                        f"{rel_path}:{j + 1}: c.JSON(...) follows "
                        f"WriteFilteredHeaders at line {idx + 1} without an "
                        f"explicit c.Writer.Header().Set(\"Content-Type\", ...) "
                        f"override. This leaks the upstream SSE Content-Type "
                        f"onto a JSON body (Wei-Shaw/sub2api#1311). Add "
                        f"`c.Writer.Header().Set(\"Content-Type\", "
                        f"\"application/json\")` (or `; charset=utf-8`) before "
                        f"c.JSON."
                    )
                # Whether or not flagged, this output ends the relevant window.
                break

            if DATA_HARDCODED_JSON_RE.search(scan_line):
                if not explicit_set_seen:
                    failures.append(
                        f"{rel_path}:{j + 1}: c.Data(..., \"application/json"
                        f"...\", ...) follows WriteFilteredHeaders at line "
                        f"{idx + 1} without an explicit "
                        f"c.Writer.Header().Set(\"Content-Type\", ...) "
                        f"override. gin's render.Data won't overwrite the "
                        f"upstream SSE Content-Type (Wei-Shaw/sub2api#1311). "
                        f"Add `c.Writer.Header().Set(\"Content-Type\", "
                        f"\"application/json; charset=utf-8\")` before c.Data."
                    )
                break

    return failures


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--quiet",
        action="store_true",
        help="only print failures; suppress the per-file pass log",
    )
    args = parser.parse_args()

    if not SERVICE_DIR.is_dir():
        print(
            f"FATAL: service source directory not found: "
            f"{SERVICE_DIR.relative_to(REPO_ROOT)}",
            file=sys.stderr,
        )
        return 2

    failures: list[str] = []
    files_scanned = 0
    for path in sorted(SERVICE_DIR.glob("*.go")):
        # Skip test files: the antipattern check is about production code paths.
        if path.name.endswith("_test.go"):
            continue
        files_scanned += 1
        failures.extend(scan_file(path))

    if failures:
        print(
            "FAIL: buffered-Content-Type-leak antipattern detected (root cause "
            "= Wei-Shaw/sub2api#1311):",
            file=sys.stderr,
        )
        for failure in failures:
            print(f"  {failure}", file=sys.stderr)
        print(
            "\nWhy this matters: gin's render.writeContentType only writes "
            "Content-Type when the header is empty. After WriteFilteredHeaders "
            "propagates the upstream SSE Content-Type, c.JSON / c.Data will "
            "NOT overwrite it — the client receives a JSON body labeled as "
            "text/event-stream and OpenAI-SDK clients that branch on "
            "Content-Type misroute it through SSE parsers.\n"
            "Reference fixes: backend/internal/service/openai_gateway_"
            "chat_completions.go and backend/internal/service/gateway_forward_"
            "as_responses.go both explicitly call c.Writer.Header().Set("
            "\"Content-Type\", \"application/json\") right before c.JSON / "
            "c.Data.",
            file=sys.stderr,
        )
        return 1

    if not args.quiet:
        print(
            f"ok: scanned {files_scanned} files in "
            f"{SERVICE_DIR.relative_to(REPO_ROOT)}, no buffered-Content-Type-"
            f"leak antipattern (Wei-Shaw/sub2api#1311) found."
        )
    return 0


if __name__ == "__main__":
    sys.exit(main())
