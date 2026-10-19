#!/usr/bin/env python3
"""Pack local repo source into a coding-explain prompt for TPM calibration.

Writes a text file under /tmp by default (not committed). Targets a minimum
character budget that typically yields >100k tokenizer tokens for Go/TS code;
the live calibrate step reports exact prompt_tokens.
"""

from __future__ import annotations

import argparse
import json
import os
from pathlib import Path

SKIP_DIR_NAMES = {
    ".git",
    "node_modules",
    "dist",
    "build",
    ".venv",
    "venv",
    "__pycache__",
    ".cursor",
    "vendor",
    "coverage",
    "playwright-report",
    "test-results",
}
SKIP_SUFFIXES = {
    ".png",
    ".jpg",
    ".jpeg",
    ".gif",
    ".webp",
    ".ico",
    ".pdf",
    ".zip",
    ".gz",
    ".wasm",
    ".exe",
    ".dll",
    ".so",
    ".dylib",
    ".bin",
    ".lock",
}
SKIP_NAME_PARTS = {
    ".env",
    "config.yaml",
    "config.yml",
    "credentials",
    "id_rsa",
    ".pem",
    ".key",
}
CODE_SUFFIXES = {
    ".go",
    ".ts",
    ".tsx",
    ".vue",
    ".js",
    ".jsx",
    ".py",
    ".sql",
    ".proto",
    ".md",
}


def _should_skip(path: Path) -> bool:
    name = path.name.lower()
    if any(part in name for part in SKIP_NAME_PARTS):
        return True
    if path.suffix.lower() in SKIP_SUFFIXES:
        return True
    if path.suffix.lower() not in CODE_SUFFIXES:
        return True
    return False


def iter_source_files(root: Path, roots: list[str]) -> list[Path]:
    files: list[Path] = []
    for rel in roots:
        base = root / rel
        if not base.exists():
            continue
        for dirpath, dirnames, filenames in os.walk(base):
            dirnames[:] = [d for d in dirnames if d not in SKIP_DIR_NAMES and not d.startswith(".")]
            for fn in filenames:
                p = Path(dirpath) / fn
                if _should_skip(p):
                    continue
                files.append(p)
    # Prefer smaller hand-written paths before giant generated trees by path hint,
    # then by size ascending so we diversify modules early.
    def sort_key(p: Path) -> tuple[int, int, str]:
        s = str(p)
        generated = 1 if "/ent/" in s or "/generated/" in s or s.endswith("_gen.go") else 0
        try:
            size = p.stat().st_size
        except OSError:
            size = 0
        return (generated, size, s)

    files.sort(key=sort_key)
    return files


EXPLAIN_PREFIX = """你是资深工程师。下面是 TokenKey/sub2api 仓库的源码摘录（多文件拼接）。
请阅读后用中文解释：
1) 整体架构与主要目录职责
2) 请求从入口到上游的关键路径
3) 你认为最值得注意的 3 个设计点或风险

要求：条理清晰，适度深入；不要复述大段源码；不要编造文件中不存在的模块。

===== SOURCE BEGIN =====
"""

EXPLAIN_SUFFIX = """
===== SOURCE END =====
请开始解释。
"""


def pack(
    *,
    repo_root: Path,
    roots: list[str],
    min_chars: int,
    max_chars: int,
    max_file_chars: int,
) -> tuple[str, dict]:
    files = iter_source_files(repo_root, roots)
    parts: list[str] = [EXPLAIN_PREFIX]
    used = 0
    included: list[dict] = []
    for path in files:
        if used >= min_chars:
            break
        try:
            raw = path.read_text(encoding="utf-8", errors="replace")
        except OSError:
            continue
        if not raw.strip():
            continue
        if len(raw) > max_file_chars:
            raw = raw[:max_file_chars] + "\n/* … truncated … */\n"
        rel = path.relative_to(repo_root).as_posix()
        block = f"\n----- FILE: {rel} -----\n{raw}\n"
        if used + len(block) > max_chars:
            remain = max_chars - used
            if remain < 2048:
                break
            block = block[:remain] + "\n/* … pack budget reached … */\n"
        parts.append(block)
        used += len(block)
        included.append({"path": rel, "chars": len(block)})
    parts.append(EXPLAIN_SUFFIX)
    text = "".join(parts)
    meta = {
        "repo_root": str(repo_root),
        "roots": roots,
        "min_chars": min_chars,
        "max_chars": max_chars,
        "total_chars": len(text),
        "body_chars": used,
        "files_n": len(included),
        "files_head": included[:20],
        "files_tail": included[-5:],
        "approx_tokens_chars_div4": len(text) // 4,
        "note": "approx_tokens_chars_div4 is only a lower-bound hint; calibrate reports prompt_tokens",
    }
    return text, meta


def main() -> int:
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--repo-root", default=str(Path(__file__).resolve().parents[2]))
    p.add_argument(
        "--roots",
        default="backend/internal,backend/cmd,backend/ent/schema,frontend/src",
        help="comma-separated dirs relative to repo root",
    )
    p.add_argument("--min-chars", type=int, default=450_000,
                   help="minimum packed source chars before explain wrapper (~>100k tokens for code)")
    p.add_argument("--max-chars", type=int, default=700_000)
    p.add_argument("--max-file-chars", type=int, default=120_000)
    p.add_argument("--out", default="/tmp/tpm-recipe1/sub2api-explain-prompt.txt")
    p.add_argument("--meta-out", default="/tmp/tpm-recipe1/sub2api-explain-prompt.meta.json")
    args = p.parse_args()

    root = Path(args.repo_root).resolve()
    roots = [x.strip() for x in args.roots.split(",") if x.strip()]
    text, meta = pack(
        repo_root=root,
        roots=roots,
        min_chars=int(args.min_chars),
        max_chars=int(args.max_chars),
        max_file_chars=int(args.max_file_chars),
    )
    out = Path(args.out)
    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_text(text, encoding="utf-8")
    meta_path = Path(args.meta_out)
    meta_path.write_text(json.dumps(meta, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(json.dumps({"wrote": str(out), "meta": str(meta_path), **{k: meta[k] for k in (
        "total_chars", "body_chars", "files_n", "approx_tokens_chars_div4")}}, ensure_ascii=False))
    if meta["approx_tokens_chars_div4"] < 100_000:
        print(json.dumps({
            "warning": "approx under 100k tokens; raise --min-chars or widen --roots",
            "approx_tokens_chars_div4": meta["approx_tokens_chars_div4"],
        }), flush=True)
        return 4
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
