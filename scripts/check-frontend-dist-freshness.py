#!/usr/bin/env python3
"""Write or verify the source hash for the embedded frontend dist."""

from __future__ import annotations

import argparse
import hashlib
import json
import subprocess
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[1]
FRONTEND_ROOT = REPO_ROOT / "frontend"
MANIFEST_NAME = "frontend-source.json"
EXCLUDED_SUFFIXES = (
    ".spec.ts",
    ".spec.tsx",
    ".test.ts",
    ".test.tsx",
)


def iter_frontend_input_paths() -> list[Path]:
    if (REPO_ROOT / ".git").exists():
        result = subprocess.run(
            ["git", "ls-files", "frontend"],
            cwd=REPO_ROOT,
            check=True,
            text=True,
            stdout=subprocess.PIPE,
        )
        return filter_input_paths(REPO_ROOT / line for line in result.stdout.splitlines())

    return filter_input_paths(path for path in FRONTEND_ROOT.rglob("*") if path.is_file())


def filter_input_paths(paths) -> list[Path]:
    filtered: list[Path] = []
    for path in paths:
        rel = path.relative_to(REPO_ROOT)
        if any(part in {"node_modules", "dist", "coverage", "__tests__", ".vite"} for part in rel.parts):
            continue
        if rel.name.endswith(EXCLUDED_SUFFIXES):
            continue
        if rel.name in {".DS_Store"}:
            continue
        filtered.append(path)
    return sorted(filtered)


def compute_digest() -> tuple[str, int]:
    h = hashlib.sha256()
    count = 0
    for path in iter_frontend_input_paths():
        rel = path.relative_to(REPO_ROOT).as_posix()
        data = path.read_bytes()
        h.update(rel.encode("utf-8"))
        h.update(b"\0")
        h.update(str(len(data)).encode("ascii"))
        h.update(b"\0")
        h.update(data)
        h.update(b"\0")
        count += 1
    return h.hexdigest(), count


def manifest_payload() -> dict[str, object]:
    digest, count = compute_digest()
    return {
        "schema": 1,
        "source": "frontend/",
        "algorithm": "sha256(git-ls-files:path,size,content)",
        "hash": digest,
        "file_count": count,
    }


def write_manifest(dist: Path) -> int:
    dist.mkdir(parents=True, exist_ok=True)
    manifest_path = dist / MANIFEST_NAME
    manifest_path.write_text(json.dumps(manifest_payload(), indent=2, sort_keys=True) + "\n", encoding="utf-8")
    print(f"wrote {manifest_path}")
    return 0


def check_manifest(dist: Path) -> int:
    manifest_path = dist / MANIFEST_NAME
    if not manifest_path.is_file():
        print(f"FAIL: {manifest_path} is missing; rebuild frontend dist", file=sys.stderr)
        return 1

    try:
        recorded = json.loads(manifest_path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        print(f"FAIL: cannot read {manifest_path}: {exc}", file=sys.stderr)
        return 1

    current = manifest_payload()
    if recorded.get("hash") != current["hash"]:
        print("FAIL: backend/internal/web/dist was not built from the current frontend sources", file=sys.stderr)
        print(f"      recorded: {recorded.get('hash')}", file=sys.stderr)
        print(f"      current:  {current['hash']}", file=sys.stderr)
        print("      run: pnpm --dir frontend run build", file=sys.stderr)
        return 1

    if recorded.get("file_count") != current["file_count"]:
        print("FAIL: frontend source file count changed since dist build", file=sys.stderr)
        print(f"      recorded: {recorded.get('file_count')}", file=sys.stderr)
        print(f"      current:  {current['file_count']}", file=sys.stderr)
        print("      run: pnpm --dir frontend run build", file=sys.stderr)
        return 1

    print("ok: embedded frontend dist source hash matches frontend/")
    return 0


def main() -> int:
    parser = argparse.ArgumentParser()
    mode = parser.add_mutually_exclusive_group(required=True)
    mode.add_argument("--write", action="store_true", help="write the frontend source hash manifest")
    mode.add_argument("--check", action="store_true", help="verify the frontend source hash manifest")
    parser.add_argument("dist", nargs="?", default=str(REPO_ROOT / "backend/internal/web/dist"))
    args = parser.parse_args()

    dist = Path(args.dist)
    if not dist.is_absolute():
        dist = (Path.cwd() / dist).resolve()

    return write_manifest(dist) if args.write else check_manifest(dist)


if __name__ == "__main__":
    raise SystemExit(main())
