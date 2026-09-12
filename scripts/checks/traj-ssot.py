#!/usr/bin/env python3
"""Reject retired trajectory projections and keep root documentation on one contract."""
from __future__ import annotations

import argparse
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
CONTRACT = "docs/approved/qa-bundle-session-export.md"
RETIRED = (
    "projection.go", "projection_v2.go", "projection_openai.go", "projection_gemini.go",
)
RETIRED_SYMBOLS = ("BuildTrajSessionsV2", "TrajSessionV2", '"traj/v2"', "traj/pipeline/schemas/")


def check(root: Path) -> list[str]:
    failures: list[str] = []
    package = root / "backend/internal/observability/trajectory"
    for name in RETIRED:
        if (package / name).exists():
            failures.append(f"retired projector returned: {name}")
    for base in (root / "backend", root / "docs", root / "ops"):
        for source in base.rglob("*"):
            if not source.is_file() or source.suffix not in {".go", ".md", ".py", ".sh"}:
                continue
            if "dist" in source.parts or source.name.endswith("_test.go"):
                continue
            text = source.read_text(encoding="utf-8")
            for token in RETIRED_SYMBOLS:
                if token in text:
                    failures.append(f"{source.relative_to(root)}: retired trajectory contract {token}")
    for name in ("AGENTS.md", "CLAUDE.md"):
        text = (root / name).read_text(encoding="utf-8")
        if CONTRACT not in text:
            failures.append(f"{name}: missing traj-ssot contract pointer")
        if "observability/trajectory/session.go" in text:
            failures.append(f"{name}: duplicate trajectory owner list; use the contract pointer")
    return failures


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, default=ROOT)
    args = parser.parse_args()
    failures = check(args.root)
    for failure in failures:
        print(f"FAIL: {failure}")
    if not failures:
        print("ok: traj has one Bundle session contract and no retired projector")
    return int(bool(failures))


if __name__ == "__main__":
    raise SystemExit(main())
