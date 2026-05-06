#!/usr/bin/env python3
"""Redact secrets from stdin -> stdout for headless CI agent stream capture.

GitHub Actions masks secret values in the live log render, but bytes that hit
a `tee`-targeted file are raw -- and that file usually becomes a public
artifact. This filter sits between the agent and the file:

    claude -p ... 2>&1 | python3 scripts/redact-agent-stream.py | tee out.txt

Two passes per line:

  1. Exact-value replacement of secrets pulled from env vars listed in
     REDACT_FROM_ENV (comma-separated; defaults to the known headless-agent
     secret set). Catches the real secret regardless of token format drift.
  2. Regex replacement of common token formats (sk-..., ghp_..., etc.) as
     defense-in-depth against accidental tokens of other origins that
     happen to be in scope.

Lines that contain no match pass through byte-identical. Stream is line-
buffered so `tee` keeps producing live CI log output.
"""

from __future__ import annotations

import os
import re
import sys

DEFAULT_ENV_VARS = (
    "ANTHROPIC_AUTH_TOKEN",
    "ANTHROPIC_API_KEY",
    "GH_TOKEN",
    "GITHUB_TOKEN",
    "UPSTREAM_MERGE_GH_TOKEN",
)

# Pattern thresholds tightened after run 25419117708 (2026-05-06) leaked
# `github_pat_11ABMYKSY` (9 chars after prefix) into a public artifact:
# the agent printed a TRUNCATED prefix while debugging a PAT permission
# issue, so neither exact-value match (full token != truncated) nor the
# previous `{20,}` regex caught it. Lowered thresholds for distinctive
# prefixes (`github_pat_*`, `ghp_*`, `gho_*`, `ghs_*`, `ghu_*`, `ghr_*`)
# so even a short suffix is redacted. `sk-` keeps a higher floor since
# the prefix is more ambiguous (test fixtures, doc strings).
TOKEN_PATTERNS = (
    re.compile(r"sk-[A-Za-z0-9_\-]{8,}"),
    re.compile(r"\b(?:ghp|gho|ghs|ghu|ghr)_[A-Za-z0-9]+"),
    re.compile(r"\bgithub_pat_[A-Za-z0-9_]*"),
)

REPLACEMENT = "***REDACTED***"

# Values shorter than this are skipped to avoid catastrophic over-redaction
# if an env var is set to a single common substring (e.g. "true").
MIN_SECRET_LEN = 8


def _collect_secrets() -> list[str]:
    overrides = os.environ.get("REDACT_FROM_ENV")
    names = (
        [v.strip() for v in overrides.split(",") if v.strip()]
        if overrides
        else list(DEFAULT_ENV_VARS)
    )
    seen: set[str] = set()
    out: list[str] = []
    for name in names:
        val = os.environ.get(name)
        if not val or len(val) < MIN_SECRET_LEN or val in seen:
            continue
        seen.add(val)
        out.append(val)
    out.sort(key=len, reverse=True)
    return out


def redact(text: str, secrets: list[str]) -> str:
    for s in secrets:
        if s in text:
            text = text.replace(s, REPLACEMENT)
    for pat in TOKEN_PATTERNS:
        text = pat.sub(REPLACEMENT, text)
    return text


def main() -> int:
    secrets = _collect_secrets()
    for line in sys.stdin:
        sys.stdout.write(redact(line, secrets))
        sys.stdout.flush()
    return 0


if __name__ == "__main__":
    sys.exit(main())
